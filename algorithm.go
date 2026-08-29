package main

import (
	"os"
	"sort"
	"strconv"
	"time"
)

// Gomoku AI: pattern evaluation + iterative-deepening negamax with alpha-beta
// pruning (guidebook ch.9 form: single role, side-to-move perspective),
// PVS null-window scouting, a forcing-only quiescence at the horizon
// and move ordering.
//
// The engine always plays player 1 ("me"); the opponent is player 2.
// The search runs on a private copy of the board, so the engine mutex is only
// held while snapshotting the position.

const (
	playerMe  = 1
	playerOpp = 2
)

// Pattern scores (棋型评分表). The exact values only need to preserve the
// ordering five > live four > rush four > live three > sleep three >
// live two > sleep two; magnitudes are spread so that mixed threats sum up
// sensibly in the evaluation.
const (
	scoreFive       = 10000000 // 五连
	scoreLiveFour   = 1000000  // 活四
	scoreRushFour   = 100000   // 冲四
	scoreLiveThree  = 30000    // 活三
	scoreJumpThree  = 15000    // 跳活三 (o.oo / oo.o with open ends)
	scoreSleepThree = 5000     // 眠三
	scoreLiveTwo    = 2000     // 活二
	scoreSleepTwo   = 500      // 眠二
	scoreOne        = 50       // 单子（两端开阔）

	// scorePosUnit: center-pyramid bonus per ring. A stone at Chebyshev ring r
	// from the center earns (n/2 - r) × unit, at most 7×unit = 70 on 15×15 —
	// below a sleep two (500), so it only breaks ties between shape-equal
	// moves and never competes with tactical patterns.
	scorePosUnit = 10

	winScore = scoreFive
)

const (
	maxSearchDepth = 14 // hard cap on search depth (even); real depth is time-bound
	rootMoveLimit  = 20 // candidate moves examined at the root
	nodeMoveLimit  = 12 // candidate moves examined at interior nodes
	neighborRadius = 2  // candidate cells must touch a stone within Chebyshev distance 2
)

var dirX = [4]int{1, 0, 1, 1}
var dirY = [4]int{0, 1, 1, -1}

// searcher holds the per-think search state over a flat board.
type searcher struct {
	n                 int
	b                 []int  // flat board, row-major: b[y*n+x]
	lineBuf           []int  // scratch buffer for line extraction
	hash              uint64 // incremental Zobrist hash of the stones
	zo                *zobrist
	tt                *transTable
	ttEnabled         bool
	aspirationEnabled bool                          // false forces full-window root searches (test-only)
	widenFactor       int                           // adaptive aspiration width: grows on score swings
	lmrEnabled        bool                          // late move reductions on the quiet tail (value-changing)
	abEnabled         bool                          // false disables alpha-beta pruning (test-only: full-window negamax)
	pvsEnabled        bool                          // false disables PVS scouting (test-only: plain alpha-beta)
	killersEnabled    bool                          // false disables killer-move ordering (test-only)
	qThreeEnabled     bool                          // quiescence also resolves live-three chains (value-changing; arena A/B gated)
	killers           [maxSearchDepth + 2][2]uint16 // per-ply refutation moves
	historyEnabled    bool                          // false disables history-heuristic ordering (test-only)
	history           [2][]int32                    // per-side cutoff statistics, indexed by move
	lineScore         [2][]int32                    // cached score per line, [me, opp] families
	lineBuf2          []int                         // scratch buffer for line gathering
	evalTotal         int                           // running sum of (me - opp) over all lines
	posEnabled        bool                          // center-pyramid bonus (value-changing; arena A/B gated)
	posTotal          int                           // running sum of (me - opp) pyramid bonuses
	stones            int                           // incremental stone count
	nearCnt           []int8                        // per cell: stones within Chebyshev 2 (candidate filter)
	near1Cnt          []int8                        // per cell: stones within Chebyshev 1 (quiescence filter)
	dirVal            []int32                       // cached per-direction shape values: (p*4+d)*2+(side-1)
	dirDirty          []uint8                       // per cell: stale bits, bit = d*2+(side-1)
	rule              int                           // INFO rule code (0 = freestyle); see rules.go
	blackSide         int                           // internal player owning black (renju forbidden side)
	book              *compiledBook
	bookHits          int
	bookAdopted       int
	deadline          time.Time
	softDeadline      time.Time // past this, do not start another depth iteration
	nodeLimit         int64     // INFO max_node: abort the search past this many nodes (0 = unlimited)
	smpWorkers        int       // lazy SMP worker count (1 = single-threaded, the default)
	smpRotate         int       // worker index: rotates root move order so SMP trees diverge
	rootDepth         int
	lastDepth         int // deepest fully completed iteration
	nodes             int
	abCuts            int
	aborted           bool
}

// newSearcher prepares the per-think search state: Zobrist codes for the
// board size and the initial hash of the given position. The transposition
// table is borrowed (shared across thinks of a game via the Engine) when tt
// is non-nil; otherwise a private table sized within maxMemory is created —
// the per-think lifecycle tests and direct callers rely on.
func newSearcher(n int, b []int, maxMemory int64) *searcher {
	return newSearcherWithTT(n, b, maxMemory, nil)
}

func newSearcherWithTT(n int, b []int, maxMemory int64, tt *transTable) *searcher {
	s := &searcher{
		n:                 n,
		b:                 b,
		lineBuf:           make([]int, n),
		zo:                newZobrist(n),
		ttEnabled:         true,
		aspirationEnabled: true,
		widenFactor:       1,
		lmrEnabled:        true,
		abEnabled:         true,
		pvsEnabled:        true,
		killersEnabled:    true,
		// Arena-gated defaults (50 games, 400ms/move each):
		//  - qThree ON won 28:22 (seed 11) → default on; BANGO_QTHREE=0 off
		//  - posBonus OFF won 30:20 vs on (seed 7) → default off; BANGO_POS=1 on
		qThreeEnabled: os.Getenv("BANGO_QTHREE") != "0",
		posEnabled:    os.Getenv("BANGO_POS") == "1",
		blackSide:     playerMe, // books and forbidden checks key off this
	}
	s.history = [2][]int32{make([]int32, n*n), make([]int32, n*n)}
	s.lineBuf2 = make([]int, n)
	s.lineScore = [2][]int32{make([]int32, lineCount(n)), make([]int32, lineCount(n))}
	for id := 0; id < lineCount(n); id++ {
		s.rescoreLine(id) // starts from an empty cache: evalTotal accumulates
	}
	// incremental candidate state: counts and cached shape values start from
	// the given stones; every direction is marked dirty so first reads
	// recompute from the actual board
	s.nearCnt = make([]int8, n*n)
	s.near1Cnt = make([]int8, n*n)
	s.dirVal = make([]int32, n*n*8)
	s.dirDirty = make([]uint8, n*n) // one stale bit per (direction, side)
	for p, v := range b {
		if v == 0 {
			continue
		}
		s.stones++
		x, y := p%n, p/n
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n {
					continue
				}
				s.nearCnt[ny*n+nx]++
			}
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n {
					continue
				}
				s.near1Cnt[ny*n+nx]++
			}
		}
	}
	for i := range s.dirDirty {
		s.dirDirty[i] = 0xFF
	}
	if tt == nil {
		tt = newTransTable(ttBitsFor(maxMemory))
	}
	s.tt = tt
	for p, v := range b {
		if v != 0 {
			s.hash ^= s.zo.code(p, v)
			if s.posEnabled {
				if v == playerMe {
					s.posTotal += posBonus(p, n)
				} else {
					s.posTotal -= posBonus(p, n)
				}
			}
		}
	}
	return s
}

