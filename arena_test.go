package main

// Smoke test for the cmd/arena match harness: build it, pit the engine
// against itself for two quick games and check the score line sums up.
// Skipped under -short; the full acceptance flow is documented in README §8.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArenaSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("arena smoke skipped in -short")
	}
	arenaBin := filepath.Join(t.TempDir(), "arena-test")
	build := exec.Command("go", "build", "-o", arenaBin, "./cmd/arena")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build arena: %v\n%s", err, out)
	}
	exe, err := filepath.Abs(testBinary)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, arenaBin,
		"-games", "2", "-time-turn", "100", "-size", "9", "-seed", "3", "-max-plies", "120",
		exe, exe) // flags first: Go's flag package stops at the first positional
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("arena run failed: %v\n%s", err, out)
	}
	score := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "score: ") {
			score = line
		}
	}
	if score == "" {
		t.Fatalf("no score line in output:\n%s", out)
	}
	// "score: A 1 - B 1 (0 draws) over 2 games ..."
	var sa, sb, d, games int
	if _, err := fmt.Sscanf(score, "score: A %d - B %d (%d draws) over %d games", &sa, &sb, &d, &games); err != nil {
		t.Fatalf("unparsable score line %q: %v", score, err)
	}
	if games != 2 || sa+sb+d != 2 {
		t.Fatalf("score line %q does not account for 2 games", score)
	}
	t.Log(score)
}
