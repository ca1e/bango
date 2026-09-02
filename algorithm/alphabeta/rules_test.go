package alphabeta

import (
	"testing"
	"time"
)

// renjuSearcher builds a searcher with the renju rule and black owned by
// playerMe unless stated otherwise.
func renjuSearcher(t *testing.T, diagram string) *searcher {
	t.Helper()
	s := diagramToSearcher(t, diagram)
	s.setRule(RuleRenju, playerMe)
	return s
}

// TestDoubleThreeForbidden: a move completing two crossing open threes is a
// 3-3 forbidden point for black.
func TestDoubleThreeForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// stones: horizontal (3,5),(4,5); vertical (5,3),(5,4)
	s.setStone(5*9+3, playerMe)
	s.setStone(5*9+4, playerMe)
	s.setStone(3*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	p := 5*9 + 5
	if !s.isForbidden(p, playerMe) {
		t.Fatal("crossing double three not detected as forbidden")
	}
	// the same shape minus one arm is a single legal three
	s.setStone(3*9+5, 0)
	if s.isForbidden(p, playerMe) {
		t.Fatal("single three reported as forbidden")
	}
}

// TestDoubleFourForbidden: a move forming two fours at once (4-4).
func TestDoubleFourForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// horizontal run (2,5),(3,5),(4,5) + P=(5,5) -> four
	s.setStone(5*9+2, playerMe)
	s.setStone(5*9+3, playerMe)
	s.setStone(5*9+4, playerMe)
	// vertical run (5,2),(5,3),(5,4) + P -> four
	s.setStone(2*9+5, playerMe)
	s.setStone(3*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	p := 5*9 + 5
	if !s.isForbidden(p, playerMe) {
		t.Fatal("crossing double four not detected as forbidden")
	}
	if s.winsMove(p, playerMe) {
		t.Fatal("a four is not yet a five")
	}
	// one arm only: a single four is legal
	s.setStone(2*9+5, 0)
	s.setStone(3*9+5, 0)
	s.setStone(4*9+5, 0)
	if s.isForbidden(p, playerMe) {
		t.Fatal("single four reported as forbidden")
	}
}

// TestSameLineDoubleThreeForbidden: oo_P_oo with open flanks is the classic
// two-threes-on-one-line forbidden shape.
func TestSameLineDoubleThreeForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// row 4: . o o . P . o o .  (P=(4,4), gaps at x=3 and x=5)
	s.setStone(4*9+1, playerMe)
	s.setStone(4*9+2, playerMe)
	s.setStone(4*9+6, playerMe)
	s.setStone(4*9+7, playerMe)
	p := 4*9 + 4
	// sanity: each gap alone completes a live four (one three each)
	if _, threes := s.lineCompletions(p, playerMe, 0); threes != 2 {
		t.Fatalf("expected two threes on the line, got %d", threes)
	}
	if !s.isForbidden(p, playerMe) {
		t.Fatal("same-line double three not detected")
	}
}

// TestOverlineForbidden: extending a five to six is forbidden for black in
// renju (while freestyle would call it a win).
func TestOverlineForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	for x := 2; x <= 6; x++ {
		s.setStone(5*9+x, playerMe)
	}
	p := 5*9 + 7 // extends to a six
	if s.winsMove(p, playerMe) {
		t.Fatal("renju black must not win with an overline")
	}
	if !s.isForbidden(p, playerMe) {
		t.Fatal("overline not detected as forbidden")
	}
	// freestyle wins with the same stone
	free := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	for x := 2; x <= 6; x++ {
		free.setStone(5*9+x, playerMe)
	}
	if !free.winsMove(p, playerMe) {
		t.Fatal("freestyle must accept five-or-more as a win")
	}
}

// TestFiveOverridesForbidden: per RIF, a move completing exactly five is a
// win even when it simultaneously creates a double three.
func TestFiveOverridesForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// horizontal: (2,5),(3,5),(4,5),(6,5) — P=(5,5) completes exactly five
	s.setStone(5*9+2, playerMe)
	s.setStone(5*9+3, playerMe)
	s.setStone(5*9+4, playerMe)
	s.setStone(5*9+6, playerMe)
	// vertical three: (5,3),(5,4)
	s.setStone(3*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	// diagonal three: (3,7),(4,6)
	s.setStone(7*9+3, playerMe)
	s.setStone(6*9+4, playerMe)
	p := 5*9 + 5
	if !s.winsMove(p, playerMe) {
		t.Fatal("the move must be recognised as a five")
	}
	// counterfactual: without the completing stone the same point is 3-3
	s.setStone(5*9+6, 0)
	if !s.isForbidden(p, playerMe) {
		t.Fatal("the double three underneath was not detected")
	}
	s.setStone(5*9+6, playerMe)
	if s.isForbidden(p, playerMe) {
		t.Fatal("five must override the forbidden shape")
	}
}

