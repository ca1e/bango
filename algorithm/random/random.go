package random

// Package random is the lightweight algorithm of the bango algorithm set: it
// plays a weighted-random contact move instead of searching, but still
// answers the threats the previous move ("前一手") created. Used as a baseline
// opponent for arena testing, a sanity opponent for opening-book builds, and
// the reference example for implementing the algorithm.Algorithm interface.
//
// Decision pipeline (Think):
//
//  1. Empty board → the tengen (天元, the board center).
//  2. Kill-move judgment (杀局判断), all of it keyed to the previous move's
//     stone L and its side:
//     a. Five threats (跳连/冲四的成五点): slide a 5-cell window along each
//     of the 4 directions through L; a window holding 4 same-side stones
//     and exactly one empty cell completes a five next turn — the empty
//     cell must be taken now. Several windows may qualify (an open four
//     offers both ends); the reply is drawn at random with proximity
//     weighting.
//     b. Open three / open four (活三/活四): the contiguous run through L
//     is 3 or 4 long with both flanking cells empty — block one of the two
//     ends, chosen at random, again proximity-weighted ("两个方向随机选
//     择，以距离前一手落子最近为优先").
//  3. Otherwise the move pool is every empty intersection within Chebyshev
//     distance < 3 of any stone on the board; only the cells at the minimum
//     Chebyshev distance from the previous move stay in play, and the draw
//     runs among them — weighted-random only when several share the minimum.
//
// Weights: w = 3 - max(|dx|, |dy|) clamped to ≥ 1. They still steer the
// kill-move picks; inside the minimum-distance set every survivor carries
// the same weight, so the tie draw is uniform. Without a previous move the
// ranking key becomes the distance to the nearest stone.

import (
	"math/rand"
	"time"

	"gomoku/algorithm"
)

// AlgorithmName is the registry name of the random algorithm.
const AlgorithmName = "random"

func init() {
	algorithm.Register(AlgorithmName, func() algorithm.Algorithm { return New() })
}

// Algorithm implements algorithm.Algorithm with weighted-random moves plus
// last-move threat defense. It keeps no state between thinks, so Reset and
// EndSession are no-ops.
type Algorithm struct {
	rng *rand.Rand
}

