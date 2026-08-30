package main

// Rule-dependent win judgement and renju forbidden-move detection.
//
// The manager announces the rule via INFO rule (bitmask per the official
// protocol; Gomocup uses these codes):
//
//	0 freestyle — five or more in a row wins, no forbidden moves
//	1 standard  — exactly five wins, overlines don't
//	4 renju     — black has forbidden moves (3-3, 4-4, overline);
//	              white has none and wins with overlines too
//	8 caro      — exactly five wins unless BOTH ends of the five are
//	              blocked by opponent stones (edge counts as open)
//	9 caro (tournament) — same win judgement as 8 here
//
// Forbidden points are filtered in move generation (never in the evaluation
// function), so the search tree never enters an illegal branch and pruning
// stays undisturbed. Per RIF, five always overrides a forbidden shape: a
// move completing exactly five is never forbidden.

import "sort"

const (
	RuleFreestyle    = 0
	RuleStandard     = 1
	RuleRenju        = 4
	RuleCaro         = 8
	RuleCaroStandard = 9
)

// setRule configures rule-dependent search behaviour. blackSide is the
// internal player (playerMe/playerOpp) that owns black — forbidden points
// apply to that side only. The zero values (freestyle, playerMe) match the
// historic engine behaviour, so existing callers need no setup.
func (s *searcher) setRule(rule, blackSide int) {
	s.rule = rule
	s.blackSide = blackSide
}

// isOpp reports whether (x,y) holds a stone of the side opposite `side`.
// Out-of-board cells count as "not opponent": a Caro five may end at the edge.
func (s *searcher) isOpp(x, y, side int) bool {
	if x < 0 || y < 0 || x >= s.n || y >= s.n {
		return false
	}
	return s.b[y*s.n+x] == 3-side
}

// winsMove reports whether placing side's stone on p wins under the active
// rule. p itself need not be occupied: countLine starts from the neighbours.
func (s *searcher) winsMove(p, side int) bool {
	n := s.n
	x, y := p%n, p/n
	for d := 0; d < 4; d++ {
		dx, dy := dirX[d], dirY[d]
		a := s.countLine(x, y, -dx, -dy, side)
		b := s.countLine(x, y, dx, dy, side)
		c := 1 + a + b
		switch s.rule {
		case RuleRenju:
			if side == s.blackSide {
				if c == 5 {
					return true // black: overline is forbidden, not a win
				}
				continue
			}
			if c >= 5 {
				return true // white: overline wins too
			}
		case RuleStandard, RuleCaro, RuleCaroStandard:
			if c == 5 {
				if s.rule == RuleStandard {
					return true
				}
				// Caro: the five dies only when the opponent holds both ends
				if !(s.isOpp(x-(a+1)*dx, y-(a+1)*dy, side) && s.isOpp(x+(b+1)*dx, y+(b+1)*dy, side)) {
					return true
				}
			}
		default: // RuleFreestyle
			if c >= 5 {
				return true
			}
		}
	}
	return false
}

// isForbidden reports whether renju forbids black to place at p. Non-black
// sides and non-renju rules are never forbidden.
//
// Algorithm: with p hypothetically black, analyse each of the four lines
// through p. A direction contributes a FOUR per distinct empty cell whose
// fill completes exactly five through p, and a THREE per distinct empty cell
// whose fill completes a live four (exact run of four with both ends open).
// Shapes are deduped by their stone set, so the two ends of one live four or
// one open three count once, while the classic same-line double shape
// (oo_P_oo) correctly counts twice. Sum over the four directions:
// two or more fours (4-4) or two or more threes (3-3) is forbidden, as is an
// overline. Known simplification: threes whose own completion would be
// forbidden are still counted (the RIF recursion is not implemented), so a
// rare legal point may be avoided — never the reverse.
func (s *searcher) isForbidden(p, black int) bool {
	if s.rule != RuleRenju || black != s.blackSide {
		return false
	}
	if s.winsMove(p, black) {
		return false // five overrides any forbidden shape
	}
	for d := 0; d < 4; d++ {
		if s.runThrough(p, black, d) >= 6 {
			return true // overline
		}
	}
	// cheap pre-filter: any double shape needs at least four existing stones
	// in line with p (two per three, three per four)
	if s.inlineStones(p, black) < 4 {
		return false
	}
	fours, threes := 0, 0
	for d := 0; d < 4; d++ {
		f, t := s.lineCompletions(p, black, d)
		fours += f
		threes += t
		if fours >= 2 || threes >= 2 {
			return true
		}
	}
	return false
}