// makeMove / undoMove place and remove a stone while keeping the incremental
// Zobrist hash and the line-score cache in sync. undoMove's side parameter is
// kept for call-site symmetry; removal only needs the cell.
func (s *searcher) makeMove(m, side int) { s.setStone(m, side) }

func (s *searcher) undoMove(m, side int) { s.setStone(m, 0) }

// setStone places or clears a stone (side 0 removes) — the single primitive
// behind makeMove/undoMove and direct test setup. Every stone change rescans
// exactly the four lines through the cell (eval cache) and maintains the
// incremental candidate state: stone count, the two neighbourhood refcounts,
// and dirty marks on every cell whose cached direction shape values the stone
// can influence (the per-direction walk reaches at most 4 run stones + 1 gap
// + 4 jump stones = 9 cells away along each line).
func (s *searcher) setStone(p, side int) {
	old := s.b[p]
	if old != 0 {
		s.hash ^= s.zo.code(p, old)
	}
	if side != 0 {
		s.hash ^= s.zo.code(p, side)
	}
	s.b[p] = side
	grew := old == 0 && side != 0
	shrank := old != 0 && side == 0
	if s.posEnabled {
		switch {
		case grew && side == playerMe:
			s.posTotal += posBonus(p, s.n)
		case grew && side == playerOpp:
			s.posTotal -= posBonus(p, s.n)
		case shrank && old == playerMe:
			s.posTotal -= posBonus(p, s.n)
		case shrank && old == playerOpp:
			s.posTotal += posBonus(p, s.n)
		}
	}
	if grew {
		s.stones++
	} else if shrank {
		s.stones--
	}
	n := s.n
	x, y := p%n, p/n
	if grew || shrank {
		delta := int8(1)
		if shrank {
			delta = -1
		}
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n || (dx == 0 && dy == 0) {
					continue
				}
				s.nearCnt[ny*n+nx] += delta
			}
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n || (dx == 0 && dy == 0) {
					continue
				}
				s.near1Cnt[ny*n+nx] += delta
			}
		}
	}
	// direction shape values are colour-sensitive, so replacements dirty too
	for d := 0; d < 4; d++ {
		dx, dy := dirX[d], dirY[d]
		for k := -9; k <= 9; k++ {
			if k == 0 {
				continue
			}
			nx, ny := x+k*dx, y+k*dy
			if nx < 0 || ny < 0 || nx >= n || ny >= n {
				continue
			}
			s.dirDirty[ny*n+nx] |= 0x03 << uint(d*2) // both sides' values go stale
		}
	}
	s.rescoreLine(y)                 // row
	s.rescoreLine(s.n + x)           // column
	s.rescoreLine(3*s.n - 1 + x - y) // diagonal (1,1)
	s.rescoreLine(4*s.n - 1 + x + y) // diagonal (1,-1)
}

// nodeKey identifies a search node: stone layout plus side to move — the
// same layout with the other player to move is a different node. Cached
// scores are side-to-move values (negamax convention), so the key must
// split them.
func (s *searcher) nodeKey(side int) uint64 {
	if side == playerMe {
		return s.hash
	}
	return s.hash ^ s.zo.side
}

// aiMove computes the engine's next move. It snapshots the shared board under
// the mutex, then searches without holding it.
func (e *Engine) aiMove() (int, int) {
	e.mu.Lock()
	n := e.size
	b := make([]int, n*n)
	for y := range e.board {
		copy(b[y*n:(y+1)*n], e.board[y])
	}
	timeoutTurn := e.info.TimeoutTurn
	timeLeft := e.info.TimeLeft
	rule := e.info.Rule
	ownBlack := e.ownIsBlack
	tt := e.tt // borrowed for this think only; the Engine owns the lifecycle
	e.mu.Unlock()

	s := newSearcherWithTT(n, b, e.info.MaxMemory, tt)
	if rule != 0 {
		black := playerMe
		if !ownBlack {
			black = playerOpp
		}
		s.setRule(rule, black)
	}
	if book := e.loadBook(); book != nil && book.rule == rule {
		s.book = book
	}
	maxDepth := maxSearchDepth
	if e.info.MaxDepth > 0 && e.info.MaxDepth < maxDepth {
		maxDepth = e.info.MaxDepth
	}
	if e.info.MaxNode > 0 {
		s.nodeLimit = e.info.MaxNode
	}
	if w := os.Getenv("BANGO_SMP"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 1 {
			s.smpWorkers = n
		}
	}
	x, y := s.run(maxDepth, thinkBudget(timeoutTurn, timeLeft))
	if x < 0 {
		// no candidate produced (full board): fall back to the first empty cell
		for p := 0; p < n*n; p++ {
			if b[p] == 0 {
				return p % n, p / n
			}
		}
		return n / 2, n / 2
	}
	return x, y
}

// thinkBudget converts manager INFO values into a per-move time budget.
// Per the Piskvork protocol all times are milliseconds. timeout_turn == 0
// means "as fast as possible", which we map to a small default so the AI
// still plays reasonably.
func thinkBudget(timeoutTurn int, timeLeft int64) time.Duration {
	ms := int64(1000)
	if timeoutTurn > 0 {
		ms = int64(timeoutTurn) * 9 / 10
	}
	if timeLeft > 0 && timeLeft/4 < ms {
		// keep a reserve of the match clock for future moves
		ms = timeLeft / 4
	}
	if ms < 100 {
		ms = 100
	}
	return time.Duration(ms) * time.Millisecond
}

// run performs the full move decision: cheap tactical short-cuts first, then
// iterative deepening on even depths so leaves are always evaluated after the
// opponent has answered. Returns (-1, -1) if the board has no playable cell.
func (s *searcher) run(maxDepth int, budget time.Duration) (int, int) {
	if s.smpWorkers > 1 && budget > 0 {
		return s.runSMP(maxDepth, budget)
	}
	return s.runSingle(maxDepth, budget)
}

