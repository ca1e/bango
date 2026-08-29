package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// diagramToSearcher builds a searcher from a square text diagram:
// 'o' = engine stone, 'x' = opponent stone, '.' = empty.
func diagramToSearcher(tb testing.TB, diagram string) *searcher {
	tb.Helper()
	rows := strings.Split(strings.TrimSpace(diagram), "\n")
	n := len(rows)
	b := make([]int, n*n)
	for y, row := range rows {
		row = strings.TrimSpace(row)
		if len(row) != n {
			tb.Fatalf("row %d has length %d, want %d", y, len(row), n)
		}
		for x := 0; x < n; x++ {
			switch row[x] {
			case 'o':
				b[y*n+x] = playerMe
			case 'x':
				b[y*n+x] = playerOpp
			}
		}
	}
	// small table: keeps test memory low, logic is size-independent
	return newSearcher(n, b, 1<<20)
}

// lineScore scores a 1-D pattern: 'o' = side stone, 'x' = block, '.' = empty.
func lineScore(t *testing.T, pattern string, side int) int {
	t.Helper()
	line := make([]int, len(pattern))
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case 'o':
			line[i] = side
		case 'x':
			line[i] = 3 // any non-empty, non-side value blocks
		}
	}
	return scoreLineFor(line, side)
}

func TestScoreLinePatterns(t *testing.T) {
	cases := []struct {
		pattern string
		want    int
	}{
		{"oooooo", scoreFive},                       // overline still wins (freestyle)
		{".oooo.", scoreLiveFour},                   // 活四
		{"xoooo..", scoreRushFour},                  // 冲四
		{"oooo..x", scoreRushFour},                  // 冲四
		{"ooo.oo", scoreRushFour},                   // 跳冲四 (filling the gap makes five)
		{".oo.oo.", scoreRushFour + scoreLiveThree}, // gap four with open scope
		{"..ooo..", scoreLiveThree},                 // 活三
		{"x.ooo.x", scoreSleepThree},                // boxed in: 眠三
		{"xooo..", scoreSleepThree},                 // 眠三
		{".oo.o.", scoreJumpThree},                  // 跳活三
		{"..oo...", scoreLiveTwo},                   // 活二
		{"xoo..", scoreSleepTwo},                    // 眠二
		{".o....", scoreOne},                        // lone stone with space
		{"xoooox", 0},                               // dead four
	}
	for _, c := range cases {
		if got := lineScore(t, c.pattern, playerMe); got != c.want {
			t.Errorf("scoreLineFor(%q) = %d, want %d", c.pattern, got, c.want)
		}
	}
}

func TestMakesFive(t *testing.T) {
	s := diagramToSearcher(t, `
.......
...o...
...o...
...o...
...o...
.......
.......
`)
	if !s.makesFive(5*7+3, playerMe) {
		t.Error("expected five at (3,5)")
	}
	if s.makesFive(5*7+3, playerOpp) {
		t.Error("opponent should not have five there")
	}
	if s.makesFive(0, playerMe) {
		t.Error("empty corner is not a five")
	}
}

func TestEmptyBoardPlaysCenter(t *testing.T) {
	s := newSearcher(15, make([]int, 15*15), 1<<20)
	x, y := s.run(4, 200*time.Millisecond)
	if x != 7 || y != 7 {
		t.Errorf("empty board move = (%d,%d), want (7,7)", x, y)
	}
}

func TestTakesImmediateWin(t *testing.T) {
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
	x, y := s.run(4, 300*time.Millisecond)
	if y != 7 || (x != 2 && x != 7) {
		t.Errorf("move = (%d,%d), want completion of own four at (2,7) or (7,7)", x, y)
	}
}

func TestBlocksOpponentFive(t *testing.T) {
	// opponent four x,x,x,x blocked on the left by our stone: block at (9,6) is forced
	s := diagramToSearcher(t, `
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
	x, y := s.run(4, 300*time.Millisecond)
	if x != 9 || y != 6 {
		t.Errorf("move = (%d,%d), want forced block at (9,6)", x, y)
	}
}

func TestBlocksOpponentFourCompletion(t *testing.T) {
	// opponent four with a single completion point at (3,7)
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
....xxxxo..
...........
...........
...........
`)
	x, y := s.run(4, 300*time.Millisecond)
	if x != 3 || y != 7 {
		t.Errorf("move = (%d,%d), want forced block at (3,7)", x, y)
	}
}

