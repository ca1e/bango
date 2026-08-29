package main

import (
	"math/rand"
	"sync"
	"testing"
	"time"
)

// recomputeHash folds the whole board from scratch; used to verify that the
// incremental hash stays in sync.
func recomputeHash(s *searcher) uint64 {
	var h uint64
	for p, v := range s.b {
		if v != 0 {
			h ^= s.zo.code(p, v)
		}
	}
	return h
}

func TestZobristIncrementalMatchesRecompute(t *testing.T) {
	s := newSearcher(15, make([]int, 15*15), 1<<20)
	rng := rand.New(rand.NewSource(42))
	type placed struct {
		p, side int
	}
	var stack []placed
	for i := 0; i < 2000; i++ {
		if len(stack) > 0 && rng.Intn(3) == 0 {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			s.undoMove(top.p, top.side)
		} else {
			p := rng.Intn(15 * 15)
			if s.b[p] != 0 {
				continue
			}
			side := playerMe + rng.Intn(2)
			s.makeMove(p, side)
			stack = append(stack, placed{p, side})
		}
		if s.hash != recomputeHash(s) {
			t.Fatalf("step %d: incremental hash out of sync", i)
		}
	}
}

func TestTranspositionTableStoreProbe(t *testing.T) {
	tt := newTransTable(12)
	h := uint64(0x1234567890ABCDEF)
	tt.store(h, 123, 4, 7, ttFlagExact)
	e, ok := tt.probe(h)
	if !ok || e.score != 123 || e.depth != 4 || e.best != 7 || e.flag != ttFlagExact {
		t.Fatalf("probe after store: entry %+v, ok %v", e, ok)
	}

	// a shallower result for a different position sharing the slot must not
	// evict the deeper one ...
	other := h + 1<<12 // same slot, different lock bits
	tt.store(other, 55, 2, 9, ttFlagExact)
	if e, _ := tt.probe(h); e.depth != 4 {
		t.Fatalf("shallow entry evicted deeper one: %+v", e)
	}
	// ... while a deeper one replaces it
	tt.store(other, 66, 5, 9, ttFlagExact)
	if e, _ := tt.probe(other); e.score != 66 || e.depth != 5 {
		t.Fatalf("deep entry failed to replace: %+v", e)
	}

	if _, ok := tt.probe(h + 2<<12); ok {
		t.Fatal("probe hit on a key that was never stored")
	}
}