// runSMP fans the iterative deepening out over smpWorkers searchers that
// share one transposition table (lazy SMP, the standard nondeterministic
// scheme): each worker runs the full ladder with its own root move rotation,
// so their trees differ and they fill the table complementarily. The deepest
// fully completed iteration wins; ties go to worker 0. The result is
// intentionally nondeterministic — arena gates, not move-equality tests,
// decide its default.
func (s *searcher) runSMP(maxDepth int, budget time.Duration) (int, int) {
	workers := s.smpWorkers
	type result struct {
		x, y, depth int
	}
	resCh := make([]chan result, workers)
	ready := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		resCh[w] = make(chan result, 1)
		go func(w int) {
			// each worker gets a private searcher over a private board copy,
			// sharing only the transposition table
			ws := newSearcherWithTT(s.n, append([]int(nil), s.b...), 0, s.tt)
			ws.smpWorkers = 1 // the worker itself runs single-threaded
			ws.smpRotate = w  // distinct root rotation → distinct tree
			ws.rule, ws.blackSide = s.rule, s.blackSide
			ws.qThreeEnabled, ws.posEnabled = s.qThreeEnabled, s.posEnabled
			ws.book = s.book
			x, y := ws.runSingle(maxDepth, budget)
			resCh[w] <- result{x, y, ws.lastDepth}
			ready <- struct{}{}
		}(w)
	}
	best := result{-1, -1, -1}
	for w := 0; w < workers; w++ {
		<-ready
	}
	// all workers reported; pick deepest (worker 0 breaks ties — its rotation
	// is the plain single-threaded ordering)
	for w := 0; w < workers; w++ {
		r := <-resCh[w]
		if r.depth > best.depth {
			best = r
		}
	}
	return best.x, best.y
}

// runSingle is the original single-threaded move decision; see run.
func (s *searcher) runSingle(maxDepth int, budget time.Duration) (int, int) {
	n := s.n
	if s.stoneCount() == 0 {
		return n / 2, n / 2 // empty board: play the center (天元)
	}

	moves := s.genMoves(rootMoveLimit, playerMe)
	if len(moves) == 0 {
		return -1, -1
	}

	// Tactical short-circuits:
	//  1. complete our own five;
	//  2. otherwise block the opponent's five (forced).
	// winsMove applies the active rule (exact five / caro ends / renju).
	for _, m := range moves {
		if s.winsMove(m, playerMe) {
			return m % n, m / n
		}
	}
	for _, m := range moves {
		if s.winsMove(m, playerOpp) {
			return m % n, m / n
		}
	}

	// opening book (gobang port): forced moves above already took precedence.
	// Quiet positions adopt the top-weighted book move directly; tactical
	// positions only let the book bias root move ordering.
	if cands := s.bookCandidates(); len(cands) > 0 {
		s.bookHits++
		if !s.hasThreatAtLeast(scoreLiveThree) {
			s.bookAdopted++
			return cands[0].move % n, cands[0].move / n
		}
		s.applyBookOrdering(cands, moves)
	}

	start := time.Now()
	if budget > 0 {
		// budget <= 0 means unlimited (deterministic fixed-depth tests)
		s.deadline = start.Add(budget)
		s.softDeadline = start.Add(budget / 3)
	}

	best := moves[0]
	prevScore := 0
	for depth := 2; depth <= maxDepth; depth += 2 {
		if depth > 2 && !s.softDeadline.IsZero() && time.Now().After(s.softDeadline) {
			break // not enough budget left to finish another iteration
		}
		if s.historyEnabled {
			s.ageHistory()
		}
		// search the previous best move first to improve pruning (plan step 8)
		for i, m := range moves {
			if m == best && i > 0 {
				moves[0], moves[i] = moves[i], moves[0]
				break
			}
		}
		if s.smpRotate > 0 && len(moves) > 1 {
			// lazy SMP worker: rotate the root list so this worker's tree (and
			// TT writes) diverges from the others — diversity is the speedup
			r := s.smpRotate % len(moves)
			rotated := append([]int(nil), moves[r:]...)
			rotated = append(rotated, moves[:r]...)
			copy(moves, rotated)
		}

		// aspiration window (guidebook ch.9.3 extension): the previous
		// iteration's score predicts this one, so a narrowed window makes the
		// root pass (and its PVS scouts) much cheaper. On a fail the window
		// re-centers on the observed bound — the freshest information about
		// the true value — and widens geometrically; the final re-search keeps
		// the score exact. widenFactor adapts to score stability: positions
		// whose scores swing between depths start wider next time.
		alpha, beta := -1<<60, 1<<60
		margin := aspirationMargin * s.widenFactor
		windowed := s.aspirationEnabled && depth > 2 && s.widenFactor < 16 &&
			prevScore > -(winScore-1024) && prevScore < winScore-1024
		if windowed {
			alpha, beta = prevScore-margin, prevScore+margin
		}
		var score, mv int
		var ok bool
		failed := false
		for {
			score, mv, ok = s.searchRootWindowed(depth, moves, alpha, beta)
			if !ok || !windowed || (score > alpha && score < beta) {
				break // aborted, full window, or exact value inside the window
			}
			failed = true
			margin *= 4
			if margin > 4*winScore { // window covers every legitimate score
				windowed = false
				alpha, beta = -1<<60, 1<<60
				continue
			}
			if score <= alpha {
				alpha = score - margin
			}
			if score >= beta {
				beta = score + margin
			}
		}
		if !ok {
			break // aborted mid-depth: keep the previous iteration's move
		}
		if failed {
			if s.widenFactor < 16 {
				s.widenFactor *= 2
			}
		} else if s.widenFactor > 1 {
			s.widenFactor /= 2
		}

		best = mv
		prevScore = score
		s.lastDepth = depth
		if score >= winScore-64 {
			break // forced win found, no deeper search needed
		}
	}

	// kill search (tutorial ch.8): when the full search found no forced win,
	// probe VCF/VCT for a deep forcing sequence the narrow search missed.
	// A win the main search itself proved is kept as-is — the kill search's
	// narrower forcing-only proof must never override it with an unproven
	// alternative.
	if prevScore < winScore-64 {
		if r := s.runKillSearch(budget); r >= 0 {
			return r % n, r / n
		}
		// opponent kill probe (reference candidateMinmax net): a proven
		// opponent forcing win outranks the search's quiet choice — occupy
		// its principal threat point when the block verifiably holds
		if r := s.runOppKillProbe(budget); r >= 0 {
			return r % n, r / n
		}
	}
	return best % n, best / n
}

// searchRoot runs one alpha-beta pass at the root over the full window,
// tracking the best move. ok is false when the search ran out of time.
func (s *searcher) searchRoot(depth int, moves []int) (score, move int, ok bool) {
	return s.searchRootWindowed(depth, moves, -1<<60, 1<<60)
}