func TestAnswersLiveThree(t *testing.T) {
	// opponent live three must be answered at one of its open ends
	s := diagramToSearcher(t, `
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
`)
	x, y := s.run(4, 300*time.Millisecond)
	if y != 7 || (x != 3 && x != 7) {
		t.Errorf("move = (%d,%d), want block at (3,7) or (7,7)", x, y)
	}
}

// TestEvaluateSymmetry checks the foundation of minimax: the static score is
// antisymmetric under a color swap (plan step 3 — "对电脑越有利分数越大").
// A biased evaluate would corrupt every max/min decision above it.
func TestEvaluateSymmetry(t *testing.T) {
	const n = 9
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 300; trial++ {
		b := make([]int, n*n)
		for p := range b {
			switch r := rng.Intn(100); {
			case r < 22:
				b[p] = playerMe
			case r < 44:
				b[p] = playerOpp
			}
		}
		mine := newSearcher(n, b, 0).evaluate()
		theirs := newSearcher(n, swapPlayers(b), 0).evaluate()
		if mine != -theirs {
			t.Fatalf("trial %d: evaluate(me)=%d, evaluate(swapped)=%d — not antisymmetric", trial, mine, theirs)
		}
	}
}

// TestMinimaxPrefersGrowingLine: at depth 2 the engine must prefer extending
// its own stone into a live two over an isolated placement, which the static
// evaluation ranks strictly higher.
func TestMinimaxPrefersGrowingLine(t *testing.T) {
	s := diagramToSearcher(t, `
.........
.........
.........
.........
....o.x..
.........
.........
.........
.........
`)
	moves := s.genMoves(rootMoveLimit, playerMe)
	_, mv, ok := s.searchRoot(2, moves)
	if !ok {
		t.Fatal("root search aborted despite no deadline")
	}
	x, y := mv%9, mv/9
	dx, dy := x-4, y-4
	if (dx != 0 && dy != 0 && dx != dy && dx != -dy) || max(abs(dx), abs(dy)) != 1 {
		t.Fatalf("depth 2 chose (%d,%d), want a cell adjacent to (4,4) on a shared line", x, y)
	}
}

// TestGenMovesPriorityLadder verifies the tutorial ch.5 bucket ordering:
// five points return alone, then live fours, then rush fours, and ordinary
// cells come after every threat cell.
func TestGenMovesPriorityLadder(t *testing.T) {
	// our four with both ends open: both cells score as five-completions and
	// the generator must return exactly those two, nothing else
	s := diagramToSearcher(t, `
.........
..oooo...
...xxx...
.........
.........
.........
.........
.........
.........
`)
	got := s.genMoves(20, playerMe)
	want := map[int]bool{1*9 + 1: true, 1*9 + 6: true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("four-on-board gen = %v, want only (1,1)/(6,1)", got)
	}

	// opponent four with two completion points: only the two blocks
	s = diagramToSearcher(t, `
.........
..xxxx...
...ooo...
.........
.........
.........
.........
.........
.........
`)
	got = s.genMoves(20, playerMe)
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("opp-four gen = %v, want only blocks (1,1)/(6,1)", got)
	}

	// our live four: return only the two completion points
	s = diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
...oooo..
..xxx....
.........
`)
	got = s.genMoves(20, playerMe)
	want4 := map[int]bool{6*9 + 2: true, 6*9 + 7: true}
	if len(got) != 2 || !want4[got[0]] || !want4[got[1]] {
		t.Fatalf("live-four gen = %v, want only (2,6)/(7,6)", got)
	}

	// quiet-only position: candidates rank by combined threat score
	// (descending) and the limit truncates the list
	s = diagramToSearcher(t, `
.........
.........
.........
.........
....ox...
.........
.........
.........
.........
`)
	got = s.genMoves(4, playerMe)
	if len(got) != 4 {
		t.Fatalf("quiet gen returned %d moves, want 4 (capped): %v", len(got), got)
	}
	first := s.pointScore(got[0], playerMe) + s.pointScore(got[0], playerOpp)
	last := s.pointScore(got[3], playerMe) + s.pointScore(got[3], playerOpp)
	if first < last {
		t.Fatalf("quiet ordering not descending: first=%d last=%d in %v", first, last, got)
	}
}

// TestIterativeDeepeningPrefersFastestWin is the tutorial ch.6 "最优解":
// among equally-scoring winning moves, iterative deepening must return the
// shortest path. A one-move win must beat a three-move win.
func TestIterativeDeepeningPrefersFastestWin(t *testing.T) {
	// our four o-o-o-o with both ends open: completing at (1,1) wins
	// immediately; no other move wins faster
	s := diagramToSearcher(t, `
