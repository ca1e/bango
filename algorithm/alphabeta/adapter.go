package alphabeta

// adapter.go bridges the alpha-beta searcher to the engine's pluggable
// algorithm interface (gomoku/algorithm). It owns everything that lives
// longer than one think — the persistent transposition table and the
// compiled opening books — and translates the engine's Request snapshot
// into a searcher run. This is the historic Engine.aiMove, lifted behind
// the interface so the session layer no longer knows how moves are made.

import (
	"os"
	"strconv"

	"gomoku/algorithm"
	"gomoku/book"
)

// AlgorithmName is the registry name of the alpha-beta search algorithm.
const AlgorithmName = "alphabeta"

func init() {
	algorithm.Register(AlgorithmName, func() algorithm.Algorithm { return New() })
}

// Algorithm implements algorithm.Algorithm with the alpha-beta searcher.
type Algorithm struct {
	// tt is the persistent transposition table shared by every think of a
	// game: Zobrist seeds are fixed, so hashes stay comparable across moves
	// and last move's deep results are found again this move. Reset keeps a
	// compatible table across a same-size RESTART; EndSession drops it.
	tt        *transTable
	ttBits    int
	ttMaxMem  int64
	ttBoardSz int

	// bookLoaded memoizes the opening-book resolution (per Folder). book is
	// the adoption book; modeBook is the classic 26-mode guidance book whose
	// replies feed the opening prior's theory zone only — it never adopts
	// moves.
	bookLoaded bool
	bookFolder string
	book       *book.Compiled
	modeBook   *book.Compiled
}

// New creates the algorithm with empty per-game state.
func New() *Algorithm { return &Algorithm{} }

// Name implements algorithm.Algorithm.
func (a *Algorithm) Name() string { return AlgorithmName }

// Reset starts the described game. The transposition table follows the game
// lifecycle: a new board size or a changed max_memory budget reallocates it;
// a compatible table survives so the next think of the same game finds the
// previous move's deep entries.
func (a *Algorithm) Reset(size int, maxMemory int64) {
	bits := ttBitsFor(maxMemory)
	if a.tt != nil && a.ttBoardSz == size && a.ttBits == bits && a.ttMaxMem == maxMemory {
		return
	}
	a.tt = newTransTable(bits)
	a.ttBits = bits
	a.ttMaxMem = maxMemory
	a.ttBoardSz = size
}

// EndSession discards every per-game cache: a new client must not inherit
// positions from the previous game.
func (a *Algorithm) EndSession() {
	a.tt = nil
	a.ttBoardSz = 0
}

// booksFor resolves the opening books once per Folder (mirrors the historic
// memoized Engine.loadBook): the manager/legacy book.json first, then the
// shipped openbook defaults; the classic mode book is guidance-only. Returns
// (adoption book, mode book).
func (a *Algorithm) booksFor(folder string) (adopted, mode *book.Compiled) {
	if !a.bookLoaded || a.bookFolder != folder {
		a.bookLoaded = true
		a.bookFolder = folder
		a.book, a.modeBook = nil, nil
		if path := book.FindBookPath(folder); path != "" {
			if cb, err := book.LoadFile(path); err == nil {
				a.book = cb
			}
		} else {
			a.book = book.LoadDefaultBooks()
		}
		a.modeBook = book.LoadModeBook()
	}
	return a.book, a.modeBook
}

// Think implements algorithm.Algorithm: snapshot in, move out. Runs the
// searcher without holding any engine lock (the Request is a private copy).
func (a *Algorithm) Think(req algorithm.Request) (int, int) {
	s := newSearcherWithTT(req.Size, req.Board, req.MaxMemory, a.tt)
	// the role→black mapping is required even under freestyle: the opening
	// book's keys are colour-encoded relative to black (bookCandidates), so
	// a white engine must map playerOpp→black or every query key describes
	// the colour-swapped position. Outside renju the forbidden-move logic
	// ignores blackSide entirely, so this only feeds the book query.
	black := playerMe
	if !req.OwnIsBlack {
		black = playerOpp
	}
	s.setRule(req.Rule, black)
	adopted, modeBook := a.booksFor(req.Folder)
	if adopted != nil && adopted.Rule == req.Rule {
		s.book = adopted
	}
	if modeBook != nil && modeBook.Rule == req.Rule && modeBook.Size == req.Size {
		s.modeBook = modeBook
	}
	maxDepth := maxSearchDepth
	if req.MaxDepth > 0 && req.MaxDepth < maxDepth {
		maxDepth = req.MaxDepth
	}
	if req.MaxNode > 0 {
		s.nodeLimit = req.MaxNode
	}
	if w := os.Getenv("BANGO_SMP"); w != "" {
		if workers, err := strconv.Atoi(w); err == nil && workers > 1 {
			s.smpWorkers = workers
		}
	}
	return s.run(maxDepth, thinkBudget(req.TimeoutTurn, req.TimeLeft))
}
