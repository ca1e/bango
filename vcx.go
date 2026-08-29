package main

// VCF/VCT search (tutorial ch.8 算杀): a proof search over forcing moves only.
//
// Where full minimax must consider every candidate, the kill search narrows
// each node to the threat moves:
//   - MAX (attacker): only our live threes and fours — any single line that
//     ends in five proves the kill;
//   - MIN (defender): both sides' fours and live threes — defence may itself
//     attack (a counter-four), and one successful defence refutes the line.
//
// Because branching is tiny, this reaches depths full search cannot. Per the
// tutorial, VCF (fours only) is tried first, then VCT (fours + live threes).
// The engine runs it after iterative deepening found no forced win; a found
// sequence is returned as the move and folded into the search score.

import "time"

const (
	vcfDepth = 12 // plies for the fours-only kill search
	vctDepth = 10 // pliers for the threes-and-fours kill search
)

// killSearch holds the state of one VCF/VCT probe. It reuses the searcher's
// board and zobrist state so the result stays consistent with the main search.
type killSearch struct {
	s        *searcher
	onlyFour bool // VCF: attacker plays fours only
	attacker int  // the side being proven (playerMe at the root)
	deadline time.Time
	nodes    int
}

// vcxResult describes a proven kill: the first move to play and its ply depth.
type vcxResult struct {
	move int
	ply  int
}

// runKillSearch probes VCF then VCT after the main search. Returns the first
// move of a proven kill, or -1.
func (s *searcher) runKillSearch(budget time.Duration) int {
	if s.stoneCount() == 0 {
		return -1
	}
	deadline := time.Time{}
	if budget > 0 {
		deadline = time.Now().Add(budget / 4) // kills get a quarter of the move budget
	}
	ks := &killSearch{s: s, deadline: deadline, attacker: playerMe}

	// VCF first (tutorial: 优先 VCF), then VCT
	ks.onlyFour = true
	if r := ks.search(playerMe, vcfDepth); r != nil {
		return r.move
	}
	ks.onlyFour = false
	if r := ks.search(playerMe, vctDepth); r != nil {
		return r.move
	}
	return -1
}

// search proves whether the attacker (always playerMe at the root; roles
// alternate by parameter) can force a win within deep plies using only
// forcing moves. Returns the kill sequence's first move, or nil.
func (ks *killSearch) search(attacker, deep int) *vcxResult {
	ks.nodes++
	if deep <= 0 {
		return nil
	}
	if !ks.deadline.IsZero() && ks.nodes%256 == 0 && time.Now().After(ks.deadline) {
		return nil // out of time: unproven, not refuted
	}

	moves := ks.forcingMoves(attacker)
	if len(moves) == 0 {
		return nil
	}
	// a five point ends the proof immediately
	if len(moves) > 0 && ks.s.winsMove(moves[0].p, attacker) {
		return &vcxResult{move: moves[0].p, ply: 1}
	}

	for _, m := range moves {
		ks.s.makeMove(m.p, attacker)
		var r *vcxResult
		if ks.s.winsMove(m.p, attacker) {
			r = &vcxResult{move: m.p, ply: 1}
		} else if !ks.defends(3-attacker, deep-1) {
			// every defence failed: this attack wins
			r = &vcxResult{move: m.p, ply: 2}
		}
		ks.s.undoMove(m.p, attacker)
		if r != nil {
			return r
		}
	}
	return nil
}

// defends reports whether the defender can survive the attacker's threats.
// One working defence is enough (tutorial MIN-layer rule).
func (ks *killSearch) defends(defender, deep int) bool {
	ks.nodes++
	if deep <= 0 {
		return true // out of horizon: assume defence holds (no false kills)
	}
	if !ks.deadline.IsZero() && ks.nodes%256 == 0 && time.Now().After(ks.deadline) {
		return true
	}

	moves := ks.forcingMoves(defender)
	if len(moves) == 0 {
		return false // nothing to do: the attack continues unchecked
	}
	for _, m := range moves {
		ks.s.makeMove(m.p, defender)
		var survived bool
		if ks.s.winsMove(m.p, defender) {
			survived = true // counter-five: attack refuted outright
		} else {
			survived = ks.search(3-defender, deep-1) == nil
		}
		ks.s.undoMove(m.p, defender)
		if survived {
			return true
		}
	}
	return false
}

// threatMove is a forcing candidate with a rough priority key.
type threatMove struct {
	p   int
	key int
}

// forcingMoves lists the threat moves for side: always fours (ours to make,
// theirs to block — a rush four is as urgent as a live four on defence),
// plus live threes for the attacker in VCT mode. Blocks carry a key just
// below an own five: when both a five and a block exist, the win is tried
// first; on defence, covering the pending four comes before counter-attacks.
func (ks *killSearch) forcingMoves(side int) []threatMove {
	s := ks.s
	n := s.n
	var out []threatMove
	for p := 0; p < n*n; p++ {
		if s.b[p] != 0 || s.near1Cnt[p] == 0 {
			continue
		}
		if s.rule == RuleRenju && side == s.blackSide && s.isForbidden(p, side) {
			continue // renju: the kill search never uses forbidden points
		}
		mine := s.pointScore(p, side)
		// five right now is always included (checked by caller)
		if mine >= scoreFive {
			out = append(out, threatMove{p, scoreFive})
			continue
		}
		// the opponent completes five through this cell: covering it is the
		// defence a pending four demands. Without it "no counter-four" was
		// misread as "no defence" — a live four is unstoppable, but a plain
		// rush four is blocked, and that block must be searchable.
		if s.pointScore(p, 3-side) >= scoreFive {
			out = append(out, threatMove{p, scoreFive - 1})
			continue
		}
		// defender only: the attacker's live three makes a live four here.
		// Blocking one end is the standard answer to a fresh three — without
		// it "no counter-threat" was misread as "no defence", and a mere live
		// TWO was proven a kill the moment it grew into a three (the three's
		// four points are not five points yet, so nothing was blockable).
		if side != ks.attacker && !ks.onlyFour && s.pointScore(p, 3-side) >= scoreLiveFour {
			out = append(out, threatMove{p, scoreRushFour - 1})
			continue
		}
		if mine >= scoreRushFour {
			key := mine // live four (1e6) sorts above rush four (1e5)
			out = append(out, threatMove{p, key})
			continue
		}
		if ks.onlyFour && side == playerMe {
			continue // VCF attacker: fours only
		}
		if mine >= scoreLiveThree {
			key := mine
			if side != playerMe {
				key = -mine // defender's threes rank below the fours above
			}
			out = append(out, threatMove{p, key})
		}
	}
	// strongest threat first; five first of all
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].key > out[j-1].key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > nodeMoveLimit {
		out = out[:nodeMoveLimit]
	}
	return out
}
