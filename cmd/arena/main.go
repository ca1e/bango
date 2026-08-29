// Command arena pits two pbrain-* engines against each other over the
// Piskvork protocol, reproducing the README's acceptance flow (random
// openings, colours alternated, fixed per-move budget) as one reproducible
// command:
//
//	go run ./cmd/arena ./engineA ./engineB -games 50 -time-turn 400
//
// Each game runs fresh engine processes on pipes. The harness validates
// legality (in-board, empty cell), five-in-a-row per the chosen rule, board
// exhaustion, timeouts and crashes — an illegal move or a dead engine loses
// the game. Renju forbidden points are the engines' own responsibility (the
// unit suite covers them); the arena only scores finished games.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	empty = 0
	black = 1
	white = 2
)

type gameRecord struct {
	Game   int    `json:"game"`
	Black  string `json:"black"`
	Winner string `json:"winner"` // "A", "B" or "draw"
	Plies  int    `json:"plies"`
	Reason string `json:"reason"`
}

func main() {
	games := flag.Int("games", 20, "number of games")
	timeTurn := flag.Int("time-turn", 400, "per-move budget in ms for engine A (INFO timeout_turn)")
	timeTurnB := flag.Int("time-turn-b", 0, "per-move budget in ms for engine B (0 = same as A); handicap matches")
	size := flag.Int("size", 15, "board size")
	rule := flag.Int("rule", 0, "rule code: 0 freestyle, 1 standard, 4 renju, 8/9 caro")
	seed := flag.Int64("seed", 1, "RNG seed for opening randomisation")
	maxPlies := flag.Int("max-plies", 400, "safety cap on plies per game")
	jsonOut := flag.String("json", "", "optional path for a JSON result file")
	verbose := flag.Bool("verbose", false, "print every move as it is played")
	flag.Parse()
	args := flag.Args()
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: arena [flags] <engineA> <engineB>")
		os.Exit(2)
	}
	engineA, engineB := args[0], args[1]
	rng := rand.New(rand.NewSource(*seed))

	scoreA, scoreB, draws := 0, 0, 0
	records := make([]gameRecord, 0, *games)

	for g := 0; g < *games; g++ {
		blackIsA := g%2 == 0
		timeB := *timeTurnB
		if timeB == 0 {
			timeB = *timeTurn
		}
		winner, plies, reason := playGame(engineA, engineB, blackIsA,
			*size, *rule, *timeTurn, timeB, *maxPlies, *verbose, rng)
		rec := gameRecord{Game: g + 1, Black: "A", Winner: winner, Plies: plies, Reason: reason}
		if !blackIsA {
			rec.Black = "B"
		}
		records = append(records, rec)
		switch winner {
		case "A":
			scoreA++
		case "B":
			scoreB++
		default:
			draws++
		}
		fmt.Printf("game %2d: black=%s winner=%-4s plies=%3d (%s)\n",
			g+1, rec.Black, winner, plies, reason)
	}

	budget := fmt.Sprintf("%dms/move", *timeTurn)
	if *timeTurnB != 0 {
		budget = fmt.Sprintf("A %dms vs B %dms", *timeTurn, *timeTurnB)
	}
	fmt.Printf("score: A %d - B %d (%d draws) over %d games (seed %d, %s, %dx%d rule %d)\n",
		scoreA, scoreB, draws, *games, *seed, budget, *size, *size, *rule)

	if *jsonOut != "" {
		out := struct {
			ScoreA int          `json:"scoreA"`
			ScoreB int          `json:"scoreB"`
			Draws  int          `json:"draws"`
			Games  []gameRecord `json:"games"`
		}{scoreA, scoreB, draws, records}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		} else if err := os.WriteFile(*jsonOut, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *jsonOut, err)
		}
	}
}

// engineProc is one running pbrain process talking the Piskvork protocol on
// stdio. Lines are pumped through a channel so reads can time out; the pump
// goroutine dies with the pipe when the process is killed.
type engineProc struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	err   chan error
}