// searchRootWindowed is the root pass over an arbitrary (alpha, beta) window —
// aspiration search re-searches with widened windows on a fail-low/high, so
// the returned score is exact exactly as with the full window, at a fraction
// of the node cost when the previous iteration's score is a good predictor.
// A fail-high (score >= beta) stops early: the re-search will re-visit.
func (s *searcher) searchRootWindowed(depth int, moves []int, alpha, beta int) (score, move int, ok bool) {
	s.rootDepth = depth
	bestMove := -1
	bestScore := -1 << 60
	for i, m := range moves {
		s.makeMove(m, playerMe)
		var v int
		if s.winsMove(m, playerMe) {
			v = winScore
		} else if depth <= 1 {
			// horizon leaf: opponent to move — negate their side-to-move eval
			v = -s.stmEvaluate(playerOpp)
		} else if i > 0 && s.abEnabled && s.pvsEnabled {
			// PVS scout at the root (guidebook ch.9): the previous best move
			// (moved to the front by run) is trusted with the full window,
			// the rest only need to beat alpha to earn a re-search
			v = -s.negamax(depth-1, playerOpp, -alpha-1, -alpha)
			if !s.aborted && v > alpha && v < beta {
				v = -s.negamax(depth-1, playerOpp, -beta, -alpha)
			}
		} else {
			v = -s.negamax(depth-1, playerOpp, -beta, -alpha)
		}
		s.undoMove(m, playerMe)
		if s.aborted {
			return 0, -1, false
		}
		if v > bestScore {
			bestScore, bestMove = v, m
		}
		if v >= beta && s.abEnabled {
			return bestScore, bestMove, true // fail high: bounds only, re-searched
		}
		if v > alpha {
			alpha = v
		}
	}
	if bestMove < 0 {
		bestMove = moves[0]
	}
	return bestScore, bestMove, true
}

