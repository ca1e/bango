package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Engine holds the shared game state. All access to board/state is guarded by
// the embedded mutex. It also owns the stdout writer and guarantees that every
// reply is flushed immediately so the manager never blocks waiting for data.
type Engine struct {
	mu sync.Mutex

	size  int
	board [][]int // 0 empty, 1 own, 2 opponent

	// info holds configuration pushed by the manager via INFO commands.
	// The engine stores these but performs no rule judgement on them.
	info Info

	// in is the command source (os.Stdin in pipe mode, the TCP connection
	// in web mode). Read only by the reader goroutine between sessions.
	in io.Reader

	// out is a buffered writer over stdout (or the TCP connection). Call
	// flushOut after every write.
	out *bufio.Writer

	// webMode switches END handling: a socket session ends (connection
	// closed, accept loop resumes) instead of terminating the process.
	webMode bool

	// ownIsBlack tracks which internal color owns black — renju forbidden
	// points apply to black only, and the engine may play either color.
	// Set from BEGIN (engine opened), the first TURN (opponent opened) or
	// the first BOARD stone (renju boards arrive in move order).
	ownIsBlack bool

	// tt is the persistent transposition table shared by every think of a
	// game. The Zobrist seeds are fixed, so hashes stay comparable across
	// moves and last move's deep results are found again this move. It is
	// reallocated on START/RESTART (new board size) and when INFO max_memory
	// changes; the searcher borrows it per think.
	tt        *transTable
	ttBits    int
	ttMaxMem  int64
	ttBoardSz int

	// book is the lazily loaded opening book (nil when no book file is
	// found or it fails to compile); bookLoaded memoizes the attempt.
	book       *compiledBook
	bookLoaded bool
}

// Info stores manager-provided configuration. All fields are held as raw
// strings except where a typed value is convenient; no semantic validation
// (e.g. rule forbidden-move checking) is performed.
type Info struct {
	TimeoutTurn  int    // ms per move (0 = as fast as possible)
	TimeoutMatch int    // ms for the whole match
	MaxMemory    int64  // bytes
	MaxDepth     int    // max search depth in plies (0 = engine default)
	MaxNode      int64  // max search nodes per move (0 = unlimited)
	TimeLeft     int64  // ms remaining on the match clock
	GameType     int    // game type code
	Rule         int    // rule code: 1 freestyle, 2 continuous, 4 renju, 9 caro
	Fast         bool   // fast mode
	Folder       string // folder for persistent files (opening book lives here)
	raw          map[string]string
}

func NewEngine() *Engine {
	e := &Engine{
		size:       15,
		out:        bufio.NewWriter(os.Stdout),
		in:         os.Stdin,
		ownIsBlack: true, // until proven otherwise (BEGIN / first TURN / BOARD)
	}
	// allocate the default board up front so commands received before START
	// (e.g. PRINT) never touch an unallocated board
	e.resetBoard(15)
	return e
}

// setIO rewires the engine's command source and reply writer. Called only
// between sessions, before the reader/thinker goroutines start, so no
// synchronization is needed beyond the engine mutex for state.
func (e *Engine) setIO(in io.Reader, out io.Writer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.in = in
	e.out = bufio.NewWriter(out)
}

// resetSession restores the fresh-process state for a new client: default
// board, no moves, no manager configuration.
func (e *Engine) resetSession() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.size = 15
	e.board = make([][]int, 15)
	for i := range e.board {
		e.board[i] = make([]int, 15)
	}
	e.info = Info{}
	e.ownIsBlack = true
	e.tt = nil // new client, new game: nothing carries over
	e.ttBoardSz = 0
}

// unplace removes a stone (TAKEBACK command). Bounds failures are ignored.
func (e *Engine) unplace(x, y int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if y < 0 || x < 0 || y >= len(e.board) || x >= len(e.board) {
		return
	}
	e.board[y][x] = 0
}

// loadBook resolves and compiles the opening book once per process: the
// INFO folder's pbrain-bango/ subfolder first, then the executable's
// directory. A missing or broken file silently disables the book — the
// engine must keep playing without it.
func (e *Engine) loadBook() *compiledBook {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.bookLoaded {
		e.bookLoaded = true
		if path := findBookPath(e.info.Folder); path != "" {
			if cb, err := loadBookFile(path); err == nil {
				e.book = cb
			}
		}
	}
	return e.book
}

// boardEmpty reports whether no stones are on the board.
func (e *Engine) boardEmpty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, row := range e.board {
		for _, v := range row {
			if v != 0 {
				return false
			}
		}
	}
	return true
}

// flushOut flushes the stdout buffer. Mirrors fflush(stdout) in C engines.
func (e *Engine) flushOut() {
	_ = e.out.Flush()
}

// writeLine writes a single line (CRLF terminated) and flushes immediately.
func (e *Engine) writeLine(line string) {
	e.out.WriteString(line)
	e.out.WriteString("\r\n")
	e.flushOut()
}

// resetBoard (re)allocates an empty board of the given size. Caller must hold
// the mutex or be in a single-threaded init path. The persistent transposition
// table follows the game lifecycle: a new size or a changed max_memory budget
// reallocates it; otherwise it survives so the next think of the same game
// finds the previous move's deep entries.
func (e *Engine) resetBoard(size int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.size = size
	e.board = make([][]int, size)
	for i := range e.board {
		e.board[i] = make([]int, size)
	}
	e.ensureTTLocked(size)
}

// ensureTTLocked (re)creates the persistent TT when the board size or the
// max_memory budget changed; a same-size START/RESTART keeps it. Caller must
// hold the engine mutex.
func (e *Engine) ensureTTLocked(size int) {
	bits := ttBitsFor(e.info.MaxMemory)
	if e.tt != nil && e.ttBoardSz == size && e.ttBits == bits && e.ttMaxMem == e.info.MaxMemory {
		return
	}
	e.tt = newTransTable(bits)
	e.ttBits = bits
	e.ttMaxMem = e.info.MaxMemory
	e.ttBoardSz = size
}

// place sets a stone on the board if the coordinates are in bounds.
func (e *Engine) place(x, y, id int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if y < 0 || x < 0 || y >= len(e.board) || x >= len(e.board) {
		return
	}
	e.board[y][x] = id
}

// cellOccupied reports whether the coordinates are on the board and taken by
// a stone or a field-3 mark. Out-of-board coordinates report false so TURN
// keeps its tolerant behavior (the stone is dropped, the engine still
// answers) — only an on-board collision is refused with ERROR.
func (e *Engine) cellOccupied(x, y int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if y < 0 || x < 0 || y >= len(e.board) || x >= len(e.board) {
		return false
	}
	return e.board[y][x] != 0
}

// markCell records a protocol field-3 cell (winning-line mark or renju
// forbidden point). Stored as a distinct value so the search treats the cell
// as occupied/blocked without attributing it to either side. Bounds failures
// are ignored, mirroring place.
func (e *Engine) markCell(x, y int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if y < 0 || x < 0 || y >= len(e.board) || x >= len(e.board) {
		return
	}
	e.board[y][x] = 3
}

func atoi(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func atoiOrZero(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}

func atoi64OrZero(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
