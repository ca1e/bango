package main

// Opening book maintenance subcommands:
//
//	pbrain-bango book validate <file>   — sanity-check a book source file
//	pbrain-bango book build [flags]     — grow a book by fixed-depth search
//
// validate performs the same checks as the gobang project's
// validate-openings script: coordinates in range, no duplicate moves inside
// a line, no five completed anywhere in the final position, renju-forbidden
// black moves rejected during progressive placement, and no duplicate
// canonical positions across lines.
//
// build grows a book from the empty board: at every position the top
// candidates are scored with a fixed-depth search (deterministic — no wall
// clock deadline inside the search), the best ones within a margin are kept
// and the walk recurses. Positions are deduplicated by their canonical key,
// so transpositions are covered once.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// runBookCommand dispatches `pbrain-bango book ...`.
func runBookCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pbrain-bango book validate <file> | book build [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "validate":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: pbrain-bango book validate <file>")
			os.Exit(2)
		}
		if err := bookValidate(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "validate failed:", err)
			os.Exit(1)
		}
	case "build":
		bookBuild(args[1:])
	case "gen-classic":
		bookGenClassic(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown book command %q\n", args[0])
		os.Exit(2)
	}
}

// loadBookSources parses a book file into raw sources (object or array form).
func loadBookSources(path string) ([]bookSourceJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var single bookSourceJSON
	if err := json.Unmarshal(data, &single); err == nil && single.Size > 0 {
		return []bookSourceJSON{single}, nil
	}
	var many []bookSourceJSON
	if err := json.Unmarshal(data, &many); err == nil && len(many) > 0 {
		return many, nil
	}
	return nil, fmt.Errorf("unrecognized book format in %s", path)
}

// bookValidate checks one book file and prints a JSON summary on success.
func bookValidate(path string) error {
	sources, err := loadBookSources(path)
	if err != nil {
		return err
	}
	seenPositions := make(map[string]bool)
	summary := struct {
		File       string `json:"file"`
		Sources    int    `json:"sources"`
		Openings   int    `json:"openings"`
		Positions  int    `json:"positions"`
		Validation string `json:"validation"`
	}{File: path, Sources: len(sources), Validation: "pass"}

	for si := range sources {
		src := &sources[si]
		rule, ok := bookRuleCode(src.Rules)
		if !ok {
			return fmt.Errorf("%s: source %q: unsupported rules %q", path, src.ID, src.Rules)
		}
		conv, err := bookConverter(src.CoordinateSystem)
		if err != nil {
			return fmt.Errorf("%s: source %q: %v", path, src.ID, err)
		}
		if src.Size < 5 {
			return fmt.Errorf("%s: source %q: unsupported size %d", path, src.ID, src.Size)
		}
		s := newSearcher(src.Size, make([]int, src.Size*src.Size), 0)
		s.setRule(rule, playerMe)

		summary.Openings += len(src.Openings)
		for _, op := range src.Openings {
			line := make([]bookStone, 0, len(op.Coordinates))
			occupied := make(map[int]bool)
			for _, c := range op.Coordinates {
				if len(c) != 2 {
					return fmt.Errorf("%s: opening %q: bad coordinate %v", path, op.ID, c)
				}
				x, y := conv(c[0], c[1], src.Size)
				if x < 0 || y < 0 || x >= src.Size || y >= src.Size {
					return fmt.Errorf("%s: opening %q: out-of-range coordinate %v", path, op.ID, c)
				}
				p := y*src.Size + x
				if occupied[p] {
					return fmt.Errorf("%s: opening %q: duplicate move (%d,%d)", path, op.ID, x, y)
				}
				occupied[p] = true
				side := playerMe
				if len(line)%2 == 1 {
					side = playerOpp
				}
				// renju: black may not fill a forbidden point mid-line
				if side == s.blackSide && s.isForbidden(p, s.blackSide) {
					return fmt.Errorf("%s: opening %q: forbidden black move at (%d,%d)", path, op.ID, x, y)
				}
				if s.winsMove(p, side) {
					return fmt.Errorf("%s: opening %q: five completed at (%d,%d)", path, op.ID, x, y)
				}
				s.makeMove(p, side)
				line = append(line, bookStone{x, y, 1})
				if len(line)%2 == 0 {
					line[len(line)-1].role = -1
				}
			}
			key, _ := bookPositionKey(line, src.Size)
			if seenPositions[key] {
				return fmt.Errorf("%s: opening %q: duplicate position", path, op.ID)
			}
			seenPositions[key] = true
			summary.Positions++
			// clear the board: lines must validate independently, not on the
			// leftovers of the previous opening
			for _, st := range line {
				s.setStone(st.y*src.Size+st.x, 0)
			}
		}
	}
	out, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// bookGenClassic emits the classic 26-mode seed lines (tengen + white reply
// family × black's second move symmetry classes) as a book source file for
// `book build --seeds`.
func bookGenClassic(args []string) {
	fs := flag.NewFlagSet("gen-classic", flag.ExitOnError)
	out := fs.String("out", "classic-26-seeds.json", "output seed book file")
	size := fs.Int("size", 15, "board size")
	fs.Parse(args)
	if *size < 5 {
		fmt.Fprintln(os.Stderr, "book gen-classic: invalid size")
		os.Exit(2)
	}
	seeds := classicOpeningSeeds(*size)
	src := bookSourceJSON{
		ID:               "bango-classic",
		Name:             fmt.Sprintf("classic opening modes, %d seed lines (size %d)", len(seeds), *size),
		Source:           "symmetry enumeration (5x5 box around the tengen)",
		Rules:            "freestyle",
		Size:             *size,
		CoordinateSystem: "board-row-column",
	}
	for i, s := range seeds {
		src.Openings = append(src.Openings, bookOpeningJSON{
			ID: fmt.Sprintf("classic-%03d", i+1),
			Coordinates: [][]int{
				{s[0], s[1]}, {s[2], s[3]}, {s[4], s[5]},
			},
		})
	}
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "book gen-classic:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "book gen-classic:", err)
		os.Exit(1)
	}
	fmt.Printf("gen-classic: %d mode seeds -> %s\n", len(seeds), *out)
}