// negamax is the guidebook ch.9 form of alpha-beta: a single role, where
// every node's value is from the side to move's perspective and the parent
// negates the child's result — score = -negamax(depth-1, -beta, -alpha).
// side is the side to move at this node; alpha/beta bound the side-to-move
// value. A transposition table is probed on entry and updated on exit;
// cached scores are side-to-move values.
func (s *searcher) negamax(depth, side, alpha, beta int) int {
	s.nodes++
	if s.limitHit() {
		s.aborted = true
		return 0
	}
	if s.aborted {
		return 0
	}

	// transposition probe: reuse a sufficiently deep result, or at least the
	// cached best move for ordering
	ttBest := noMove
	if s.ttEnabled {
		if e, ok := s.tt.probe(s.nodeKey(side)); ok {
			if e.best != noMove {
				ttBest = e.best
			}
			// scores in the win range are ply-relative (winScore - ply), so
			// they must never be reused as cutoffs on another search path;
			// their best move above is still safe to use for ordering
			if int(e.depth) >= depth && e.score > -(winScore-1024) && e.score < winScore-1024 {
				switch e.flag {
				case ttFlagExact:
					return int(e.score)
				case ttFlagLower:
					if int(e.score) >= beta {
						return int(e.score)
					}
				case ttFlagUpper:
					if int(e.score) <= alpha {
						return int(e.score)
					}
				}
			}
		}
	}

	moves, quietFrom := s.genMovesRanked(nodeMoveLimit, side)
	if len(moves) == 0 {
		return s.stmEvaluate(side) // board full
	}
	if ttBest != noMove {
		for i, m := range moves {
			if uint16(m) == ttBest && i > 0 {
				moves[0], moves[i] = moves[i], moves[0]
				break
			}
		}
	}
	if s.historyEnabled && quietFrom < len(moves) {
		// history heuristic: inside the reorderable tail, moves that caused
		// beta cutoffs at this depth in earlier visits come first. Slot 0
		// (TT move or the ladder's top threat) is kept — it gets the
		// full-window search.
		seg := moves[quietFrom:]
		if quietFrom < 1 {
			seg = moves[1:]
		}
		sort.SliceStable(seg, func(i, j int) bool {
			return s.history[side-1][seg[i]] > s.history[side-1][seg[j]]
		})
	}
	if s.killersEnabled {
		// killer moves: the refutations that caused beta cutoffs at this ply
		// in earlier visits try right after the TT move, with null-window
		// scouts making their cost near-zero when they fail again
		s.promoteKillers(moves, s.rootDepth-depth, ttBest != noMove)
	}

	ply := s.rootDepth - depth
	alphaOrig, betaOrig := alpha, beta
	best := -1 << 60
	var bestMove = noMove
	for i, m := range moves {
		s.makeMove(m, side)
		var v int
		if s.winsMove(m, side) {
			// the mover wins: sooner is better; the negating parent sees
			// -(winScore - ply), so losing later is better for them
			v = winScore - ply
		} else if depth <= 1 {
			// horizon: resolve forcing four-chains before scoring instead of
			// trusting the static evaluation (replaces the old rush-four
			// extension, which made null-window PVS scouts explode)
			v = -s.quiescence(3-side, -beta, -alpha, ply+1, quiescenceDepth, m, side)
		} else {
			// PVS (guidebook ch.9): the first move is searched with the full
			// window; later moves get a null-window scout (-alpha-1, -alpha)
			// that must only prove the move falls outside (alpha, beta). A
			// scout value still inside the window earns a full re-search to
			// pin down its exact value. winsMove and quiescence leaves above
			// are already exact.
			if i > 0 && s.abEnabled && s.pvsEnabled {
				// late move reduction (quiet tail only): a late quiet move
				// rarely raises alpha, so prove it one ply shallower first and
				// re-search at full depth when the scout looks promising.
				// Unlike pruning, LMR can change search values — gated behind
				// lmrEnabled so the value-equivalence suite can exclude it.
				if s.lmrEnabled && i >= 4 && depth >= 3 && i >= quietFrom {
					v = -s.negamax(depth-2, 3-side, -alpha-1, -alpha)
					if !s.aborted && v > alpha {
						v = -s.negamax(depth-1, 3-side, -alpha-1, -alpha)
						if !s.aborted && v > alpha && v < beta {
							v = -s.negamax(depth-1, 3-side, -beta, -alpha)
						}
					}
				} else {
					v = -s.negamax(depth-1, 3-side, -alpha-1, -alpha)
					if !s.aborted && v > alpha && v < beta {
						v = -s.negamax(depth-1, 3-side, -beta, -alpha)
					}
				}
			} else {
				v = -s.negamax(depth-1, 3-side, -beta, -alpha)
			}
		}
		s.undoMove(m, side)
		if s.aborted {
			return 0 // aborted results are never stored below
		}
		if v > best {
			best = v
			bestMove = uint16(m)
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta && s.abEnabled {
			s.abCuts++
			if bestMove != noMove {
				if s.killersEnabled {
					s.recordKiller(ply, bestMove)
				}
				if s.historyEnabled {
					s.addHistory(side, int(bestMove), depth)
				}
			}
			break // prune: this branch cannot influence the decision
		}
	}

	// transposition store: classify against this node's window — a beta
	// cutoff is a lower bound, staying below alpha is an upper bound,
	// anything else is exact. Values are side-to-move scores.
	flag := ttFlagExact
	switch {
	case best <= alphaOrig:
		flag = ttFlagUpper
	case best >= betaOrig:
		flag = ttFlagLower
	}
	s.tt.store(s.nodeKey(side), int32(best), depth, bestMove, flag)
	return best
}

// quiescenceDepth bounds the forcing-only search past the horizon (the
// reference engine uses the same limit).
const quiescenceDepth = 4

// aspirationMargin is the half-width of the root aspiration window: one live
// three (30,000) on each side of the previous iteration's score. Fail-soft
// bounds outside the window widen geometrically before falling back to the
// full window, so the final root score stays exact.
const aspirationMargin = 30000

// quiescence resolves forcing sequences past the depth horizon before a leaf
// is scored: instead of trusting the static evaluation, four-threats for
// either side are expanded so a forced block is always searched. This takes
// over the "see past the forced block" duty of the removed rush-four
// extension at a fraction of its node cost — and it is what keeps PVS
// null-window scouts cheap, since they can now terminate at a narrow
// quiescence instead of grinding through extended chains.
//
// Negamax convention: side is the side to move, the returned value is from
// that side's perspective, and children are searched with negated windows
// (-beta, -alpha). Unlike the guidebook's fail-hard sketch (§9.4) this is
// fail-soft — it returns the best value found even outside the window.
//
// The gate is the load-bearing trick: every move quiescence itself expands is
// a four-completion or its block, so a pending four can only exist when the
// last move just created one (lastP/lastSide; lastP < 0 means "unknown" and
// conservatively engages the search). A leftover four after a block — the
// double-four the gate cannot see — is already scored as a near-loss by the
// static line evaluation, so falling straight back to the static evaluation
// is safe and keeps quiet leaves at raw-evaluation cost.
func (s *searcher) quiescence(side, alpha, beta, ply, qdepth int, lastP, lastSide int) int {
	if lastP >= 0 && !s.extendsOn(lastP, lastSide) {
		return s.stmEvaluate(side)
	}
	s.nodes++
	if s.limitHit() {
		s.aborted = true
		return 0
	}
	if s.aborted {
		return 0
	}

	stand := s.stmEvaluate(side)
	if stand >= beta {
		return stand
	}
	if stand > alpha {
		alpha = stand
	}
	if qdepth <= 0 {
		return stand
	}

	best := stand
	for _, m := range s.fourThreats(side) {
		s.makeMove(m.p, side)
		var v int
		if s.winsMove(m.p, side) {
			v = winScore - ply
		} else {
			v = -s.quiescence(3-side, -beta, -alpha, ply+1, qdepth-1, m.p, side)
		}
		s.undoMove(m.p, side)
		if s.aborted {
			return 0 // aborted results are never stored below
		}
		if v > best {
			best = v
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break
		}
	}
	return best
}

// limitHit is the periodic abort check shared by negamax and quiescence:
// the wall-clock deadline (every 512nd node) or the max_node budget, when
// the manager set one.
func (s *searcher) limitHit() bool {
	if s.nodes%512 == 0 && !s.deadline.IsZero() && time.Now().After(s.deadline) {
		return true
	}
	return s.nodeLimit > 0 && int64(s.nodes) > s.nodeLimit
}

// recordKiller stores the move that just caused a beta cutoff at ply: slot 0
// holds the most recent refutation, slot 1 the one before; a repeat is not
// recorded again (tutorial ch.9 links / guidebook killer heuristic).
func (s *searcher) recordKiller(ply int, mv uint16) {
	if ply < 0 || ply >= len(s.killers) {
		return
	}
	k := &s.killers[ply]
	if k[0] == mv {
		return
	}
	k[1] = k[0]
	k[0] = mv
}

// addHistory credits the refuting move with depth squared: the deeper the
// subtree it cut, the more valuable trying it early is (guidebook history
// heuristic). Per-side tables keep the two roles' statistics apart.
func (s *searcher) addHistory(side, mv, depth int) {
	idx := side - 1
	if idx < 0 || idx > 1 || mv < 0 || mv >= len(s.history[idx]) {
		return
	}
	s.history[idx][mv] += int32(depth * depth)
}

// ageHistory halves all statistics; run() calls it between iterative-deepening
// iterations so recent cutoffs outweigh stale ones and the counters stay
// bounded.
func (s *searcher) ageHistory() {
	for i := range s.history[0] {
		s.history[0][i] /= 2
		s.history[1][i] /= 2
	}
}

// promoteKillers swaps the ply's killer moves to the slots right after the
// TT move (positions 1 and 2, or 0 and 1 without one) so they are searched
// early. Killers absent from the current candidate list are skipped.
func (s *searcher) promoteKillers(moves []int, ply int, hasTT bool) {
	if ply < 0 || ply >= len(s.killers) {
		return
	}
	from := 0
	if hasTT {
		from = 1
	}
	for _, k := range s.killers[ply] {
		if k == noMove || from >= len(moves) {
			continue
		}
		for i := from; i < len(moves); i++ {
			if uint16(moves[i]) == k {
				moves[from], moves[i] = moves[i], moves[from]
				from++
				break
			}
		}
	}
}

// fourThreats lists the cells that complete a four (or five) for either side
// — the forcing moves quiescence expands — strongest first. Under renju the
// black side's forbidden points are excluded, mirroring genMoves. With the
// live-three extension on, the opponent's live-four makers (the growth points
// of their fresh three) join the set as blocking moves — without them the
// widened gate would let the three's owner convert while the defender stands
// still (the same blind spot the VCT defence layer had).
func (s *searcher) fourThreats(side int) []scoredMove {
	n := s.n
	opp := 3 - side
	var out []scoredMove
	for p := 0; p < n*n; p++ {
		if s.b[p] != 0 || s.near1Cnt[p] == 0 {
			continue
		}
		if s.rule == RuleRenju && side == s.blackSide && s.isForbidden(p, side) {
			continue
		}
		mine := s.pointScore(p, side)
		theirs := s.pointScore(p, opp)
		floor := scoreRushFour
		if s.qThreeEnabled && theirs >= scoreLiveFour {
			floor = scoreLiveFour // blocking their live-four maker is forcing too
		}
		if mine < floor && theirs < floor {
			continue
		}
		key := max(mine, theirs)
		out = append(out, scoredMove{p: p, key: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key > out[j].key })
	return out
}

func (s *searcher) extendsOn(p, side int) bool {
	if s.threatMadeAt(p, side) {
		return true
	}
	if s.qThreeEnabled {
		// live-three gate: the last move made a live three in some direction,
		// counted on the actual stones through p — pointScore values the
		// hypothetical placement and is meaningless on an occupied cell.
		// Growth points of a live three are live-four makers, which the
		// rush-four floor of fourThreats already lists, so opening the gate
		// alone extends the resolved chains from fours to threes. Jump
		// threes (o.oo) are covered by the span window below.
		n := s.n
		x, y := p%n, p/n
		for d := 0; d < 4; d++ {
			dx, dy := dirX[d], dirY[d]
			a := s.countLine(x, y, -dx, -dy, side)
			b := s.countLine(x, y, dx, dy, side)
			c := 1 + a + b
			if c == 3 && s.isEmptyAt(x-(a+1)*dx, y-(a+1)*dy) && s.isEmptyAt(x+(b+1)*dx, y+(b+1)*dy) {
				return true // contiguous live three
			}
			// jump three: p's run plus a one-gap continuation inside a
			// 5-cell span — 2 more stones within ±4 steps of p on this line,
			// with p itself making 3 of a five-window
			stones, lo, hi := 1, 0, 0
			for k := -4; k <= 4; k++ {
				if k == 0 {
					continue
				}
				nx, ny := x+k*dx, y+k*dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n || s.b[ny*n+nx] != side {
					continue
				}
				stones++
				if lo == 0 || k < lo {
					lo = k
				}
				if k > hi {
					hi = k
				}
			}
			if stones >= 3 && hi-lo <= 4 {
				// three stones (incl. p) inside a 5-window: a jump-three
				// shape whose fill point is a four maker
				return true
			}
		}
	}
	return false
}

// threatMadeAt reports whether side's stone at p completed a four (or five)
// in any direction — the original quiescence gate.
func (s *searcher) threatMadeAt(p, side int) bool {
	n := s.n
	x, y := p%n, p/n
	for d := 0; d < 4; d++ {
		c := 1 + s.countLine(x, y, dirX[d], dirY[d], side) + s.countLine(x, y, -dirX[d], -dirY[d], side)
		if c >= 4 {
			return true
		}
	}
	return false
}

// genMoves is the heuristic move generator (plan step 6 / tutorial ch.5).
// side is the player to move at the node being generated for: under renju,
// forbidden points are removed for the black side right here, so the search
// tree never enters an illegal branch (evaluation is never involved).
// Every remaining empty cell with a nearby stone is classified by what
// placing a stone there would produce — five, live four, rush four, double
// three, live three, live two, or just a neighbor — and the candidate list is
// assembled by threat priority: forcing moves first, so alpha-beta prunes at
// its best.
//
// Classification is per-role: a cell may be our winning point or the
// opponent's, and defending ranks together with attacking (a cell completing
// the opponent's five must be searched exactly like our own five).
// genMoves is the candidate generator; see genMovesRanked for the ordering
// contract. quietFrom (the start of the history-reorderable tail) is dropped.
func (s *searcher) genMoves(limit, side int) []int {
	moves, _ := s.genMovesRanked(limit, side)
	return moves
}

// genMovesRanked returns the candidate moves plus quietFrom, the index where
// the history heuristic may start reordering: everything before it is the
// forcing part of the threat ladder — fives and fours everywhere, and the
// double threes on quiet paths — whose relative order carries tactical
// meaning; live threes and quieter moves may be shuffled by cutoff
// statistics.
func (s *searcher) genMovesRanked(limit, side int) ([]int, int) {
	clamp := func(moves []int, quietFrom int) ([]int, int) {
		moves = s.capMoves(moves, limit)
		if quietFrom > len(moves) {
			quietFrom = len(moves)
		}
		return moves, quietFrom
	}
	n := s.n
	if s.stones == 0 {
		return []int{(n/2)*n + n/2}, 1 // empty board: center (天元)
	}
	forbidden := s.rule == RuleRenju && side == s.blackSide

	type bucket = []int
	var (
		fivesMe, fivesOpp []int
		liveFourMe        []int
		rushFourMe        []int
		rushFourOpp       []int
		liveFourOpp       []int
		doubleThreeMe     []int
		doubleThreeOpp    []int
		liveThreeMe       []int
		liveThreeOpp      []int
		twoScored         []scoredMove
		neighbors         []scoredMove
	)

	for p := 0; p < n*n; p++ {
		if s.b[p] != 0 || s.nearCnt[p] == 0 {
			continue
		}
		x, y := p%n, p/n
		if forbidden && s.isForbidden(p, side) {
			continue // renju: black may never play here
		}
		scoreMe := s.pointScore(p, playerMe)
		scoreOpp := s.pointScore(p, playerOpp)

		// classify by the stronger of the two roles, five first (tutorial:
		// "别急着返回，说不定电脑自己能成五" — collect all fives before returning)
		switch {
		case scoreMe >= scoreFive:
			fivesMe = append(fivesMe, p)
		case scoreOpp >= scoreFive:
			fivesOpp = append(fivesOpp, p)
		case scoreMe >= scoreLiveFour:
			liveFourMe = append(liveFourMe, p)
		case scoreMe >= scoreRushFour:
			rushFourMe = append(rushFourMe, p)
		case scoreOpp >= scoreLiveFour:
			liveFourOpp = append(liveFourOpp, p)
		case scoreOpp >= scoreRushFour:
			rushFourOpp = append(rushFourOpp, p)
		case scoreMe >= 2*scoreLiveThree:
			doubleThreeMe = append(doubleThreeMe, p)
		case scoreOpp >= 2*scoreLiveThree:
			doubleThreeOpp = append(doubleThreeOpp, p)
		case scoreMe >= scoreLiveThree:
			liveThreeMe = append(liveThreeMe, p)
		case scoreOpp >= scoreLiveThree:
			liveThreeOpp = append(liveThreeOpp, p)
		default:
			key := scoreMe + scoreOpp
			if key >= scoreLiveTwo {
				twoScored = append(twoScored, scoredMove{p: p, key: key})
			} else {
				dist := x - n/2
				if dist < 0 {
					dist = -dist
				}
				vy := y - n/2
				if vy < 0 {
					vy = -vy
				}
				neighbors = append(neighbors, scoredMove{p: p, key: key, dist: dist + vy})
			}
		}
	}

	// priority assembly (tutorial ch.5 return ladder). Every threat rung keeps
	// BOTH roles' cells — the side to move's own winning points first, the
	// opponent's right behind — the reference implementation's invariant
	// (eval.js bySide/orderedSet). A single-side early return is only "forced"
	// at an engine-to-move node; at an opponent-to-move node it hides the
	// reply's own counter-win (the live-three growth points) and the search
	// constructs phantom forced wins — the live-three-no-defence bug.
	//
	// rung 1 — fives: the mover completes five, or blocks the opponent's.
	if len(fivesMe) > 0 || len(fivesOpp) > 0 {
		self, other := fivesMe, fivesOpp
		if side == playerOpp {
			self, other = fivesOpp, fivesMe
		}
		out := make([]int, 0, len(self)+len(other))
		out = append(out, self...)
		out = append(out, other...)
		return out, len(out)
	}
	// rung 2 — live fours: the mover's own wins the race; the opponent's are
	// both the blocks a live three demands and, at an opponent-to-move node,
	// the refutation of any phantom win. Rush fours ride along in the same
	// rung like the reference's block fours.
	if len(liveFourMe) > 0 || len(liveFourOpp) > 0 {
		self, other := liveFourMe, liveFourOpp
		if side == playerOpp {
			self, other = liveFourOpp, liveFourMe
		}
		out := make([]int, 0, len(self)+len(other)+len(rushFourMe)+len(rushFourOpp))
		out = append(out, self...)
		out = append(out, other...)
		out = append(out, rushFourMe...)
		out = append(out, rushFourOpp...)
		return clamp(out, len(out))
	}
	if len(rushFourMe) > 0 || len(rushFourOpp) > 0 {
		fours := make([]int, 0, len(rushFourMe)+len(rushFourOpp)+
			len(doubleThreeMe)+len(doubleThreeOpp)+
			len(liveThreeMe)+len(liveThreeOpp))
		fours = append(fours, rushFourMe...)
		fours = append(fours, rushFourOpp...)
		fours = append(fours, doubleThreeMe...)
		fours = append(fours, doubleThreeOpp...)
		fours = append(fours, liveThreeMe...)
		fours = append(fours, liveThreeOpp...)
		return clamp(fours, len(rushFourMe)+len(rushFourOpp))
	}

	doubleThrees := len(doubleThreeMe) + len(doubleThreeOpp)
	out := make([]int, 0, limit)
	out = append(out, doubleThreeMe...)
	out = append(out, doubleThreeOpp...)
	out = append(out, liveThreeMe...)
	out = append(out, liveThreeOpp...)
	if doubleThrees > 0 {
		// double threes outrank simple live threes unpredictably (tutorial:
		// "能形成双三的不一定比一个活三强") — do not dilute with weaker moves
		return clamp(out, doubleThrees)
	}

	sort.Slice(twoScored, func(i, j int) bool { return twoScored[i].key > twoScored[j].key })
	if len(twoScored) > 0 {
		for _, m := range twoScored {
			out = append(out, m.p)
		}
	} else {
		sort.Slice(neighbors, func(i, j int) bool {
			if neighbors[i].key != neighbors[j].key {
				return neighbors[i].key > neighbors[j].key
			}
			return neighbors[i].dist < neighbors[j].dist // prefer the center on ties
		})
		for _, m := range neighbors {
			out = append(out, m.p)
		}
	}
	return clamp(out, doubleThrees)
}

type scoredMove struct {
	p, key, dist int
}

func (s *searcher) capMoves(moves []int, limit int) []int {
	if limit > 0 && len(moves) > limit {
		return moves[:limit]
	}
	return moves
}

// pointScore estimates the value of placing `side`'s stone on p by looking at
// the shape formed in each of the four directions. It is used for move
// ordering only, so an approximation is fine. Values are cached per
// (cell, direction, side) and invalidated by setStone's dirty marks — the
// sum reads four cached ints instead of walking the lines on every call.
func (s *searcher) pointScore(p, side int) int {
	total := 0
	for d := 0; d < 4; d++ {
		total += int(s.dirShape(p, d, side))
	}
	return total
}

// dirShape returns side's shape value for direction d through p, recomputing
// (and caching) when the dirty bit says a stone change may have touched the
// line's ±9-step dependency window.
func (s *searcher) dirShape(p, d, side int) int32 {
	idx := (p*4+d)*2 + side - 1
	bit := uint8(1) << uint(d*2+side-1)
	if s.dirDirty[p]&bit == 0 {
		return s.dirVal[idx]
	}
	n := s.n
	x, y := p%n, p/n
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
	s.dirVal[idx] = int32(v)
	s.dirDirty[p] &^= bit
	return int32(v)
}

// shapeVal scores a hypothetical run of c stones (after placement) whose two
// ends are empty (e1/e2) or blocked.
func shapeVal(c int, e1, e2 bool) int {
	switch {
	case c >= 5:
		return scoreFive
	case c == 4:
		if e1 && e2 {
			return scoreLiveFour
		}
		if e1 || e2 {
			return scoreRushFour
		}
	case c == 3:
		if e1 && e2 {
			return scoreLiveThree
		}
		if e1 || e2 {
			return scoreSleepThree
		}
	case c == 2:
		if e1 && e2 {
			return scoreLiveTwo
		}
		if e1 || e2 {
			return scoreSleepTwo
		}
	case c == 1:
		if e1 && e2 {
			return scoreOne
		}
	}
	return 0
}

// posBonus is the center-pyramid value of one stone: rings toward the edge
// decay to zero. Purely positional — it never outweighs a sleep two.
func posBonus(p, n int) int {
	c := n / 2
	x, y := p%n, p/n
	dx, dy := x-c, y-c
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	r := dx
	if dy > r {
		r = dy
	}
	v := c - r
	if v <= 0 {
		return 0
	}
	return v * scorePosUnit
}

// stmEvaluate returns the static evaluation from the side-to-move's
// perspective — the negamax convention (guidebook §5.1: positive = good for
// `side`). evaluate() itself stays engine-perspective, preserved bit for bit
// by the line cache.
func (s *searcher) stmEvaluate(side int) int {
	if side == playerMe {
		return s.evaluate()
	}
	return -s.evaluate()
}

// evaluate returns the static board evaluation (plan step 3) from the
// engine's perspective: positive when the engine is better. The value is
// maintained incrementally by setStone — one O(n) rescan per touched line —
// and equals the from-scratch evaluateFull bit for bit, which is kept as the
// correctness reference (TestEvalIncrementalExact).
func (s *searcher) evaluate() int {
	if s.posEnabled {
		return s.evalTotal + s.posTotal
	}
	return s.evalTotal
}

// evaluateFull is the original from-scratch board scan: every row, column and
// diagonal scored once for both sides. Only tests use it.
func (s *searcher) evaluateFull() int {
	n := s.n
	me, opp := 0, 0
	line := s.lineBuf

	// rows: the board rows are already contiguous slices
	for y := 0; y < n; y++ {
		row := s.b[y*n : (y+1)*n]
		me += scoreLineFor(row, playerMe)
		opp += scoreLineFor(row, playerOpp)
	}
	// columns
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			line[y] = s.b[y*n+x]
		}
		me += scoreLineFor(line[:n], playerMe)
		opp += scoreLineFor(line[:n], playerOpp)
	}
	// both diagonal families
	for _, dir := range [2][2]int{{1, 1}, {1, -1}} {
		dx, dy := dir[0], dir[1]
		scan := func(sx, sy int) {
			idx := 0
			for x, y := sx, sy; x >= 0 && x < n && y >= 0 && y < n; x, y = x+dx, y+dy {
				line[idx] = s.b[y*n+x]
				idx++
			}
			me += scoreLineFor(line[:idx], playerMe)
			opp += scoreLineFor(line[:idx], playerOpp)
		}
		for sy := 0; sy < n; sy++ {
			scan(0, sy)
		}
		for sx := 1; sx < n; sx++ {
			if dy == 1 {
				scan(sx, 0)
			} else {
				scan(sx, n-1)
			}
		}
	}
	if s.posEnabled {
		for p, v := range s.b {
			switch v {
			case playerMe:
				me += posBonus(p, n)
			case playerOpp:
				opp += posBonus(p, n)
			}
		}
	}
	return me - opp
}

