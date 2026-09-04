package random

import (
	"math/rand"
	"testing"

	"gomoku/algorithm"
)

// buildReq assembles a Request from a stone map {coordinate: side}.
func buildReq(n int, stones map[[2]int]int, last [2]int, hasLast bool) algorithm.Request {
	b := make([]int, n*n)
	for pt, side := range stones {
		b[pt[1]*n+pt[0]] = side
	}
	return algorithm.Request{
		Size:    n,
		Board:   b,
		HasLast: hasLast,
		LastX:   last[0],
		LastY:   last[1],
	}
}

// TestRandomOpensAtTengen: an empty board must answer with the tengen
// (天元), whatever the board size.
func TestRandomOpensAtTengen(t *testing.T) {
	for _, n := range []int{9, 15, 20} {
		a := newWithRand(rand.New(rand.NewSource(1)))
		x, y := a.Think(buildReq(n, nil, [2]int{}, false))
		if x != n/2 || y != n/2 {
			t.Fatalf("size %d: empty board = (%d,%d), want tengen (%d,%d)", n, x, y, n/2, n/2)
		}
	}
}

// TestRandomPlaysInContactZone: with one stone every reply lies within
// Chebyshev distance 2 of it, on an empty cell — and the draw actually
// varies (the point of a random algorithm).
func TestRandomPlaysInContactZone(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(7)))
	stones := map[[2]int]int{{4, 4}: 2}
	seen := make(map[[2]int]bool)
	for i := 0; i < 500; i++ {
		req := buildReq(n, stones, [2]int{4, 4}, true)
		x, y := a.Think(req)
		if req.Board[y*n+x] != 0 {
			t.Fatalf("draw %d: played on an occupied cell (%d,%d)", i, x, y)
		}
		dx, dy := x-4, y-4
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx > 2 || dy > 2 {
			t.Fatalf("draw %d: (%d,%d) outside the Chebyshev-2 contact zone", i, x, y)
		}
		seen[[2]int{x, y}] = true
	}
	// with the minimum-distance rule the pool is the 8 ring-1 cells around
	// the lone stone; the draw must still not collapse onto a couple of them
	if len(seen) < 6 {
		t.Fatalf("only %d distinct cells drawn — not random enough", len(seen))
	}
}

// TestRandomBlocksJumpFive: 跳连 — four same-side stones with exactly one
// gap in a 5-span (x.xxx) must be answered by taking the gap.
func TestRandomBlocksJumpFive(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(3)))
	// opponent (side 2): X.XXX on row 4, the last move closed the XXX part
	stones := map[[2]int]int{
		{3, 4}: 2, {5, 4}: 2, {6, 4}: 2, {7, 4}: 2,
		{0, 0}: 1, // one own stone far away
	}
	for i := 0; i < 50; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{7, 4}, true))
		if x != 4 || y != 4 {
			t.Fatalf("draw %d: jump five answered at (%d,%d), want the gap (4,4)", i, x, y)
		}
	}
}

// TestRandomBlocksBlockedStraightFour: a straight four with one open end
// must be answered at that end (a five-threat window too).
func TestRandomBlocksBlockedStraightFour(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(4)))
	stones := map[[2]int]int{
		{2, 4}: 1,                                  // engine blocks the west end
		{3, 4}: 2, {4, 4}: 2, {5, 4}: 2, {6, 4}: 2, // opponent four, east end open
	}
	for i := 0; i < 50; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{6, 4}, true))
		if x != 7 || y != 4 {
			t.Fatalf("draw %d: straight four answered at (%d,%d), want (7,4)", i, x, y)
		}
	}
}

// TestRandomBlocksOpenFourAtEitherEnd: an open four offers two five windows
// — the reply must be one of the two ends.
func TestRandomBlocksOpenFourAtEitherEnd(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(5)))
	stones := map[[2]int]int{
		{3, 4}: 2, {4, 4}: 2, {5, 4}: 2, {6, 4}: 2,
		{0, 0}: 1,
	}
	for i := 0; i < 100; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{6, 4}, true))
		if !((x == 2 && y == 4) || (x == 7 && y == 4)) {
			t.Fatalf("draw %d: open four answered at (%d,%d), want an end (2,4)/(7,4)", i, x, y)
		}
	}
}