// bookBuild grows a book via fixed-depth search — from the empty board, or
// from the prefixes of a seed book (--seeds), e.g. the classic 26-mode lines.
func bookBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("out", "book-built.json", "output book file")
	rule := fs.Int("rule", RuleFreestyle, "rule code (0 freestyle, 4 renju)")
	size := fs.Int("size", 15, "board size")
	depth := fs.Int("depth", 8, "fixed search depth for scoring candidates")
	maxPly := fs.Int("ply", 4, "opening depth in plies (total, including seeds)")
	width := fs.Int("width", 2, "candidates kept per position")
	margin := fs.Int("margin", 0, "keep candidates scoring within this margin of the best")
	budget := fs.Int("time", 60000, "total build budget in milliseconds")
	seeds := fs.String("seeds", "", "seed book file: grow continuations from each line")
	fs.Parse(args)

	if *size < 5 || *depth < 2 || *maxPly < 2 || *width < 1 {
		fmt.Fprintln(os.Stderr, "book build: invalid parameters")
		os.Exit(2)
	}
	rulesName := "freestyle"
	if *rule == RuleRenju {
		rulesName = "renju"
	}

	s := newSearcher(*size, make([]int, *size**size), 0)
	s.setRule(*rule, playerMe)
	b := &bookBuilder{
		s:         s,
		depth:     *depth,
		maxPly:    *maxPly,
		width:     *width,
		margin:    *margin,
		visited:   make(map[string]bool),
		emitted:   make(map[string]bool),
		budgetEnd: time.Now().Add(time.Duration(*budget) * time.Millisecond),
	}
	if *seeds != "" {
		if err := b.walkSeeds(*seeds, *size); err != nil {
			fmt.Fprintln(os.Stderr, "book build:", err)
			os.Exit(1)
		}
	} else {
		b.walk(nil, nil)
	}

	src := bookSourceJSON{
		ID:               "bango-built",
		Name:             fmt.Sprintf("pbrain-bango built opening book (rule %d, depth %d)", *rule, *depth),
		Source:           "self-search",
		Rules:            rulesName,
		Size:             *size,
		CoordinateSystem: "board-row-column",
		Openings:         b.openings,
	}
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "book build:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "book build:", err)
		os.Exit(1)
	}
	fmt.Printf("built %s: %d openings, %d positions visited\n", *out, len(b.openings), len(b.visited))
}

