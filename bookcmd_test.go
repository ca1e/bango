package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeBook writes a book source file and returns its path.
func writeBook(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBookValidatePass(t *testing.T) {
	path := writeBook(t, `{"id":"t","rules":"freestyle","size":9,
		"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[4,4],[6,3],[4,5],[6,6]]},
		            {"id":"b","coordinates":[[2,2],[3,3],[4,4]]}]}`)
	if err := bookValidate(path); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestBookValidateFailures(t *testing.T) {
	cases := map[string]string{
		"out of range": `{"id":"t","rules":"freestyle","size":9,
			"coordinateSystem":"board-row-column",
			"openings":[{"id":"a","coordinates":[[4,4],[99,3]]}]}`,
		"duplicate move": `{"id":"t","rules":"freestyle","size":9,
			"coordinateSystem":"board-row-column",
			"openings":[{"id":"a","coordinates":[[4,4],[4,4]]}]}`,
		"five in line": `{"id":"t","rules":"freestyle","size":9,
			"coordinateSystem":"board-row-column",
			"openings":[{"id":"a","coordinates":[[0,0],[1,1],[0,1],[1,0],[0,2],[2,0],[0,3],[3,0],[0,4]]}]}`,
		"renju forbidden": `{"id":"t","rules":"renju","size":9,
			"coordinateSystem":"board-row-column",
			"openings":[{"id":"a","minPrefixLength":8,"coordinates":[
				[3,5],[8,0],[4,5],[0,8],[5,3],[8,8],[5,4],[0,0],[5,5]]}]}`,
	}
	for name, body := range cases {
		path := writeBook(t, body)
		if err := bookValidate(path); err == nil {
			t.Errorf("%s: validate accepted an invalid book", name)
		}
	}
}

// TestBookValidateRenjuForbiddenShape: the renju failure case above is a real
// 3-3 — double-check the shape is forbidden in isolation so the validator's
// rejection is meaningful.
func TestBookValidateRenjuForbiddenShape(t *testing.T) {
	s := newSearcher(9, make([]int, 81), 0)
	s.setRule(RuleRenju, playerMe)
	for _, st := range []struct {
		x, y int
		side int
	}{{3, 5, playerMe}, {8, 0, playerOpp}, {4, 5, playerMe}, {0, 8, playerOpp},
		{5, 3, playerMe}, {8, 8, playerOpp}, {5, 4, playerMe}, {0, 0, playerOpp}} {
		s.setStone(st.y*9+st.x, st.side)
	}
	if !s.isForbidden(5*9+5, playerMe) {
		t.Fatal("shape did not produce a forbidden point; validator case is weak")
	}
}

// TestBookBuildEndToEnd: build a small book, validate it, load it back and
// confirm the compiler resolves the first position's reply.
func TestBookBuildEndToEnd(t *testing.T) {
	out := filepath.Join(t.TempDir(), "built.json")
	bookBuildForTest(out, 9, 4, 3, 2)

	if err := bookValidate(out); err != nil {
		t.Fatalf("built book failed validation: %v", err)
	}
	cb, err := loadBookFile(out)
	if err != nil {
		t.Fatalf("load built book: %v", err)
	}
	if len(cb.positions) == 0 {
		t.Fatal("built book has no positions")
	}
	// the first position (single center stone) must resolve to a reply
	stones := []bookStone{{4, 4, 1}}
	if cands := cb.movesFor(stones); len(cands) == 0 {
		t.Fatal("built book does not cover the opening move's reply")
	}
}

// bookBuildForTest runs the builder with test-friendly parameters.
func bookBuildForTest(out string, size, depth, ply, width int) {
	s := newSearcher(size, make([]int, size*size), 0)
	s.setRule(RuleFreestyle, playerMe)
	b := &bookBuilder{
		s:         s,
		depth:     depth,
		maxPly:    ply,
		width:     width,
		margin:    0,
		visited:   make(map[string]bool),
		emitted:   make(map[string]bool),
		budgetEnd: time.Now().Add(60 * time.Second),
	}
	b.walk(nil, nil)
	src := bookSourceJSON{
		ID:               "bango-built",
		Name:             "test built book",
		Source:           "self-search",
		Rules:            "freestyle",
		Size:             size,
		CoordinateSystem: "board-row-column",
		Openings:         b.openings,
	}
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		panic(err)
	}
}
