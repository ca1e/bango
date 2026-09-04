package alphabeta

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gomoku/book"
)

const testBookBody = `{"id":"t","name":"test book","rules":"freestyle","size":9,
	"coordinateSystem":"board-row-column",
	"openings":[{"id":"a","coordinates":[[4,4],[6,3],[4,5],[6,6],[3,3]]}]}`

// bookTestSearcher builds a 9x9 searcher with the test book loaded.
func bookTestSearcher(t *testing.T) *searcher {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.json"), []byte(testBookBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := book.LoadFile(filepath.Join(dir, "book.json"))
	if err != nil {
		t.Fatalf("load book: %v", err)
	}
	s := newSearcher(9, make([]int, 81), 0)
	s.book = cb
	return s
}

// TestEngineBookDirectAdoption: a quiet covered position plays the book move
// without searching.
func TestEngineBookDirectAdoption(t *testing.T) {
	s := bookTestSearcher(t)
	s.setStone(4*9+4, playerMe)  // black (x=4,y=4)
	s.setStone(3*9+6, playerOpp) // white (x=6,y=3): board index is y*9+x
	p, q := s.run(4, time.Second)
	if p != 4 || q != 5 {
		t.Fatalf("run = (%d,%d), want book move (4,5)", p, q)
	}
	if s.bookHits != 1 || s.bookAdopted != 1 {
		t.Fatalf("hits/adopted = %d/%d, want 1/1", s.bookHits, s.bookAdopted)
	}
}