.........
..oooo.x.
.........
.........
.........
.........
.........
.........
.........
`)
	x, y := s.run(6, 2*time.Second)
	if y != 1 || (x != 1 && x != 6) {
		t.Fatalf("move = (%d,%d), want immediate five at (1,1) or (6,1)", x, y)
	}
}

// TestIterativeDeepeningBlocksWhenLosing covers the tutorial's fatal-bug
// regression: when every line loses, the engine must still defend (delay the
// loss), never pick the shortest path to its own defeat.
func TestIterativeDeepeningBlocksWhenLosing(t *testing.T) {
	// opponent has a jump three x.x: leaving any gap lets them force a win,
	// so the engine must defend inside the shape
	s := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
....x.x..
.........
`)
	x, y := s.run(4, 2*time.Second)
	// the two interior gap/end points are the only real defences
	if y != 7 || (x != 4 && x != 6 && x != 7 && x != 9) {
		t.Fatalf("move = (%d,%d), want a defensive point on row 7 (x=4/6/7/9)", x, y)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestEngineAiMoveSmoke(t *testing.T) {
	e := NewEngine()
	e.resetBoard(9)
	e.info.TimeoutTurn = 200 // ms
	e.place(4, 4, 2)
	e.place(5, 5, 1)

	x, y := e.aiMove()
	if x < 0 || x >= 9 || y < 0 || y >= 9 || e.board[y][x] != 0 {
		t.Errorf("aiMove returned illegal cell (%d,%d)", x, y)
	}
}

// TestQuiescenceGateQuietPosition: without a freshly created four the gate
// must fall straight back to the static evaluation — the quiescence value is
// exactly evaluate(), and the scan costs nothing.
func TestQuiescenceGateQuietPosition(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
.....oo....
......x....
...........
...........
`)
	// last move o at (5,7)/(6,7) made a two, not a four
	lastP := 7*11 + 6
	if s.extendsOn(lastP, playerMe) {
		t.Fatal("test setup: a two must not count as a four")
	}
	want := -s.evaluate() // side-to-move (opponent) perspective
	got := s.quiescence(playerOpp, -1<<60, 1<<60, 2, quiescenceDepth, lastP, playerMe)
	if got != want {
		t.Fatalf("quiet position: quiescence %d != -evaluate %d", got, want)
	}
}

// TestQuiescenceSeesFive: a rush four at the horizon must be resolved — the
// static evaluation only prices the four pattern, while quiescence must see
// the five the opponent completes next move.
func TestQuiescenceSeesFive(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
...xxxx....
...........
...........
...........
`)
	// pretend the last move o... x just completed the four at (6,7)
	lastP := 7*11 + 6
	if !s.extendsOn(lastP, playerOpp) {
		t.Fatal("test setup: the four must trigger the gate")
	}
	static := s.stmEvaluate(playerOpp)
	if static >= winScore-1024 {
		t.Fatalf("static eval should not already see the five: %d", static)
	}
	got := s.quiescence(playerOpp, -1<<60, 1<<60, 2, quiescenceDepth, lastP, playerOpp)
	if got < winScore-1024 {
		t.Fatalf("quiescence missed the five: got %d, static %d", got, static)
	}
	t.Logf("static %d, quiescence %d (side-to-move perspective)", static, got)
}

// TestQuiescenceUnknownLastMoveExpands: lastP < 0 means "caller knows
// nothing" and must conservatively engage the search — the same position
// that TestQuiescenceSeesFive resolves through the gate also resolves when
// the last move is unknown.
func TestQuiescenceUnknownLastMoveExpands(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
...xxxx....
...........
...........
...........
`)
	got := s.quiescence(playerOpp, -1<<60, 1<<60, 2, quiescenceDepth, -1, 0)
	if got < winScore-1024 {
		t.Fatalf("unknown last move did not expand: got %d", got)
	}
}

// TestQuiescenceLiveFourSeesFive: a fresh live four has TWO completion
// points; the gate must open and price the position as won no matter which
// one the defender takes.
func TestQuiescenceLiveFourSeesFive(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
...xxxx....
...........
...........
...........
`)
	lastP := 7*11 + 6 // the x that completed the four
	if !s.extendsOn(lastP, playerOpp) {
		t.Fatal("test setup: the live four must trigger the gate")
	}
	got := s.quiescence(playerOpp, -1<<60, 1<<60, 2, quiescenceDepth, lastP, playerOpp)
	if got < winScore-1024 {
		t.Fatalf("quiescence priced a live-four position at %d (side-to-move perspective)", got)
	}
}

