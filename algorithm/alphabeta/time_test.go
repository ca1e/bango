package alphabeta

// Time-control and interruption paths: the budget formula, the abort
// fallbacks, and the invariant that an aborted search leaves no trace in the
// transposition table. Nothing asserts wall-clock precision — only the
// formula and behavioural invariants — so the suite stays stable on a loaded
// machine. Deadlines are injected, not measured.

import (
	"testing"
	"time"
)

// newMiddlegame builds a scattered 15×15 position: wide enough that a
// deadline expiring mid-iteration is easy to arrange, quiet enough that the
// kill search finds nothing.
func newMiddlegame() *searcher {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
		{3, 3, playerMe}, {11, 4, playerOpp},
	}
	b := make([]int, n*n)
	for _, st := range stones {
		b[st[1]*n+st[0]] = st[2]
	}
	return newSearcher(n, b, 0)
}

func countTTEntries(s *searcher) int {
	c := 0
	for j := range s.tt.entries {
		b := &s.tt.entries[j]
		for i := 0; i < ttWays; i++ {
			if b[i].Load() != nil {
				c++
			}
		}
	}
	return c
}

// TestThinkBudgetFormula pins the Piskvork budget derivation: 90% of
// timeout_turn, capped by a quarter of the remaining match clock, floored at
// 100ms so the engine still thinks on "as fast as possible" settings.
func TestThinkBudgetFormula(t *testing.T) {
	cases := []struct {
		name        string
		timeoutTurn int
		timeLeft    int64
		want        time.Duration
	}{
		{"no info: default second", 0, 0, 1000 * time.Millisecond},
		{"90% of turn", 1000, 0, 900 * time.Millisecond},
		{"turn small: 90ms hits the floor", 100, 0, 100 * time.Millisecond},
		{"turn 200", 200, 0, 180 * time.Millisecond},
		{"match clock tighter than turn", 1000, 3000, 750 * time.Millisecond},
		{"match clock looser than turn", 1000, 10000, 900 * time.Millisecond},
		{"match clock alone", 0, 1000, 250 * time.Millisecond},
		{"match clock below floor", 0, 200, 100 * time.Millisecond},
		{"both tiny", 1000, 100, 100 * time.Millisecond},
	}
	for _, c := range cases {
		if got := thinkBudget(c.timeoutTurn, c.timeLeft); got != c.want {
			t.Errorf("%s: thinkBudget(%d, %d) = %v, want %v",
				c.name, c.timeoutTurn, c.timeLeft, got, c.want)
		}
	}
}

// TestRunTinyBudgetTacticalShortCircuit: the forced five/block replies run
// before any deadline arithmetic, so even a microsecond budget must take
// them — an engine that "thinks" instead of winning is broken.
func TestRunTinyBudgetTacticalShortCircuit(t *testing.T) {
	// our four with both ends open: completing at (2,7)/(7,7) wins now
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
...oooo....
...........
...........
...........
`)
	x, y := s.run(12, time.Microsecond)
	if y != 7 || (x != 2 && x != 7) {
		t.Errorf("win with 1µs budget = (%d,%d), want (2,7)/(7,7)", x, y)
	}

	// opponent four with a single completion: the block is forced
	b := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
....oxxxx..
...........
...........
...........
...........
`)
	x, y = b.run(12, time.Microsecond)
	if x != 9 || y != 6 {
		t.Errorf("block with 1µs budget = (%d,%d), want (9,6)", x, y)
	}
}

// TestRunTinyBudgetReturnsLegal: a real think under a starved budget must
// still return a legal empty cell (and not panic, not wander off board).
func TestRunTinyBudgetReturnsLegal(t *testing.T) {
	for _, budget := range []time.Duration{time.Millisecond, time.Microsecond} {
		s := newMiddlegame()
		x, y := s.run(12, budget)
		if x < 0 || y < 0 || x >= 15 || y >= 15 {
			t.Fatalf("budget %v: move (%d,%d) off the board", budget, x, y)
		}
		if s.b[y*15+x] != 0 {
			t.Fatalf("budget %v: played on an occupied cell (%d,%d)", budget, x, y)
		}
	}
}

// TestAbortedNodesStoreNothing: the "aborted results are never stored"
// guarantee, directly. A node that returns the 0 sentinel — at entry or
// mid-loop after a child set the flag — must not touch the table.
func TestAbortedNodesStoreNothing(t *testing.T) {
	const diagram = `
...........
...........
...........
...........
...........
...........
...........
....xxx....
...........
...........
...........
`
	// sanity for the counter itself: a real search does fill the table
	alive := diagramToSearcher(t, diagram)
	moves := alive.genMoves(rootMoveLimit, playerMe)
	alive.searchRoot(6, moves)
	if countTTEntries(alive) == 0 {
		t.Fatal("counter saw no entries after a real search")
	}

	// entry abort: the flag is already set when negamax is called
	s := diagramToSearcher(t, diagram)
	moves = s.genMoves(rootMoveLimit, playerMe)
	s.makeMove(moves[0], playerMe)
	s.aborted = true
	before := countTTEntries(s)
	if v := s.negamax(4, playerOpp, -1<<60, 1<<60); v != 0 {
		t.Fatalf("aborted node returned %d, want the 0 sentinel", v)
	}
	if countTTEntries(s) != before {
		t.Fatal("entry-aborted node stored a TT entry")
	}
	s.undoMove(moves[0], playerMe)

	// mid-loop abort: the deadline expires inside the first subtree, after
	// the root completed its probe and entered its move loop. Subtrees that
	// finished before the flag went up legitimately keep their entries — but
	// the root itself bails before its own store, so its key must stay absent.
	m := newMiddlegame()
	m.deadline = time.Now().Add(-time.Hour)
	if v := m.negamax(6, playerMe, -1<<60, 1<<60); v != 0 {
		t.Fatalf("mid-loop aborted node returned %d, want the 0 sentinel", v)
	}
	if !m.aborted {
		t.Fatal("deadline check never fired — position too small for the test")
	}
	if e, ok := m.tt.probe(m.nodeKey(playerMe)); ok {
		t.Fatalf("mid-loop aborted root stored its pseudo-value: %+v", e)
	}
}

