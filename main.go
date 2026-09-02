package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"gomoku/algorithm/alphabeta" // register the alphabeta algorithm (registry import)
	_ "gomoku/algorithm/random"  // register the random algorithm
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "web":
			// web subcommand: the only place --port/--addr are accepted
			fs := flag.NewFlagSet("web", flag.ExitOnError)
			port := fs.Int("port", 9527, "TCP listening port")
			addr := fs.String("addr", "0.0.0.0", "bind address")
			fs.Parse(os.Args[2:])
			runWebMode(NewEngine(), *addr, *port)
			return
		case "book":
			alphabeta.RunBookCommand(os.Args[2:])
			return
		case "help", "-h", "-help", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "pbrain-bango: unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	runPipeSession(NewEngine())
}

// usage prints the invocation forms. --port/--addr belong to the web
// subcommand only; the bare executable is the pipe-mode manager engine.
func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  pbrain-bango                     pipe mode: protocol over stdin/stdout (for managers)
  pbrain-bango web [flags]         web mode: protocol over a TCP socket
  pbrain-bango book validate <f>   sanity-check an opening book file
  pbrain-bango book build [flags]  grow an opening book by fixed-depth search

Web flags:
  --port N      TCP listening port (default 9527)
  --addr HOST   bind address (default 0.0.0.0)

Book build flags:
  --out F       output file (default book-built.json)
  --rule N      rule code: 0 freestyle, 4 renju (default 0)
  --size N      board size (default 15)
  --depth N     fixed search depth for scoring (default 8)
  --ply N       opening depth in plies (default 4)
  --width N     candidates kept per position (default 2)
  --margin N    keep candidates within this margin of the best (default 0)
  --time MS     total build budget (default 60000)
