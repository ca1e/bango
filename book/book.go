package book

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
//
// This package is the book data module: pure loading/compiling/querying with
// no search dependency, so the engine session (package main), the alphabeta
// algorithm and the book CLI tooling can all share it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Protocol rule codes, mirroring the INFO rule bitmask (see the alphabeta
// package for the full set). Books for other rules are rejected outright.
const (
	RuleFreestyle = 0
	RuleRenju     = 4
)

// Source mirrors the gobang opening source format (one JSON object; a file
// may also hold an array of sources).
type Source struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Source           string    `json:"source"`
	License          string    `json:"license"`
	Rules            string    `json:"rules"`
	Size             int       `json:"size"`
	CoordinateSystem string    `json:"coordinateSystem"`
	Openings         []Opening `json:"openings"`
}

// Opening is one numbered move line.
type Opening struct {
	ID          string  `json:"id"`
	MinPrefix   int     `json:"minPrefixLength,omitempty"`
	Coordinates [][]int `json:"coordinates"`
}

// Stone is one stone with role encoded relative to black: +1 black, -1
// white. Encoding relative to the first mover makes the book usable no
// matter which internal color owns black.
type Stone struct {
	X, Y int
	Role int
}

// Candidate is a book reply: a flat board index (y*size+x) and its weight.
type Candidate struct {
	Move   int
	Weight int
}

// Compiled is the query-ready book: canonical position key → candidate move
// index → accumulated weight.
type Compiled struct {
	Rule int
	Size int

	name         string
	lines        int
	positions    map[string]map[int]int
	firstReplies [][2][2]int // per line: (first move, second move) for translation
}

// RuleCode maps the JSON "rules" field onto engine rule codes. Books for
// unmapped rules are rejected outright instead of guessed.
func RuleCode(rules string) (int, bool) {
	switch rules {
	case "freestyle":
		return RuleFreestyle, true
	case "renju":
		return RuleRenju, true
	}
	return 0, false
}