// TestGenMovesFiltersForbidden: with renju, black's candidate list never
// contains a forbidden point; the same position under freestyle keeps it.
func TestGenMovesFiltersForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s.setStone(5*9+3, playerMe)
	s.setStone(5*9+4, playerMe)
	s.setStone(3*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	p := 5*9 + 5

	for _, m := range s.genMoves(20, playerMe) {
		if m == p {
			t.Fatal("renju genMoves offered a forbidden point to black")
		}
	}
	free := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	free.setStone(5*9+3, playerMe)
	free.setStone(5*9+4, playerMe)
	free.setStone(3*9+5, playerMe)
	free.setStone(4*9+5, playerMe)
	found := false
	for _, m := range free.genMoves(20, playerMe) {
		if m == p {
			found = true
		}
	}
	if !found {
		t.Fatal("freestyle genMoves lost the crossing point")
	}
}

// TestStandardExactlyFiveWins: rule 1 wins only with exactly five.
func TestStandardExactlyFiveWins(t *testing.T) {
	s := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s.setRule(RuleStandard, playerMe)
	for x := 2; x <= 5; x++ {
		s.setStone(5*9+x, playerMe)
	}
	if !s.winsMove(5*9+6, playerMe) {
		t.Fatal("standard rule must accept exactly five")
	}
	// extending to six is not a win and the game continues
	s.setStone(5*9+6, playerMe)
	if s.winsMove(5*9+7, playerMe) {
		t.Fatal("standard rule must reject an overline")
	}
	if s.winsMove(5*9+1, playerMe) {
		t.Fatal("standard rule must reject an overline")
	}
}

// TestCaroEndsRule: rule 8 — an exact five loses its winning value only when
// the opponent holds BOTH ends; an edge counts as open.
func TestCaroEndsRule(t *testing.T) {
	s := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s.setRule(RuleCaro, playerMe)
	// row 5: x o o o o P x  -> both ends blocked by the opponent
	s.setStone(5*9+2, playerOpp)
	for x := 3; x <= 6; x++ {
		s.setStone(5*9+x, playerMe)
	}
	s.setStone(5*9+8, playerOpp)
	p := 5*9 + 7
	if s.winsMove(p, playerMe) {
		t.Fatal("caro five blocked on both ends must not win")
	}
	// open the right end: the five wins again
	s.setStone(5*9+8, 0)
	if !s.winsMove(p, playerMe) {
		t.Fatal("caro five with one open end must win")
	}
	// edge counts as an open end: run ending at the board border
	s2 := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s2.setRule(RuleCaro, playerMe)
	for x := 5; x <= 8; x++ {
		s2.b[5*9+x] = playerMe
	}
	s2.b[5*9+3] = playerOpp // left end blocked; right end is the board edge
	if !s2.winsMove(5*9+4, playerMe) {
		t.Fatal("caro five ending at the edge must win")
	}
}

// TestRenjuWhiteOverlineWins: white has no forbidden moves and wins with
// five or more, overlines included.
func TestRenjuWhiteOverlineWins(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	for x := 2; x <= 6; x++ {
		s.setStone(5*9+x, playerOpp)
	}
	p := 5*9 + 7
	if !s.winsMove(p, playerOpp) {
		t.Fatal("renju white must win with an overline")
	}
	if s.isForbidden(p, playerOpp) {
		t.Fatal("white is never forbidden")
	}
}

// TestRuleNineMatchesCaro: code 9 (caro tournament) uses the same win
// judgement as code 8 in this engine.
func TestRuleNineMatchesCaro(t *testing.T) {
	build := func(rule int) *searcher {
		s := diagramToSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
		s.setRule(rule, playerMe)
		s.setStone(5*9+2, playerOpp)
		for x := 3; x <= 6; x++ {
			s.setStone(5*9+x, playerMe)
		}
		s.setStone(5*9+8, playerOpp)
		return s
	}
	if build(RuleCaro).winsMove(5*9+7, playerMe) != build(RuleCaroStandard).winsMove(5*9+7, playerMe) {
		t.Fatal("rule 8 and 9 must judge identical positions alike")
	}
}

// TestFourThreeIsLegal: one four plus one three crossing at P is the most
// important renju attack shape and must NOT be forbidden. Also guards the
// make/undo symmetry of lineCompletions: the enumeration temporarily places
// stones, so the incremental eval cache and hash must come out untouched.
func TestFourThreeIsLegal(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// horizontal three (2,5),(3,5),(4,5) + P=(5,5) → four
	for x := 2; x <= 4; x++ {
		s.setStone(5*9+x, playerMe)
	}
	// vertical three (5,3),(5,4) + P → live three
	for y := 3; y <= 4; y++ {
		s.setStone(y*9+5, playerMe)
	}
	p := 5*9 + 5
	if s.isForbidden(p, playerMe) {
		t.Fatal("four-three reported as forbidden")
	}
	if s.winsMove(p, playerMe) {
		t.Fatal("four-three is not yet a five")
	}
	if s.evaluate() != s.evaluateFull() || s.hash != recomputeHash(s) {
		t.Fatal("isForbidden left the incremental eval/hash out of sync")
	}
	// counter-case: extend the vertical three into a second four → 4-4
	s.setStone(2*9+5, playerMe)
	if !s.isForbidden(p, playerMe) {
		t.Fatal("four-four not detected after the vertical extension")
	}
	if s.evaluate() != s.evaluateFull() || s.hash != recomputeHash(s) {
		t.Fatal("isForbidden left the incremental eval/hash out of sync")
	}
}