// lineCount is the number of scoreable lines on an n×n board: n rows, n
// columns and 2n-1 diagonals per family. Line ids: row y → y, column x → n+x,
// diagonal (1,1) through (x,y) → 3n-1+(x-y), diagonal (1,-1) → 4n-1+(x+y).
func lineCount(n int) int { return 6*n - 2 }

// gatherLine collects the stones of line id into a contiguous slice so
// scoreLineFor can scan it. Rows alias the board directly, everything else
// uses the scratch buffer.
func (s *searcher) gatherLine(id int) []int {
	n := s.n
	switch {
	case id < n: // row y = id
		return s.b[id*n : id*n+n]
	case id < 2*n: // column x = id-n
		x := id - n
		for y := 0; y < n; y++ {
			s.lineBuf2[y] = s.b[y*n+x]
		}
		return s.lineBuf2[:n]
	case id < 4*n-1: // diagonal (1,1), constant x-y = id-(3n-1)
		c := id - (3*n - 1)
		x0, y0 := 0, -c
		if c > 0 {
			x0, y0 = c, 0
		}
		k := 0
		for x, y := x0, y0; x < n && y < n; x, y = x+1, y+1 {
			s.lineBuf2[k] = s.b[y*n+x]
			k++
		}
		return s.lineBuf2[:k]
	default: // diagonal (1,-1), constant x+y = id-(4n-1)
		sum := id - (4*n - 1)
		x0, y0 := 0, sum
		if sum >= n {
			x0, y0 = sum-(n-1), n-1
		}
		k := 0
		for x, y := x0, y0; x >= 0 && x < n && y >= 0 && y < n; x, y = x+1, y-1 {
			s.lineBuf2[k] = s.b[y*n+x]
			k++
		}
		return s.lineBuf2[:k]
	}
}

