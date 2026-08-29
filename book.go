package main

// Opening book: a Go port of the tutorial project's opening-book module
// (gobang src/ai/opening-book), kept format-compatible so the same JSON
// sources (Gomocup official openings, Rapfi-verified books) load directly.
//
// Pipeline: source JSON (coordinate normalization) → prefix expansion over
// 8-fold symmetry canonicalization → "position → candidate move" map with
// weight accumulation. Queries are keyed by the stone set with colors
// encoded relative to black, so the engine needs no move history and
// BOARD-restored positions hit the book like played-out ones. The book's
// reply for a position with a single stone falls back to the translated
// first-reply generalization: every line's first→second move displacement
// is applied at the actual stone position.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bookSourceJSON mirrors the gobang opening source format.
type bookSourceJSON struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Source           string            `json:"source"`
	License          string            `json:"license"`
	Rules            string            `json:"rules"`
	Size             int               `json:"size"`
	CoordinateSystem string            `json:"coordinateSystem"`
	Openings         []bookOpeningJSON `json:"openings"`
}

type bookOpeningJSON struct {
	ID          string  `json:"id"`
	MinPrefix   int     `json:"minPrefixLength,omitempty"`
	Coordinates [][]int `json:"coordinates"`
}

// bookStone is one stone with role encoded relative to black: +1 black, -1
// white. Encoding relative to the first mover makes the book usable no
// matter which internal color (playerMe/playerOpp) black happens to be.
type bookStone struct {
	x, y int
	role int
}

type bookCandidate struct {
	move   int // board index y*size+x
	weight int
}

// compiledBook is the query-ready book: canonical position key → candidate
// move index → accumulated weight.
type compiledBook struct {
	rule         int
	size         int
	name         string
	lines        int
	positions    map[string]map[int]int
	firstReplies [][2][2]int // per line: (first move, second move) for translation
}

// bookRuleCode maps the JSON "rules" field onto engine rule codes. Books for
// unmapped rules are rejected outright instead of guessed.
func bookRuleCode(rules string) (int, bool) {
	switch rules {
	case "freestyle":
		return RuleFreestyle, true
	case "renju":
		return RuleRenju, true
	}
	return 0, false
}

// bookConverter returns the coordinate conversion for a source's declared
// coordinate system. Note the 8-symmetry group contains the transpose, so a
// consistent row/column interpretation is absorbed by canonicalization.
func bookConverter(system string) (func(x, y, size int) (int, int), error) {
	switch system {
	case "", "board-row-column":
		return func(x, y, size int) (int, int) { return x, y }, nil
	case "center-relative-x-y":
		return func(x, y, size int) (int, int) {
			c := size / 2
			return c - y, c + x
		}, nil
	}
	return nil, fmt.Errorf("unsupported coordinate system %q", system)
}

// transformPoint applies one of the 8 board symmetries (identity, three
// rotations, four mirrors) — identical numbering to the gobang original.
func transformPoint(x, y, size, t int) (int, int) {
	last := size - 1
	switch t {
	case 1:
		return y, last - x
	case 2:
		return last - x, last - y
	case 3:
		return last - y, x
	case 4:
		return x, last - y
	case 5:
		return last - x, y
	case 6:
		return y, x
	case 7:
		return last - y, last - x
	default:
		return x, y
	}
}

var bookInverseTransforms = [8]int{0, 3, 2, 1, 4, 5, 6, 7}

// bookPositionKey canonicalizes a stone set: among the 8 symmetries the
// lexicographically smallest encoding wins; the returned transform list
// holds every symmetry achieving it (the position's automorphism group).
func bookPositionKey(stones []bookStone, size int) (key string, transforms []int) {
	best := ""
	for t := 0; t < 8; t++ {
		parts := make([]string, 0, len(stones))
		for _, st := range stones {
			tx, ty := transformPoint(st.x, st.y, size, t)
			parts = append(parts, fmt.Sprintf("%d,%d,%d", tx, ty, st.role))
		}
		sort.Strings(parts)
		k := fmt.Sprintf("%d|%s", size, strings.Join(parts, ";"))
		if best == "" || k < best {
			best = k
			transforms = []int{t}
		} else if k == best {
			transforms = append(transforms, t)
		}
	}
	return best, transforms
}

