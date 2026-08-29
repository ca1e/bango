package main

// Transposition table backed by Zobrist hashing.
//
// Different move orders often reach the same position (a "transposition").
// The table caches search results keyed by a 64-bit position fingerprint so
// repeated subtrees are searched only once, and cached best moves sharpen
// move ordering on the remaining nodes.
//
// Concurrency: each way is an atomic pointer to an immutable entry. Readers
// load a pointer; writers build a fresh entry and swap it in — no locks, no
// torn reads, so the lazy-SMP workers can share one table (and the single-
// threaded search keeps the same interface at pointer-swap cost).

import "sync/atomic"

const (
	ttFlagEmpty uint8 = 0 // slot unused (zero value)
	ttFlagExact uint8 = 1 // stored score is the exact node value
	ttFlagLower uint8 = 2 // stored score is a lower bound (beta cutoff)
	ttFlagUpper uint8 = 3 // stored score is an upper bound (fail-low)
)

const (
	ttBits    = 18 // default bucket count 1<<18 × 2 ways (~12.6MB)
	ttMinBits = 12 // lower bound when shrinking to max_memory
	ttEntrySz = 24 // sizeof(ttEntry), for memory accounting
	ttWays    = 2  // set-associative slots per bucket (guidebook §7.2)
)

const noMove = uint16(0xFFFF)

// ttBitsFor maps a max_memory budget (bytes, 0 = default) to the table's
// bucket bits: shrink while the whole table would not fit, down to ttMinBits.
func ttBitsFor(maxMemory int64) int {
	bits := ttBits
	for maxMemory > 0 && bits > ttMinBits && uint64(ttWays*ttEntrySz)<<uint(bits) > uint64(maxMemory) {
		bits--
	}
	return bits
}

// zobrist holds the random codes used for position fingerprints: two per
// board cell (one per side) plus one code that separates "engine to move"
// nodes from "opponent to move" nodes.
type zobrist struct {
	piece []uint64 // index: p*2 + (side-1)
	side  uint64
}

// newZobrist fills the code table from a fixed-seed splitmix64 stream so the
// whole search stays reproducible across runs.
func newZobrist(n int) *zobrist {
	z := &zobrist{piece: make([]uint64, n*n*2)}
	x := uint64(0x9E3779B97F4A7C15)
	for i := range z.piece {
		x = splitmix64(x)
		z.piece[i] = x
	}
	z.side = splitmix64(x)
	return z
}

func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

func (z *zobrist) code(p, side int) uint64 {
	return z.piece[p*2+side-1]
}

// ttEntry is one cached search result, published immutable. flag marks
// whether score is exact or only a bound — reusing bounds correctly is what
// keeps table cutoffs sound inside alpha-beta.
type ttEntry struct {
	key   uint64 // hash >> ttBits: locks the slot against collisions
	score int32
	depth int16
	best  uint16 // cached best move (board index), noMove if none
	flag  uint8
}

// ttBucket holds the two ways of one set-associative set. Two ways keep a
// "shallow but fresh" result alongside a "deep" one instead of letting them
// evict each other on every collision.
type ttBucket [ttWays]atomic.Pointer[ttEntry]

// transTable is a fixed-size set-associative table safe for concurrent
// readers and writers (entry-level atomic publication, bucket-level races
// resolved by last swap wins — losing a write only costs a re-search).
type transTable struct {
	entries []ttBucket
	bits    uint // index bits; everything above them forms the lock
}

func newTransTable(bits int) *transTable {
	if bits < ttMinBits {
		bits = ttMinBits
	}
	return &transTable{entries: make([]ttBucket, 1<<bits), bits: uint(bits)}
}

func (t *transTable) mask() uint64 { return uint64(len(t.entries) - 1) }

// probe returns the cached entry for hash, if a slot is filled and the lock
// bits match. A 64-bit fingerprint makes spurious matches vanishingly rare,
// so no secondary verification is done.
func (t *transTable) probe(hash uint64) (ttEntry, bool) {
	b := &t.entries[hash&t.mask()]
	lock := hash >> t.bits
	for i := 0; i < ttWays; i++ {
		if e := b[i].Load(); e != nil && e.key == lock {
			return *e, true
		}
	}
	return ttEntry{}, false
}

// store writes the entry with depth-preferred replacement: an empty way is
// filled first, the same position always updates in place, and otherwise the
// shallower way is evicted — a strictly deeper result is never dropped for a
// shallower one. (guidebook §7.4, two-way variant)
func (t *transTable) store(hash uint64, score int32, depth int, best uint16, flag uint8) {
	b := &t.entries[hash&t.mask()]
	k := hash >> t.bits
	newE := &ttEntry{key: k, score: score, depth: int16(depth), best: best, flag: flag}
	for i := 0; i < ttWays; i++ {
		if e := b[i].Load(); e == nil || e.key == k {
			b[i].Store(newE)
			return
		}
	}
	victim := 0
	if d1, d0 := b[1].Load().depth, b[0].Load().depth; d1 < d0 {
		victim = 1
	}
	if depth >= int(b[victim].Load().depth) {
		b[victim].Store(newE)
	}
}