// rescoreLine re-scans one line for both sides and folds the difference into
// the running evaluation total.
func (s *searcher) rescoreLine(id int) {
	line := s.gatherLine(id)
	old := int(s.lineScore[0][id]) - int(s.lineScore[1][id])
	me := scoreLineFor(line, playerMe)
	opp := scoreLineFor(line, playerOpp)
	s.lineScore[0][id] = int32(me)
	s.lineScore[1][id] = int32(opp)
	s.evalTotal += me - opp - old
}

// scoreLineFor scans one line and accumulates `side`'s pattern score.
// Each maximal stone run is classified exactly once; a one-cell gap followed
// by more stones (jump shapes) is folded into the leading run.
func scoreLineFor(line []int, side int) int {
	total, n := 0, len(line)
	for i := 0; i < n; {
		if line[i] != side {
			i++
			continue
		}
		k := i
		for k < n && line[k] == side {
			k++
		}
		c := k - i
		if c >= 5 {
			total += scoreFive
			i = k
			continue
		}
		e1 := i > 0 && line[i-1] == 0
		e2 := k < n && line[k] == 0

		// jump continuation: one empty gap, then more of our stones
		if e2 {
			j := k + 1
			for j < n && line[j] == side {
				j++
			}
			if c2 := j - (k + 1); c2 > 0 {
				e3 := j < n && line[j] == 0
				switch sum := c + c2; {
				case sum >= 4:
					// filling the gap completes five: a four, plus extra
					// scope when both outer ends are open
					total += scoreRushFour
					if e1 && e3 {
						total += scoreLiveThree
					}
					i = j
					continue
				case sum == 3:
					// one move from a four (live three equivalent when the
					// gap fill yields an open four)
					if e1 && e3 {
						total += scoreJumpThree
					} else if e1 || e3 {
						total += scoreSleepThree
					}
					i = j
					continue
				}
				// sum <= 2: too weak to merge, score the runs separately
			}
		}

		// contiguous run alone; a live three needs air beyond one of its ends
		deep := (i >= 2 && line[i-2] == 0) || (k+1 < n && line[k+1] == 0)
		total += runScore(c, e1, e2, deep)
		i = k
	}
	return total
}

