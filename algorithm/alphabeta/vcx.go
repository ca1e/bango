package alphabeta

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
//
// Time: the probe shares the move's one absolute deadline (runSingle sets it
// from the per-move budget). Once it fires, ks.timedOut latches and every
// caller unwinds immediately; a timed-out proof is *inconclusive* — runKill
// Search reports no kill and runOppKillProbe never claims a defence from one.

import "time"

const (
	vcfDepth = 12 // plies for the fours-only kill search
	vctDepth = 10 // pliers for the threes-and-fours kill search

	// probeBlockLimit caps how many defence candidates runOppKillProbe may
	// verify; the deadline bounds the total either way.
	probeBlockLimit = 6

	// unlimitedMoves: the defence layer's forcing-move list is never
	// truncated — a working defence cut off by a cap would fake a kill.
	// Only the attack layer accepts that incompleteness.
	unlimitedMoves = 1 << 20
)

// killSearch holds the state of one VCF/VCT probe. It reuses the searcher's
// board and zobrist state so the result stays consistent with the main search.
type killSearch struct {
	s        *searcher
	onlyFour bool // VCF: attacker plays fours only
	attacker int  // the side being proven (playerMe at the root)
	deadline time.Time
	nodes    int
	timedOut bool // latched once the deadline fires; unwinds the whole proof
}

// vcxResult describes a proven kill: the first move to play and its ply depth.
type vcxResult struct {
	move int
	ply  int
}

// runKillSearch probes VCF then VCT after the main search. Returns the first
// move of a proven kill, or -1. A proof cut short by the deadline is
// inconclusive and reports no kill.
func (s *searcher) runKillSearch(deadline time.Time) int {
	if s.stoneCount() == 0 {
		return -1
	}
	ks := &killSearch{s: s, deadline: deadline, attacker: playerMe}

	// VCF first (tutorial: 优先 VCF), then VCT — both under the one absolute
	// deadline, so a VCF timeout leaves nothing for VCT either
	ks.onlyFour = true
	if r := ks.search(playerMe, vcfDepth); r != nil {
		return r.move
	}
	if ks.timedOut {
		return -1
	}
	ks.onlyFour = false
	if r := ks.search(playerMe, vctDepth); r != nil {
		return r.move
	}
	return -1
}

// runOppKillProbe is the defence layer behind the kill search (the reference
// implementation's candidateMinmax net): it proves whether the OPPONENT —
// given the move the engine is about to spend elsewhere — could force a win
// with forcing moves alone. A proven opponent kill outranks the main search's
// quiet choice — but only once an actual defence is found: the probe tries
// the mandatory answering points (the kill's own first move plus the defender's
// blocks of fives and live-three growth points) and returns the first cell
// whose occupation verifiably dissolves the proof. A fork no single cell can
// answer — and equally a proof or a verification cut short by the deadline —
// returns -1 and the main search's move stands.
func (s *searcher) runOppKillProbe(deadline time.Time) int {
	if s.stoneCount() == 0 {
		return -1
	}
	ks := &killSearch{s: s, deadline: deadline, attacker: playerOpp}
	r := ks.search(playerOpp, vctDepth)
	if ks.timedOut || r == nil {
		return -1 // no proven kill, or inconclusive: keep the quiet move
	}
	cands := append([]int(nil), r.move)
	for _, m := range ks.forcingMoves(playerMe, unlimitedMoves) {
		if m.key == scoreFive-1 || m.key == scoreRushFour-1 {
			cands = append(cands, m.p)
		}
	}
	seen := make(map[int]bool, len(cands))
	tried := 0
	for _, c := range cands {
		if seen[c] {
			continue
		}
		seen[c] = true
		if tried >= probeBlockLimit {
			break
		}
		if s.rule == RuleRenju && playerMe == s.blackSide && s.isForbidden(c, playerMe) {
			continue // the block point is illegal for the engine: no defence here
		}
		tried++
		s.makeMove(c, playerMe)
		r2 := ks.search(playerOpp, vctDepth)
		s.undoMove(c, playerMe)
		if ks.timedOut {
			return -1 // inconclusive verification is never a verified defence
		}
		if r2 == nil {
			return c
		}
	}
	return -1
}

// passDeadline reports whether the proof may continue. Checked on every node:
// node costs here are microseconds, so the clock read is noise, and latching
// timedOut lets every caller unwind without waiting for the next check.
func (ks *killSearch) passDeadline() bool {
	if ks.timedOut {
		return true
	}
	if !ks.deadline.IsZero() && time.Now().After(ks.deadline) {
		ks.timedOut = true
		return true
	}
	return false
}

// search proves whether the attacker (always playerMe at the root; roles
// alternate by parameter) can force a win within deep plies using only
// forcing moves. Returns the kill sequence's first move, or nil — nil means
// "unproven or timed out"; callers consulting ks.timedOut tell the two apart.
func (ks *killSearch) search(attacker, deep int) *vcxResult {
	ks.nodes++
	if deep <= 0 {
		return nil
	}
	if ks.passDeadline() {
		return nil
	}

	moves := ks.forcingMoves(attacker, nodeMoveLimit)
	if len(moves) == 0 {
		return nil
	}
	// a five point ends the proof immediately
	if ks.s.winsMove(moves[0].p, attacker) {
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
		if ks.timedOut {
			return nil // a proof finished before the timeout is returned above
		}
		if r != nil {
			return r
		}
	}
	return nil
}

// defends reports whether the defender can survive the attacker's threats.
// One working defence is enough (tutorial MIN-layer rule). A timeout reads as
// "defended" — conservative for the proof itself — and latches ks.timedOut so
// no caller mistakes the inconclusive result for a real refutation.
func (ks *killSearch) defends(defender, deep int) bool {
	ks.nodes++
	if deep <= 0 {
		return true // out of horizon: assume defence holds (no false kills)
	}
	if ks.passDeadline() {
		return true
	}

	moves := ks.forcingMoves(defender, unlimitedMoves)
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
		if ks.timedOut {
			return true
		}
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
// The attack layer's list is truncated to limit (completeness there only
// costs missed kills); pass unlimitedMoves on defence — truncating the
// defenders' answers would fake a kill.
func (ks *killSearch) forcingMoves(side, limit int) []threatMove {
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
		if ks.onlyFour && side == ks.attacker {
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
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
