package main

// Engine-level algorithm integration tests: the session layer drives a
// registered algorithm through the gomoku/algorithm interface and must stay
// correct regardless of which implementation is behind it. Searcher-internal
// behavior lives in the algorithm/alphabeta tests; these cover the boundary.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEngineAiMoveSmoke: a mid-opening position produces a legal cell.
func TestEngineAiMoveSmoke(t *testing.T) {
	e := NewEngine()
	e.resetBoard(9)
	e.info.TimeoutTurn = 200 // ms
	e.place(4, 4, 2)
	e.place(5, 5, 1)

	x, y := e.aiMove()
	if x < 0 || x >= 9 || y < 0 || y >= 9 || e.board[y][x] != 0 {
		t.Errorf("aiMove returned illegal cell (%d,%d)", x, y)
	}
}

// TestEngineUsesKillSearch: the engine-level run must still behave when the
// kill search is wired in (regression: run returns a legal move).
func TestEngineUsesKillSearch(t *testing.T) {
	e := NewEngine()
	e.resetBoard(9)
	e.info.TimeoutTurn = 300
	e.place(4, 4, 2)
	e.place(5, 5, 1)
	x, y := e.aiMove()
	if x < 0 || x >= 9 || y < 0 || y >= 9 || e.board[y][x] != 0 {
		t.Errorf("aiMove returned illegal cell (%d,%d)", x, y)
	}
}

// TestEngineBookProtocol: end-to-end — a book file in the INFO folder is
// loaded by the alphabeta adapter and the BOARD reply comes from the book.
func TestEngineBookProtocol(t *testing.T) {
	body := `{"id":"t","name":"test book","rules":"freestyle","size":9,
		"coordinateSystem":"board-row-column",
		"openings":[{"id":"a","coordinates":[[4,4],[6,3],[4,5],[6,6],[3,3]]}]}`
	dir := t.TempDir()
	sub := filepath.Join(dir, "pbrain-bango")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "book.json"), []byte(body), 0o644); err != nil {
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
