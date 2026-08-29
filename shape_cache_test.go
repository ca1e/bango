package main

import (
	"math/rand"
	"testing"
)

// pointScoreUncached is the pre-cache implementation of pointScore, kept as
// an independent oracle for the incremental (cell, direction, side) shape
// cache. It must never be wired into the search — its only job is to diverge
// loudly when the dirty-bit invalidation misses a dependency.
func pointScoreUncached(s *searcher, p, side int) int {
	n := s.n
	x, y := p%n, p/n
	total := 0
	for d := 0; d < 4; d++ {
		dx, dy := dirX[d], dirY[d]
		l1 := s.countLine(x, y, -dx, -dy, side)
		lOpen := s.isEmptyAt(x-(l1+1)*dx, y-(l1+1)*dy)
		l2 := 0
		if lOpen {
			l2 = s.countLine(x-(l1+1)*dx, y-(l1+1)*dy, -dx, -dy, side)
		}
		r1 := s.countLine(x, y, dx, dy, side)
		rOpen := s.isEmptyAt(x+(r1+1)*dx, y+(r1+1)*dy)
		r2 := 0
		if rOpen {
			r2 = s.countLine(x+(r1+1)*dx, y+(r1+1)*dy, dx, dy, side)
		}

		v := shapeVal(1+l1+r1, lOpen, rOpen)
		// jump shapes across a gap, discounted because the gap needs filling
		if l2 > 0 {
			if g := shapeVal(l1+l2+2, true, true) / 2; g > v {
				v = g
			}
		}
		if r2 > 0 {
			if g := shapeVal(r1+r2+2, true, true) / 2; g > v {
				v = g
			}
		}
		total += v
	}
	return total
}

// TestShapeCacheExact is the correctness gate for the incremental candidate
// state: after every placement/removal on random playouts, every cached
// (cell, direction, side) shape value must equal the from-scratch walk, and
// the stone/neighborhood counters must match a full rescan.
func TestShapeCacheExact(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
	}{
		{"board9", 9},
		{"board15", 15},
	} {
		n := tc.n
		s := newSearcher(n, make([]int, n*n), 0)
		rng := rand.New(rand.NewSource(424242))
		type placed struct {
			p, side int
		}
		var stack []placed
		for i := 0; i < 1500; i++ {
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
				s.makeMove(p, side)
				stack = append(stack, placed{p, side})
			}

			// counters
			live := 0
			for _, v := range s.b {
				if v != 0 {
					live++
				}
			}
			if s.stones != live {
				t.Fatalf("%s step %d: stones counter %d != board %d", tc.name, i, s.stones, live)
			}
			for p := range s.b {
				near2, near1 := 0, 0
				x, y := p%n, p/n
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nx, ny := x+dx, y+dy
						if nx < 0 || ny < 0 || nx >= n || ny >= n || s.b[ny*n+nx] == 0 {
							continue
						}
						near2++
						if dx >= -1 && dx <= 1 && dy >= -1 && dy <= 1 {
							near1++
						}
					}
				}
				if s.nearCnt[p] != int8(near2) || s.near1Cnt[p] != int8(near1) {
					t.Fatalf("%s step %d cell (%d,%d): near counts (%d,%d) != (%d,%d)",
						tc.name, i, x, y, s.nearCnt[p], s.near1Cnt[p], near2, near1)
				}
			}

			// cached shape values vs the independent oracle
			for p := 0; p < n*n; p++ {
				for side := playerMe; side <= playerOpp; side++ {
					if got, want := s.pointScore(p, side), pointScoreUncached(s, p, side); got != want {
						t.Fatalf("%s step %d cell (%d,%d) side %d: cached pointScore %d != oracle %d",
							tc.name, i, p%n, p/n, side, got, want)
					}
				}
			}
		}
	}
}
