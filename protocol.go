package main

import (
	"strconv"
	"strings"
)

// Command is a fully-parsed manager command ready for the thinker to process.
// BOARD commands carry their multi-line body already assembled (lines until DONE).
type Command struct {
	Type  string
	Args  []string
	Board [][3]int // only for BOARD: x, y, id
	Pts   [][2]int // only for SWAP2BOARD: x, y stone pairs
}

// parseMove parses an "X,Y" argument into coordinates.
func parseMove(s string) (int, int, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	y, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return x, y, true
}
