package main

// Black-box protocol conformance tests: run the compiled engine binary and
// speak the Piskvork protocol to it over pipes, asserting the replies against
// the official spec (https://plastovicka.github.io/protocl2en.htm).
//
// Build the binary first (TestMain skips the suite when it is missing).

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBinary = "./pbrain-bango-test"

func TestMain(m *testing.M) {
	// build once for the black-box suite
	build := exec.Command("go", "build", "-o", testBinary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "skip black-box suite: build failed: %v\n%s", err, out)
		os.Remove(testBinary)
		os.Exit(m.Run())
	}
	code := m.Run()
	os.Remove(testBinary)
	os.Exit(code)
}

type session struct {
	cmd    *exec.Cmd
	stdin  chan string
	lines  chan string
	closed chan struct{}
	once   sync.Once
}

// startSession launches the engine and two pump goroutines.
func startSession(t *testing.T) *session {
	t.Helper()
	exe, err := filepath.Abs(testBinary)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command(exe)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s := &session{
		cmd:    cmd,
		stdin:  make(chan string, 16),
		lines:  make(chan string, 256),
		closed: make(chan struct{}),
	}
	go func() {
		w := bufio.NewWriter(stdinPipe)
		for line := range s.stdin {
			w.WriteString(line)
			w.WriteString("\r\n")
			w.Flush()
		}
	}()
	go func() {
		r := bufio.NewScanner(stdoutPipe)
		r.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for r.Scan() {
			s.lines <- r.Text()
		}
		close(s.closed)
	}()
	t.Cleanup(func() {
		s.once.Do(func() {
			close(s.stdin)
			s.cmd.Process.Kill()
			s.cmd.Wait()
		})
	})
	return s
}

// send writes one command line.
func (s *session) send(line string) { s.stdin <- line }

// expect reads lines until deadline and asserts one non-empty reply.
func expect(t *testing.T, s *session, timeout time.Duration, want string) string {
	t.Helper()
	select {
	case got := <-s.lines:
		got = strings.TrimRight(got, "\r")
		if got != want {
			t.Fatalf("reply = %q, want %q", got, want)
		}
		return got
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for %q", want)
		return ""
	}
}

// expectMove asserts a "X,Y" reply inside the board and returns it.
func expectMove(t *testing.T, s *session, timeout time.Duration, size int) (int, int) {
	t.Helper()
	select {
	case got := <-s.lines:
		got = strings.TrimRight(got, "\r")
		var x, y int
		if _, err := fmt.Sscanf(got, "%d,%d", &x, &y); err != nil {
			t.Fatalf("reply %q is not a move", got)
		}
		if x < 0 || y < 0 || x >= size || y >= size {
			t.Fatalf("move (%d,%d) out of board %d", x, y, size)
		}
		return x, y
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for a move")
		return -1, -1
	}
}

// TestProtocolStartSizes: START answers OK for every size the engine
// supports (Gomocup requires 20; 5 is the floor) and ERROR for anything
// else — never a silent fallback.
func TestProtocolStartSizes(t *testing.T) {
	cases := []struct {
		arg  string
		want string
	}{
		{"20", "OK"},
		{"5", "OK"},
		{"4", "ERROR unsupported board size 4"},
		{"3", "ERROR unsupported board size 3"},
		{"abc", "ERROR unsupported board size abc"},
	}
	for _, c := range cases {
		s := startSession(t)
		s.send("START " + c.arg)
		expect(t, s, 2*time.Second, c.want)
	}
}

// TestProtocolBadMoveArgs: malformed TURN/TAKEBACK arguments answer ERROR
// and leave the engine alive. Out-of-board coordinates parse as integers and
// are ignored by place() — the engine still answers for itself (permissive
// by design; the manager is trusted to send on-board moves).
func TestProtocolBadMoveArgs(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO timeout_turn 200")

	s.send("TURN")
	expect(t, s, 2*time.Second, "ERROR missing move")
	s.send("TURN abc")
	expect(t, s, 2*time.Second, "ERROR bad move")
	s.send("TURN 7")
	expect(t, s, 2*time.Second, "ERROR bad move")
	s.send("TAKEBACK")
	expect(t, s, 2*time.Second, "ERROR missing takeback")
	s.send("TAKEBACK zz")
	expect(t, s, 2*time.Second, "ERROR bad takeback")

	s.send("TURN 99,99")
	if x, y := expectMove(t, s, 5*time.Second, 15); x != 7 || y != 7 {
		t.Fatalf("out-of-board TURN answer = (%d,%d), want center (7,7)", x, y)
	}

	// still alive and answering normally afterwards
	s.send("TURN 7,8")
	expectMove(t, s, 5*time.Second, 15)
}