// TestRandomBlocksOpenThreeNearEndFirst: 活三 → block one of the two ends,
// random but proximity-weighted: the end nearer the last move must come up
// clearly more often.
func TestRandomBlocksOpenThreeNearEndFirst(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(6)))
	stones := map[[2]int]int{
		{3, 4}: 2, {4, 4}: 2, {5, 4}: 2, // open three, last stone on its east end
		{0, 0}: 1,
	}
	near, far := 0, 0
	for i := 0; i < 600; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{5, 4}, true))
		switch {
		case x == 6 && y == 4: // distance 1 from (5,4)
			near++
		case x == 2 && y == 4: // distance 3 from (5,4)
			far++
		default:
			t.Fatalf("draw %d: open three answered at (%d,%d), want an end (6,4)/(2,4)", i, x, y)
		}
	}
	if near <= far {
		t.Fatalf("proximity weighting inverted: near-end %d vs far-end %d", near, far)
	}
}

// TestRandomCompletesOwnFourFirst: 两层搜索·第一层 — the engine's own four
// is completed for the immediate win, ahead of every defense: even while the
// opponent threatens an open three or an open four, the five point is played.
func TestRandomCompletesOwnFourFirst(t *testing.T) {
	const n = 9
	cases := []struct {
		name   string
		stones map[[2]int]int
		want   [2]int
	}{
		{
			name: "rush four 冲四, opponent open three waits",
			stones: map[[2]int]int{
				{1, 2}: 2,                                  // opponent caps the west end
				{2, 2}: 1, {3, 2}: 1, {4, 2}: 1, {5, 2}: 1, // own four, five point east
				{3, 6}: 2, {4, 6}: 2, {5, 6}: 2, // opponent open three (would be blocked without this layer)
			},
			want: [2]int{6, 2},
		},
		{
			name: "jump four 跳四, the gap wins",
			stones: map[[2]int]int{
				{1, 2}: 2,
				{2, 2}: 1, {3, 2}: 1, {4, 2}: 1, {6, 2}: 1, // x.xxx, gap at (5,2)
				{7, 7}: 2,
			},
			want: [2]int{5, 2},
		},
		{
			name: "own four outranks the opponent's open four",
			stones: map[[2]int]int{
				{1, 2}: 2,
				{2, 2}: 1, {3, 2}: 1, {4, 2}: 1, {5, 2}: 1,
				{2, 6}: 2, {3, 6}: 2, {4, 6}: 2, {5, 6}: 2, // opponent open four!
			},
			want: [2]int{6, 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newWithRand(rand.New(rand.NewSource(21)))
			for i := 0; i < 50; i++ {
				x, y := a.Think(buildReq(n, tc.stones, [2]int{5, 6}, true))
				if x != tc.want[0] || y != tc.want[1] {
					t.Fatalf("draw %d: answered at (%d,%d), want the own five point (%d,%d)",
						i, x, y, tc.want[0], tc.want[1])
				}
			}
		})
	}
}

// TestRandomBlocksJumpOpenThree: 两层搜索·第二层 — 跳活三 (.xx.x. / .x.xx. with
// both outer cells empty) is invisible to the contiguous-run check; the gap
// must be taken so the shape dies before it becomes a live four.
func TestRandomBlocksJumpOpenThree(t *testing.T) {
	const n = 9
	cases := []struct {
		name   string
		stones map[[2]int]int
		last   [2]int
		want   [2]int
	}{
		{
			name:   "horizontal .xx.x.",
			stones: map[[2]int]int{{0, 0}: 1, {2, 4}: 2, {3, 4}: 2, {5, 4}: 2},
			last:   [2]int{5, 4},
			want:   [2]int{4, 4},
		},
		{
			name:   "horizontal .x.xx.",
			stones: map[[2]int]int{{0, 0}: 1, {2, 4}: 2, {4, 4}: 2, {5, 4}: 2},
			last:   [2]int{5, 4},
			want:   [2]int{3, 4},
		},
		{
			name:   "diagonal .xx.x.",
			stones: map[[2]int]int{{0, 8}: 1, {3, 3}: 2, {4, 4}: 2, {6, 6}: 2},
			last:   [2]int{6, 6},
			want:   [2]int{5, 5},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newWithRand(rand.New(rand.NewSource(22)))
			for i := 0; i < 50; i++ {
				x, y := a.Think(buildReq(n, tc.stones, tc.last, true))
				if x != tc.want[0] || y != tc.want[1] {
					t.Fatalf("draw %d: jump open three answered at (%d,%d), want the gap (%d,%d)",
						i, x, y, tc.want[0], tc.want[1])
				}
			}
		})
	}
}

