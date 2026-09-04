package alphabeta

// Regression tests for the kill-search budget rework: the probes share the
// move's ONE absolute deadline, a fired deadline is *inconclusive* (never a
// defence, never a kill), and the defence layer's candidate list is whole.

import (
	"testing"
	"time"

	"gomoku/algorithm"
)

// overrunPosition replays the recorded arena position (seed 1, game 15,
// after ply 71) that drove the old opponent-kill probe to 5s against its
// 45ms slice. Black = opponent, white = engine, white to move.
func overrunPosition(tb testing.TB) *searcher {
	tb.Helper()
	const n = 15
	moves := [][2]int{
		{7, 7}, {6, 6}, {5, 7}, {5, 9}, {4, 9}, {6, 7}, {7, 8}, {6, 9}, {6, 8}, {7, 9},
		{6, 10}, {9, 9}, {8, 9}, {5, 8}, {4, 8}, {4, 6}, {4, 7}, {7, 6}, {5, 6}, {4, 10},
		{3, 10}, {9, 6}, {8, 6}, {9, 4}, {8, 5}, {9, 5}, {9, 7}, {8, 8}, {9, 8}, {11, 11},
		{10, 12}, {5, 11}, {6, 11}, {7, 10}, {8, 11}, {7, 12}, {7, 13}, {12, 12}, {10, 10}, {9, 3},
		{9, 2}, {10, 11}, {9, 10}, {13, 11}, {12, 11}, {6, 4}, {5, 3}, {5, 12}, {5, 10}, {3, 7},
		{5, 5}, {5, 4}, {6, 3}, {2, 8}, {3, 9}, {4, 3}, {6, 5}, {7, 4}, {8, 4}, {4, 4},
		{3, 4}, {4, 5}, {4, 2}, {8, 3}, {8, 2}, {2, 9}, {1, 9}, {3, 8}, {2, 7}, {8, 12},
		{6, 12},
	}
	b := make([]int, n*n)
	for i, m := range moves {
		if i%2 == 0 {
			b[m[1]*n+m[0]] = playerOpp // black held the odd plies
		} else {
			b[m[1]*n+m[0]] = playerMe // white (the engine) the even ones
		}
	}
	return newSearcher(n, b, 1<<20)
}

// TestKillSearchPastDeadlineInconclusive: with the deadline already fired the
// probes must bail out at once and report nothing — nil with the timedOut
// latch set, -1 from both entry points. The old nodes%-granularity check
// explored essentially the whole forcing tree before returning the same nil.
func TestKillSearchPastDeadlineInconclusive(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	s := overrunPosition(t)
	past := time.Now().Add(-time.Second)

	ks := &killSearch{s: s, deadline: past, attacker: playerOpp}
	start := time.Now()
	if r := ks.search(playerOpp, vctDepth); r != nil {
		t.Fatal("a fired deadline still returned a proof")
	}
	if !ks.timedOut {
		t.Fatal("timedOut was not latched")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("search past its deadline still explored for %v", el)
	}

	if r := s.runKillSearch(past); r >= 0 {
		t.Fatalf("kill search returned a move on a fired deadline: (%d,%d)", r%15, r/15)
	}
	if r := s.runOppKillProbe(past); r >= 0 {
		t.Fatalf("defence probe returned a block on a fired deadline: (%d,%d)", r%15, r/15)
	}
}

// TestOppKillProbeBoundedOnOverrunPosition: the recorded regression — the
// probe on this position used to run ~5s against a 45ms slice. It must now
// hold its slice with room to spare. (It returns -1 here: a fork no single
// block answers, so the main search's move stands either way.)
func TestOppKillProbeBoundedOnOverrunPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	s := overrunPosition(t)
	start := time.Now()
	r := s.runOppKillProbe(time.Now().Add(360 * time.Millisecond))
	el := time.Since(start)
	t.Logf("probe returned %d in %v", r, el.Round(time.Millisecond))
	if el > 1500*time.Millisecond {
		t.Fatalf("opponent-kill probe overran its 360ms slice: %v", el)
	}
}

// TestThinkHoldsWholeMoveBudget: through the public entry point the whole
// move — ladder plus both kill probes — must stay inside roughly the
// timeout_turn budget. The recorded run of this exact position took 5.3s.
func TestThinkHoldsWholeMoveBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	s := overrunPosition(t)
	algo := New()
	algo.Reset(s.n, 1<<20)
	start := time.Now()
	x, y := algo.Think(algorithm.Request{
		Size:        s.n,
		Board:       append([]int(nil), s.b...),
		OwnIsBlack:  false,
		TimeoutTurn: 400,
	})
	el := time.Since(start)
	t.Logf("think finished in %v at (%d,%d)", el.Round(time.Millisecond), x, y)
	if x < 0 || y < 0 || x >= s.n || y >= s.n || s.b[y*s.n+x] != 0 {
		t.Fatalf("think played an illegal cell (%d,%d)", x, y)
	}
	if el > 1500*time.Millisecond {
		t.Fatalf("think with timeout_turn 400 ran %v (budget ≈ 360ms)", el)
	}
}

// TestLadderMoveKeptWhenNoDanger: the recorded tengen-line position — the
// probe used to override the ladder's (6,5) with its "verified block" (8,6)
// whenever scheduling noise left it probe budget, and (8,6) lost ~85% of its
// games. With the prevScore<0 gate the ladder's move stands on any budget.
func TestLadderMoveKeptWhenNoDanger(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	s := diagramToSearcher(t, `
...............
...............
...............
...............
.....ox........
...............
......x........
.......o.......
...............
...............
...............
...............
...............
...............
...............
`)
	algo := New()
	algo.Reset(s.n, 1<<20)
	for _, tt := range []int{70, 110, 150} {
		x, y := algo.Think(algorithm.Request{
			Size:        s.n,
			Board:       append([]int(nil), s.b...),
			OwnIsBlack:  true,
			TimeoutTurn: tt,
		})
		if x != 6 || y != 5 {
			t.Fatalf("tt=%d: think played (%d,%d), want the ladder's move (6,5)", tt, x, y)
		}
	}
}

// TestDefenceForcingMovesUncapped: the defence layer must see every forcing
// candidate — truncating it would fake kills. The scattered defender twos
// (plus the vertical shapes their grid forms) give well over a dozen
// live-three growth points, past the attack layer's cap.
func TestDefenceForcingMovesUncapped(t *testing.T) {
	s := diagramToSearcher(t, `
.........
..xx..xx.
.........
..xx..xx.
.xx..xx..
..xx..xx.
.........
..xx..xx.
.........
`)
	ks := &killSearch{s: s, attacker: playerMe}
	all := ks.forcingMoves(playerOpp, unlimitedMoves)
	if len(all) <= nodeMoveLimit {
		t.Fatalf("fixture broke: only %d defence candidates, want > %d", len(all), nodeMoveLimit)
	}
	capped := ks.forcingMoves(playerOpp, nodeMoveLimit)
	if len(capped) != nodeMoveLimit {
		t.Fatalf("attack-style cap broken: %d candidates, want %d", len(capped), nodeMoveLimit)
	}
}
