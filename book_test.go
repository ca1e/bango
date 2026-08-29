package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// lineSource builds a single-source JSON book from a literal body.
func lineSource(t *testing.T, body string) *bookSourceJSON {
	t.Helper()
	var src bookSourceJSON
	if err := json.Unmarshal([]byte(body), &src); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &src
}

// TestBookCompileAndQuery: a line compiles into per-prefix positions and the
// query returns the continuation, inverse-transformed to the query frame.
func TestBookCompileAndQuery(t *testing.T) {
	src := lineSource(t, `{
		"id": "t", "rules": "freestyle", "size": 9,
		"coordinateSystem": "board-row-column",
		"openings": [{"id": "a", "coordinates": [[4,4],[6,3],[4,5],[6,6],[3,3]]}]
	}`)
	cb, err := compileBook(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cb.lines != 1 {
		t.Fatalf("lines = %d, want 1", cb.lines)
	}

	// position after two stones (black (4,4), white (6,3)) → third move (4,5);
	// the pair has no board symmetry, so exactly one candidate exists
	stones := []bookStone{{4, 4, 1}, {6, 3, -1}}
	cands := cb.movesFor(stones)
	if len(cands) != 1 || cands[0].move != 5*9+4 {
		t.Fatalf("cands = %v, want move (4,5)", cands)
	}
	// after three stones → fourth move
	stones = append(stones, bookStone{4, 5, 1})
	cands = cb.movesFor(stones)
	if len(cands) != 1 || cands[0].move != 6*9+6 {
		t.Fatalf("cands = %v, want move (6,6)", cands)
	}
	// unknown position → no candidates
	if got := cb.movesFor([]bookStone{{0, 0, 1}, {1, 1, -1}}); len(got) != 0 {
		t.Fatalf("unknown position returned %v", got)
	}
}

// TestBookSymmetryLookup: the same position in any of the 8 symmetries
// resolves to the equivalent move in that frame.
func TestBookSymmetryLookup(t *testing.T) {
	src := lineSource(t, `{
		"id": "t", "rules": "freestyle", "size": 9,
		"coordinateSystem": "board-row-column",
		"openings": [{"id": "a", "coordinates": [[4,4],[4,3],[4,5],[3,4],[5,4]]}]
	}`)
	cb, err := compileBook(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base := []bookStone{{4, 4, 1}, {4, 3, -1}}
	baseCands := cb.movesFor(base)
	// the position is x-mirror symmetric, so the reply's automorphic variants
	// coincide on one cell and accumulate weight 2
	if len(baseCands) != 1 || baseCands[0].move != 5*9+4 || baseCands[0].weight != 2 {
		t.Fatalf("base candidate = %v, want single (4,5) with weight 2", baseCands)
	}
	for tIdx := 0; tIdx < 8; tIdx++ {
		symmetric := make([]bookStone, len(base))
		for i, st := range base {
			x, y := transformPoint(st.x, st.y, 9, tIdx)
			symmetric[i] = bookStone{x, y, st.role}
		}
		// the reply cell expressed in the rotated frame
		ex, ey := transformPoint(4, 5, 9, tIdx)
		want := ey*9 + ex
		cands := cb.movesFor(symmetric)
		if len(cands) != 1 || cands[0].move != want || cands[0].weight != 2 {
			t.Fatalf("transform %d: cands = %v, want single move %d with weight 2", tIdx, cands, want)
		}
	}
}

// TestBookWeightAccumulation: two sources contributing the same reply
// double the weight; the merged array file works.
func TestBookWeightAccumulation(t *testing.T) {
	srcA := `{"id":"a","rules":"freestyle","size":9,"coordinateSystem":"board-row-column",
		"openings":[{"id":"a1","coordinates":[[4,4],[6,3],[4,5]]}]}`
	srcB := `{"id":"b","rules":"freestyle","size":9,"coordinateSystem":"board-row-column",
		"openings":[{"id":"b0","coordinates":[[2,2],[3,3],[5,5]]},
		            {"id":"b1","coordinates":[[4,4],[6,3],[4,5]]}]}`
	merged := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(merged, []byte("["+srcA+","+srcB+"]"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := loadBookFile(merged)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cands := a.movesFor([]bookStone{{4, 4, 1}, {6, 3, -1}})
	if len(cands) != 1 || cands[0].weight != 2 || cands[0].move != 5*9+4 {
		t.Fatalf("cands = %v, want one candidate (4,5) with weight 2", cands)
	}
}

// TestBookSideEncodingRejected: same stones with colors swapped must not hit
// the book — the reply belongs to the other color.
func TestBookSideEncodingRejected(t *testing.T) {
	src := lineSource(t, `{
		"id": "t", "rules": "freestyle", "size": 9,
		"coordinateSystem": "board-row-column",
		"openings": [{"id": "a", "coordinates": [[4,4],[5,4],[4,5]]}]
	}`)
	cb, err := compileBook(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := cb.movesFor([]bookStone{{4, 4, -1}, {5, 4, 1}}); len(got) != 0 {
		t.Fatalf("color-swapped position hit the book: %v", got)
	}
}

// TestBookTranslatedFirstMoves: with one stone that the book does not cover,
// every line's first→second displacement is offered at that stone.
func TestBookTranslatedFirstMoves(t *testing.T) {
	src := lineSource(t, `{
		"id": "t", "rules": "freestyle", "size": 9,
		"coordinateSystem": "board-row-column",
		"openings": [{"id": "a", "coordinates": [[4,4],[5,4],[4,5],[6,6]]}]
	}`)
	cb, err := compileBook(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// displacement (4,4)→(5,4) is (+1,0); anchored at (2,2) the reply is (3,2)
	cands := cb.translatedFirstMoves(2, 2)
	if len(cands) == 0 {
		t.Fatal("no translated replies")
	}
	found := false
	for _, c := range cands {
		if c.move == 2*9+3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("translated reply (3,2) missing in %v", cands)
	}
	// a lone center stone matches the book's first-stone position; the reply
	// spreads over its automorphic variants (four orthogonal neighbours,
	// weight 2 each — the two folds of each direction)
	got := cb.movesFor([]bookStone{{4, 4, 1}})
	if len(got) != 4 {
		t.Fatalf("center-stone lookup = %v, want 4 neighbour candidates", got)
	}
	for _, c := range got {
		if c.weight != 2 {
			t.Fatalf("candidate %v has weight %d, want 2", c, c.weight)
		}
	}
}

// TestBookCenterRelativeConversion: the Gomocup site's center-relative
// coordinates land on the expected board cells; the compiled book resolves.
func TestBookCenterRelativeConversion(t *testing.T) {
	src := lineSource(t, `{
		"id": "t", "rules": "freestyle", "size": 15,
		"coordinateSystem": "center-relative-x-y",
		"openings": [{"id": "a", "coordinates": [[-2,1],[1,-2],[2,0]]}]
	}`)
	cb, err := compileBook(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// [-2,1] -> (6,5); [1,-2] -> (9,8); the reply [2,0] -> (7,9)
	cands := cb.movesFor([]bookStone{{6, 5, 1}, {9, 8, -1}})
	if len(cands) != 1 || cands[0].move != 9*15+7 {
		t.Fatalf("cands = %v, want (7,9)", cands)
	}
}

// TestBookRejectsUnsupported: bad rules / coordinate systems / sizes fail at
// compile time instead of silently producing a broken book.
func TestBookRejectsUnsupported(t *testing.T) {
	bad := []string{
		`{"id":"t","rules":"standard","size":9,"coordinateSystem":"board-row-column","openings":[{"id":"a","coordinates":[[4,4],[5,4]]}]}`,
		`{"id":"t","rules":"freestyle","size":9,"coordinateSystem":"polar","openings":[{"id":"a","coordinates":[[4,4],[5,4]]}]}`,
		`{"id":"t","rules":"freestyle","size":3,"coordinateSystem":"board-row-column","openings":[{"id":"a","coordinates":[[1,1],[2,1]]}]}`,
		`{"id":"t","rules":"freestyle","size":9,"coordinateSystem":"board-row-column","openings":[{"id":"a","coordinates":[[4,4],[99,4]]}]}`,
	}
	for _, body := range bad {
		src := lineSource(t, body)
		if _, err := compileBook(src); err == nil {
			t.Errorf("compile accepted bad source: %s", body)
		}
	}
}

// TestLoadBookFileAndPath: array files merge; findBookPath prefers the INFO
// folder's brain subfolder and falls back to the executable directory.
func TestLoadBookFileAndPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "book.json")
	body := `{"id":"t","rules":"renju","size":9,"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[4,4],[5,4],[4,5]]}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := loadBookFile(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cb.rule != RuleRenju || cb.size != 9 {
		t.Fatalf("rule/size = %d/%d", cb.rule, cb.size)
	}

	if got := findBookPath(""); got != "" {
		t.Fatalf("findBookPath without folder or exe-side file = %q", got)
	}
	sub := filepath.Join(dir, "pbrain-bango")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(sub, "book.json")
	if err := os.WriteFile(bookPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBookPath(dir); got != bookPath {
		t.Fatalf("findBookPath = %q, want %q", got, bookPath)
	}
}