func startEngine(path, label string) (*engineProc, error) {
	cmd := exec.Command(path)
	cmd.Stderr = os.Stderr // engine diagnostics pass through
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	e := &engineProc{
		name:  label,
		cmd:   cmd,
		stdin: stdin,
		lines: make(chan string, 32),
		err:   make(chan error, 1),
	}
	go func() {
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				e.lines <- strings.TrimRight(line, "\r\n")
			}
			if err != nil {
				e.err <- err
				return
			}
		}
	}()
	return e, nil
}

func (e *engineProc) send(format string, args ...any) error {
	_, err := fmt.Fprintf(e.stdin, format+"\r\n", args...)
	return err
}

// readLine waits for one reply line, treating EOF as an error.
func (e *engineProc) readLine(timeout time.Duration) (string, error) {
	select {
	case line, ok := <-e.lines:
		if !ok {
			return "", fmt.Errorf("%s: engine closed stdout", e.name)
		}
		return line, nil
	case err := <-e.err:
		return "", fmt.Errorf("%s: engine exited (%v)", e.name, err)
	case <-time.After(timeout):
		return "", fmt.Errorf("%s: no reply within %v", e.name, timeout)
	}
}

func (e *engineProc) stop() {
	_ = e.stdin.Close()
	_ = e.cmd.Process.Kill()
	_ = e.cmd.Wait()
}

// playGame drives one full game and returns the winner ("A"/"B"/"draw"),
// the ply count and the reason string.
func playGame(pathA, pathB string, blackIsA bool, size, rule, timeTurnA, timeTurnB, maxPlies int,
	verbose bool, rng *rand.Rand) (string, int, string) {

	a, err := startEngine(pathA, "A")
	if err != nil {
		return "B", 0, "engine A failed to start: " + err.Error()
	}
	b, err := startEngine(pathB, "B")
	if err != nil {
		a.stop()
		return "A", 0, "engine B failed to start: " + err.Error()
	}
	defer a.stop()
	defer b.stop()

	moveTimeout := time.Duration(max(timeTurnA, timeTurnB)*3+2000) * time.Millisecond

	// handshake: START expects OK; INFO lines expect no reply. Engines may
	// run on different budgets (handicap matches): timeTurnA vs timeTurnB.
	for _, tc := range []struct {
		e        *engineProc
		timeTurn int
	}{{a, timeTurnA}, {b, timeTurnB}} {
		if err := tc.e.send("START %d", size); err != nil {
			return opponentOf(tc.e.name), 0, "START failed: " + err.Error()
		}
		if line, err := tc.e.readLine(moveTimeout); err != nil || strings.TrimSpace(line) != "OK" {
			return opponentOf(tc.e.name), 0, fmt.Sprintf("START reply %q (%v)", line, err)
		}
		_ = tc.e.send("INFO rule %d", rule)
		_ = tc.e.send("INFO timeout_turn %d", tc.timeTurn)
		_ = tc.e.send("INFO timeout_match 0")
		_ = tc.e.send("INFO time_left 0")
	}

	board := make([]int, size*size)
	var stones [][3]int

	// random opening for game diversity: half the games start from the
	// centre (BEGIN), half from a random first stone near the centre imposed
	// on the black engine with PLAY. Two pre-placed stones are NOT possible:
	// a BOARD with equal stone counts makes every engine believe it is to
	// move, so the second engine could never learn the position (it played
	// blind and forfeited on occupied cells) — one forced first stone reaches
	// both engines cleanly (owner via PLAY, opponent via the first TURN).
	if rng.Intn(2) == 0 {
		c := size / 2
		bx, by := c+rng.Intn(5)-2, c+rng.Intn(5)-2
		board[by*size+bx] = black
		stones = append(stones, [3]int{bx, by, black})
		if verbose {
			fmt.Printf("  opening: PLAY black %d,%d\n", bx, by)
		}
	}

	engines := map[int]*engineProc{black: a, white: b}
	if !blackIsA {
		engines[black] = b
		engines[white] = a
	}

	toMove := black
	var prev [2]int
	if len(stones) > 0 {
		// impose the random first stone; PLAY echoes the coordinates back
		e := engines[black]
		_ = e.send("PLAY %d,%d", stones[0][0], stones[0][1])
		reply, err := e.readLine(moveTimeout)
		if err != nil {
			return opponentOf(e.name), 0, err.Error()
		}
		x, y, ok := parseCoords(reply)
		if !ok || x != stones[0][0] || y != stones[0][1] {
			return opponentOf(e.name), 0, fmt.Sprintf("%s PLAY echo %q", e.name, reply)
		}
		prev = [2]int{stones[0][0], stones[0][1]}
		toMove = white
	}

	plies := 0
	reason := "ply limit"
	winner := "draw"
	for plies = 0; plies < maxPlies; plies++ {
		e := engines[toMove]
		if plies == 0 && len(stones) == 0 {
			_ = e.send("BEGIN")
		} else {
			_ = e.send("TURN %d,%d", prev[0], prev[1])
		}

		reply, err := e.readLine(moveTimeout)
		if err != nil {
			return opponentOf(e.name), plies, err.Error()
		}
		x, y, ok := parseCoords(reply)
		if !ok {
			return opponentOf(e.name), plies, fmt.Sprintf("%s replied %q, not a move", e.name, reply)
		}
		if x < 0 || y < 0 || x >= size || y >= size || board[y*size+x] != empty {
			return opponentOf(e.name), plies,
				fmt.Sprintf("%s played illegal (%d,%d)", e.name, x, y)
		}
		board[y*size+x] = toMove
		prev = [2]int{x, y}
		if verbose {
			color := "black"
			if toMove == white {
				color = "white"
			}
			fmt.Printf("  %3d: %s (%s) -> %d,%d\n", plies+1, e.name, color, x, y)
		}

		if fiveAt(board, size, x, y, rule) {
			winner, reason, plies = e.name, "five", plies+1
			return winner, plies, reason
		}
		if boardFull(board) {
			return "draw", plies + 1, "board full"
		}
		toMove = 3 - toMove
	}
	return winner, maxPlies, reason
}