// compileBook expands every opening line at every prefix length (from
// minPrefixLength) into candidate entries, distributing weight across the
// position's automorphic move variants.
func compileBook(src *bookSourceJSON) (*compiledBook, error) {
	if src.Size < 5 {
		return nil, fmt.Errorf("book %q: unsupported size %d", src.ID, src.Size)
	}
	rule, ok := bookRuleCode(src.Rules)
	if !ok {
		return nil, fmt.Errorf("book %q: unsupported rules %q", src.ID, src.Rules)
	}
	conv, err := bookConverter(src.CoordinateSystem)
	if err != nil {
		return nil, fmt.Errorf("book %q: %v", src.ID, err)
	}
	cb := &compiledBook{
		rule:      rule,
		size:      src.Size,
		name:      src.Name,
		positions: make(map[string]map[int]int),
	}
	for _, op := range src.Openings {
		if len(op.Coordinates) < 2 {
			continue // need at least a move and a reply to be usable
		}
		minPrefix := op.MinPrefix
		if minPrefix < 1 {
			minPrefix = 1
		}
		moves := make([]bookStone, 0, len(op.Coordinates))
		for _, c := range op.Coordinates {
			if len(c) != 2 {
				return nil, fmt.Errorf("book %q opening %q: bad coordinate %v", src.ID, op.ID, c)
			}
			x, y := conv(c[0], c[1], src.Size)
			if x < 0 || y < 0 || x >= src.Size || y >= src.Size {
				return nil, fmt.Errorf("book %q opening %q: out-of-range coordinate %v", src.ID, op.ID, c)
			}
			moves = append(moves, bookStone{x, y, 1})
			if len(moves)%2 == 0 {
				moves[len(moves)-1].role = -1
			}
		}
		cb.lines++
		if minPrefix <= 1 {
			cb.firstReplies = append(cb.firstReplies,
				[2][2]int{{moves[0].x, moves[0].y}, {moves[1].x, moves[1].y}})
		}
		for pl := minPrefix; pl < len(moves); pl++ {
			key, transforms := bookPositionKey(moves[:pl], src.Size)
			weights := cb.positions[key]
			if weights == nil {
				weights = make(map[int]int)
				cb.positions[key] = weights
			}
			mv := moves[pl]
			for _, t := range transforms {
				tx, ty := transformPoint(mv.x, mv.y, src.Size, t)
				weights[tx*src.Size+ty]++
			}
		}
	}
	if len(cb.positions) == 0 {
		return nil, fmt.Errorf("book %q: no usable openings", src.ID)
	}
	return cb, nil
}

// merge folds another compiled book of the same rule/size into cb.
func (cb *compiledBook) merge(other *compiledBook) error {
	if other.rule != cb.rule || other.size != cb.size {
		return fmt.Errorf("cannot merge book %q (rule %d size %d) into %q (rule %d size %d)",
			other.name, other.rule, other.size, cb.name, cb.rule, cb.size)
	}
	for key, weights := range other.positions {
		dst := cb.positions[key]
		if dst == nil {
			dst = make(map[int]int)
			cb.positions[key] = dst
		}
		for mk, w := range weights {
			dst[mk] += w
		}
	}
	cb.firstReplies = append(cb.firstReplies, other.firstReplies...)
	cb.lines += other.lines
	return nil
}

// movesFor returns the book candidates for a position, inverse-transformed
// into the query's coordinate frame and sorted by weight (best first).
func (cb *compiledBook) movesFor(stones []bookStone) []bookCandidate {
	key, transforms := bookPositionKey(stones, cb.size)
	weights := cb.positions[key]
	if len(weights) == 0 {
		return nil
	}
	inv := bookInverseTransforms[transforms[0]]
	out := make([]bookCandidate, 0, len(weights))
	for mk, w := range weights {
		x, y := mk/cb.size, mk%cb.size
		tx, ty := transformPoint(x, y, cb.size, inv)
		out = append(out, bookCandidate{move: ty*cb.size + tx, weight: w})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].weight != out[j].weight {
			return out[i].weight > out[j].weight
		}
		return out[i].move < out[j].move
	})
	return out
}

// translatedFirstMoves applies every line's first→second move displacement
// at the actual first stone (the query position holds exactly one stone that
// is not itself covered by the book).
func (cb *compiledBook) translatedFirstMoves(ax, ay int) []bookCandidate {
	weights := make(map[int]int)
	for _, pr := range cb.firstReplies {
		for t := 0; t < 8; t++ {
			fx, fy := transformPoint(pr[0][0], pr[0][1], cb.size, t)
			sx, sy := transformPoint(pr[1][0], pr[1][1], cb.size, t)
			mx, my := ax+sx-fx, ay+sy-fy
			if mx < 0 || my < 0 || mx >= cb.size || my >= cb.size {
				continue
			}
			weights[mx*cb.size+my]++
		}
	}
	out := make([]bookCandidate, 0, len(weights))
	for mk, w := range weights {
		out = append(out, bookCandidate{move: mk, weight: w})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].weight != out[j].weight {
			return out[i].weight > out[j].weight
		}
		return out[i].move < out[j].move
	})
	return out
}

// loadBookFile reads a book file: either a single source object or an array
// of sources (merged; rule and size must agree across entries).
func loadBookFile(path string) (*compiledBook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var single bookSourceJSON
	if err := json.Unmarshal(data, &single); err == nil && single.Size > 0 {
		return compileBook(&single)
	}
	var many []bookSourceJSON
	if err := json.Unmarshal(data, &many); err == nil && len(many) > 0 {
		var merged *compiledBook
		for i := range many {
			cb, err := compileBook(&many[i])
			if err != nil {
				return nil, err
			}
			if merged == nil {
				merged = cb
				continue
			}
			if err := merged.merge(cb); err != nil {
				return nil, err
			}
		}
		return merged, nil
	}
	return nil, fmt.Errorf("unrecognized book format in %s", path)
}

// findBookPath locates the book file: protocol-compliant INFO folder first
// (the brain must use its own subfolder there), then the executable's
// directory as a read-only fallback. Empty when no file exists.
func findBookPath(folder string) string {
	var candidates []string
	if folder != "" {
		candidates = append(candidates, filepath.Join(folder, "pbrain-bango", "book.json"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "book.json"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