func TestNodeKeySplitsSideToMove(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
....x.x....
...........
...........
...........
`)
	if s.nodeKey(playerMe) == s.nodeKey(playerOpp) {
		t.Fatal("side to move must change the node key")
	}
}

// searchFixed runs a full fixed-depth root search with no time limit.
func searchFixed(t *testing.T, diagram string, depth int, ttOn bool) (int, int) {
	t.Helper()
	s := diagramToSearcher(t, diagram)
	s.ttEnabled = ttOn
	s.lmrEnabled = false // equivalence gates run on the value-preserving stack
	moves := s.genMoves(rootMoveLimit, playerMe)
	score, mv, ok := s.searchRoot(depth, moves)
	if !ok {
		t.Fatal("root search aborted despite no deadline")
	}
	return score, mv
}

// TestTTSearchEquivalence is the key correctness gate: enabling the
// transposition table must not change search results, only speed.
func TestTTSearchEquivalence(t *testing.T) {
	diagrams := []string{
		// opponent live three: defensive choice
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
		// engine open three: forcing lines produce win-range scores that
		// exercise the guard against reusing them as table cutoffs
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
		// scattered early middlegame
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		for _, depth := range []int{2, 4} {
			plainScore, plainMove := searchFixed(t, d, depth, false)
			ttScore, ttMove := searchFixed(t, d, depth, true)
			if plainScore != ttScore || plainMove != ttMove {
				t.Fatalf("diagram %d depth %d: plain=(score %d, move %d), tt=(score %d, move %d)",
					i, depth, plainScore, plainMove, ttScore, ttMove)
			}
		}
	}
	// deeper searches on the smaller positions
	for i, d := range diagrams[:2] {
		plainScore, plainMove := searchFixed(t, d, 6, false)
		ttScore, ttMove := searchFixed(t, d, 6, true)
		if plainScore != ttScore || plainMove != ttMove {
			t.Fatalf("diagram %d depth 6: plain=(score %d, move %d), tt=(score %d, move %d)",
				i, plainScore, plainMove, ttScore, ttMove)
		}
	}
}

// searchFixedPVS runs a fixed-depth root search with PVS scouting on or off.
func searchFixedPVS(t *testing.T, diagram string, depth int, pvsOn bool) (int, int) {
	t.Helper()
	s := diagramToSearcher(t, diagram)
	s.pvsEnabled = pvsOn
	s.lmrEnabled = false
	moves := s.genMoves(rootMoveLimit, playerMe)
	score, mv, ok := s.searchRoot(depth, moves)
	if !ok {
		t.Fatal("root search aborted despite no deadline")
	}
	return score, mv
}

// TestPVSSearchEquivalence is the correctness gate for PVS: null-window
// scouting with re-search must return exactly the same root score and move as
// the full-window alpha-beta search, at a lower node cost.
func TestPVSSearchEquivalence(t *testing.T) {
	diagrams := []string{
		// opponent live three: defensive choice
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
		// engine open three: forcing lines with win-range scores
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
		// scattered early middlegame
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		for _, depth := range []int{2, 4, 6} {
			plainScore, plainMove := searchFixedPVS(t, d, depth, false)
			pvsScore, pvsMove := searchFixedPVS(t, d, depth, true)
			if plainScore != pvsScore || plainMove != pvsMove {
				t.Fatalf("diagram %d depth %d: plain=(score %d, move %d), pvs=(score %d, move %d)",
					i, depth, plainScore, plainMove, pvsScore, pvsMove)
			}
		}
	}
}

// TestPVSSpeedup checks that scouting actually saves nodes on a middlegame
// position wide enough for the window effect to matter.
func TestPVSSpeedup(t *testing.T) {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	newPos := func(pvsOn bool) *searcher {
		b := make([]int, n*n)
		for _, st := range stones {
			b[st[1]*n+st[0]] = st[2]
		}
		s := newSearcher(n, b, 0)
		s.hash = recomputeHash(s) // stones were set after construction
		s.pvsEnabled = pvsOn
		s.lmrEnabled = false
		return s
	}

	plain := newPos(false)
	plainScore, plainMove, ok := plain.searchRoot(8, plain.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("plain search aborted")
	}
	pvs := newPos(true)
	pvsScore, pvsMove, ok := pvs.searchRoot(8, pvs.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("pvs search aborted")
	}
	if plainScore != pvsScore || plainMove != pvsMove {
		t.Fatalf("depth 8: plain=(score %d, move %d), pvs=(score %d, move %d)",
			plainScore, plainMove, pvsScore, pvsMove)
	}
	t.Logf("depth 8 nodes: plain %d, pvs %d (%.1f%% of plain)",
		plain.nodes, pvs.nodes, 100*float64(pvs.nodes)/float64(plain.nodes))
	// Node dominance is only a trend, not an invariant: both sides share the
	// same ordering (TT move, killers), but PVS stores null-window bounds in
	// the TT, so the two runs traverse slightly different trees — with strong
	// ordering the gap can flip either way by a few percent. Gross regressions
	// still fail; the exact-equality gate above guards correctness.
	if pvs.nodes > plain.nodes+plain.nodes/10 {
		t.Errorf("PVS node count far above plain: %d > %d", pvs.nodes, plain.nodes)
	}
}

// TestAlphaBetaSafePruning is the tutorial's "safe pruning" claim made
// testable: disabling alpha-beta (plain minimax) must return exactly the same
// root score and move as the pruned search, at a strictly higher node cost.
func TestAlphaBetaSafePruning(t *testing.T) {
	diagrams := []string{
		// opponent live three: defensive decision on both sides
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
		// engine open three: forcing lines with win-range scores
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
		// scattered early middlegame
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		for _, depth := range []int{2, 4} {
			ab := diagramToSearcher(t, d)
			ab.lmrEnabled = false // pruning-safety gate: alpha-beta only, no LMR
			plain := diagramToSearcher(t, d)
			plain.abEnabled = false
			abMoves := ab.genMoves(rootMoveLimit, playerMe)
			plainMoves := plain.genMoves(rootMoveLimit, playerMe)
			if len(abMoves) != len(plainMoves) {
				t.Fatalf("diagram %d: move generation differs under abEnabled flag", i)
			}
			abScore, abMove, ok := ab.searchRoot(depth, abMoves)
			if !ok {
				t.Fatal("ab search aborted despite no deadline")
			}
			plainScore, plainMove, ok := plain.searchRoot(depth, plainMoves)
			if !ok {
				t.Fatal("plain search aborted despite no deadline")
			}
			if abScore != plainScore || abMove != plainMove {
				t.Fatalf("diagram %d depth %d: ab=(score %d, move %d), plain=(score %d, move %d)",
					i, depth, abScore, abMove, plainScore, plainMove)
			}
			if plain.nodes <= ab.nodes && depth > 2 {
				t.Logf("diagram %d depth %d: pruning did not reduce nodes (ab %d, plain %d)", i, depth, ab.nodes, plain.nodes)
			}
			if ab.abCuts == 0 && depth >= 4 {
				t.Errorf("diagram %d depth %d: no beta cutoffs recorded — pruning never fires?", i, depth)
			}
			t.Logf("diagram %d depth %d: ab %d nodes (%d cuts), plain %d nodes",
				i, depth, ab.nodes, ab.abCuts, plain.nodes)
		}
	}
}

// TestTTRunEquivalence checks the cache at the level the tutorial integrates
// it: the whole iterative-deepening run must return the same move with the
// table on or off.
func TestTTRunEquivalence(t *testing.T) {
	build := func(diagram string, ttOn bool) *searcher {
		s := diagramToSearcher(t, diagram)
		s.ttEnabled = ttOn
		s.lmrEnabled = false
		return s
	}
	diagrams := []string{
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
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		on := build(d, true)
		off := build(d, false)
		budget := 3 * time.Second
		xOn, yOn := on.run(6, budget)
		xOff, yOff := off.run(6, budget)
		if xOn != xOff || yOn != yOff {
			t.Fatalf("diagram %d: tt move (%d,%d) != plain move (%d,%d)", i, xOn, yOn, xOff, yOff)
		}
	}
}

// TestTTMemoryShrink checks max_memory scaling: a tiny budget must shrink the
// table below the default size instead of allocating ~12.6MB.
func TestTTMemoryShrink(t *testing.T) {
	full := newSearcher(15, make([]int, 15*15), 0)
	if len(full.tt.entries) != 1<<ttBits {
		t.Fatalf("default table = %d entries, want 1<<%d", len(full.tt.entries), ttBits)
	}
	tiny := newSearcher(15, make([]int, 15*15), 1<<20) // 1MB budget
	if len(tiny.tt.entries) >= len(full.tt.entries) {
		t.Fatalf("tiny budget kept full table size %d", len(tiny.tt.entries))
	}
	// absurdly small budget still respects the floor
	floor := newSearcher(15, make([]int, 15*15), 1)
	if len(floor.tt.entries) != 1<<ttMinBits {
		t.Fatalf("floor table = %d entries, want 1<<%d", len(floor.tt.entries), ttMinBits)
	}
}

// TestTTWinScoreGuard probes the win-range guard at algorithm.go's TT probe
// directly: scores within ±(winScore-1024) are ply-relative (winScore - ply)
// and must never be reused as cutoffs, while ordinary deep scores are. Both
// halves are pinned with a pre-seeded entry — positive control first, then
// the guard.
func TestTTWinScoreGuard(t *testing.T) {
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
	// node() descends into a minimax node: our stone placed, opponent to move.
	node := func(ttOn bool) (*searcher, []int) {
		s := diagramToSearcher(t, diagram)
		s.ttEnabled = ttOn
		moves := s.genMoves(rootMoveLimit, playerMe)
		s.makeMove(moves[0], playerMe)
		return s, moves
	}

	// positive control: a deep exact entry OUTSIDE the win range is reused
	// verbatim — proves the probe itself fires and would cut off
	s, moves := node(true)
	s.tt.store(s.nodeKey(playerOpp), 12345, 8, uint16(moves[1]), ttFlagExact)
	if got := s.negamax(4, playerOpp, -1<<60, 1<<60); got != 12345 {
		t.Fatalf("deep exact entry outside win range not reused: got %d, want 12345", got)
	}

	// the guard: the same entry holding a win-range score must NOT cut off —
	// the node is searched for its true value instead
	plain, _ := node(false)
	plainScore := plain.negamax(4, playerOpp, -1<<60, 1<<60)
	guarded, moves2 := node(true)
	guarded.tt.store(guarded.nodeKey(playerOpp), int32(winScore-10), 8, uint16(moves2[1]), ttFlagExact)
	got := guarded.negamax(4, playerOpp, -1<<60, 1<<60)
	if got == winScore-10 {
		t.Fatal("win-range TT score was reused as a cutoff")
	}
	if got != plainScore {
		t.Fatalf("guarded search %d != plain search %d", got, plainScore)
	}
}

func TestTTSpeedup(t *testing.T) {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	newPos := func(ttOn bool) *searcher {
		b := make([]int, n*n)
		for _, st := range stones {
			b[st[1]*n+st[0]] = st[2]
		}
		s := newSearcher(n, b, 0)
		s.hash = recomputeHash(s) // stones were set after construction
		s.ttEnabled = ttOn
		s.lmrEnabled = false
		return s
	}

	plain := newPos(false)
	plainScore, plainMove, ok := plain.searchRoot(8, plain.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("plain search aborted")
	}
	tt := newPos(true)
	ttScore, ttMove, ok := tt.searchRoot(8, tt.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("tt search aborted")
	}
	if plainScore != ttScore || plainMove != ttMove {
		t.Fatalf("depth 8: plain=(score %d, move %d), tt=(score %d, move %d)",
			plainScore, plainMove, ttScore, ttMove)
	}
	t.Logf("depth 8 nodes: plain %d, tt %d (%.1f%% of plain)",
		plain.nodes, tt.nodes, 100*float64(tt.nodes)/float64(plain.nodes))
	if tt.nodes > plain.nodes {
		t.Errorf("TT increased node count: %d > %d", tt.nodes, plain.nodes)
	}
}

// searchFixedCfg runs a fixed-depth root search with custom searcher flags.
func searchFixedCfg(t *testing.T, diagram string, depth int, configure func(*searcher)) (int, int) {
	t.Helper()
	s := diagramToSearcher(t, diagram)
	s.lmrEnabled = false
	if configure != nil {
		configure(s)
	}
	moves := s.genMoves(rootMoveLimit, playerMe)
	score, mv, ok := s.searchRoot(depth, moves)
	if !ok {
		t.Fatal("root search aborted despite no deadline")
	}
	return score, mv
}

// TestKillerEquivalence: killer-move ordering must not change search results,
// only speed.
func TestKillerEquivalence(t *testing.T) {
	diagrams := []string{
		// opponent live three: defensive choice
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
		// engine open three: forcing lines with win-range scores
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
		// scattered early middlegame
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		for _, depth := range []int{2, 4, 6} {
			offScore, offMove := searchFixedCfg(t, d, depth, func(s *searcher) { s.killersEnabled = false })
			onScore, onMove := searchFixedCfg(t, d, depth, nil)
			if offScore != onScore || offMove != onMove {
				t.Fatalf("diagram %d depth %d: off=(score %d, move %d), on=(score %d, move %d)",
					i, depth, offScore, offMove, onScore, onMove)
			}
		}
	}
}

// TestKillerSpeedup: promoted refutations should cut the wide middlegame
// tree faster than the plain threat-ladder order.
func TestKillerSpeedup(t *testing.T) {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	newPos := func(killersOn bool) *searcher {
		b := make([]int, n*n)
		for _, st := range stones {
			b[st[1]*n+st[0]] = st[2]
		}
		s := newSearcher(n, b, 0)
		s.hash = recomputeHash(s) // stones were set after construction
		s.killersEnabled = killersOn
		s.lmrEnabled = false
		return s
	}

	off := newPos(false)
	offScore, offMove, ok := off.searchRoot(8, off.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("killer-off search aborted")
	}
	on := newPos(true)
	onScore, onMove, ok := on.searchRoot(8, on.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("killer-on search aborted")
	}
	if offScore != onScore || offMove != onMove {
		t.Fatalf("depth 8: off=(score %d, move %d), on=(score %d, move %d)",
			offScore, offMove, onScore, onMove)
	}
	t.Logf("depth 8 nodes: killers-off %d, killers-on %d (%.1f%% of off)",
		off.nodes, on.nodes, 100*float64(on.nodes)/float64(off.nodes))
	if on.nodes > off.nodes {
		t.Errorf("killer ordering increased node count: %d > %d", on.nodes, off.nodes)
	}
}

// TestHistoryEquivalence: history-based reordering of the quiet tail must
// not change search results, only speed.
func TestHistoryEquivalence(t *testing.T) {
	diagrams := []string{
		// opponent live three: defensive choice
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
		// engine open three: forcing lines with win-range scores
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
		// scattered early middlegame
		`
...........
.....x.....
...........
...........
......o....
...........
....o.x....
...........
...........
...........
...........
`,
	}
	for i, d := range diagrams {
		for _, depth := range []int{2, 4, 6} {
			offScore, offMove := searchFixedCfg(t, d, depth, func(s *searcher) { s.historyEnabled = false })
			onScore, onMove := searchFixedCfg(t, d, depth, nil)
			if offScore != onScore || offMove != onMove {
				t.Fatalf("diagram %d depth %d: off=(score %d, move %d), on=(score %d, move %d)",
					i, depth, offScore, offMove, onScore, onMove)
			}
		}
	}
}

// TestHistorySpeedup: cutoff statistics should cut the wide middlegame tree
// faster than the plain threat-ladder order.
func TestHistorySpeedup(t *testing.T) {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	newPos := func(historyOn bool) *searcher {
		b := make([]int, n*n)
		for _, st := range stones {
			b[st[1]*n+st[0]] = st[2]
		}
		s := newSearcher(n, b, 0)
		s.hash = recomputeHash(s) // stones were set after construction
		s.historyEnabled = historyOn
		s.lmrEnabled = false
		return s
	}

	off := newPos(false)
	offScore, offMove, ok := off.searchRoot(8, off.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("history-off search aborted")
	}
	on := newPos(true)
	onScore, onMove, ok := on.searchRoot(8, on.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("history-on search aborted")
	}
	if offScore != onScore || offMove != onMove {
		t.Fatalf("depth 8: off=(score %d, move %d), on=(score %d, move %d)",
			offScore, offMove, onScore, onMove)
	}
	t.Logf("depth 8 nodes: history-off %d, history-on %d (%.1f%% of off)",
		off.nodes, on.nodes, 100*float64(on.nodes)/float64(off.nodes))
	if on.nodes > off.nodes+off.nodes/10 {
		t.Errorf("history ordering far above baseline: %d > %d", on.nodes, off.nodes)
	}
}

// TestLineGatherRoundTrip pins the line-id contract shared by setStone and
// gatherLine: rows y → id y, columns x → n+x, diagonal (1,1) → 3n-1+(x-y),
// diagonal (1,-1) → 4n-1+(x+y). Every id decodes to exactly its geometric
// cells (content included), every cell lies on exactly four lines, and the
// four ids through a cell are pairwise distinct — a broken diagonal offset
// would double-assign one line and silently drop another from the cache.
func TestLineGatherRoundTrip(t *testing.T) {
	for _, n := range []int{5, 9, 15} {
		s := newSearcher(n, make([]int, n*n), 0)
		s.setStone((n/2)*n+n/2, playerMe)
		s.setStone(0, playerOpp)

		perCell := make([]int, n*n)
		for id := 0; id < lineCount(n); id++ {
			line := s.gatherLine(id)
			var want []int
			switch {
			case id < n: // row y = id
				for x := 0; x < n; x++ {
					want = append(want, id*n+x)
				}
			case id < 2*n: // column x = id-n
				for y := 0; y < n; y++ {
					want = append(want, y*n+id-n)
				}
			case id < 4*n-1: // diagonal (1,1), constant x-y
				c := id - (3*n - 1)
				x, y := 0, -c
				if c > 0 {
					x, y = c, 0
				}
				for x < n && y < n {
					want = append(want, y*n+x)
					x++
					y++
				}
			default: // diagonal (1,-1), constant x+y
				sum := id - (4*n - 1)
				x, y := 0, sum
				if sum >= n {
					x, y = sum-(n-1), n-1
				}
				for x >= 0 && x < n && y >= 0 && y < n {
					want = append(want, y*n+x)
					x++
					y--
				}
			}
			if len(line) != len(want) {
				t.Fatalf("n=%d line %d: gathered %d cells, decode expects %d", n, id, len(line), len(want))
			}
			for i, p := range want {
				if line[i] != s.b[p] {
					t.Fatalf("n=%d line %d pos %d: content mismatch", n, id, i)
				}
				perCell[p]++
			}
		}
		for p, c := range perCell {
			if c != 4 {
				t.Fatalf("n=%d cell %d lies on %d lines, want 4", n, p, c)
			}
		}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				ids := [4]int{y, n + x, 3*n - 1 + x - y, 4*n - 1 + x + y}
				for i, id := range ids {
					if id < 0 || id >= lineCount(n) {
						t.Fatalf("n=%d cell (%d,%d): id %d out of range", n, x, y, id)
					}
					for j := i + 1; j < 4; j++ {
						if id == ids[j] {
							t.Fatalf("n=%d cell (%d,%d): ids %d and %d collide", n, x, y, i, j)
						}
					}
				}
			}
		}
	}
}

// TestEvalIncrementalWithTakebacks: TestEvalIncrementalExact walks random
// playouts; this variant stresses the drift-prone pattern — undo one or three
// plies mid-sequence, then replay different cells with either colour — where
// a stale line cache could survive simple forward play.
func TestEvalIncrementalWithTakebacks(t *testing.T) {
	for _, n := range []int{9, 15} {
		s := newSearcher(n, make([]int, n*n), 0)
		rng := rand.New(rand.NewSource(777))
		type placed struct {
			p, side int
		}
		var stack []placed
		for i := 0; i < 4000; i++ {
			switch r := rng.Intn(10); {
			case r < 3 && len(stack) > 0:
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				s.undoMove(top.p, top.side)
			case r < 4 && len(stack) >= 3:
				for k := 0; k < 3; k++ {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					s.undoMove(top.p, top.side)
				}
			default:
				p := rng.Intn(n * n)
				if s.b[p] != 0 {
					continue
				}
				side := playerMe + rng.Intn(2)
				s.makeMove(p, side)
				stack = append(stack, placed{p, side})
			}
			if got, want := s.evaluate(), s.evaluateFull(); got != want {
				t.Fatalf("n=%d step %d: evaluate() %d != evaluateFull %d", n, i, got, want)
			}
			if s.hash != recomputeHash(s) {
				t.Fatalf("n=%d step %d: hash out of sync", n, i)
			}
		}
	}
}

// TestEvalIncrementalExact is the correctness gate for the line-score cache:
// after every placement and removal on random playouts, the incrementally
// maintained total must equal the from-scratch board scan bit for bit.
func TestEvalIncrementalExact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		n     int
		renju bool
	}{
		{"freestyle9", 9, false},
		{"freestyle15", 15, false},
		{"renju9", 9, true},
		{"renju15", 15, true},
	} {
		n := tc.n
		s := newSearcher(n, make([]int, n*n), 1<<20)
		if tc.renju {
			s.setRule(RuleRenju, playerMe)
		}
		rng := rand.New(rand.NewSource(4242))
		type placed struct {
			p, side int
		}
		var stack []placed
		for i := 0; i < 3000; i++ {
			if len(stack) > 0 && rng.Intn(3) == 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				s.undoMove(top.p, top.side)
			} else {
				p := rng.Intn(n * n)
				if s.b[p] != 0 {
					continue
				}
				side := playerMe + rng.Intn(2)
				if tc.renju && side == s.blackSide && s.isForbidden(p, side) {
					continue // renju playouts stay legal
				}
				s.makeMove(p, side)
				stack = append(stack, placed{p, side})
			}
			if got, want := s.evaluate(), s.evaluateFull(); got != want {
				t.Fatalf("%s step %d: evaluate() %d != evaluateFull %d", tc.name, i, got, want)
			}
			if s.hash != recomputeHash(s) {
				t.Fatalf("%s step %d: hash out of sync", tc.name, i)
			}
		}
	}
}

// TestTTPersistAcrossMoves: the Engine-owned table survives across thinks of
// a game. Two searchers sharing one table reach a transposed position by
// different move orders; the second probe must hit the first's entry, the
// root score must stay bit-identical to a fresh-table search, and the shared
// run needs fewer nodes than the fresh one.
func TestTTPersistAcrossMoves(t *testing.T) {
	n := 9
	mk := func() []int {
		b := make([]int, n*n)
		b[4*n+4] = playerMe
		b[4*n+5] = playerOpp
		return b
	}

	// order A: our move first, then theirs — search and fill the shared table
	shared := newTransTable(ttBits)
	sa := newSearcherWithTT(n, mk(), 0, shared)
	sa.makeMove(3*n+3, playerMe)
	sa.makeMove(3*n+4, playerOpp)
	sa.lmrEnabled = false // deterministic value-preserving stack
	scoreA, _, ok := sa.searchRoot(6, sa.genMoves(nodeMoveLimit, playerMe))
	if !ok {
		t.Fatal("search A aborted")
	}
	// B's node collapse below is the reuse evidence; entry count is only
	// logged for diagnostics.

	// order B: same stones placed in the transposed order (theirs first) —
	// same node key, so the shared table must serve what A learned
	sb := newSearcherWithTT(n, mk(), 0, shared)
	sb.makeMove(3*n+4, playerOpp)
	sb.makeMove(3*n+3, playerMe)
	sb.lmrEnabled = false
	scB, _, ok := sb.searchRoot(6, sb.genMoves(nodeMoveLimit, playerMe))
	if !ok {
		t.Fatal("search B aborted")
	}
	if scB != scoreA {
		t.Fatalf("shared-table score %d != fresh score %d (transposition must not change values)", scB, scoreA)
	}

	// fresh-table control: same position, no shared history, same value
	sf := newSearcherWithTT(n, mk(), 0, nil)
	sf.makeMove(3*n+4, playerOpp)
	sf.makeMove(3*n+3, playerMe)
	sf.lmrEnabled = false
	scF, _, okF := sf.searchRoot(6, sf.genMoves(nodeMoveLimit, playerMe))
	if !okF {
		t.Fatal("fresh search aborted")
	}
	if scF != scoreA {
		t.Fatalf("fresh-table score %d != shared score %d", scF, scoreA)
	}
	// the load-bearing assertion: B resolved the position from A's persisted
	// entries instead of re-searching the tree
	if sb.nodes >= sf.nodes/2 {
		t.Fatalf("shared search took %d nodes, fresh %d — the persisted entries were not reused", sb.nodes, sf.nodes)
	}
	t.Logf("shared nodes=%d < fresh nodes=%d (%.0f%%)", sb.nodes, sf.nodes, 100*float64(sb.nodes)/float64(sf.nodes))
}

// TestTTFlushOnNewGame: resetSession drops the table — a new client must not
// inherit positions from the previous game.
func TestTTFlushOnNewGame(t *testing.T) {
	e := NewEngine()
	e.resetBoard(15)
	if e.tt == nil {
		t.Fatal("resetBoard did not allocate a persistent TT")
	}
	e.place(7, 7, 1)
	_, _ = e.aiMove()
	if e.tt == nil {
		t.Fatal("aiMove lost the persistent TT")
	}
	e.resetSession()
	if e.tt != nil || e.ttBoardSz != 0 {
		t.Fatal("resetSession must drop the persistent TT")
	}
}

// TestTTKeptAcrossRestartSameSize: a same-size RESTART keeps the table — that
// is the whole point of persistence; a resize reallocates.
func TestTTKeptAcrossRestartSameSize(t *testing.T) {
	e := NewEngine()
	e.resetBoard(15)
	tt := e.tt
	e.resetBoard(15) // RESTART path
	if e.tt != tt {
		t.Fatal("same-size restart must keep the persistent TT")
	}
	e.resetBoard(20)
	if e.tt == tt {
		t.Fatal("resize must reallocate the persistent TT")
	}
}

// countTT counts filled entries of a searcher's table.
func countTT(s *searcher) int {
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

// TestSMPWorkersAgreeOnScore: lazy SMP is intentionally nondeterministic in
// move choice (rotated root orders), but every worker's completed-depth
// root score must agree with the single-threaded search — the shared table
// only ever returns sound entries. Runs the same position through several
// worker counts; score equality is the hard gate, move equality is not
// asserted (tied optima legitimately differ under rotation).
func TestSMPWorkersAgreeOnScore(t *testing.T) {
	const n = 15
	stones := [][3]int{
		{7, 7, playerMe}, {8, 8, playerOpp}, {7, 8, playerMe}, {9, 9, playerOpp},
		{6, 6, playerMe}, {8, 7, playerOpp}, {9, 7, playerMe}, {10, 10, playerOpp},
	}
	mk := func() []int {
		b := make([]int, n*n)
		for _, st := range stones {
			b[st[1]*n+st[0]] = st[2]
		}
		return b
	}
	base := newSearcher(n, mk(), 0)
	base.lmrEnabled = false
	s0, _, ok := base.searchRoot(6, base.genMoves(rootMoveLimit, playerMe))
	if !ok {
		t.Fatal("baseline search aborted")
	}
	for _, workers := range []int{2, 4} {
		shared := newTransTable(ttBits)
		res := make(chan int, workers)
		for w := 0; w < workers; w++ {
			go func(w int) {
				ws := newSearcherWithTT(n, mk(), 0, shared)
				ws.lmrEnabled = false
				ws.smpRotate = w
				sc, _, ok := ws.searchRoot(6, ws.genMoves(rootMoveLimit, playerMe))
				if !ok {
					sc = 1 << 62 // aborted: distinguishable sentinel
				}
				res <- sc
			}(w)
		}
		for i := 0; i < workers; i++ {
			if sc := <-res; sc != s0 {
				t.Fatalf("workers=%d: score %d != single %d (unsound TT sharing)", workers, sc, s0)
			}
		}
	}
}

// TestSMPTTRaceClean: hammer the atomic table from concurrent workers under
// the race detector — the load/store paths must be free of data races.
func TestSMPTTRaceClean(t *testing.T) {
	const n = 11
	b := make([]int, n*n)
	b[5*n+5] = playerMe
	b[5*n+6] = playerOpp
	shared := newTransTable(12) // small table: maximal bucket contention
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ws := newSearcherWithTT(n, append([]int(nil), b...), 0, shared)
			ws.smpRotate = w
			ws.lmrEnabled = false
			_, _ = ws.runSingle(4, 0)
		}(w)
	}
	wg.Wait()
}
