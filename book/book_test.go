package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lineSource builds a single-source JSON book from a literal body.
func lineSource(t *testing.T, body string) *Source {
	t.Helper()
	var src Source
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
	cb, err := compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cb.lines != 1 {
		t.Fatalf("lines = %d, want 1", cb.lines)
	}

	// position after two stones (black (4,4), white (6,3)) → third move (4,5);
	// the pair has no board symmetry, so exactly one candidate exists
	stones := []Stone{{4, 4, 1}, {6, 3, -1}}
	cands := cb.MovesFor(stones)
	if len(cands) != 1 || cands[0].Move != 5*9+4 {
		t.Fatalf("cands = %v, want move (4,5)", cands)
	}
	// after three stones → fourth move
	stones = append(stones, Stone{4, 5, 1})
	cands = cb.MovesFor(stones)
	if len(cands) != 1 || cands[0].Move != 6*9+6 {
		t.Fatalf("cands = %v, want move (6,6)", cands)
	}
	// unknown position → no candidates
	if got := cb.MovesFor([]Stone{{0, 0, 1}, {1, 1, -1}}); len(got) != 0 {
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
	cb, err := compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base := []Stone{{4, 4, 1}, {4, 3, -1}}
	baseCands := cb.MovesFor(base)
	// the position is x-mirror symmetric, so the reply's automorphic variants
	// coincide on one cell and accumulate weight 2
	if len(baseCands) != 1 || baseCands[0].Move != 5*9+4 || baseCands[0].Weight != 2 {
		t.Fatalf("base candidate = %v, want single (4,5) with weight 2", baseCands)
	}
	for tIdx := 0; tIdx < 8; tIdx++ {
		symmetric := make([]Stone, len(base))
		for i, st := range base {
			x, y := TransformPoint(st.X, st.Y, 9, tIdx)
			symmetric[i] = Stone{x, y, st.Role}
		}
		// the reply cell expressed in the rotated frame
		ex, ey := TransformPoint(4, 5, 9, tIdx)
		want := ey*9 + ex
		cands := cb.MovesFor(symmetric)
		if len(cands) != 1 || cands[0].Move != want || cands[0].Weight != 2 {
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
	a, err := LoadFile(merged)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cands := a.MovesFor([]Stone{{4, 4, 1}, {6, 3, -1}})
	if len(cands) != 1 || cands[0].Weight != 2 || cands[0].Move != 5*9+4 {
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
	cb, err := compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := cb.MovesFor([]Stone{{4, 4, -1}, {5, 4, 1}}); len(got) != 0 {
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
	cb, err := compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// displacement (4,4)→(5,4) is (+1,0); anchored at (2,2) the reply is (3,2)
	cands := cb.TranslatedFirstMoves(2, 2)
	if len(cands) == 0 {
		t.Fatal("no translated replies")
	}
	found := false
	for _, c := range cands {
		if c.Move == 2*9+3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("translated reply (3,2) missing in %v", cands)
	}
	// a lone center stone matches the book's first-stone position; the reply
	// spreads over its automorphic variants (four orthogonal neighbours,
	// weight 2 each — the two folds of each direction)
	got := cb.MovesFor([]Stone{{4, 4, 1}})
	if len(got) != 4 {
		t.Fatalf("center-stone lookup = %v, want 4 neighbour candidates", got)
	}
	for _, c := range got {
		if c.Weight != 2 {
			t.Fatalf("candidate %v has weight %d, want 2", c, c.Weight)
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
	cb, err := compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// [-2,1] -> (6,5); [1,-2] -> (9,8); the reply [2,0] -> (7,9)
	cands := cb.MovesFor([]Stone{{6, 5, 1}, {9, 8, -1}})
	if len(cands) != 1 || cands[0].Move != 9*15+7 {
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
		if _, err := compile(src); err == nil {
			t.Errorf("compile accepted bad source: %s", body)
		}
	}
}

// TestLoadBookFileAndPath: array files merge; FindBookPath prefers the INFO
// folder's brain subfolder and falls back to the executable directory.
func TestLoadBookFileAndPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "book.json")
	body := `{"id":"t","rules":"renju","size":9,"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[4,4],[5,4],[4,5]]}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cb, err := LoadFile(file)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cb.Rule != RuleRenju || cb.Size != 9 {
		t.Fatalf("rule/size = %d/%d", cb.Rule, cb.Size)
	}

	// with the repository's openbook/ default present the cwd fallback finds
	// it, so the empty result only holds from a directory without one
	if wd, err := os.Getwd(); err == nil {
		defer os.Chdir(wd)
		empty := t.TempDir()
		if err := os.Chdir(empty); err != nil {
			t.Fatal(err)
		}
		if got := FindBookPath(""); got != "" {
			t.Fatalf("FindBookPath without any candidate file = %q", got)
		}
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(dir, "pbrain-bango")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(sub, "book.json")
	if err := os.WriteFile(bookPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindBookPath(dir); got != bookPath {
		t.Fatalf("FindBookPath = %q, want %q", got, bookPath)
	}
}

// TestFindDefaultBookPaths: the shipped openbook defaults are found relative
// to the working directory (covers `go run .`, whose executable lives in a
// temp dir); FindBookPath itself no longer looks at openbook.
func TestFindDefaultBookPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "openbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openbook", defaultBookFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := FindBookPath(""); got != "" {
		t.Fatalf("FindBookPath = %q, want empty (openbook is the defaults' job)", got)
	}
	paths := FindDefaultBookPaths([]string{defaultBookFile})
	if len(paths) != 1 || !strings.HasSuffix(paths[0], filepath.Join("openbook", defaultBookFile)) {
		t.Fatalf("FindDefaultBookPaths = %v, want the one existing openbook file", paths)
	}
}

// TestDefaultBookShippedAndLoads: the repository's openbook default exists,
// compiles, and matches the engine's default rule (freestyle 15×15).
func TestDefaultBookShippedAndLoads(t *testing.T) {
	cb := LoadDefaultBooks()
	if cb == nil {
		t.Fatal("default openbook did not load from the package directory")
	}
	if cb.Rule != RuleFreestyle || cb.Size != 15 {
		t.Fatalf("default book rule/size = %d/%d, want freestyle/15", cb.Rule, cb.Size)
	}
	if len(cb.positions) == 0 {
		t.Fatal("default book compiled to zero positions")
	}
}