`)
}

// runWebMode serves the protocol over TCP. Sessions are serial: after END or
// a disconnect the engine state resets and the next client is served.
func runWebMode(e *Engine, addr string, port int) {
	e.webMode = true
	listener, err := net.Listen("tcp", net.JoinHostPort(addr, fmt.Sprint(port)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pbrain-bango: listen failed:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "pbrain-bango: web mode listening on", listener.Addr())
	acceptLoop(e, listener)
}

// acceptLoop serves connections one at a time. A session ends when the
// client disconnects or sends END; the board is then reset and the loop
// waits for the next client.
func acceptLoop(e *Engine, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return // listener closed
		}
		e.runSession(conn)
	}
}

// runSession runs one full protocol conversation over conn. It mirrors the
// pipe-mode setup: reader goroutine + thinker, joined at the end.
func (e *Engine) runSession(conn net.Conn) {
	e.resetSession()
	e.setIO(conn, conn)

	cmdChan := make(chan Command, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.readLoop(conn, cmdChan)
		close(cmdChan)
	}()

	e.thinkLoop(cmdChan)
	// session over (END or reader hit EOF): close the socket first so a
	// reader still blocked in conn.Read wakes up and the Wait below returns
	conn.Close()
	wg.Wait()
}

// runPipeSession is the classic stdin/stdout mode: the manager spawns the
// engine with pipes instead of a socket.
func runPipeSession(e *Engine) {
	cmdChan := make(chan Command, 16)

	// Thread 1: read commands from stdin.
	// This goroutine is the ONLY consumer of stdin, so the thinker never has
	// to read input while thinking. That structurally avoids the deadlock
	// described in the protocol (engine & manager waiting on each other).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.readLoop(e.in, cmdChan)
		close(cmdChan)
	}()

	// Thread 2: think and write replies.
	e.thinkLoop(cmdChan)

	wg.Wait()
}

// readLoop reads manager commands from in and forwards fully-parsed
// Command values to cmdChan. It handles the multi-line BOARD command by
// accumulating lines until DONE.
func (e *Engine) readLoop(in io.Reader, cmdChan chan<- Command) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		head := strings.ToUpper(fields[0])

		if head == "BOARD" {
			cells, ok := e.readBoardBody(scanner)
			if !ok {
				// known command failed to parse: the protocol says to answer
				// ERROR (not UNKNOWN) with a short message
				cmdChan <- Command{Type: "ERROR", Args: []string{"malformed BOARD"}}
				return
			}
			cmdChan <- Command{Type: "BOARD", Board: cells}
			continue
		}

		if head == "SWAP2BOARD" {
			pts, ok := e.readSwap2Body(scanner)
			if !ok {
				cmdChan <- Command{Type: "ERROR", Args: []string{"malformed SWAP2BOARD"}}
				return
			}
			cmdChan <- Command{Type: "SWAP2BOARD", Pts: pts}
			continue
		}

		cmdChan <- Command{Type: head, Args: fields[1:]}
	}
}

// readBoardBody consumes board lines ("x,y,id") until it sees DONE.
func (e *Engine) readBoardBody(scanner *bufio.Scanner) ([][3]int, bool) {
	var cells [][3]int
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "DONE") {
			return cells, true
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			return nil, false
		}
		var c [3]int
		var err error
		if c[0], err = atoi(parts[0]); err != nil {
			return nil, false
		}
		if c[1], err = atoi(parts[1]); err != nil {
			return nil, false
		}
		if c[2], err = atoi(parts[2]); err != nil {
			return nil, false
		}
		cells = append(cells, c)
	}
	return nil, false
}

// readSwap2Body consumes SWAP2BOARD stone lines ("x,y") until DONE.
func (e *Engine) readSwap2Body(scanner *bufio.Scanner) ([][2]int, bool) {
	var pts [][2]int
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "DONE") {
			return pts, true
		}
		parts := strings.Split(line, ",")
		if len(parts) != 2 {
			return nil, false
		}
		var p [2]int
		var err error
		if p[0], err = atoi(parts[0]); err != nil {
			return nil, false
		}
		if p[1], err = atoi(parts[1]); err != nil {
			return nil, false
		}
		pts = append(pts, p)
	}
	return nil, false
}

// thinkLoop is the second thread: it processes commands and writes replies,
// flushing stdout after every line so the manager is never left waiting.
func (e *Engine) thinkLoop(cmdChan <-chan Command) {
	for cmd := range cmdChan {
		switch cmd.Type {
		case "START":
			e.cmdStart(cmd)
		case "BEGIN":
			e.cmdBegin()
		case "TURN":
			e.cmdTurn(cmd)
		case "BOARD":
			e.cmdBoard(cmd)
		case "INFO":
			e.cmdInfo(cmd)
		case "END":
			// the protocol expects no reply and no further output. Pipe mode
			// terminates the process; web mode ends the session and the
			// accept loop serves the next client.
			if e.webMode {
				return
			}
			os.Exit(0)
		case "RESTART":
			e.resetBoard(e.size)
			e.writeLine("OK")
		case "ABOUT":
			e.writeLine(`name="pbrain-bango", version="0.1", author="cale && GLM5.3", ai="negamax+alphabeta+vcx"`)
		case "PRINT":
			e.cmdPrint()
		case "TAKEBACK":
			e.cmdTakeback(cmd)
		case "PLAY":
			e.cmdPlay(cmd)
		case "RECTSTART":
			e.cmdRectStart(cmd)
		case "SWAP2BOARD":
			e.cmdSwap2(cmd)
		case "ERROR":
			// injected by the reader when a known command's body failed to
			// parse; the protocol answer for this is ERROR [message]
			msg := "malformed command"
			if len(cmd.Args) > 0 {
				msg = strings.Join(cmd.Args, " ")
			}
			e.writeLine("ERROR " + msg)
		default:
			e.writeLine("UNKNOWN command: " + cmd.Type)
		}
	}
}

func (e *Engine) cmdStart(cmd Command) {
	size := 15
	if len(cmd.Args) > 0 {
		s, err := atoi(cmd.Args[0])
		if err != nil || s < 5 {
			// protocol: a size the brain doesn't accept must be answered ERROR
			e.writeLine("ERROR unsupported board size " + cmd.Args[0])
			return
		}
		size = s
	}
	e.size = size
	e.resetBoard(size)
	e.mu.Lock()
	e.ownIsBlack = true
	e.mu.Unlock()
	e.writeLine("OK")
}

func (e *Engine) cmdBegin() {
	e.mu.Lock()
	e.ownIsBlack = true // the engine opened, so it owns black
	e.mu.Unlock()
	x, y := e.aiMove()
	e.place(x, y, 1)
	e.writeLine(itoa(x) + "," + itoa(y))
}

func (e *Engine) cmdTurn(cmd Command) {
	if len(cmd.Args) < 1 {
		e.writeLine("ERROR missing move")
		return
	}
	ox, oy, ok := parseMove(cmd.Args[0])
	if !ok {
		e.writeLine("ERROR bad move")
		return
	}
	if e.cellOccupied(ox, oy) {
		// a stone already sits there — overwriting it would recolor the
		// board behind the manager's back, so refuse instead
		e.writeLine("ERROR occupied cell " + cmd.Args[0])
		return
	}
	if e.boardEmpty() {
		// the opponent opened, so the opponent owns black (renju forbidden side)
		e.mu.Lock()
		e.ownIsBlack = false
		e.mu.Unlock()
	}
	e.place(ox, oy, 2)

	x, y := e.aiMove()
	e.place(x, y, 1)
	e.writeLine(itoa(x) + "," + itoa(y))
}

// cmdBoard imposes the manager's position. Per protocol, after DONE the brain
// answers like TURN or BEGIN when it is to move — not with OK. Stones with
// field 3 (winning-line marks / renju forbidden points) are recorded as
// marked so the search treats those cells as occupied.
func (e *Engine) cmdBoard(cmd Command) {
	e.mu.Lock()
	size := e.size
	e.mu.Unlock()
	e.resetBoard(size)
	mine, theirs := 0, 0
	for _, c := range cmd.Board {
		if c[2] == 3 {
			e.markCell(c[0], c[1])
			continue
		}
		e.place(c[0], c[1], c[2])
		switch c[2] {
		case 1:
			mine++
		case 2:
			theirs++
		}
	}
	// renju boards arrive in move order: the first stone is black
	if len(cmd.Board) > 0 {
		e.mu.Lock()
		switch cmd.Board[0][2] {
		case 1:
			e.ownIsBlack = true
		case 2:
			e.ownIsBlack = false
		}
		e.mu.Unlock()
	}

	// it is our turn unless the opponent is to move (we have at least as many
	// stones as they do means we already moved last)
	if mine <= theirs {
		x, y := e.aiMove()
		e.place(x, y, 1)
		e.writeLine(itoa(x) + "," + itoa(y))
		return
	}
	e.writeLine("OK")
}

// cmdTakeback removes the stone at the given coordinates (undo).
func (e *Engine) cmdTakeback(cmd Command) {
	if len(cmd.Args) < 1 {
		e.writeLine("ERROR missing takeback")
		return
	}
	x, y, ok := parseMove(cmd.Args[0])
	if !ok {
		e.writeLine("ERROR bad takeback")
		return
	}
	e.unplace(x, y)
	e.writeLine("OK")
}

// cmdPlay imposes a move chosen by the manager (the answer to SUGGEST) and
// confirms it with the same coordinates.
func (e *Engine) cmdPlay(cmd Command) {
	if len(cmd.Args) < 1 {
		e.writeLine("ERROR missing play")
		return
	}
	x, y, ok := parseMove(cmd.Args[0])
	if !ok {
		e.writeLine("ERROR bad play")
		return
	}
	if e.cellOccupied(x, y) {
		e.writeLine("ERROR occupied cell " + cmd.Args[0])
		return
	}
	e.place(x, y, 1)
	e.writeLine(itoa(x) + "," + itoa(y))
}

// cmdRectStart handles the optional rectangular-board init. Only square
// boards are supported; the protocol allows an ERROR reply for the rest.
func (e *Engine) cmdRectStart(cmd Command) {
	if len(cmd.Args) < 1 {
		e.writeLine("ERROR missing size")
		return
	}
	parts := strings.Split(cmd.Args[0], ",")
	if len(parts) != 2 {
		e.writeLine("ERROR bad RECTSTART")
		return
	}
	w, err1 := atoi(parts[0])
	h, err2 := atoi(parts[1])
	if err1 != nil || err2 != nil || w != h || w < 5 {
		e.writeLine("ERROR rectangular or unsupported board")
		return
	}
	e.size = w
	e.resetBoard(w)
	e.mu.Lock()
	e.ownIsBlack = true
	e.mu.Unlock()
	e.writeLine("OK")
}

// cmdSwap2 answers the SWAP2 opening protocol. Stone colors alternate by
// position (1st/3rd/5th = black, 2nd/4th = white), matching the official
// example. With no stones we place a balanced trio; with three or five we
// take black via SWAP when black is clearly ahead, otherwise stay white and
// output the next stone.
func (e *Engine) cmdSwap2(cmd Command) {
	if len(cmd.Pts) == 0 {
		e.writeLine("7,7 6,6 8,8")
		return
	}
	if len(cmd.Pts) != 3 && len(cmd.Pts) != 5 {
		e.writeLine("ERROR unexpected SWAP2BOARD body")
		return
	}
	e.mu.Lock()
	n := e.size
	rule := e.info.Rule
	timeoutTurn := e.info.TimeoutTurn
	timeLeft := e.info.TimeLeft
	e.mu.Unlock()
	for _, pt := range cmd.Pts {
		if pt[0] < 0 || pt[1] < 0 || pt[0] >= n || pt[1] >= n {
			e.writeLine("ERROR bad swap2 stone")
			return
		}
	}

	// the swap2 decision needs search internals: evaluate the stones as
	// black, either swap or answer with white's next stone (alphabeta pkg)
	x, y, swap := alphabeta.Swap2Reply(n, rule, cmd.Pts, timeoutTurn, timeLeft)
	if swap {
		e.writeLine("SWAP")
		return
	}
	e.writeLine(itoa(x) + "," + itoa(y))
}

// cmdInfo parses and stores configuration pushed by the manager. The engine
// keeps the state but does not apply any rule judgement.
func (e *Engine) cmdInfo(cmd Command) {
	if len(cmd.Args) < 1 {
		return
	}
	key := strings.ToLower(cmd.Args[0])
	value := ""
	if len(cmd.Args) > 1 {
		value = strings.Join(cmd.Args[1:], " ")
	}

	e.mu.Lock()
	if e.info.raw == nil {
		e.info.raw = make(map[string]string)
	}
	e.info.raw[key] = value

	switch key {
	case "timeout_turn":
		e.info.TimeoutTurn = atoiOrZero(value)
	case "timeout_match":
		e.info.TimeoutMatch = atoiOrZero(value)
	case "max_memory":
		e.info.MaxMemory = atoi64OrZero(value)
		// the algorithm's per-game caches follow the budget: notify it now
		// so the next think already respects the new limit (Reset keeps the
		// old table when neither size nor budget changed)
		e.algo.Reset(e.size, e.info.MaxMemory)
	case "max_depth":
		e.info.MaxDepth = atoiOrZero(value)
	case "max_node":
		e.info.MaxNode = atoi64OrZero(value)
	case "time_left":
		e.info.TimeLeft = atoi64OrZero(value)
	case "game_type":
		e.info.GameType = atoiOrZero(value)
	case "rule":
		e.info.Rule = atoiOrZero(value)
	case "fast":
		e.info.Fast = value == "1" || strings.EqualFold(value, "true")
	case "folder":
		e.info.Folder = value // persistent files: opening book lives here
	}
	e.mu.Unlock()
}

func (e *Engine) cmdPrint() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, row := range e.renderBoard() {
		e.writeLine("MESSAGE " + row)
	}
}

// renderBoard returns one string per board row ('O' own, 'X' opponent,
// '.' empty). Caller must hold the mutex.
func (e *Engine) renderBoard() []string {
	rows := make([]string, len(e.board))
	for y := range e.board {
		var row strings.Builder
		for x := range e.board[y] {
			switch e.board[y][x] {
			case 1:
				row.WriteByte('O')
			case 2:
				row.WriteByte('X')
			default:
				row.WriteByte('.')
			}
		}
		rows[y] = row.String()
	}
	return rows
}