// runThrough returns the contiguous run length of side through p along
// direction d, counting p itself.
func (s *searcher) runThrough(p, side, d int) int {
	n := s.n
	x, y := p%n, p/n
	return 1 + s.countLine(x, y, -dirX[d], -dirY[d], side) + s.countLine(x, y, dirX[d], dirY[d], side)
}

// inlineStones counts side's stones lying on one of the four lines through
// p within four steps (p itself excluded).
func (s *searcher) inlineStones(p, side int) int {
	n := s.n
	x, y := p%n, p/n
	c := 0
	for d := 0; d < 4; d++ {
		for _, sign := range [2]int{-1, 1} {
			for k := 1; k <= 4; k++ {
				nx, ny := x+sign*dirX[d]*k, y+sign*dirY[d]*k
				if nx < 0 || ny < 0 || nx >= n || ny >= n {
					break
				}
				if s.b[ny*n+nx] == side {
					c++
				}
			}
		}
	}
	return c
}

// bookCandidates resolves the opening book for the current position.
// Returns nil when no book is set, the book's rule/size don't match the
// game, the position isn't covered, or every candidate was dropped
// (occupied / renju-forbidden for the mover). Guard rails mirror the
// gobang original: the book only speaks in quiet openings.
func (s *searcher) bookCandidates() []bookCandidate {
	cb := s.book
	if cb == nil || cb.rule != s.rule || cb.size != s.n {
		return nil
	}
	stones := make([]bookStone, 0, 16)
	blackCount, whiteCount := 0, 0
	for p, v := range s.b {
		if v == 0 {
			continue
		}
		if v != playerMe && v != playerOpp {
			return nil // marked cells (field 3): position is not a plain opening
		}
		if v == s.blackSide {
			blackCount++
			stones = append(stones, bookStone{p % s.n, p / s.n, 1})
		} else {
			whiteCount++
			stones = append(stones, bookStone{p % s.n, p / s.n, -1})
		}
	}
	if len(stones) == 0 {
		return nil // empty board: the engine opens by search (tutorial behavior)
	}

	cands := cb.movesFor(stones)
	if len(cands) == 0 && len(stones) == 1 {
		// displacement generalization: a line's first→second offset only
		// transfers meaningfully when it lands in the contact zone — remote
		// displacements (some source lines reply 2-3 steps away) would
		// produce geometrically arbitrary first replies and bypass the
		// opening prior's classic-opening filter. Same criterion, applied
		// before adoption.
		for _, c := range cb.translatedFirstMoves(stones[0].x, stones[0].y) {
			if s.classicOpeningPoint(c.move) {
				cands = append(cands, c)
			}
		}
		// displacement candidates carry a different position's geometry —
		// they bias ordering but must not be adopted outright (arena: the
		// weight-ordered pick measured 26:34 against the search choice)
		s.bookViaTranslation = len(cands) > 0
	}
	if len(cands) == 0 {
		return nil
	}

	// side to move: black on equal counts, white otherwise
	moverIsBlack := blackCount == whiteCount
	out := make([]bookCandidate, 0, len(cands))
	for _, c := range cands {
		if s.b[c.move] != 0 {
			continue
		}
		if moverIsBlack && s.isForbidden(c.move, s.blackSide) {
			continue // renju: a forbidden point never reaches the search
		}
		out = append(out, c)
	}
	return out
}