// TestProtocolTurnOccupied: a TURN (or PLAY) landing on a stone already on
// the board is refused with ERROR — silently overwriting it would recolor
// the board behind the manager's back. The refused command must leave the
// position untouched and the engine fully functional.
func TestProtocolTurnOccupied(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO timeout_turn 500")

	// normal exchange: opponent takes 7,7, the engine answers elsewhere
	s.send("TURN 7,7")
	fx, fy := expectMove(t, s, 5*time.Second, 15)

	// the occupied cell is refused, with no reply move played
	s.send("TURN 7,7")
	expect(t, s, 2*time.Second, "ERROR occupied cell 7,7")
	s.send("PLAY 7,7")
	expect(t, s, 2*time.Second, "ERROR occupied cell 7,7")

	// PRINT shows exactly the two stones of the first exchange: nothing
	// added, nothing recolored
	s.send("PRINT")
	xs, os := 0, 0
	for i := 0; i < 15; i++ {
		select {
		case got := <-s.lines:
			got = strings.TrimRight(got, "\r")
			row, found := strings.CutPrefix(got, "MESSAGE ")
			if !found || len(row) != 15 {
				t.Fatalf("PRINT row %d = %q, want a 15-wide MESSAGE row", i, got)
			}
			xs += strings.Count(row, "X")
			os += strings.Count(row, "O")
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout reading PRINT row %d", i)
		}
	}
	if xs != 1 || os != 1 {
		t.Fatalf("board after refused TURN/PLAY has %d X / %d O, want 1/1", xs, os)
	}

	// the engine stays fully functional and avoids both taken cells. The
	// probe cell must not collide with the engine's first reply, which since
	// the tengen-reply diversification can be any neighbour of (7,7) —
	// direct or diagonal — so probe from the far side, filtered against fx,fy.
	probe := [2]int{7, 8}
	if probe == [2]int{fx, fy} {
		probe = [2]int{7, 6}
	}
	s.send(fmt.Sprintf("TURN %d,%d", probe[0], probe[1]))
	x, y := expectMove(t, s, 5*time.Second, 15)
	if (x == 7 && y == 7) || (x == fx && y == fy) || (x == probe[0] && y == probe[1]) {
		t.Fatalf("reply (%d,%d) landed on an occupied cell", x, y)
	}

	// out-of-board stays tolerated by design (see TestProtocolBadMoveArgs)
	s.send("TURN 99,99")
	expectMove(t, s, 5*time.Second, 15)
}

// TestProtocolBoardAnswersMove: the official spec requires the brain to
// answer BOARD...DONE with a move when it is to move — not OK.
func TestProtocolBoardAnswersMove(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO timeout_turn 1000")
	s.send("INFO timeout_match 300000")
	s.send("INFO time_left 300000")
	// a 2-stone position, opponent moved last (theirs > mine) → our turn
	s.send("BOARD")
	s.send("7,7,1")
	s.send("8,8,2")
	s.send("7,8,2")
	s.send("DONE")
	expectMove(t, s, 5*time.Second, 15)
}

// TestProtocolBoardFieldThree: field-3 lines (renju forbidden marks) must not
// crash or get played on; the engine still answers with a legal move.
func TestProtocolBoardFieldThree(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("BOARD")
	s.send("7,7,1")
	s.send("8,8,2")
	s.send("6,6,3") // marked cell: winning-line stone / forbidden point
	s.send("DONE")
	x, y := expectMove(t, s, 5*time.Second, 15)
	if x == 6 && y == 6 {
		t.Fatal("engine played on a field-3 marked cell")
	}
}

// TestProtocolBoardNotOurTurn: per protocol id 1 is "own stone". A BOARD
// where we (as black, first mover) already have more stones than the
// opponent means the opponent is to move next, so the brain answers OK
// instead of inventing a move.
func TestProtocolBoardNotOurTurn(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("BOARD")
	s.send("7,7,1")
	s.send("7,8,2")
	s.send("8,8,1")
	s.send("DONE")
	expect(t, s, 2*time.Second, "OK")
}

// TestProtocolEndSilent: END expects no reply and the engine must exit.
func TestProtocolEndSilent(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("END")
	select {
	case got := <-s.lines:
		t.Fatalf("engine wrote %q after END; protocol expects silence", got)
	case <-time.After(500 * time.Millisecond):
	}
	done := make(chan struct{})
	go func() { s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine did not exit after END")
	}
}

// tcpSession is a black-box session over the web mode's TCP listener.
type tcpSession struct {
	conn net.Conn
	s    *session
}

// webServer is one engine process running in web mode; sessions dial it
// sequentially to prove the process survives across connections.
type webServer struct {
	cmd  *exec.Cmd
	port int
}

