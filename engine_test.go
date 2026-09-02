package main

import (
	"testing"
)

// Regression: PRINT (and any board access) before START used to panic with an
// index-out-of-range because the board was only allocated on START/BOARD.
func TestBoardAllocatedBeforeStart(t *testing.T) {
	e := NewEngine()
	if len(e.board) != e.size {
		t.Fatalf("board has %d rows, size = %d", len(e.board), e.size)
	}
	rows := e.renderBoard()
	if len(rows) != 15 {
		t.Fatalf("renderBoard returned %d rows, want 15", len(rows))
	}
	for y, row := range rows {
		if len(row) != 15 {
			t.Fatalf("row %d has length %d, want 15", y, len(row))
		}
		for x := 0; x < 15; x++ {
			if row[x] != '.' {
				t.Fatalf("fresh board not empty at (%d,%d): %q", x, y, row[x])
			}
		}
	}
}

func TestRenderBoardContents(t *testing.T) {
	e := NewEngine()
	e.resetBoard(9)
	e.place(0, 0, 1)
	e.place(8, 8, 2)

	rows := e.renderBoard()
	if rows[0][0] != 'O' {
		t.Errorf("own stone at (0,0) rendered as %q", rows[0][0])
	}
	if rows[8][8] != 'X' {
		t.Errorf("opponent stone at (8,8) rendered as %q", rows[8][8])
	}
	if len(rows) != 9 || len(rows[4]) != 9 {
		t.Errorf("board rendered with wrong dimensions")
	}
}

// TestFullBoardAiMoveStaysInBounds: aiMove wraps the algorithm's Think with a
// fallback and, when even that finds nothing, returns the board center. It
// must never panic or return out-of-board coordinates.
func TestFullBoardAiMoveStaysInBounds(t *testing.T) {
	e := NewEngine()
	e.resetBoard(9)
	for y := 0; y < 9; y++ {
		for x := 0; x < 9; x++ {
			e.place(x, y, (x+y)%2+1)
		}
	}
	x, y := e.aiMove()
	if x < 0 || x >= 9 || y < 0 || y >= 9 {
		t.Fatalf("aiMove on a full board = (%d,%d), out of board", x, y)
	}
}