// Converter returns the coordinate conversion for a source's declared
// coordinate system. Note the 8-symmetry group contains the transpose, so a
// consistent row/column interpretation is absorbed by canonicalization.
func Converter(system string) (func(x, y, size int) (int, int), error) {
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

// TransformPoint applies one of the 8 board symmetries (identity, three
// rotations, four mirrors) — identical numbering to the gobang original.
func TransformPoint(x, y, size, t int) (int, int) {
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

// PositionKey canonicalizes a stone set: among the 8 symmetries the
// lexicographically smallest encoding wins; the returned transform list
// holds every symmetry achieving it (the position's automorphism group).
func PositionKey(stones []Stone, size int) (key string, transforms []int) {
	best := ""
	for t := 0; t < 8; t++ {
		parts := make([]string, 0, len(stones))
		for _, st := range stones {
			tx, ty := TransformPoint(st.X, st.Y, size, t)
			parts = append(parts, fmt.Sprintf("%d,%d,%d", tx, ty, st.Role))
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

// compile expands every opening line at every prefix length (from
// minPrefixLength) into candidate entries, distributing weight across the
// position's automorphic move variants.
func compile(src *Source) (*Compiled, error) {
	if src.Size < 5 {
		return nil, fmt.Errorf("book %q: unsupported size %d", src.ID, src.Size)
	}
	rule, ok := RuleCode(src.Rules)
	if !ok {
		return nil, fmt.Errorf("book %q: unsupported rules %q", src.ID, src.Rules)
	}
	conv, err := Converter(src.CoordinateSystem)
	if err != nil {
		return nil, fmt.Errorf("book %q: %v", src.ID, err)
	}
	cb := &Compiled{
		Rule:      rule,
		Size:      src.Size,
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
		moves := make([]Stone, 0, len(op.Coordinates))
		for _, c := range op.Coordinates {
			if len(c) != 2 {
				return nil, fmt.Errorf("book %q opening %q: bad coordinate %v", src.ID, op.ID, c)
			}
			x, y := conv(c[0], c[1], src.Size)
			if x < 0 || y < 0 || x >= src.Size || y >= src.Size {
				return nil, fmt.Errorf("book %q opening %q: out-of-range coordinate %v", src.ID, op.ID, c)
			}
			moves = append(moves, Stone{X: x, Y: y, Role: 1})
			if len(moves)%2 == 0 {
				moves[len(moves)-1].Role = -1
			}
		}
		cb.lines++
		if minPrefix <= 1 {
			cb.firstReplies = append(cb.firstReplies,
				[2][2]int{{moves[0].X, moves[0].Y}, {moves[1].X, moves[1].Y}})
		}
		for pl := minPrefix; pl < len(moves); pl++ {
			key, transforms := PositionKey(moves[:pl], src.Size)
			weights := cb.positions[key]
			if weights == nil {
				weights = make(map[int]int)
				cb.positions[key] = weights
			}
			mv := moves[pl]
			for _, t := range transforms {
				tx, ty := TransformPoint(mv.X, mv.Y, src.Size, t)
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
func (cb *Compiled) merge(other *Compiled) error {
	if other.Rule != cb.Rule || other.Size != cb.Size {
		return fmt.Errorf("cannot merge book %q (rule %d size %d) into %q (rule %d size %d)",
			other.name, other.Rule, other.Size, cb.name, cb.Rule, cb.Size)
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

// PositionReplies returns the raw reply-weight map (move index → weight,
// canonical x-major indices) for an exact canonical position key, e.g. one
// produced by PositionKey. Callers that need query-frame candidates should
// prefer MovesFor.
func (cb *Compiled) PositionReplies(key string) map[int]int {
	return cb.positions[key]
}

// InverseTransform applies the inverse of the symmetry t: mapping a point
// transformed with t back to its original coordinates.
func InverseTransform(x, y, size, t int) (int, int) {
	return TransformPoint(x, y, size, bookInverseTransforms[t])
}

// MovesFor returns the book candidates for a position, inverse-transformed
// into the query's coordinate frame and sorted by weight (best first).
func (cb *Compiled) MovesFor(stones []Stone) []Candidate {
	key, transforms := PositionKey(stones, cb.Size)
	weights := cb.positions[key]
	if len(weights) == 0 {
		return nil
	}
	inv := bookInverseTransforms[transforms[0]]
	out := make([]Candidate, 0, len(weights))
	for mk, w := range weights {
		x, y := mk/cb.Size, mk%cb.Size
		tx, ty := TransformPoint(x, y, cb.Size, inv)
		out = append(out, Candidate{Move: ty*cb.Size + tx, Weight: w})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Move < out[j].Move
	})
	return out
}

// TranslatedFirstMoves applies every line's first→second move displacement
// at the actual first stone (the query position holds exactly one stone that
// is not itself covered by the book).
func (cb *Compiled) TranslatedFirstMoves(ax, ay int) []Candidate {
	weights := make(map[int]int)
	for _, pr := range cb.firstReplies {
		for t := 0; t < 8; t++ {
			fx, fy := TransformPoint(pr[0][0], pr[0][1], cb.Size, t)
			sx, sy := TransformPoint(pr[1][0], pr[1][1], cb.Size, t)
			mx, my := ax+sx-fx, ay+sy-fy
			if mx < 0 || my < 0 || mx >= cb.Size || my >= cb.Size {
				continue
			}
			weights[mx*cb.Size+my]++
		}
	}
	out := make([]Candidate, 0, len(weights))
	for mk, w := range weights {
		out = append(out, Candidate{Move: mk, Weight: w})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Move < out[j].Move
	})
	return out
}

// LoadFile reads a book file: either a single source object or an array of
// sources (merged; rule and size must agree across entries).
func LoadFile(path string) (*Compiled, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var single Source
	if err := json.Unmarshal(data, &single); err == nil && single.Size > 0 {
		return compile(&single)
	}
	var many []Source
	if err := json.Unmarshal(data, &many); err == nil && len(many) > 0 {
		var merged *Compiled
		for i := range many {
			cb, err := compile(&many[i])
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

// defaultBookFile is the shipped adoption book loaded when no book.json
// exists at the protocol locations: the Gomocup 2026 freestyle 15×15
// openings (tournament-grade lines — safe to adopt outright).
const defaultBookFile = "gomocup-2026-15x15.json"

// modeBookFile is the classic 26-mode guidance book (opening theory zone):
// engine-grown continuations feed the opening prior only.
const modeBookFile = "classic-26-15x15.json"

// LoadModeBook compiles the classic mode guidance book; nil when missing or
// incompatible.
func LoadModeBook() *Compiled {
	for _, path := range FindDefaultBookPaths([]string{modeBookFile}) {
		if cb, err := LoadFile(path); err == nil {
			return cb
		}
	}
	return nil
}

// FindBookPath locates a manager/legacy single-book file: the protocol
// INFO folder's pbrain-bango/book.json first, then the executable's own
// directory. Empty when neither exists (the shipped openbook defaults apply).
func FindBookPath(folder string) string {
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

// FindDefaultBookPaths returns the shipped openbook files (from names) that
// exist: executable-directory copies first, then the working directory and
// its ancestors (covers `go run .`, whose executable lives in a temp dir,
// and tests running from nested package directories).
func FindDefaultBookPaths(names []string) []string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, f := range names {
			candidates = append(candidates, filepath.Join(dir, "openbook", f))
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for level := 0; level < 6; level++ {
			for _, f := range names {
				candidates = append(candidates, filepath.Join(dir, "openbook", f))
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	seen := make(map[string]bool)
	var out []string
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// LoadDefaultBooks compiles and merges the shipped openbook defaults; a file
// whose rule or size is incompatible is skipped silently (the whole-book
// disable on mismatch, matching the single-file behaviour).
func LoadDefaultBooks() *Compiled {
	var merged *Compiled
	for _, path := range FindDefaultBookPaths([]string{defaultBookFile}) {
		cb, err := LoadFile(path)
		if err != nil {
			continue
		}
		if merged == nil {
			merged = cb
			continue
		}
		if err := merged.merge(cb); err != nil {
			continue // incompatible rule/size: leave this file out
		}
	}
	return merged
}
