package main

import (
	"testing"
	"time"
)

// swapPlayers returns a board copy with colors flipped, so a searcher built
// on it plays for the opposite side (evaluation is symmetric).
func swapPlayers(b []int) []int {
	out := make([]int, len(b))
	for i, v := range b {
		switch v {
		case playerMe:
			out[i] = playerOpp
		case playerOpp:
			out[i] = playerMe
		}
	}
	return out
}

// TestSelfGame lets the engine play against itself (colors flipped for the
// second searcher) and checks that a full legal game completes with a winner.
func TestSelfGame(t *testing.T) {
	const n = 15
	const budget = 150 * time.Millisecond
	b := make([]int, n*n)

	place := func(x, y, side int) { b[y*n+x] = side }

	fiveAt := func(side int) bool {
		s := &searcher{n: n, b: b, lineBuf: make([]int, n)}
		for p := 0; p < n*n; p++ {
			if b[p] == side && s.makesFive(p, side) {
				return true
			}
		}
		return false
	}

	for move := 0; move < 225; move++ {
		side := playerMe
		flip := b
		if move%2 == 1 {
			side = playerOpp
			flip = swapPlayers(b)
		}
		s := newSearcher(n, flip, 0)
		x, y := s.run(8, budget)
		if x < 0 || y < 0 {
			t.Fatalf("move %d: no move for side %d (board full without a winner?)", move, side)
		}
		if b[y*n+x] != 0 {
			t.Fatalf("move %d: side %d played on occupied cell (%d,%d)", move, side, x, y)
		}
		place(x, y, side)

		if fiveAt(side) {
			t.Logf("game finished after %d plies, winner = side %d, last move (%d,%d)",
				move+1, side, x, y)
			return
		}
	}
	t.Fatal("no five within 225 moves")
}

// TestSearchStats logs the depth/nodes reached on a mid-game position, mainly
// for manual inspection via `go test -v -run TestSearchStats`.
func TestSearchStats(t *testing.T) {
	const n = 15
	b := make([]int, n*n)
	// a plausible early-middle-game position
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	for _, st := range stones {
		b[st[1]*n+st[0]] = st[2]
	}
	s := newSearcher(n, b, 0)
	start := time.Now()
	x, y := s.run(10, 5*time.Second)
	elapsed := time.Since(start)
	if x < 0 || b[y*n+x] != 0 {
		t.Fatalf("illegal move (%d,%d)", x, y)
	}
	t.Logf("move (%d,%d), elapsed %v, nodes %d, depth %d, aborted=%v",
		x, y, elapsed, s.nodes, s.lastDepth, s.aborted)
	if elapsed > 6*time.Second {
		t.Errorf("search exceeded the budget: %v", elapsed)
	}
}
