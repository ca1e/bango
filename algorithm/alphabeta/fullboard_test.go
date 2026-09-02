package alphabeta

import (
	"testing"
	"time"
)

// TestFullBoardRunReportsNoMove: a full board leaves the search without
// candidates — run must report (-1,-1) instead of panicking. The protocol
// layer never asks on a finished game, but a malformed BOARD sequence must
// not take the engine down.
func TestFullBoardRunReportsNoMove(t *testing.T) {
	const n = 9
	b := make([]int, n*n)
	for p := range b {
		b[p] = p%2 + 1
	}
	s := newSearcher(n, b, 0)
	if x, y := s.run(4, 200*time.Millisecond); x != -1 || y != -1 {
		t.Fatalf("run on a full board = (%d,%d), want (-1,-1)", x, y)
	}
}