func opponentOf(name string) string {
	if name == "A" {
		return "B"
	}
	return "A"
}

func parseCoords(s string) (int, int, bool) {
	xy := strings.Split(strings.TrimSpace(s), ",")
	if len(xy) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(xy[0]))
	y, err2 := strconv.Atoi(strings.TrimSpace(xy[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return x, y, true
}

func boardFull(board []int) bool {
	for _, v := range board {
		if v == empty {
			return false
		}
	}
	return true
}

// fiveAt reports whether the stone just placed at (x,y) wins under the rule.
func fiveAt(board []int, n, x, y, rule int) bool {
	side := board[y*n+x]
	dirs := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for _, d := range dirs {
		a := countDir(board, n, x, y, -d[0], -d[1], side)
		b := countDir(board, n, x, y, d[0], d[1], side)
		c := 1 + a + b
		switch rule {
		case 0: // freestyle: five or more
			if c >= 5 {
				return true
			}
		case 1: // standard: exactly five
			if c == 5 {
				return true
			}
		case 4: // renju: black exactly five, white five or more
			if side == black && c == 5 {
				return true
			}
			if side == white && c >= 5 {
				return true
			}
		case 8, 9: // caro: exactly five, unless BOTH ends are blocked by the
			// opponent; the board edge counts as open
			if c == 5 {
				e1Blocked := endBlocked(board, n, x-d[0]*(a+1), y-d[1]*(a+1), side)
				e2Blocked := endBlocked(board, n, x+d[0]*(b+1), y+d[1]*(b+1), side)
				if !(e1Blocked && e2Blocked) {
					return true
				}
			}
		default:
			if c >= 5 {
				return true
			}
		}
	}
	return false
}

func countDir(board []int, n, x, y, dx, dy, side int) int {
	c := 0
	for {
		x += dx
		y += dy
		if x < 0 || y < 0 || x >= n || y >= n || board[y*n+x] != side {
			return c
		}
		c++
	}
}

// endBlocked reports whether the cell beyond the run's end (already one step
// outside the last stone) holds the opponent's stone. Out-of-board counts as
// open, mirroring the engine's isOpp.
func endBlocked(board []int, n, x, y, side int) bool {
	if x < 0 || y < 0 || x >= n || y >= n {
		return false
	}
	return board[y*n+x] == 3-side
}