// TestRandomPrefersFivePointOverJumpThree: the layers keep their order — a
// jump four (an immediate five threat) through the last move is answered
// before a jump open three crossing the same stone.
func TestRandomPrefersFivePointOverJumpThree(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(23)))
	stones := map[[2]int]int{
		{0, 0}: 1,
		// row 4: jump four x.xxx through L=(6,4), five point (5,4)
		{2, 4}: 2, {3, 4}: 2, {4, 4}: 2, {6, 4}: 2,
		// column 6: jump open three .x.xx. through the same L, gap (6,3)
		{6, 2}: 2, {6, 5}: 2,
	}
	for i := 0; i < 50; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{6, 4}, true))
		if x != 5 || y != 4 {
			t.Fatalf("draw %d: answered at (%d,%d), want the jump four's gap (5,4)", i, x, y)
		}
	}
}

// TestRandomPlaysOnlyAtMinimumDistance: in the plain random phase every
// reply must sit in the ring at minimum Chebyshev distance from the last
// move — never one ring further out.
func TestRandomPlaysOnlyAtMinimumDistance(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(8)))
	stones := map[[2]int]int{{4, 4}: 2}
	seen := make(map[[2]int]bool)
	for i := 0; i < 3000; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{4, 4}, true))
		if x < 3 || x > 5 || y < 3 || y > 5 {
			t.Fatalf("draw %d: (%d,%d) is not at Chebyshev distance 1 from (4,4)", i, x, y)
		}
		seen[[2]int{x, y}] = true
	}
	// the ring-1 set is 8 cells; the draw must not collapse onto one or two
	if len(seen) < 6 {
		t.Fatalf("only %d distinct cells drawn inside the minimum ring — tie draw broken", len(seen))
	}
}

// TestRandomAnchorsDistanceOnLastMove: the ranking key is the last move, not
// the nearest stone — a last move far from the cluster drags the reply to
// its own ring 1.
func TestRandomAnchorsDistanceOnLastMove(t *testing.T) {
	const n = 15
	a := newWithRand(rand.New(rand.NewSource(11)))
	stones := map[[2]int]int{
		{7, 7}: 1, {8, 8}: 2, // a cluster around the tengen
		{3, 3}: 2, // the last move, far from the cluster
	}
	for i := 0; i < 200; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{3, 3}, true))
		if x < 2 || x > 4 || y < 2 || y > 4 {
			t.Fatalf("draw %d: answered at (%d,%d), want ring-1 of the last move (3,3)", i, x, y)
		}
	}
}

// TestRandomNoLastMoveFallsBackToNearestStone: without last-move info the
// ranking key becomes the distance to the nearest stone — with a single
// stone on the board the reply hugs it.
func TestRandomNoLastMoveFallsBackToNearestStone(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(12)))
	stones := map[[2]int]int{{2, 2}: 2}
	for i := 0; i < 200; i++ {
		x, y := a.Think(buildReq(n, stones, [2]int{}, false))
		if x < 1 || x > 3 || y < 1 || y > 3 {
			t.Fatalf("draw %d: answered at (%d,%d), want ring-1 of the only stone (2,2)", i, x, y)
		}
	}
}

// TestRandomNeverPlaysOccupiedOnBusyBoard: on a midgame position the reply
// is always an empty in-bounds cell.
func TestRandomNeverPlaysOccupiedOnBusyBoard(t *testing.T) {
	const n = 9
	a := newWithRand(rand.New(rand.NewSource(9)))
	stones := map[[2]int]int{
		{4, 4}: 2, {5, 5}: 1, {3, 5}: 1, {5, 3}: 2, {4, 6}: 1, {6, 4}: 2,
	}
	for i := 0; i < 300; i++ {
		req := buildReq(n, stones, [2]int{6, 4}, true)
		x, y := a.Think(req)
		if x < 0 || x >= n || y < 0 || y >= n || req.Board[y*n+x] != 0 {
			t.Fatalf("draw %d: illegal reply (%d,%d)", i, x, y)
		}
	}
}

// TestRandomRegisteredInTheAlgorithmSet: the registry hands out the random
// algorithm by name.
func TestRandomRegisteredInTheAlgorithmSet(t *testing.T) {
	algo, err := algorithm.New(AlgorithmName)
	if err != nil {
		t.Fatalf("registry lookup: %v", err)
	}
	if algo.Name() != AlgorithmName {
		t.Fatalf("name = %q, want %q", algo.Name(), AlgorithmName)
	}
	// Reset/EndSession must be safe no-ops on a stateless algorithm
	algo.Reset(15, 0)
	algo.EndSession()
}