// TestEngineBookThreatGuard: when the covered position already carries a
// live three (the book line's own stones form one) the book is only used for
// ordering — the search decides.
func TestEngineBookThreatGuard(t *testing.T) {
	body := `{"id":"t","name":"test book","rules":"freestyle","size":9,
		"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[2,5],[8,0],[3,5],[0,8],[4,5],[1,1],[5,5]]}]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := book.LoadFile(filepath.Join(dir, "book.json"))
	if err != nil {
		t.Fatalf("load book: %v", err)
	}
	s := newSearcher(9, make([]int, 81), 0)
	s.book = cb
	// the book position's black stones form an open three (2,5),(3,5),(4,5)
	for _, st := range []struct {
		x, y int
		side int
	}{{2, 5, playerMe}, {8, 0, playerOpp}, {3, 5, playerMe}, {0, 8, playerOpp},
		{4, 5, playerMe}, {1, 1, playerOpp}} {
		s.setStone(st.y*9+st.x, st.side)
	}
	if !s.hasOpenThreat() {
		t.Fatal("test setup: the position should carry a live three")
	}
	p, q := s.run(4, time.Second)
	if p < 0 || s.b[q*9+p] != 0 {
		t.Fatalf("illegal move (%d,%d)", p, q)
	}
	if s.bookHits != 1 {
		t.Fatalf("bookHits = %d, want 1", s.bookHits)
	}
	if s.bookAdopted != 0 {
		t.Fatalf("book adopted in a tactical position: (%d,%d)", p, q)
	}
}

// TestEngineBookRuleMismatch: a freestyle book must stay silent in a renju
// game and vice versa.
func TestEngineBookRuleMismatch(t *testing.T) {
	s := bookTestSearcher(t)
	s.setRule(RuleRenju, playerMe)
	s.setStone(4*9+4, playerMe)
	s.setStone(6*9+3, playerOpp)
	p, q := s.run(4, time.Second)
	if p < 0 || s.b[q*9+p] != 0 {
		t.Fatalf("illegal move (%d,%d)", p, q)
	}
	if s.bookHits != 0 {
		t.Fatal("freestyle book used under renju")
	}
}

// TestEngineBookForbiddenFiltered: a renju book whose reply is a forbidden
// point must not offer it — the candidate is dropped and the search takes
// over. The forbidden shape is built entirely from the book line's own
// stones (the position must match a compiled prefix to hit the book).
func TestEngineBookForbiddenFiltered(t *testing.T) {
	// line: black (4,3),(4,4) stack vertically and (3,6),(2,7) on the
	// diagonal; the 5th black stone (4,5) completes a vertical AND a
	// diagonal open three — a 3-3 forbidden point for black
	body := `{"id":"t","rules":"renju","size":9,
		"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","minPrefixLength":8,"coordinates":[
			[4,3],[8,0],[4,4],[0,8],[3,6],[8,8],[2,7],[0,0],[4,5]]}]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := book.LoadFile(filepath.Join(dir, "book.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := newSearcher(9, make([]int, 81), 0)
	s.book = cb
	s.setRule(RuleRenju, playerMe)
	for _, st := range []struct {
		x, y int
		side int
	}{{4, 3, playerMe}, {8, 0, playerOpp}, {4, 4, playerMe}, {0, 8, playerOpp},
		{3, 6, playerMe}, {8, 8, playerOpp}, {2, 7, playerMe}, {0, 0, playerOpp}} {
		s.setStone(st.y*9+st.x, st.side)
	}

	reply := 5*9 + 4 // (4,5)
	if !s.isForbidden(reply, s.blackSide) {
		t.Fatal("test setup: (4,5) should be a 3-3 forbidden point")
	}
	raw := cb.MovesFor([]book.Stone{
		{X: 4, Y: 3, Role: 1}, {X: 8, Y: 0, Role: -1}, {X: 4, Y: 4, Role: 1}, {X: 0, Y: 8, Role: -1},
		{X: 3, Y: 6, Role: 1}, {X: 8, Y: 8, Role: -1}, {X: 2, Y: 7, Role: 1}, {X: 0, Y: 0, Role: -1},
	})
	found := false
	for _, c := range raw {
		if c.Move == reply {
			found = true
		}
	}
	if !found {
		t.Fatal("test setup: the raw book does not offer (4,5) for this prefix")
	}
	for _, c := range s.bookCandidates() {
		if c.Move == reply {
			t.Fatal("forbidden point (4,5) survived the book candidate filter")
		}
		if s.isForbidden(c.Move, s.blackSide) {
			t.Fatalf("forbidden move %d offered by book", c.Move)
		}
	}
}

// TestBookWhiteFreestyleAdoption: regression for the freestyle colour-mapping
// bug. The engine plays WHITE (blackSide defaults to playerMe — exactly what
// aiMove builds under INFO rule 0), and the covered position must still be
// encoded correctly and adopted outright.
func TestBookWhiteFreestyleAdoption(t *testing.T) {
	body := `{"id":"t","name":"test book","rules":"freestyle","size":9,
		"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[4,4],[4,5],[3,4],[5,4]]}]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := book.LoadFile(filepath.Join(dir, "book.json"))
	if err != nil {
		t.Fatalf("load book: %v", err)
	}
	// engine white: the human's black stone at (4,4) is playerOpp
	b := make([]int, 81)
	b[4*9+4] = playerOpp
	s := newSearcher(9, b, 0)
	s.setRule(RuleFreestyle, playerOpp) // the aiMove mapping for a white engine
	s.book = cb
	if s.blackSide != playerOpp {
		t.Fatalf("blackSide = %d, want playerOpp (white engine)", s.blackSide)
	}
	x, y := s.run(4, 0)
	// (4,4) is the 9×9 centre: the position has the full symmetry group, so
	// the book's reply is stored under all eight transforms — every direct
	// neighbour is a valid symmetric twin of (4,5)
	if !((x == 4 && (y == 3 || y == 5)) || (y == 4 && (x == 3 || x == 5))) {
		t.Fatalf("white reply = (%d,%d), want a symmetric twin of the book move (4,5)", x, y)
	}
	if s.bookAdopted != 1 {
		t.Fatalf("bookAdopted = %d, want 1", s.bookAdopted)
	}
}

// TestHasOpenThreatShapes: the book gate classifies forcing shapes exactly.
// The summed-pointScore gate misflagged two unrelated discounted jump shapes
// in different directions as a live three; the exact gate must stay quiet
// there while still catching real threes (contiguous, jump, x.x.x) and fours.
func TestHasOpenThreatShapes(t *testing.T) {
	build := func(stones ...[3]int) *searcher {
		b := make([]int, 81)
		for _, st := range stones {
			b[st[1]*9+st[0]] = st[2]
		}
		return newSearcher(9, b, 0)
	}
	cases := []struct {
		name   string
		pos    [][3]int
		threat bool
	}{
		// quiet: stones pairwise off-line — every empty cell holds at most one
		// discounted jump shape per direction, no contiguous pair anywhere
		{"quiet spread", [][3]int{{4, 4, playerMe}, {0, 3, playerMe}, {0, 4, playerOpp}}, false},
		// the old gate's artifact: cell (3,3) sums two 15000 jump shapes
		// (row to (5,3), column to (3,6)) to 30100 >= scoreLiveThree, but no
		// single direction carries a three and no cell sees both stones
		{"two-direction jump twos", [][3]int{{5, 3, playerMe}, {3, 6, playerMe}, {0, 0, playerOpp}}, false},
		// real threats
		{"contiguous pair (three maker)", [][3]int{{4, 4, playerMe}, {3, 4, playerMe}, {0, 0, playerOpp}}, true},
		{"jump pair fill (x.x)", [][3]int{{4, 4, playerMe}, {6, 4, playerMe}, {0, 0, playerOpp}}, true},
		{"double jump x.x.x", [][3]int{{4, 4, playerMe}, {6, 4, playerMe}, {8, 4, playerMe}, {0, 0, playerOpp}}, true},
		{"four point", [][3]int{{4, 4, playerMe}, {5, 4, playerMe}, {6, 4, playerMe}, {7, 4, playerOpp}, {0, 0, playerOpp}}, true},
	}
	for _, c := range cases {
		s := build(c.pos...)
		if got := s.hasOpenThreat(); got != c.threat {
			t.Errorf("%s: hasOpenThreat = %v, want %v", c.name, got, c.threat)
		}
	}
}

// TestTranslatedFallbackClassicOnly: regression for the random-first-stone
// opening quality finding — the displacement fallback must not adopt remote
// replies. Every candidate for a non-tengen black opening (and the engine's
// actual reply) is a contact/knight development, matching the opening prior.
func TestTranslatedFallbackClassicOnly(t *testing.T) {
	defaults := book.LoadDefaultBooks()
	if defaults == nil {
		t.Fatal("default adoption book did not load")
	}
	for _, black1 := range [][2]int{{6, 6}, {8, 7}, {5, 8}} {
		b := make([]int, 225)
		b[black1[1]*15+black1[0]] = playerOpp // engine is white
		s := newSearcher(15, b, 0)
		s.setRule(RuleFreestyle, playerOpp)
		s.book = defaults
		cands := s.bookCandidates()
		if len(cands) == 0 {
			t.Fatalf("black1 %v: displacement fallback produced no candidates", black1)
		}
		for _, c := range cands {
			if !s.classicOpeningPoint(c.Move) {
				t.Fatalf("black1 %v: remote candidate (%d,%d) survived the classic filter",
					black1, c.Move%15, c.Move/15)
			}
		}
		x, y := s.run(8, 0)
		if !s.classicOpeningPoint(y*15 + x) {
			t.Fatalf("black1 %v: reply (%d,%d) is not a contact/knight development", black1, x, y)
		}
	}
}

// TestTengenReplyBalancedFamilies: the single-tengen-stone reply pool must
// carry BOTH classic families — direct (orthogonal neighbour) and diagonal —
// at equal top weight, and the adoption draw must actually pick each family
// about half the time. The old weight-order pick always played diagonal
// (self-play: 2529/2529 tengen replies diagonal, ~96.6% of them lost).
func TestTengenReplyBalancedFamilies(t *testing.T) {
	defaults := book.LoadDefaultBooks()
	if defaults == nil {
		t.Fatal("default adoption book did not load")
	}
	b := make([]int, 225)
	b[7*15+7] = playerOpp // engine is white, black opened at the tengen
	s := newSearcher(15, b, 0)
	s.setRule(RuleFreestyle, playerOpp)
	s.book = defaults

	cands := s.bookCandidates()
	if len(cands) < 2 {
		t.Fatalf("tengen reply pool has %d candidates, want both families", len(cands))
	}
	direct, diag := 0, 0
	for _, c := range cands {
		dx, dy := c.Move%15-7, c.Move/15-7
		switch {
		case dx == 0 || dy == 0:
			direct++
		case dx != 0 && dy != 0:
			diag++
		}
	}
	if direct == 0 || diag == 0 {
		t.Fatalf("unbalanced pool: %d direct, %d diagonal", direct, diag)
	}
	// after rebalance the pool tops out at one shared weight level holding
	// both families: every candidate at the max weight must not be a single
	// family
	top := cands[0].Weight
	fams := map[bool]bool{} // true = direct
	for _, c := range cands {
		if c.Weight != top {
			break
		}
		dx, dy := c.Move%15-7, c.Move/15-7
		fams[dx == 0 || dy == 0] = true
	}
	if !fams[true] || !fams[false] {
		t.Fatalf("top weight level %d holds only one family: %v", top, fams)
	}

	// the draw: fresh RNG per draw over the rebalanced pool — mirrors the
	// adoption path's weighted pick without running a full search each time
	directPicks, diagPicks := 0, 0
	for i := 0; i < 200; i++ {
		draw := rand.New(rand.NewSource(int64(i))).Intn(cands[0].Weight * len(cands))
		acc := 0
		var pick book.Candidate
		for _, c := range cands {
			acc += c.Weight
			if draw < acc {
				pick = c
				break
			}
		}
		dx, dy := pick.Move%15-7, pick.Move/15-7
		if dx == 0 || dy == 0 {
			directPicks++
		} else {
			diagPicks++
		}
	}
	t.Logf("200 draws: %d direct, %d diagonal", directPicks, diagPicks)
	if directPicks < 60 || diagPicks < 60 {
		t.Fatalf("draw not balanced: %d direct, %d diagonal of 200", directPicks, diagPicks)
	}
}