// TestJumpFourCrossForbidden: a jump four (o.ooo, gap mask) counts for 4-4
// like any other four. This exercises the bitmask construction across the
// gap in lineCompletions, which the plain crossing four does not reach.
func TestJumpFourCrossForbidden(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	// horizontal four (2,5),(3,5),(4,5) + P=(5,5)
	for x := 2; x <= 4; x++ {
		s.setStone(5*9+x, playerMe)
	}
	// vertical jump four (5,2),(5,4),(5,6) + P: filling (5,3) completes five
	s.setStone(2*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	s.setStone(6*9+5, playerMe)
	p := 5*9 + 5
	if s.winsMove(p, playerMe) {
		t.Fatal("test setup: P itself must not complete five")
	}
	if !s.isForbidden(p, playerMe) {
		t.Fatal("jump-four crossing not detected as 4-4")
	}
	if s.evaluate() != s.evaluateFull() || s.hash != recomputeHash(s) {
		t.Fatal("isForbidden left the incremental eval/hash out of sync")
	}
}

// TestWhiteDoubleThreeLegal: white has no forbidden moves — the same
// crossing double three that forbids black is a normal move for white.
func TestWhiteDoubleThreeLegal(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s.setStone(5*9+3, playerOpp)
	s.setStone(5*9+4, playerOpp)
	s.setStone(3*9+5, playerOpp)
	s.setStone(4*9+5, playerOpp)
	if s.isForbidden(5*9+5, playerOpp) {
		t.Fatal("white must never be forbidden")
	}
}

// TestGenMovesRenjuWhiteKeepsPoints: forbidden points are filtered for the
// renju-black mover only — white's candidate list keeps the same point.
func TestGenMovesRenjuWhiteKeepsPoints(t *testing.T) {
	s := renjuSearcher(t, `
.........
.........
.........
.........
.........
.........
.........
.........
.........
`)
	s.setStone(5*9+3, playerMe)
	s.setStone(5*9+4, playerMe)
	s.setStone(3*9+5, playerMe)
	s.setStone(4*9+5, playerMe)
	p := 5*9 + 5
	if !s.isForbidden(p, playerMe) {
		t.Fatal("test setup: the point must be forbidden for black")
	}
	found := false
	for _, m := range s.genMoves(20, playerOpp) {
		if m == p {
			found = true
		}
	}
	if !found {
		t.Fatal("white's candidate list dropped a legal point")
	}
}

// TestSelfGameRenju: a full self-play game under renju — black respects
// forbidden points (they are filtered before the search), the game stays
// legal and reaches a winner.
func TestSelfGameRenju(t *testing.T) {
	const n = 15
	const budget = 150 * time.Millisecond
	b := make([]int, n*n)

	place := func(x, y, side int) { b[y*n+x] = side }

	fiveAt := func(side int) bool {
		s := &searcher{n: n, b: b, lineBuf: make([]int, n)}
		for p := 0; p < n*n; p++ {
			if b[p] == side && s.winsMove(p, side) {
				return true
			}
		}
		return false
	}

	for move := 0; move < 225; move++ {
		side := playerMe
		flip := b
		blackSide := playerMe
		if move%2 == 1 {
			side = playerOpp
			flip = swapPlayers(b)
			blackSide = playerOpp // black is still the original first mover
		}
		s := newSearcher(n, flip, 0)
		s.setRule(RuleRenju, blackSide)
		x, y := s.run(8, budget)
		if x < 0 || y < 0 {
			t.Fatalf("move %d: no move for side %d", move, side)
		}
		if b[y*n+x] != 0 {
			t.Fatalf("move %d: side %d played on an occupied cell", move, side)
		}
		// the mover must never sit on its own forbidden point — only the
		// renju-black side has any, and in the searcher's encoding the mover
		// is always playerMe, so the check applies only when blackSide says
		// playerMe is black
		if s.blackSide == playerMe && s.isForbidden(y*n+x, playerMe) {
			t.Fatalf("move %d: forbidden point played at (%d,%d)", move, x, y)
		}
		place(x, y, side)

		if fiveAt(side) {
			t.Logf("renju selfplay finished after %d plies, winner = side %d, last move (%d,%d)",
				move+1, side, x, y)
			return
		}
	}
	t.Fatal("no five within 225 renju moves")
}