// TestQuiescenceLeftoverFourFallsBackToStatic: our block does not itself
// make a four, so the gate stays closed even though the opponent still owns
// a rush four — the documented blind spot, covered by the static evaluation
// pricing the leftover four as near-loss. The fallback must be EXACTLY the
// static value (no hidden expansion) and it must actually be good for the
// opponent to move.
func TestQuiescenceLeftoverFourFallsBackToStatic(t *testing.T) {
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
...oxxxx...
...........
...........
...........
`)
	// the opponent just completed the four at (7,7); we blocked at (3,7)
	lastP := 7*11 + 3
	if s.extendsOn(lastP, playerMe) {
		t.Fatal("test setup: the block must not open the gate")
	}
	want := s.stmEvaluate(playerOpp)
	got := s.quiescence(playerOpp, -1<<60, 1<<60, 2, quiescenceDepth, lastP, playerMe)
	if got != want {
		t.Fatalf("leftover four: quiescence %d != static %d — gate leaked", got, want)
	}
	if want < scoreRushFour/2 {
		t.Fatalf("static eval does not price the leftover four as near-loss: %d", want)
	}
}

// TestGenMovesNearEdges: the Chebyshev-2 candidate window must clip at the
// board border without going out of bounds, duplicating cells, or offering
// occupied ones. Exact counts are shape-dependent (two-or-better cells crowd
// out plain neighbours), so the assertion is the window itself.
func TestGenMovesNearEdges(t *testing.T) {
	const n = 9
	stones := []int{0, 4} // corner (0,0) and mid-edge (4,0)
	for _, st := range stones {
		b := make([]int, n*n)
		b[st] = playerMe
		s := newSearcher(n, b, 0)
		got := s.genMoves(0, playerMe)
		if len(got) == 0 {
			t.Fatalf("stone (%d,%d): no candidates", st%n, st/n)
		}
		seen := make(map[int]bool)
		for _, m := range got {
			if seen[m] {
				t.Fatalf("stone (%d,%d): duplicate candidate %d", st%n, st/n, m)
			}
			seen[m] = true
			if m < 0 || m >= n*n || s.b[m] != 0 {
				t.Fatalf("stone (%d,%d): illegal candidate (%d,%d)", st%n, st/n, m%n, m/n)
			}
			x, y := m%n, m/n
			sx, sy := st%n, st/n
			dx, dy := x-sx, y-sy
			if dx < -2 || dx > 2 || dy < -2 || dy > 2 {
				t.Fatalf("stone (%d,%d): candidate (%d,%d) outside the radius-2 window", sx, sy, x, y)
			}
		}
	}
}

// TestGenMovesQuietTailContract pins the genMovesRanked ordering contract:
// without threats the whole list is the reorderable tail (quietFrom == 0,
// slot-0 protection lives in minimax) ranked by descending combined score;
// with a pending four threat the strongest reply leads and quietFrom fences
// the forced segment off from history reordering.
func TestGenMovesQuietTailContract(t *testing.T) {
	// quiet position: two stones, no threats anywhere
	s := diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
.....o.....
......x....
...........
...........
...........
`)
	moves, quietFrom := s.genMovesRanked(rootMoveLimit, playerMe)
	if len(moves) == 0 || len(moves) > rootMoveLimit {
		t.Fatalf("quiet gen returned %d moves", len(moves))
	}
	if quietFrom != 0 {
		t.Fatalf("quiet position reported quietFrom=%d, want 0", quietFrom)
	}
	key := func(m int) int { return s.pointScore(m, playerMe) + s.pointScore(m, playerOpp) }
	for i := 1; i < len(moves); i++ {
		if key(moves[i-1]) < key(moves[i]) {
			t.Fatalf("quiet tail not descending at %d: %v", i, moves)
		}
	}

	// opponent three with one end blocked by our stone at (2,7): (7,7) makes
	// the opponent a jump four (o.ooo scores above the live-four threshold)
	// and (6,7) a plain rush four — the jump reply leads the forced segment
	s = diagramToSearcher(t, `
...........
...........
...........
...........
...........
...........
...........
..oxxx.....
...........
...........
...........
`)
	moves, quietFrom = s.genMovesRanked(rootMoveLimit, playerMe)
	if quietFrom != 1 {
		t.Fatalf("threat position quietFrom=%d, want 1", quietFrom)
	}
	if moves[0] != 7*11+7 {
		t.Fatalf("forced reply = (%d,%d), want the jump-four point (7,7)", moves[0]%11, moves[0]/11)
	}
	for i := 0; i < quietFrom; i++ {
		if s.pointScore(moves[i], playerOpp) < scoreRushFour {
			t.Fatalf("forced segment contains a non-four-threat cell %d", moves[i])
		}
	}
}

