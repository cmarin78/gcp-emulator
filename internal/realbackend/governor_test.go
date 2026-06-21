package realbackend

import (
	"testing"
	"time"
)

type fakeBackend struct {
	kind      string
	footprint int
	stopped   bool
}

func (f *fakeBackend) Kind() string     { return f.kind }
func (f *fakeBackend) FootprintMB() int { return f.footprint }
func (f *fakeBackend) Stop() error      { f.stopped = true; return nil }

func TestAdmitWithinBudget(t *testing.T) {
	g := NewGovernor(1000)
	b := &fakeBackend{kind: "x", footprint: 200}
	admitted, evicted := g.Admit("a", b)
	if !admitted || len(evicted) != 0 {
		t.Fatalf("admit: got admitted=%v evicted=%v", admitted, evicted)
	}
	snap := g.Snapshot()
	if snap.UsedMB != 200 || len(snap.Backends) != 1 {
		t.Fatalf("snapshot: %+v", snap)
	}
}

func TestAdmitRejectsSingleBackendBiggerThanBudget(t *testing.T) {
	g := NewGovernor(100)
	b := &fakeBackend{kind: "x", footprint: 200}
	admitted, _ := g.Admit("a", b)
	if admitted {
		t.Fatal("expected admission to be rejected: footprint exceeds total budget")
	}
}

func TestAdmitEvictsLRUWhenOverBudget(t *testing.T) {
	g := NewGovernor(300)
	first := &fakeBackend{kind: "x", footprint: 200}
	second := &fakeBackend{kind: "y", footprint: 200}

	admitted, evicted := g.Admit("first", first)
	if !admitted || len(evicted) != 0 {
		t.Fatalf("admit first: admitted=%v evicted=%v", admitted, evicted)
	}
	time.Sleep(2 * time.Millisecond) // ensure distinguishable lastUsed ordering

	admitted, evicted = g.Admit("second", second)
	if !admitted {
		t.Fatal("expected second to be admitted after evicting first")
	}
	if len(evicted) != 1 || evicted[0] != "first" {
		t.Fatalf("expected first to be evicted, got %v", evicted)
	}
	if !first.stopped {
		t.Fatal("expected evicted backend's Stop to be called")
	}
	snap := g.Snapshot()
	if len(snap.Backends) != 1 || snap.Backends[0].ID != "second" {
		t.Fatalf("snapshot after eviction: %+v", snap)
	}
}

func TestTouchProtectsRecentlyUsedFromEviction(t *testing.T) {
	// Budget has just enough slack that evicting only "second" (the
	// untouched, older-by-LRU backend) makes room for "third" -- so this
	// isolates Touch's effect from Admit's "keep evicting until it fits"
	// loop (covered separately by TestAdmitEvictsLRUWhenOverBudget).
	g := NewGovernor(400)
	first := &fakeBackend{kind: "x", footprint: 200}
	second := &fakeBackend{kind: "y", footprint: 100}
	g.Admit("first", first)
	time.Sleep(2 * time.Millisecond)
	g.Admit("second", second)

	// Touch "first" so it's now more recently used than "second".
	time.Sleep(2 * time.Millisecond)
	g.Touch("first")

	third := &fakeBackend{kind: "z", footprint: 150}
	admitted, evicted := g.Admit("third", third)
	if !admitted {
		t.Fatal("expected third to be admitted after eviction")
	}
	if len(evicted) != 1 || evicted[0] != "second" {
		t.Fatalf("expected second (least recently touched) to be evicted, got %v", evicted)
	}
	if !second.stopped {
		t.Fatal("expected evicted backend's Stop to be called")
	}
	if first.stopped {
		t.Fatal("expected touched backend 'first' to survive eviction")
	}
}

func TestReleaseStopsAndRemovesBackend(t *testing.T) {
	g := NewGovernor(300)
	b := &fakeBackend{kind: "x", footprint: 100}
	g.Admit("a", b)
	g.Release("a")
	if !b.stopped {
		t.Fatal("expected Release to call Stop")
	}
	if len(g.Snapshot().Backends) != 0 {
		t.Fatal("expected backend to be removed from snapshot after Release")
	}
}

func TestAdmitRejectsDuplicateID(t *testing.T) {
	g := NewGovernor(300)
	b1 := &fakeBackend{kind: "x", footprint: 100}
	b2 := &fakeBackend{kind: "y", footprint: 100}
	g.Admit("a", b1)
	admitted, _ := g.Admit("a", b2)
	if admitted {
		t.Fatal("expected duplicate id admission to be rejected")
	}
}

func TestIdleTimeoutScalesWithPressure(t *testing.T) {
	g := NewGovernor(1000)
	if g.IdleTimeout() != maxIdleTimeout {
		t.Fatalf("expected max idle timeout at zero pressure, got %v", g.IdleTimeout())
	}
	g.Admit("a", &fakeBackend{kind: "x", footprint: 1000})
	if g.IdleTimeout() != minIdleTimeout {
		t.Fatalf("expected min idle timeout at full pressure, got %v", g.IdleTimeout())
	}
}

func TestSetOnEvictNotifiesOnEvictionAndRelease(t *testing.T) {
	g := NewGovernor(300)
	var notified []string
	g.SetOnEvict(func(id string) { notified = append(notified, id) })

	first := &fakeBackend{kind: "x", footprint: 200}
	second := &fakeBackend{kind: "y", footprint: 200}
	g.Admit("first", first)
	time.Sleep(2 * time.Millisecond)

	// Admitting "second" forces a budget-driven eviction of "first".
	admitted, evicted := g.Admit("second", second)
	if !admitted || len(evicted) != 1 || evicted[0] != "first" {
		t.Fatalf("admit second: admitted=%v evicted=%v", admitted, evicted)
	}
	if len(notified) != 1 || notified[0] != "first" {
		t.Fatalf("expected onEvict to fire once for the budget-driven eviction, got %v", notified)
	}

	// Releasing "second" explicitly should also notify.
	g.Release("second")
	if len(notified) != 2 || notified[1] != "second" {
		t.Fatalf("expected onEvict to fire for Release too, got %v", notified)
	}
}

// TestSetOnEvictSupportsMultipleCallbacks covers Phase 14's fix to the
// Phase 12 limitation noted in SetOnEvict's doc comment: two independent
// real-backend consumers (e.g. cloudsql and cloudrun) each register their
// own callback, and both must fire for every eviction/release — neither
// registration should clobber the other.
func TestSetOnEvictSupportsMultipleCallbacks(t *testing.T) {
	g := NewGovernor(300)
	var first, second []string
	g.SetOnEvict(func(id string) { first = append(first, id) })
	g.SetOnEvict(func(id string) { second = append(second, id) })

	b := &fakeBackend{kind: "x", footprint: 100}
	g.Admit("a", b)
	g.Release("a")

	if len(first) != 1 || first[0] != "a" {
		t.Fatalf("expected first callback to fire for release, got %v", first)
	}
	if len(second) != 1 || second[0] != "a" {
		t.Fatalf("expected second callback to fire for release too, got %v", second)
	}
}

func TestSnapshotBackendsNeverNil(t *testing.T) {
	g := NewGovernor(100)
	snap := g.Snapshot()
	if snap.Backends == nil {
		t.Fatal("expected Backends to be an empty slice, not nil, so it serializes as []")
	}
}