// openThreatAt reports whether placing side's stone on p would create a
// forcing shape: a four or five (pointScore prices fours exactly) or a live
// three (exact shape test via threeShapeAt). The summed pointScore cannot
// decide the three: dirShape prices jump shapes at half value, so two
// unrelated discounted shapes in different directions summed past
// scoreLiveThree and misflagged quiet positions as tactical.
func (s *searcher) openThreatAt(p, side int) bool {
	if s.pointScore(p, side) >= scoreRushFour {
		return true // four/five makers are priced exactly
	}
	for d := 0; d < 4; d++ {
		if s.threeShapeAt(p, side, d) {
			return true
		}
	}
	return false
}

// hasOpenThreat reports whether either side owns a forcing shape anywhere on
// the board — the opening book's "quiet openings only" guard: quiet
// positions adopt the book's move outright, tactical ones only let the book
// bias root ordering. Replaces the summed-pointScore hasThreatAtLeast, whose
// jump-shape discounts let artifact sums (and every contact opening from the
// second move on) block direct adoption.
func (s *searcher) hasOpenThreat() bool {
	n := s.n
	for p := 0; p < n*n; p++ {
		if s.b[p] != 0 || s.nearCnt[p] == 0 {
			continue
		}
		if s.openThreatAt(p, playerMe) || s.openThreatAt(p, playerOpp) {
			return true
		}
	}
	return false
}

// applyBookOrdering reorders the root candidates so the book's preferred
// moves are searched first (rank 0 = not in book). The search itself still
// decides — the book only steers the ordering.
func (s *searcher) applyBookOrdering(cands []bookCandidate, moves []int) {
	rank := make([]int, s.n*s.n)
	for i, c := range cands {
		rank[c.move] = len(cands) - i
	}
	sort.SliceStable(moves, func(i, j int) bool { return rank[moves[i]] > rank[moves[j]] })
}

// lineCompletions enumerates the empty cells within four steps of p along
// direction d and classifies what filling each produces through p: an exact
// five (a four) or an exact live four (a three). Shapes are deduped by the
// set of pre-existing stones they complete, encoded as a bitmask over the
// offsets -4..+4 relative to p.
func (s *searcher) lineCompletions(p, black, d int) (fours, threes int) {
	n := s.n
	px, py := p%n, p/n
	dx, dy := dirX[d], dirY[d]

	var fourCores, threeCores []uint16
	add := func(list []uint16, mask uint16) []uint16 {
		for _, m := range list {
			if m == mask {
				return list
			}
		}
		return append(list, mask)
	}
	emptyAt := func(j int) bool {
		x2, y2 := px+j*dx, py+j*dy
		return x2 >= 0 && y2 >= 0 && x2 < n && y2 < n && s.b[y2*n+x2] == 0
	}

	for k := -4; k <= 4; k++ {
		if k == 0 {
			continue
		}
		ex, ey := px+k*dx, py+k*dy
		if ex < 0 || ey < 0 || ex >= n || ey >= n {
			continue
		}
		ep := ey*n + ex
		if s.b[ep] != 0 {
			continue
		}

		s.makeMove(ep, black)
		a := s.countLine(px, py, -dx, -dy, black)
		b := s.countLine(px, py, dx, dy, black)
		switch c := 1 + a + b; c {
		case 5: // filling e completes exactly five: a four
			mask := uint16(0)
			for j := -a; j <= b; j++ {
				if j != k {
					mask |= 1 << uint(j+4)
				}
			}
			fourCores = add(fourCores, mask)
		case 4: // filling e completes an exact run of four: a three iff live
			// e must actually join the run (every cell between P and e filled,
			// i.e. k within the run's span). When P alone already completes a
			// live four, a bystander e elsewhere on the line leaves c at 4
			// without being part of it — a phantom three that turned genuine
			// four-three points into a false 3-3.
			if k >= -a && k <= b && emptyAt(-a-1) && emptyAt(b+1) {
				mask := uint16(0)
				for j := -a; j <= b; j++ {
					if j != k {
						mask |= 1 << uint(j+4)
					}
				}
				threeCores = add(threeCores, mask)
			}
		}
		s.undoMove(ep, black)
	}
	return len(fourCores), len(threeCores)
}
