package alphabeta

// Classic opening modes (开局 26 式): black opens at the tengen, white
// answers direct (orthogonal-adjacent) or diagonal, and black's second move
// takes one symmetry class inside the 5×5 box around the tengen. Folding the
// box under the stabiliser of {tengen, white reply} yields 14 direct and 12
// diagonal classes — 26 modes in total.
//
// The enumeration and the classifier share one machine with the opening
// book's canonicalisation (book.PositionKey): a mode is the canonical key of
// its three-stone set, and the frame transform maps the classic book's
// replies between the canonical and the actual board orientation.

import (
	"fmt"
	"gomoku/book"
	"sort"
	"strings"
)

// classicOpeningSeeds returns one seed line [tengen, white, black2] (board
// coordinates) per classic opening mode: every white reply family
// (direct/diagonal) crossed with black's second move inside the 5×5 box,
// deduplicated by canonical key.
func classicOpeningSeeds(size int) [][]int {
	c := size / 2
	bases := [2][2]int{{0, -1}, {1, -1}} // direct, diagonal
	var seeds [][]int
	seen := make(map[string]bool)
	for _, base := range bases {
		wx, wy := c+base[0], c+base[1]
		for bx := -2; bx <= 2; bx++ {
			for by := -2; by <= 2; by++ {
				if (bx == 0 && by == 0) || (bx == base[0] && by == base[1]) {
					continue // tengen / white stone occupied
				}
				stones := []book.Stone{
					{X: c, Y: c, Role: 1},
					{X: wx, Y: wy, Role: -1},
					{X: c + bx, Y: c + by, Role: 1},
				}
				key, _ := book.PositionKey(stones, size)
				if seen[key] {
					continue // symmetric twin of an earlier class
				}
				seen[key] = true
				seeds = append(seeds, []int{c, c, wx, wy, c + bx, c + by})
			}
		}
	}
	return seeds
}

// openingFrame describes a recognized classic opening: the mode's canonical
// three-stone frame ([tengen, white, black2]) and the transform t that maps
// board stones into it (t⁻¹ maps book replies back to the board).
type openingFrame struct {
	canon []book.Stone
	t     int
	id    string // canonical key — the mode's identity
}

// openingModeOf recognizes the classic opening mode on the current board.
// Requirements: black owns the tengen; exactly one white stone adjacent to
// it (direct or diagonal); black's other stone inside the 5×5 box. With four
// stones the extra white stone (the deviation) is ignored — the mode is
// defined by the first three stones. Anything else (non-tengen opening,
// remote white reply, three black stones in the box) is not a classic mode.
func openingModeOf(s *searcher) (openingFrame, bool) {
	n := s.n
	c := n / 2
	if s.b[c*n+c] != s.blackSide {
		return openingFrame{}, false // black does not own the tengen
	}
	black2 := -1
	var whiteAdj []int
	for p, v := range s.b {
		if v == 0 || p == c*n+c {
			continue
		}
		x, y := p%n, p/n
		dx, dy := x-c, y-c
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if v == s.blackSide {
			if black2 >= 0 || dx > 2 || dy > 2 {
				return openingFrame{}, false // third black stone / outside the box
			}
			black2 = p
			continue
		}
		if (dx == 0 && dy == 1) || (dx == 1 && dy == 0) || (dx == 1 && dy == 1) {
			whiteAdj = append(whiteAdj, p)
		}
	}
	if black2 < 0 || len(whiteAdj) != 1 {
		return openingFrame{}, false // no (or ambiguous) white reply
	}

	stones := []book.Stone{
		{X: c, Y: c, Role: 1},
		{X: whiteAdj[0] % n, Y: whiteAdj[0] / n, Role: -1},
		{X: black2 % n, Y: black2 / n, Role: 1},
	}
	// find the min-encoding transform — the canonical frame of this mode.
	// Must mirror book.PositionKey exactly: per transform the parts are SORTED
	// before joining, so the keys match the compiled book's map.
	best, bestT := "", -1
	for t := 0; t < 8; t++ {
		parts := make([]string, 0, 3)
		for _, st := range stones {
			tx, ty := book.TransformPoint(st.X, st.Y, n, t)
			parts = append(parts, fmt.Sprintf("%d,%d,%d", tx, ty, st.Role))
		}
		sort.Strings(parts)
		enc := strings.Join(parts, ";")
		if best == "" || enc < best {
			best, bestT = enc, t
		}
	}
	canonical := make([]book.Stone, len(stones))
	for i, st := range stones {
		tx, ty := book.TransformPoint(st.X, st.Y, n, bestT)
		canonical[i] = book.Stone{X: tx, Y: ty, Role: st.Role}
	}
	return openingFrame{canon: canonical, t: bestT, id: fmt.Sprintf("%d|%s", n, best)}, true
}

// modeTheoryZone projects the classic book's replies for the recognized
// mode's canonical prefix into the board frame — the opening's strategic
// cells. nil when no compatible book, no mode, or no coverage. The cells are
// a soft prior (they rank first and stay in the candidate list); the search
// still decides.
func (s *searcher) modeTheoryZone() map[int]bool {
	if s.modeBook == nil || s.modeBook.Rule != s.rule || s.modeBook.Size != s.n {
		return nil
	}
	fr, ok := openingModeOf(s)
	if !ok {
		return nil
	}
	weights := s.modeBook.PositionReplies(fr.id)
	if len(weights) == 0 {
		return nil
	}
	inv := fr.t
	zone := make(map[int]bool, len(weights))
	for mk := range weights {
		cx, cy := mk/s.modeBook.Size, mk%s.modeBook.Size // compileBook stores x-major
		bx, by := book.InverseTransform(cx, cy, s.n, inv)
		zone[by*s.n+bx] = true
	}
	return zone
}