// walkSeeds runs the growth walk from every seed line's position. The seed
// stones are placed on the builder's board (and removed afterwards); the
// walk's own make/undo pairs handle the continuation.
func (b *bookBuilder) walkSeeds(path string, size int) error {
	srcs, err := loadBookSources(path)
	if err != nil {
		return err
	}
	placed := 0
	for si := range srcs {
		src := &srcs[si]
		if src.Size != size {
			return fmt.Errorf("%s: source %q: size %d, want %d", path, src.ID, src.Size, size)
		}
		rule, ok := bookRuleCode(src.Rules)
		if !ok || rule != b.s.rule {
			return fmt.Errorf("%s: source %q: rules %q does not match the build rule", path, src.ID, src.Rules)
		}
		conv, err := bookConverter(src.CoordinateSystem)
		if err != nil {
			return fmt.Errorf("%s: source %q: %v", path, src.ID, err)
		}
		for _, op := range src.Openings {
			var seq []bookStone
			var cellPath []int
			var placedCells []int
			bad := false
			for _, c := range op.Coordinates {
				if len(c) != 2 {
					return fmt.Errorf("%s: opening %q: bad coordinate %v", path, op.ID, c)
				}
				x, y := conv(c[0], c[1], size)
				p := y*size + x
				if b.s.b[p] != 0 {
					bad = true // overlapping seed: skip the line
					break
				}
				side := playerMe
				if len(cellPath)%2 == 1 {
					side = playerOpp
				}
				b.s.makeMove(p, side)
				placedCells = append(placedCells, p)
				seq = append(seq, bookStone{x, y, 1})
				if len(seq)%2 == 0 {
					seq[len(seq)-1].role = -1
				}
				cellPath = append(cellPath, p)
			}
			if !bad {
				b.walk(seq, cellPath)
			}
			for i := len(placedCells) - 1; i >= 0; i-- {
				b.s.setStone(placedCells[i], 0)
				placed++
			}
		}
	}
	if placed == 0 {
		return fmt.Errorf("%s: no seed lines walked", path)
	}
	return nil
}

// bookBuilder walks the opening tree, scoring candidates by fixed-depth
// search and keeping the near-best ones.
type bookBuilder struct {
	s         *searcher
	depth     int
	maxPly    int
	width     int
	margin    int
	visited   map[string]bool
	emitted   map[string]bool
	openings  []bookOpeningJSON
	budgetEnd time.Time
	counter   int
}

func (b *bookBuilder) walk(seq []bookStone, path []int) {
	key, _ := bookPositionKey(seq, b.s.n)
	if b.visited[key] {
		b.emit(path) // transposition: the continuation is covered elsewhere
		return
	}
	b.visited[key] = true
	if len(path) >= b.maxPly || time.Now().After(b.budgetEnd) {
		b.emit(path)
		return
	}

	side := playerMe
	if len(path)%2 == 1 {
		side = playerOpp
	}
	type scored struct {
		move  int
		value int
	}
	var cands []scored
	best := -1 << 60
	for _, m := range b.s.genMoves(b.width*3, side) {
		var value int
		if b.s.winsMove(m, side) {
			value = winScore
		} else {
			b.s.makeMove(m, side)
			// negamax returns the value for the child's side to move (3-side);
			// rank candidates from `side`'s own perspective
			v := b.s.negamax(b.depth-1, 3-side, -1<<60, 1<<60)
			b.s.undoMove(m, side)
			value = -v
		}
		if value > best {
			best = value
		}
		cands = append(cands, scored{m, value})
	}
	if len(cands) == 0 {
		b.emit(path)
		return
	}
	var kept []scored
	for _, c := range cands {
		if c.value >= best-b.margin {
			kept = append(kept, c)
		}
	}
	// strongest first, deterministic tie-break by cell index
	for i := 1; i < len(kept); i++ {
		for j := i; j > 0 && (kept[j].value > kept[j-1].value ||
			(kept[j].value == kept[j-1].value && kept[j].move < kept[j-1].move)); j-- {
			kept[j], kept[j-1] = kept[j-1], kept[j]
		}
	}
	if len(kept) > b.width {
		kept = kept[:b.width]
	}
	for _, k := range kept {
		if time.Now().After(b.budgetEnd) {
			b.emit(path)
			return
		}
		if b.s.winsMove(k.move, side) {
			// a completing five ends the opening: the line must end BEFORE
			// the winning move — an opening book never contains a five
			b.emit(path)
			continue
		}
		b.s.makeMove(k.move, side)
		st := bookStone{x: k.move % b.s.n, y: k.move / b.s.n, role: 1}
		if side != playerMe {
			st.role = -1
		}
		b.walk(append(seq, st), append(path, k.move))
		b.s.undoMove(k.move, side)
	}
}

// emit records a finished line (at least a move and a reply) unless an
// identical line was already recorded.
func (b *bookBuilder) emit(path []int) {
	if len(path) < 2 {
		return
	}
	line := make([]bookStone, len(path))
	coords := make([][]int, len(path))
	for i, p := range path {
		x, y := p%b.s.n, p/b.s.n
		role := 1
		if i%2 == 1 {
			role = -1
		}
		line[i] = bookStone{x, y, role}
		coords[i] = []int{x, y}
	}
	key, _ := bookPositionKey(line, b.s.n)
	if b.emitted[key] {
		return
	}
	b.emitted[key] = true
	b.counter++
	b.openings = append(b.openings, bookOpeningJSON{
		ID:          fmt.Sprintf("auto-%03d", b.counter),
		Coordinates: coords,
	})
}