// New creates the random algorithm seeded from the current time.
func New() *Algorithm {
	return &Algorithm{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// newWithRand builds the algorithm on a caller-provided source (tests pin a
// seed for deterministic draws).
func newWithRand(rng *rand.Rand) *Algorithm { return &Algorithm{rng: rng} }

// Name implements algorithm.Algorithm.
func (a *Algorithm) Name() string { return AlgorithmName }

// Reset implements algorithm.Algorithm; the random algorithm is stateless.
func (a *Algorithm) Reset(size int, maxMemory int64) {}

// EndSession implements algorithm.Algorithm; nothing to drop.
func (a *Algorithm) EndSession() {}

// dirX/dirY are the 4 reading directions (row, column, two diagonals).
var (
	dirX = [4]int{1, 0, 1, 1}
	dirY = [4]int{0, 1, 1, -1}
)

// Think implements algorithm.Algorithm. It always returns an in-bounds empty
// cell when one exists (the tengen as the very last resort).
func (a *Algorithm) Think(req algorithm.Request) (int, int) {
	n := req.Size
	b := req.Board

	// stones are the real pieces (1/2); field-3 marks never anchor anything
	var stones []int
	for p, v := range b {
		if v == 1 || v == 2 {
			stones = append(stones, p)
		}
	}
	if len(stones) == 0 {
		return n / 2, n / 2 // empty board: the tengen 天元
	}

	// last-move anchor for threat analysis and proximity weighting
	last := -1
	if req.HasLast {
		if p := req.LastY*n + req.LastX; p >= 0 && p < n*n && (b[p] == 1 || b[p] == 2) {
			last = p
		}
	}

	// 杀局判断 — only meaningful when the previous move is known
	if last >= 0 {
		if p, ok := a.fiveThreat(b, n, last); ok {
			return p % n, p / n
		}
		if p, ok := a.openRunBlock(b, n, last); ok {
			return p % n, p / n
		}
	}

	// move pool: empty cells within Chebyshev distance < 3 of any stone,
	// then narrowed to the minimum Chebyshev distance from the last move
	var cands []int
	for p, v := range b {
		if v != 0 {
			continue
		}
		if nearAnyStone(stones, n, p) {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		// contact zone exhausted (near-full board): any empty cell
		for p, v := range b {
			if v == 0 {
				cands = append(cands, p)
			}
		}
	}
	if len(cands) == 0 {
		return n / 2, n / 2 // full board: nothing legal, defer to the engine
	}
	pick := a.weightedPick(nearestToLast(cands, stones, n, last), func(p int) int { return proximityWeight(p, last, n) })
	return pick % n, pick / n
}

// chebyshev is |x1-x2| ⊔ |y1-y2|, the board distance used throughout.
func chebyshev(p, q, n int) int {
	dx, dy := p%n-q%n, p/n-q/n
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// nearestToLast keeps only the candidates at the minimum Chebyshev distance
// from the last move (取距前一切比雪夫距离最小的点); with no known last move
// the ranking key is the distance to the nearest stone instead. Ties stay —
// the weighted draw breaks them.
func nearestToLast(cands, stones []int, n, last int) []int {
	if len(cands) <= 1 {
		return cands
	}
	key := func(p int) int {
		if last >= 0 {
			return chebyshev(p, last, n)
		}
		best := n * 2
		for _, s := range stones {
			if d := chebyshev(p, s, n); d < best {
				best = d
			}
		}
		return best
	}
	best := key(cands[0])
	out := cands[:0]
	for _, p := range cands {
		switch k := key(p); {
		case k < best:
			best = k
			out = out[:0]
			out = append(out, p)
		case k == best:
			out = append(out, p)
		}
	}
	return out
}

// nearAnyStone reports whether p lies within Chebyshev distance 2 of one of
// the stones (距离当前棋盘所有子的切比雪夫距离 < 3).
func nearAnyStone(stones []int, n, p int) bool {
	x, y := p%n, p/n
	for _, s := range stones {
		dx, dy := x-s%n, y-s/n
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx < 3 && dy < 3 {
			return true
		}
	}
	return false
}

// fiveThreat finds a five-in-a-row threat created by the last move's side:
// along the 4 directions through L, any fully on-board 5-cell window holding
// 4 same-side stones and exactly 1 empty cell completes five next turn
// (straight four, jump four 跳连 — 同一方子数量为 4 且有且仅有一个空位置).
// Returns the empty cell to block; ties (an open four's two ends) resolve
// through the proximity-weighted draw.
func (a *Algorithm) fiveThreat(b []int, n, last int) (int, bool) {
	side := b[last]
	lx, ly := last%n, last/n

	var blocks []int
	for d := 0; d < 4; d++ {
		dx, dy := dirX[d], dirY[d]
		for k := -4; k <= 0; k++ {
			stonesIn, empties, empty := 0, 0, -1
			blocked := false
			for i := 0; i < 5; i++ {
				x, y := lx+(k+i)*dx, ly+(k+i)*dy
				if x < 0 || y < 0 || x >= n || y >= n {
					blocked = true // off-board: this window can never make five
					break
				}
				switch b[y*n+x] {
				case side:
					stonesIn++
				case 0:
					empties++
					empty = y*n + x
				default: // opponent stone or field-3 mark
					blocked = true
				}
			}
			if !blocked && stonesIn == 4 && empties == 1 {
				blocks = append(blocks, empty)
			}
		}
	}
	if len(blocks) == 0 {
		return 0, false
	}
	pick := a.weightedPick(dedupe(blocks), func(p int) int { return proximityWeight(p, last, n) })
	return pick, true
}

// openRunBlock detects the open three / open four through the last move's
// stone: a contiguous same-side run of length 3-4 whose two flanking cells
// are both empty (活三/活四局面). Returns one of the two run ends, drawn at
// random with proximity weighting (两个方向随机选择).
func (a *Algorithm) openRunBlock(b []int, n, last int) (int, bool) {
	side := b[last]
	lx, ly := last%n, last/n

	var ends []int
	for d := 0; d < 4; d++ {
		dx, dy := dirX[d], dirY[d]
		count := func(stepX, stepY int) int {
			c := 0
			for x, y := lx+stepX, ly+stepY; ; x, y = x+stepX, y+stepY {
				if x < 0 || y < 0 || x >= n || y >= n || b[y*n+x] != side {
					return c
				}
				c++
			}
		}
		fwd := count(dx, dy)
		bwd := count(-dx, -dy)
		run := 1 + fwd + bwd
		if run < 3 || run > 4 {
			continue
		}
		empty := func(x, y int) bool {
			return x >= 0 && y >= 0 && x < n && y < n && b[y*n+x] == 0
		}
		e1x, e1y := lx+(fwd+1)*dx, ly+(fwd+1)*dy
		e2x, e2y := lx-(bwd+1)*dx, ly-(bwd+1)*dy
		if empty(e1x, e1y) && empty(e2x, e2y) {
			ends = append(ends, e1y*n+e1x, e2y*n+e2x)
		}
	}
	if len(ends) == 0 {
		return 0, false
	}
	pick := a.weightedPick(ends, func(p int) int { return proximityWeight(p, last, n) })
	return pick, true
}

// proximityWeight is the draw weight of cell p: 3 at Chebyshev distance 0
// from the last move, 2 at distance 1, 1 further out (and everywhere when
// no last move exists) — 距离前一手落子最近为优先.
func proximityWeight(p, last, n int) int {
	if last < 0 {
		return 1
	}
	dx, dy := p%n-last%n, p/n-last/n
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	max := dx
	if dy > max {
		max = dy
	}
	w := 3 - max
	if w < 1 {
		return 1
	}
	return w
}

// weightedPick draws one candidate with the given weights (each weight
// clamped to ≥ 1 so every candidate keeps a chance).
func (a *Algorithm) weightedPick(cands []int, weight func(int) int) int {
	total := 0
	for _, c := range cands {
		total += clampWeight(weight(c))
	}
	r := a.rng.Intn(total)
	for _, c := range cands {
		r -= clampWeight(weight(c))
		if r < 0 {
			return c
		}
	}
	return cands[len(cands)-1]
}

func clampWeight(w int) int {
	if w < 1 {
		return 1
	}
	return w
}

// dedupe returns the candidates once each, preserving order.
func dedupe(cands []int) []int {
	seen := make(map[int]bool, len(cands))
	out := make([]int, 0, len(cands))
	for _, p := range cands {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
