package realbackend

import (
	"context"
	"testing"
)

// TestDetectDockerNeverPanics doesn't assert a specific Available value —
// whether Docker is installed depends entirely on the machine running the
// test. It only confirms DetectDocker always returns a usable, explained
// result instead of panicking or returning an ambiguous empty Detail.
func TestDetectDockerNeverPanics(t *testing.T) {
	avail := DetectDocker(context.Background())
	if avail.Detail == "" {
		t.Fatal("expected Detail to always be non-empty, explaining the Available value")
	}
}

// TestDetectBudgetMBAlwaysPositive confirms the fallback path: regardless
// of whether host RAM detection succeeds on this machine, DetectBudgetMB
// always returns at least the conservative default rather than 0 or a
// negative number.
func TestDetectBudgetMBAlwaysPositive(t *testing.T) {
	budget := DetectBudgetMB()
	if budget < defaultBudgetMB {
		t.Fatalf("expected budget to be at least the conservative default (%dMB), got %dMB", defaultBudgetMB, budget)
	}
}

// TestDetectHostRAMMBDoesNotPanic confirms calling the platform-specific
// detector never panics; the actual value is environment-specific so we
// only check internal consistency (a positive value whenever ok is true).
func TestDetectHostRAMMBDoesNotPanic(t *testing.T) {
	mb, ok := DetectHostRAMMB()
	if ok && mb <= 0 {
		t.Fatalf("expected positive RAM when ok=true, got %d", mb)
	}
}
