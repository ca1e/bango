package alphabeta

// Classic opening mode tests: enumeration, recognition, and the theory-zone
// prior projection.

import (
	"testing"

	"gomoku/book"
)

func TestClassicSeeds26Modes(t *testing.T) {
	seeds := classicOpeningSeeds(15)
	if len(seeds) != 26 {
		t.Fatalf("mode seeds = %d, want the classic 26", len(seeds))
	}
	direct, diag := 0, 0
	seen := make(map[string]bool)
	for _, s := range seeds {
		if s[0] != 7 || s[1] != 7 {
			t.Fatalf("seed %v does not open at the tengen", s)
		}
		dx, dy := s[2]-7, s[3]-7
		adx, ady := dx, dy
		if adx < 0 {
			adx = -adx
		}
		if ady < 0 {
			ady = -ady
		}
		switch {
		case adx+ady == 1:
			direct++
		case adx == 1 && ady == 1:
			diag++
		default:
			t.Fatalf("seed %v: white reply is neither direct nor diagonal", s)
		}
		stones := []book.Stone{{X: s[0], Y: s[1], Role: 1}, {X: s[2], Y: s[3], Role: -1}, {X: s[4], Y: s[5], Role: 1}}
		key, _ := book.PositionKey(stones, 15)
		if seen[key] {
			t.Fatalf("seed %v duplicates a canonical mode", s)
		}
		seen[key] = true
	}
	if direct != 13 || diag != 13 {
		t.Fatalf("families = %d direct / %d diag, want 13 / 13", direct, diag)
	}
}

func TestOpeningModeOfFrames(t *testing.T) {
	build := func(stones ...[3]int) *searcher {
		b := make([]int, 225)
		for _, st := range stones {
			b[st[1]*15+st[0]] = st[2]
		}
		s := newSearcher(15, b, 0)
		s.setRule(RuleFreestyle, playerOpp) // the human owns black
		return s
	}
	// the canonical 花月-like geometry: tengen + direct white + diagonal black2
	base := build([3]int{7, 7, playerOpp}, [3]int{7, 6, playerMe}, [3]int{8, 5, playerOpp})
	fr, ok := openingModeOf(base)
	if !ok {
		t.Fatal("canonical classic position not recognized")
	}
	// every symmetric twin of the same three-stone pattern must recognize to
	// the SAME mode id, with the frame mapping book cells back onto stones
	// adjacent to the actual position
	twins := [][3][3]int{
		{{7, 7, playerOpp}, {7, 6, playerMe}, {8, 5, playerOpp}},
		{{7, 7, playerOpp}, {7, 8, playerMe}, {6, 9, playerOpp}},
		{{7, 7, playerOpp}, {8, 7, playerMe}, {9, 6, playerOpp}},
		{{7, 7, playerOpp}, {6, 7, playerMe}, {5, 8, playerOpp}},
	}
	for i, tw := range twins {
		s := build(tw[0], tw[1], tw[2])
		fr2, ok := openingModeOf(s)
		if !ok {
			t.Fatalf("twin %d not recognized", i)
		}
		if fr2.id != fr.id {
			t.Fatalf("twin %d: mode id %q, want %q", i, fr2.id, fr.id)
		}
		if len(fr2.canon) != 3 {
			t.Fatalf("twin %d: canonical frame incomplete", i)
		}
	}
	// non-classic openings must not recognize
	negatives := [][][3]int{
		{{3, 3, playerOpp}, {4, 4, playerMe}, {2, 2, playerOpp}}, // non-tengen black1
		{{7, 7, playerOpp}, {2, 2, playerMe}, {8, 8, playerOpp}}, // remote white reply
		{{7, 7, playerMe}, {7, 6, playerOpp}, {8, 5, playerMe}},  // engine owns tengen (black side)
	}
	for i, neg := range negatives {
		if _, ok := openingModeOf(build(neg[0], neg[1], neg[2])); ok {
			t.Fatalf("negative %d recognized as a classic mode", i)
		}
	}
}

func TestModeTheoryZoneProjection(t *testing.T) {
	modeBook := book.LoadModeBook()
	if modeBook == nil {
		t.Fatal("classic mode book did not load")
	}
	// a recognized mode in a non-canonical orientation: tengen + direct white
	// reply at the south + black second to the south-west
	b := make([]int, 225)
	b[7*15+7] = playerOpp
	b[7*15+8] = playerMe
	b[5*15+5] = playerOpp
	s := newSearcher(15, b, 0)
	s.setRule(RuleFreestyle, playerOpp)
	s.modeBook = modeBook
	zone := s.modeTheoryZone()
	if zone == nil {
		t.Fatal("recognized mode without theory zone (classic book missing?)")
	}
	for p := range zone {
		if s.b[p] != 0 {
			t.Fatalf("theory cell (%d,%d) is occupied", p%15, p/15)
		}
		x, y := p%15, p/15
		dx, dy := x-7, y-7
		adx, ady := dx, dy
		if adx < 0 {
			adx = -adx
		}
		if ady < 0 {
			ady = -ady
		}
		if adx > 2 || ady > 2 {
			t.Fatalf("theory cell (%d,%d) outside the mode box", x, y)
		}
	}
	// stones 1-2 and non-classic positions have no zone
	b1 := make([]int, 225)
	b1[7*15+7] = playerOpp
	s1 := newSearcher(15, b1, 0)
	s1.setRule(RuleFreestyle, playerOpp)
	s1.modeBook = modeBook
	if s1.modeTheoryZone() != nil {
		t.Fatal("single-stone position produced a theory zone")
	}
	s3 := newSearcher(15, b, 0) // no setRule: blackSide = playerMe, tengen not black
	s3.modeBook = modeBook
	if s3.modeTheoryZone() != nil {
		t.Fatal("non-classic position produced a theory zone")
	}
}

func TestOpeningPriorRanksTheoryCells(t *testing.T) {
	modeBook := book.LoadModeBook()
	if modeBook == nil {
		t.Fatal("classic mode book did not load")
	}
	b := make([]int, 225)
	b[7*15+7] = playerOpp
	b[7*15+8] = playerMe
	b[5*15+5] = playerOpp
	s := newSearcher(15, b, 0)
	s.setRule(RuleFreestyle, playerOpp)
	s.modeBook = modeBook
	zone := s.modeTheoryZone()
	if zone == nil {
		t.Fatal("no theory zone")
	}
	moves := s.genMoves(rootMoveLimit, playerMe)
	filtered := s.applyOpeningPrior(moves)
	if len(filtered) == 0 {
		t.Fatal("prior emptied the list")
	}
	theoryCount := 0
	for i, m := range filtered {
		if zone[m] {
			theoryCount++
			continue
		}
		// after the first non-theory cell no theory cell may follow: they rank first
		for _, later := range filtered[i+1:] {
			if zone[later] {
				t.Fatal("theory cell ranked after a non-theory cell")
			}
		}
		break
	}
	if theoryCount == 0 {
		t.Fatal("no theory cell ranked first")
	}
}