// TestAbortedRunFallsBackLegal: with the deadline already expired the
// iterative-deepening loop cannot complete any depth — run must fall back to
// the first candidate, never crash, and never write a table entry.
func TestAbortedRunFallsBackLegal(t *testing.T) {
	s := newMiddlegame()
	s.deadline = time.Now().Add(-time.Hour)
	s.softDeadline = time.Now().Add(-time.Hour)
	s.nodes = 511 // abort at the very first node of the first iteration

	x, y := s.run(6, 0) // budget 0: keep the injected deadlines
	if x < 0 || y < 0 || x >= 15 || y >= 15 || s.b[y*15+x] != 0 {
		t.Fatalf("aborted run move (%d,%d) illegal", x, y)
	}
	if s.lastDepth != 0 {
		t.Fatalf("aborted run reported completed depth %d, want 0", s.lastDepth)
	}
	if n := countTTEntries(s); n != 0 {
		t.Fatalf("aborted run left %d TT entries, want 0", n)
	}
}

// TestInterruptedThenResumedConsistent: a run cut short by its budget stores
// only sound (depth-tagged) entries from completed subtrees. Resuming to full
// depth must therefore converge on exactly the move a fresh unlimited search
// finds — this is the end-to-end form of the "aborted pseudo-values never
// reach the table" invariant.
func TestInterruptedThenResumedConsistent(t *testing.T) {
	diagrams := []string{
		// opponent live three: the defence must come out identical
		`
...........
...........
...........
...........
...........
...........
...........
....xxx....
...........
...........
...........
`,
		// engine open three: forcing lines produce win-range scores
		`
...........
...........
...........
...........
...........
...........
...........
...ooo.....
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		ref := diagramToSearcher(t, d)
		ref.lmrEnabled = false // deterministic resume gate: value-preserving stack
		rx, ry := ref.run(6, 0)

		part := diagramToSearcher(t, d)
		part.lmrEnabled = false
		part.run(6, 300*time.Microsecond) // may stop anywhere — that's the point
		part.aborted = false              // searcher state is per-think; resume by hand
		part.deadline = time.Time{}
		part.softDeadline = time.Time{}
		part.nodes = 0
		x, y := part.run(6, 0)
		if x != rx || y != ry {
			t.Fatalf("diagram %d: resumed move (%d,%d) != fresh move (%d,%d)", i, x, y, rx, ry)
		}
	}
}

// TestMaxDepthClamp: INFO max_depth caps the iterative-deepening ceiling; the
// last completed iteration must never exceed it. Even depths only, so clamp
// to 4 is observed as exactly 4 (or lower under a tiny budget).
func TestMaxDepthClamp(t *testing.T) {
	s := newMiddlegame()
	x, y := s.run(4, 0) // unlimited time, depth clamped to 4
	if x < 0 || y < 0 {
		t.Fatalf("no move produced under max_depth=4")
	}
	if s.lastDepth > 4 {
		t.Fatalf("lastDepth = %d, want <= 4", s.lastDepth)
	}
	if s.lastDepth != 4 {
		t.Logf("lastDepth = %d (soft deadline may stop early)", s.lastDepth)
	}
}

// TestMaxNodeAbortsGracefully: a tiny node budget must abort the search and
// still return a legal move (the previous iteration's best), never garbage.
func TestMaxNodeAbortsGracefully(t *testing.T) {
	s := newMiddlegame()
	s.nodeLimit = 500
	x, y := s.run(maxSearchDepth, 0)
	n := s.n
	if x < 0 || x >= n || y < 0 || y >= n || s.b[y*n+x] != 0 {
		t.Fatalf("illegal move (%d,%d) under max_node", x, y)
	}
	if s.nodes > 500+512 { // check granularity: abort is detected on the %512 tick
		t.Fatalf("nodes = %d, want <= %d (one check tick past the limit)", s.nodes, 500+512)
	}
}

// TestMaxNodeZeroMeansUnlimited: the default (unset) limit must not abort a
// full-depth deterministic search — regression for the wiring itself.
func TestMaxNodeZeroMeansUnlimited(t *testing.T) {
	s := newMiddlegame()
	s.run(6, 0)
	if s.aborted {
		t.Fatal("search aborted with no node limit set")
	}
	if s.lastDepth != 6 {
		t.Fatalf("lastDepth = %d, want 6", s.lastDepth)
	}
}
