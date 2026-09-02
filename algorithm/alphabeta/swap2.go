package alphabeta

// swap2.go: the SWAP2 opening decision. This is the one protocol decision
// that needs raw search internals (a static evaluation of the stones as
// black, then a full-strength reply move as white), so it lives next to the
// searcher instead of leaking searcher API into the session layer.

// Swap2Reply answers a SWAP2BOARD position: stone colors alternate by
// position (1st/3rd/5th = black, 2nd/4th = white). When the stones evaluated
// from black's perspective score at least a live three, black is clearly
// ahead and the engine takes black (swap = true). Otherwise it stays white
// and returns the move to play now (swap = false, x/y = the next stone).
//
// Bounds are the caller's responsibility (the session layer validates pts).
func Swap2Reply(n, rule int, pts [][2]int, timeoutTurn int, timeLeft int64) (x, y int, swap bool) {
	// evaluate the position from black's perspective
	b := make([]int, n*n)
	for i, pt := range pts {
		p := pt[1]*n + pt[0]
		if i%2 == 0 {
			b[p] = playerMe
		} else {
			b[p] = playerOpp
		}
	}
	s := newSearcher(n, b, 0)
	s.setRule(rule, playerMe)
	if s.evaluate() >= scoreLiveThree {
		return 0, 0, true // SWAP: take black
	}

	// stay white: rebuild with our stones as playerMe and play the next stone
	b2 := make([]int, n*n)
	for i, pt := range pts {
		p := pt[1]*n + pt[0]
		if i%2 == 0 {
			b2[p] = playerOpp
		} else {
			b2[p] = playerMe
		}
	}
	s2 := newSearcher(n, b2, 0)
	s2.setRule(rule, playerOpp) // black belongs to the opponent now
	x, y = s2.run(maxSearchDepth, thinkBudget(timeoutTurn, timeLeft))
	return x, y, false
}