// TestPositionBonusProperties: the center pyramid pays the center most, edge
// nothing, stays far below a sleep two, and flips sign with the side to move
// (anti-symmetry, same contract as the line scores).
func TestPositionBonusProperties(t *testing.T) {
	const n = 15
	if got := posBonus(7*n+7, n); got != 7*scorePosUnit {
		t.Fatalf("center bonus = %d, want %d", got, 7*scorePosUnit)
	}
	if got := posBonus(0, n); got != 0 {
		t.Fatalf("corner bonus = %d, want 0", got)
	}
	if 7*scorePosUnit >= scoreSleepTwo {
		t.Fatalf("max pyramid %d must stay below a sleep two %d", 7*scorePosUnit, scoreSleepTwo)
	}
	// incremental total matches the oracle and flips sign across sides —
	// stones placed through setStone so the running sum tracks them
	s := newSearcher(n, make([]int, n*n), 0)
	s.posEnabled = true // bonus defaults off (arena-gated); this pins the on-variant
	s.setStone(7*n+7, playerMe)
	s.setStone(4*n+4, playerOpp)
	if s.evaluate() != s.evaluateFull() {
		t.Fatalf("incremental %d != oracle %d", s.evaluate(), s.evaluateFull())
	}
	want := posBonus(7*n+7, n) - posBonus(4*n+4, n)
	if s.posTotal != want {
		t.Fatalf("posTotal = %d, want %d", s.posTotal, want)
	}
}

// TestPositionBonusDisabledIsOldEval: with the bonus off, evaluate() must
// equal the pure line-cache total — the arena A/B switch keeps the legacy
// semantics bit for bit.
func TestPositionBonusDisabledIsOldEval(t *testing.T) {
	const n = 15
	b := make([]int, n*n)
	b[7*n+7] = playerMe
	b[4*n+4] = playerOpp
	s := newSearcher(n, b, 0)
	s.posEnabled = false
	s.posTotal = 0
	if s.evaluate() != s.evalTotal {
		t.Fatalf("disabled bonus leaked: evaluate=%d evalTotal=%d", s.evaluate(), s.evalTotal)
	}
}

// TestQuiescenceThreeExtensionSeesFourChain: with qThreeEnabled, a horizon
// leaf where the last move made a live three must see the coming four chain —
// the static evaluation only sees the three, the extended quiescence sees
// the live-four/five potential at full weight.
func TestQuiescenceThreeExtensionSeesFourChain(t *testing.T) {
	s := diagramToSearcher(t, `
.........
.........
.........
.........
...xx....
.........
.........
.........
.........
`)
	s.qThreeEnabled = true // gate defaults off until its arena pass
	// opponent just grew their pair into a fresh live three at (2,4): three
	// contiguous x with both ends open, engine to move (flat index y*n+x)
	s.makeMove(4*9+2, playerOpp)
	static := s.stmEvaluate(playerMe)
	v := s.quiescence(playerMe, -1<<60, 1<<60, 1, quiescenceDepth, 4*9+2, playerOpp)
	if v == static {
		t.Fatalf("three-extension quiescence returned the static %d — the four chain was not resolved", static)
	}
	t.Logf("static=%d q=%d", static, v)

	// and with the gate off, the same leaf must stay at the static value
	s.qThreeEnabled = false
	off := s.quiescence(playerMe, -1<<60, 1<<60, 1, quiescenceDepth, 4*9+2, playerOpp)
	if off != static {
		t.Fatalf("gate off: quiescence %d != static %d", off, static)
	}
}

// TestQuiescenceThreeGateQuietStaysFree: a quiet leaf must cost the same
// nodes with the extension on or off — the gate admits only fresh threes.
func TestQuiescenceThreeGateQuietStaysFree(t *testing.T) {
	s := diagramToSearcher(t, `
.........
.........
.........
.........
....o.x..
.........
.........
.........
.........
`)
	s.makeMove(6*9+4, playerOpp) // a scattered pair, no three anywhere
	base := s.nodes
	_ = s.quiescence(playerMe, -1<<60, 1<<60, 1, quiescenceDepth, 6*9+4, playerOpp)
	on := s.nodes - base
	t.Logf("quiet-leaf quiescence nodes with extension on: %d", on)
	if on > 3 {
		t.Fatalf("quiet leaf expanded %d nodes — the three gate is leaking", on)
	}
}