// runScore classifies a plain contiguous run of c stones with ends e1/e2.
func runScore(c int, e1, e2, deep bool) int {
	switch c {
	case 4:
		if e1 && e2 {
			return scoreLiveFour
		}
		if e1 || e2 {
			return scoreRushFour
		}
	case 3:
		if e1 && e2 {
			if deep {
				return scoreLiveThree
			}
			return scoreSleepThree
		}
		if e1 || e2 {
			return scoreSleepThree
		}
	case 2:
		if e1 && e2 {
			return scoreLiveTwo
		}
		if e1 || e2 {
			return scoreSleepTwo
		}
	case 1:
		if e1 && e2 {
			return scoreOne
		}
	}
	return 0
}

// makesFive reports whether placing side's stone on p completes five or more
// in a row (plan step 1: check_win). p itself is treated as side's stone.
func (s *searcher) makesFive(p, side int) bool {
	n := s.n
	x, y := p%n, p/n
	for d := 0; d < 4; d++ {
		c := 1 + s.countLine(x, y, dirX[d], dirY[d], side) + s.countLine(x, y, -dirX[d], -dirY[d], side)
		if c >= 5 {
			return true
		}
	}
	return false
}

// countLine counts side's consecutive stones starting from (x,y) stepped by
// (dx,dy); the starting cell itself is not examined.
func (s *searcher) countLine(x, y, dx, dy, side int) int {
	n := s.n
	c := 0
	for {
		x += dx
		y += dy
		if x < 0 || y < 0 || x >= n || y >= n || s.b[y*n+x] != side {
			return c
		}
		c++
	}
}

func (s *searcher) isEmptyAt(x, y int) bool {
	if x < 0 || y < 0 || x >= s.n || y >= s.n {
		return false
	}
	return s.b[y*s.n+x] == 0
}

func (s *searcher) stoneCount() int {
	return s.stones
}
