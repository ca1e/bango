package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	cb, err := loadBookFile(filepath.Join(dir, "book.json"))
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
	cb, err := loadBookFile(filepath.Join(dir, "book.json"))
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
	if !s.hasThreatAtLeast(scoreLiveThree) {
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
	cb, err := loadBookFile(filepath.Join(dir, "book.json"))
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
	raw := cb.movesFor([]bookStone{
		{4, 3, 1}, {8, 0, -1}, {4, 4, 1}, {0, 8, -1},
		{3, 6, 1}, {8, 8, -1}, {2, 7, 1}, {0, 0, -1},
	})
	found := false
	for _, c := range raw {
		if c.move == reply {
			found = true
		}
	}
	if !found {
		t.Fatal("test setup: the raw book does not offer (4,5) for this prefix")
	}
	for _, c := range s.bookCandidates() {
		if c.move == reply {
			t.Fatal("forbidden point (4,5) survived the book candidate filter")
		}
		if s.isForbidden(c.move, s.blackSide) {
			t.Fatalf("forbidden move %d offered by book", c.move)
		}
	}
}

// TestEngineBookProtocol: end-to-end — a book file in the INFO folder is
// loaded and the BOARD reply comes from the book.
func TestEngineBookProtocol(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "pbrain-bango")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "book.json"), []byte(testBookBody), 0o644); err != nil {
		t.Fatal(err)
	}
	s := startSession(t)
	s.send("START 9")
	expect(t, s, 2*time.Second, "OK")
	s.send("INFO folder " + dir)
	s.send("BOARD")
	s.send("4,4,1")
	s.send("6,3,2")
	s.send("DONE")
	x, y := expectMove(t, s, 5*time.Second, 9)
	if x != 4 || y != 5 {
		t.Fatalf("BOARD reply = (%d,%d), want book move (4,5)", x, y)
	}
}
