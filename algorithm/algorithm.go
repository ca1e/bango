// Package algorithm defines the pluggable engine interface shared by every
// move strategy ("algorithm") the bango engine can run, plus a small
// registry so the session layer can select one by name.
//
// An Algorithm turns a position snapshot (Request) into the next move. The
// session layer (package main) owns the protocol state machine and knows
// nothing about search: it snapshots the board, builds a Request and calls
// Think. Concrete algorithms live in subpackages (alphabeta, random) and
// register themselves via init, so adding an algorithm never touches the
// engine core.
package algorithm

import (
	"fmt"
	"sort"
	"sync"
)

// Request is one move decision's full context — an immutable snapshot of the
// session state at the moment the engine must move. Implementations must not
// retain Board (they may keep a copy for the duration of the Think call).
type Request struct {
	// Size is the board edge length N; the board is N*N intersections.
	Size int

	// Board is the row-major N*N snapshot: 0 empty, 1 own (the side this
	// engine plays), 2 opponent, 3 marked (protocol field-3 cells: winning
	// line marks or renju forbidden points — blocked for both sides).
	Board []int

	// OwnIsBlack reports which internal color owns black (renju forbidden
	// points and color-keyed opening books depend on it).
	OwnIsBlack bool

	// Rule is the INFO rule code: 0 freestyle, 1 standard, 4 renju, 8/9 caro.
	Rule int

	// Time controls in milliseconds (Piskvork protocol). TimeoutTurn == 0
	// means "as fast as possible"; TimeLeft <= 0 means unknown.
	TimeoutTurn int
	TimeLeft    int64

	// Manager search limits; 0 / negative = engine default (unlimited).
	MaxDepth int
	MaxNode  int64

	// MaxMemory is the INFO max_node…/max_memory budget in bytes; 0 = no
	// explicit budget. Algorithms may size caches (transposition tables)
	// from it.
	MaxMemory int64

	// Folder is the INFO folder for persistent files (opening books live
	// under Folder/pbrain-bango).
	Folder string

	// HasLast reports that LastX/LastY identify the most recently placed
	// stone (false on an empty board). "前一手落子" — threat analysis and
	// proximity weighting key off it.
	HasLast      bool
	LastX, LastY int
}

// Algorithm is the move-strategy interface every engine algorithm implements.
//
// Contract:
//   - Think returns the move to play. The coordinates must satisfy
//     0 <= x, y < req.Size and point at an empty cell. Returning (-1, -1)
//     signals "no playable cell" — the session layer then falls back to the
//     first empty cell itself.
//   - Think is invoked for one side per turn, serially; implementations need
//     no internal locking but must not retain the Request.
//   - Reset is called when a session starts or the board is (re)created: a
//     new game begins, so per-game state (transposition tables, caches) must
//     follow the new size / memory budget.
type Algorithm interface {
	// Name returns the registry name ("alphabeta", "random", ...).
	Name() string

	// Think decides the next move for the snapshot in req.
	Think(req Request) (x, y int)

	// Reset discards/reshapes per-game state for the coming game
	// (size/maxMemory describe it). Compatible caches (e.g. a transposition
	// table of the same size and budget) may survive a same-size restart.
	Reset(size int, maxMemory int64)

	// EndSession tears a session down: every per-game cache must be dropped
	// so a new client cannot inherit positions from the previous game.
	EndSession()
}

const (
	// DefaultName is the algorithm the engine runs when nothing else is
	// selected (BANGO_ALGO environment variable).
	DefaultName = "alphabeta"
)

var (
	regMu    sync.RWMutex
	registry = map[string]func() Algorithm{}
)

// Register makes a factory buildable by name. Called from algorithm
// subpackages' init functions; registering an existing name panics (a
// programming error, not a runtime condition).
func Register(name string, factory func() Algorithm) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("algorithm: duplicate registration %q", name))
	}
	registry[name] = factory
}

// New builds the algorithm registered under name.
func New(name string) (Algorithm, error) {
	regMu.RLock()
	factory, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("algorithm: unknown algorithm %q (available: %v)", name, Names())
	}
	return factory(), nil
}

// Names lists the registered algorithms, sorted for stable display.
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
