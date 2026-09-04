// Command selfplay runs mass alphabeta-vs-alphabeta self-play over the
// Piskvork protocol and logs every game as one JSON line: opening, per-move
// wall times, result and reason. Protocol flow mirrors cmd/arena (one fresh
// process pair per game, random first stone or tengen opening, colours
// alternated) but parallelises the run and records move timings.
//
//	go run ./cmd/selfplay -games 10000 -workers 12 -json /tmp/selfplay.jsonl
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
	"sync"
	"time"
)

type moveRec struct {
	N  int    `json:"n"`
	S  string `json:"s"` // "B" or "W"
	X  int    `json:"x"`
	Y  int    `json:"y"`
	MS int64  `json:"ms"` // wall time from TURN/BEGIN send to reply
}

type gameRec struct {
	Game    int       `json:"game"`
	Seed    int64     `json:"seed"`
	TT      int       `json:"tt"` // INFO timeout_turn (ms) used by both sides
	Opening [2]int    `json:"open"`
	Plies   int       `json:"plies"`
	Winner  string    `json:"winner"` // "B", "W" or "D"
	Reason  string    `json:"reason"`
	DurMS   int64     `json:"dur_ms"`
	Moves   []moveRec `json:"moves"`
}

type engineProc struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	err   chan error
}

func startEngine(path, label string) (*engineProc, error) {
	cmd := exec.Command(path)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", label, sc.Text())
		}
	}()
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
	e := &engineProc{name: label, cmd: cmd, stdin: stdin, lines: make(chan string, 32), err: make(chan error, 1)}
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

func (e *engineProc) readLine(timeout time.Duration) (string, error) {
	select {
	case line, ok := <-e.lines:
		if !ok {
			return "", fmt.Errorf("%s: closed stdout", e.name)
		}
		return line, nil
	case err := <-e.err:
		return "", fmt.Errorf("%s: exited (%v)", e.name, err)
	case <-time.After(timeout):
		return "", fmt.Errorf("%s: no reply within %v", e.name, timeout)
	}
}

func (e *engineProc) stop() {
	_ = e.stdin.Close()
	_ = e.cmd.Process.Kill()
	_ = e.cmd.Wait()
}

func parseCoords(s string) (int, int, bool) {
	xy := strings.Split(strings.TrimSpace(s), ",")
	if len(xy) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(xy[0]))
	y, err2 := strconv.Atoi(strings.TrimSpace(xy[1]))
	return x, y, err1 == nil && err2 == nil
}