// freePort asks the kernel for an unused TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startWebServer launches one engine process in web mode via the `web`
// subcommand, bound to localhost on a free port.
func startWebServer(t *testing.T) *webServer {
	t.Helper()
	port := freePort(t)
	exe, err := filepath.Abs(testBinary)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command(exe, "web", "--port", fmt.Sprint(port), "--addr", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start web engine: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	return &webServer{cmd: cmd, port: port}
}

// connect dials the server and wires a line-reading session over the socket.
func (ws *webServer) connect(t *testing.T) *tcpSession {
	t.Helper()
	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ws.port), time.Second)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine never listened on %d: %v", ws.port, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { conn.Close() })

	s := &session{
		lines:  make(chan string, 256),
		closed: make(chan struct{}),
	}
	go func() {
		r := bufio.NewScanner(conn)
		for r.Scan() {
			s.lines <- r.Text()
		}
		close(s.closed)
	}()
	return &tcpSession{conn: conn, s: s}
}

// send writes one command line (CRLF terminated) to the socket.
func (ts *tcpSession) send(line string) {
	ts.conn.Write(append([]byte(line), '\r', '\n'))
}

// expectClose waits for the server to close the connection (EOF).
func (ts *tcpSession) expectClose(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case got := <-ts.s.lines:
		t.Fatalf("expected EOF, got line %q", got)
	case <-ts.s.closed:
	case <-time.After(timeout):
		t.Fatal("connection was not closed by the server")
	}
}

// TestWebModeBasicFlow: over TCP the protocol behaves exactly like pipe mode,
// and END closes the connection silently.
func TestWebModeBasicFlow(t *testing.T) {
	ws := startWebServer(t)
	ts := ws.connect(t)
	ts.send("START 15")
	expect(t, ts.s, 2*time.Second, "OK")
	ts.send("TURN 7,8")
	expectMove(t, ts.s, 5*time.Second, 15)
	ts.send("ABOUT")
	select {
	case got := <-ts.s.lines:
		if !strings.HasPrefix(strings.TrimRight(got, "\r"), `name="pbrain-bango", version="0.1"`) {
			t.Fatalf("ABOUT = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ABOUT reply")
	}
	ts.send("END")
	ts.expectClose(t, 2*time.Second)
}

// TestWebModeSecondSession: after END the process stays alive and serves a
// fresh session (reset board) to the next connection — same server process.
func TestWebModeSecondSession(t *testing.T) {
	ws := startWebServer(t)

	ts := ws.connect(t)
	ts.send("START 20")
	expect(t, ts.s, 2*time.Second, "OK")
	ts.send("END")
	ts.expectClose(t, 2*time.Second)

	// second client, SAME process: fresh engine state, default board size 15
	ts2 := ws.connect(t)
	ts2.send("BEGIN")
	if x, y := expectMove(t, ts2.s, 5*time.Second, 15); x != 7 || y != 7 {
		t.Fatalf("fresh session BEGIN = (%d,%d), want center (7,7)", x, y)
	}
	ts2.send("END")
	ts2.expectClose(t, 2*time.Second)
}

// TestWebModeDefaults: no flags at all means classic pipe mode (the binary
// answers on stdin/stdout as before).
func TestWebModeDefaults(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
}

// TestWebSubcommandDefaultPort: `pbrain-bango web` with no flags listens on
// the documented default 0.0.0.0:9527. Skipped while that port is occupied
// by another application on this machine.
func TestWebSubcommandDefaultPort(t *testing.T) {
	exe, err := filepath.Abs(testBinary)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command(exe, "web")
	// guarded stderr: the banner poller below reads while the child writes,
	// a plain bytes.Buffer would be a data race (found by -race)
	stderr := newSyncBuffer()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	// wait for the "listening" banner (or notice an early exit)
	addr := ""
	deadline := time.Now().Add(3 * time.Second)
	for addr == "" {
		if s := stderr.String(); strings.Contains(s, "web mode listening on") {
			addr = s
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(addr, ":9527") {
		if strings.Contains(stderr.String(), "listen failed") {
			t.Skip("default port 9527 busy on this machine")
		}
		t.Fatalf("no listening banner, stderr = %q", stderr.String())
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:9527", time.Second)
	if err != nil {
		t.Fatalf("dial default port: %v", err)
	}
	defer conn.Close()
	// handshake check: whatever accepted the connection must speak OUR
	// protocol (the port may be shared with an unrelated local app)
	conn.Write([]byte("START 15\r\n"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read after START: %v", err)
	}
	if strings.TrimRight(line, "\r\n") != "OK" {
		t.Skipf("port 9527 answered by another application (%q)", strings.TrimRight(line, "\r\n"))
	}
}

// TestPipeModeRejectsWebFlags: --port/--addr are web-subcommand-only. The
// bare executable must not start a listener (or silently ignore them).
func TestPipeModeRejectsWebFlags(t *testing.T) {
	exe, err := filepath.Abs(testBinary)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command(exe, "--port", "8080")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		t.Fatal("--port without web subcommand must fail, but the process exited 0")
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command message", stderr.String())
	}
}

// TestProtocolOptionalCommands: TAKEBACK, PLAY, RECTSTART and SWAP2BOARD
// per the official spec's optional-command section.
func TestProtocolOptionalCommands(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")

	// PLAY imposes a move; the brain confirms with the same coordinates
	s.send("PLAY 7,7")
	expect(t, s, 2*time.Second, "7,7")

	// TAKEBACK removes it
	s.send("TAKEBACK 7,7")
	expect(t, s, 2*time.Second, "OK")

	// square RECTSTART behaves like START
	s.send("RECTSTART 9,9")
	expect(t, s, 2*time.Second, "OK")
	// rectangular boards are refused with ERROR (allowed by the spec)
	s.send("RECTSTART 30,20")
	expect(t, s, 2*time.Second, "ERROR rectangular or unsupported board")

	// SWAP2BOARD case 1: the brain places the three opening stones
	s.send("SWAP2BOARD")
	s.send("DONE")
	expect(t, s, 2*time.Second, "7,7 6,6 8,8")

	// SWAP2BOARD case 2: three stones given (board is 9x9 here), the brain
	// swaps or stays with a 4th-stone coordinate — both are legal answers
	s.send("SWAP2BOARD")
	s.send("4,4")
	s.send("5,4")
	s.send("3,3")
	s.send("DONE")
	select {
	case got := <-s.lines:
		got = strings.TrimRight(got, "\r")
		if got != "SWAP" {
			var x, y int
			if _, err := fmt.Sscanf(got, "%d,%d", &x, &y); err != nil {
				t.Fatalf("SWAP2BOARD case 2 reply %q is neither SWAP nor a move", got)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for SWAP2BOARD case 2 reply")
	}
}

// TestProtocolRenjuEngineAvoidsForbidden: with INFO rule 4 and the engine as
// black, the move answered after BOARD never lands on a forbidden point.
func TestProtocolRenjuEngineAvoidsForbidden(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO rule 4")
	// black (own) pairs cross at (9,8): horizontal (7,8),(8,8) + vertical
	// (9,6),(9,7) — placing there makes a double three. Four stones each
	// keep it black's turn, so the brain answers with a move.
	s.send("BOARD")
	s.send("7,8,1")
	s.send("8,8,1")
	s.send("9,7,1")
	s.send("9,6,1")
	s.send("3,3,2")
	s.send("11,11,2")
	s.send("4,3,2")
	s.send("10,12,2")
	s.send("DONE")
	x, y := expectMove(t, s, 5*time.Second, 15)
	if x == 9 && y == 8 {
		t.Fatal("engine played the double-three forbidden point (9,8) under renju")
	}
}

// TestProtocolUnknownCommand: strange lines get UNKNOWN and the engine stays
// alive (it must not exit).
func TestProtocolUnknownCommand(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("FOOBAR 1 2")
	expect(t, s, 2*time.Second, "UNKNOWN command: FOOBAR")
	// still alive: a follow-up command works
	s.send("ABOUT")
	select {
	case got := <-s.lines:
		if !strings.HasPrefix(strings.TrimRight(got, "\r"), `name="pbrain-bango"`) {
			t.Fatalf("ABOUT after UNKNOWN = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine dead after UNKNOWN")
	}
}

// TestProtocolInfoMaxDepthMaxNode: the optional INFO max_depth / max_node
// keys must be accepted (no ERROR) and produce sane play end-to-end — a
// depth-2 engine must still answer legal moves quickly.
func TestProtocolInfoMaxDepthMaxNode(t *testing.T) {
	s := startSession(t)
	s.send("START 15")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO timeout_turn 2000")
	s.send("INFO max_depth 2")
	s.send("INFO max_node 20000")

	s.send("TURN 7,7")
	x, y := expectMove(t, s, 5*time.Second, 15)
	if x < 0 || x >= 15 || y < 0 || y >= 15 {
		t.Fatalf("move (%d,%d) out of board under max_depth/max_node", x, y)
	}

	// a second move keeps working: the budget counters are per-think. The
	// probe cell avoids the first reply — the tengen answer may now be any
	// neighbour of (7,7), including the previously impossible (8,8).
	s.send("TURN 9,9")
	expectMove(t, s, 5*time.Second, 15)
}

// syncBuffer is a mutex-guarded bytes.Buffer for concurrent read/write, used
// as a child process's stderr collector.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