// fiveAt reports whether the stone just placed wins (freestyle: five or more).
func fiveAt(board []int, n, x, y int) bool {
	side := board[y*n+x]
	dirs := [4][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	for _, d := range dirs {
		c := 1
		for _, s := range [2]int{-1, 1} {
			for i := 1; ; i++ {
				cx, cy := x+d[0]*i*s, y+d[1]*i*s
				if cx < 0 || cy < 0 || cx >= n || cy >= n || board[cy*n+cx] != side {
					break
				}
				c++
			}
		}
		if c >= 5 {
			return true
		}
	}
	return false
}

// playGame runs one self-play game: both colours are the same engine binary,
// A and B alternate as black per game. The returned record always carries the
// moves made so far; an early exit records the deciding reason.
func playGame(id int, seed int64, path string, blackIsA bool, n, tt, maxPlies int, rng *rand.Rand) gameRec {
	rec := gameRec{Game: id, Seed: seed, TT: tt, Winner: "D", Reason: "ply limit"}
	start := time.Now()

	a, err := startEngine(path, fmt.Sprintf("g%d-A", id))
	if err != nil {
		rec.Reason = "engine A failed to start"
		return rec
	}
	b, err := startEngine(path, fmt.Sprintf("g%d-B", id))
	if err != nil {
		a.stop()
		rec.Reason = "engine B failed to start"
		return rec
	}
	defer a.stop()
	defer b.stop()

	black, white := a, b
	if !blackIsA {
		black, white = b, a
	}
	lose := func(loser string, reason string) { // loser: "B" or "W"
		rec.Reason = reason
		if loser == "B" {
			rec.Winner = "W"
		} else {
			rec.Winner = "B"
		}
	}

	moveTimeout := time.Duration(tt*3+1500) * time.Millisecond
	for _, e := range []*engineProc{a, b} {
		if err := e.send("START %d", n); err != nil {
			lose("B", "START failed: "+err.Error())
			rec.DurMS = time.Since(start).Milliseconds()
			return rec
		}
		line, err := e.readLine(moveTimeout)
		if err != nil || strings.TrimSpace(line) != "OK" {
			lose("B", fmt.Sprintf("START reply %q (%v)", line, err))
			rec.DurMS = time.Since(start).Milliseconds()
			return rec
		}
		_ = e.send("INFO rule 0")
		_ = e.send("INFO timeout_turn %d", tt)
		_ = e.send("INFO timeout_match 0")
		_ = e.send("INFO time_left 0")
	}

	board := make([]int, n*n)
	toMove := black
	var prev [2]int
	// opening: half the games a random first stone in the centre 9x9 imposed
	// on black via PLAY (white learns it from the first TURN), half tengen
	if rng.Intn(2) == 0 {
		bx, by := 3+rng.Intn(9), 3+rng.Intn(9)
		rec.Opening = [2]int{bx, by}
		if err := black.send("PLAY %d,%d", bx, by); err != nil {
			lose("B", "PLAY failed: "+err.Error())
			rec.DurMS = time.Since(start).Milliseconds()
			return rec
		}
		line, err := black.readLine(moveTimeout)
		if err != nil {
			lose("B", "PLAY echo error: "+err.Error())
			rec.DurMS = time.Since(start).Milliseconds()
			return rec
		}
		x, y, ok := parseCoords(line)
		if !ok || x != bx || y != by {
			lose("B", "PLAY echo mismatch")
			rec.DurMS = time.Since(start).Milliseconds()
			return rec
		}
		board[by*n+bx] = 1
		prev = [2]int{bx, by}
		rec.Moves = append(rec.Moves, moveRec{N: 1, S: "B", X: bx, Y: by, MS: 0})
		rec.Plies = 1
		toMove = white
	}

	for plies := len(rec.Moves); plies < maxPlies; plies++ {
		e := toMove
		col, side := "W", 2
		if e == black {
			col, side = "B", 1
		}
		if plies == 0 {
			_ = e.send("BEGIN")
		} else {
			_ = e.send("TURN %d,%d", prev[0], prev[1])
		}
		t0 := time.Now()
		line, err := e.readLine(moveTimeout)
		ms := time.Since(t0).Milliseconds()
		if err != nil {
			lose(col, err.Error())
			break
		}
		x, y, ok := parseCoords(line)
		if !ok || x < 0 || y < 0 || x >= n || y >= n || board[y*n+x] != 0 {
			lose(col, fmt.Sprintf("illegal reply %q", line))
			break
		}
		board[y*n+x] = side
		prev = [2]int{x, y}
		rec.Moves = append(rec.Moves, moveRec{N: plies + 1, S: col, X: x, Y: y, MS: ms})
		rec.Plies = plies + 1
		if fiveAt(board, n, x, y) {
			rec.Winner, rec.Reason = col, "five"
			break
		}
		full := true
		for _, v := range board {
			if v == 0 {
				full = false
				break
			}
		}
		if full {
			rec.Winner, rec.Reason = "D", "board full"
			break
		}
		if e == black {
			toMove = white
		} else {
			toMove = black
		}
	}
	rec.DurMS = time.Since(start).Milliseconds()
	return rec
}

func main() {
	games := flag.Int("games", 10000, "number of games")
	workers := flag.Int("workers", 12, "parallel games")
	tt := flag.Int("tt", 110, "INFO timeout_turn base (ms); per-game jitter ±40")
	size := flag.Int("size", 15, "board size")
	maxPlies := flag.Int("max-plies", 300, "safety cap on plies per game")
	seed := flag.Int64("seed", 1, "rng seed")
	engine := flag.String("engine", "./pbrain-bango", "engine binary")
	jsonOut := flag.String("json", "/tmp/selfplay.jsonl", "JSONL output path")
	flag.Parse()

	f, err := os.Create(*jsonOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open output:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	var mu sync.Mutex

	progress := make(chan int, *workers)
	done := make(chan struct{})
	go func() {
		finished := 0
		t0 := time.Now()
		for range progress {
			finished++
			if finished%100 == 0 || finished == *games {
				rate := float64(finished) / time.Since(t0).Seconds()
				eta := time.Duration(float64(*games-finished)/rate) * time.Second
				fmt.Printf("%d/%d games, %.2f games/s, ETA %v\n", finished, *games, rate, eta.Round(time.Second))
			}
		}
		close(done)
	}()

	var wg sync.WaitGroup
	next := make(chan int)
	for wk := 0; wk < *workers; wk++ {
		wg.Add(1)
		go func(wk int) {
			defer wg.Done()
			for id := range next {
				gseed := *seed + int64(id)*7919
				grng := rand.New(rand.NewSource(gseed))
				jitter := *tt - 40 + grng.Intn(81)
				if jitter < 60 {
					jitter = 60
				}
				rec := playGame(id, gseed, *engine, id%2 == 0, *size, jitter, *maxPlies, grng)
				line, _ := json.Marshal(rec)
				mu.Lock()
				w.Write(line)
				w.WriteByte('\n')
				if id%100 == 0 {
					w.Flush()
				}
				mu.Unlock()
				progress <- 1
			}
		}(wk)
	}
	for id := 0; id < *games; id++ {
		next <- id
	}
	close(next)
	wg.Wait()
	close(progress)
	<-done
	w.Flush()
	fmt.Println("done")
}
