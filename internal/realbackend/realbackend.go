// Package realbackend is Phase 12 of the roadmap: the pluggable
// real-execution foundation that Phase 13+ (real Cloud Run/Functions via
// Docker, real embedded Postgres for Cloud SQL) builds on. This package
// itself adds no user-visible real backend — only the mechanism:
//
//   - Backend is the interface every later real backend implements.
//   - Governor enforces a RAM budget across admitted backends, admitting
//     a new one when it fits and evicting least-recently-used backends
//     first when it doesn't (see Admit's doc comment for the one
//     deliberate simplification this foundation makes).
//   - DetectDocker/DetectHostRAMMB/DetectBudgetMB (docker.go, ram.go)
//     probe the host without adding a new Go module dependency — they
//     shell out to binaries a real Docker/host setup already has,
//     mirroring this project's existing "duplicate small helpers, avoid
//     new deps" convention from Phase 11.
//   - WantsReal (optin.go) is the per-resource opt-in check every future
//     real backend's create handler calls before trying to admit one.
//   - RegisterAdmin (admin.go) exposes the governor's state at
//     GET /admin/real-backends, per the roadmap's "make the adaptive
//     behavior visible, not a black box" requirement.
package realbackend

import (
	"sort"
	"sync"
	"time"
)

// Backend is the interface every concrete "real" backend (Phase 13+:
// Docker-backed Cloud Run/Functions, an embedded Postgres for Cloud SQL,
// ...) implements. No concrete implementation exists in this phase —
// only the interface and the Governor that admits/evicts instances of it.
type Backend interface {
	// Kind identifies the backend flavor (e.g. "cloudsql-postgres-embedded",
	// "cloudrun-docker"). Used only for logging/introspection.
	Kind() string
	// FootprintMB is the backend's own estimate of its RAM footprint, used
	// by the Governor for budget-aware admission. A backend that doesn't
	// know better should return a conservative (high) estimate rather than
	// 0, since underestimating defeats the budget's purpose.
	FootprintMB() int
	// Stop releases whatever resources were acquired (e.g. stopping a
	// docker container or an embedded Postgres process). Stop must be safe
	// to call even on a backend that was never fully started.
	Stop() error
}

type managedBackend struct {
	id        string
	backend   Backend
	footprint int
	lastUsed  time.Time
}

// Governor enforces a RAM budget across every admitted real backend. It
// has no idea what a "request" or a "resource" is — callers (future
// Phase 13+ service code) own that; Governor only tracks (id, Backend,
// footprint, lastUsed) tuples and the arithmetic around them.
//
// Eviction simplification, documented rather than hidden: the roadmap
// calls for evicting "least-recently-used *idle*" backends. This
// foundation has no concept yet of "currently in use" beyond Touch's
// lastUsed timestamp — that concept depends on what a concrete backend
// actually does (an open SQL connection vs. a closed one, a container
// mid-request vs. sitting still), which doesn't exist until Phase 13+
// implements one. So for now every admitted backend is eviction-eligible
// by plain LRU; a concrete backend that needs stronger in-use protection
// can call Touch on every active use to keep its lastUsed fresh.
type Governor struct {
	mu       sync.Mutex
	budgetMB int
	backends map[string]*managedBackend
	onEvict  func(id string)
}

// NewGovernor creates a Governor with the given RAM budget in MB. Use
// DetectBudgetMB to derive a sane default from the host.
func NewGovernor(budgetMB int) *Governor {
	return &Governor{budgetMB: budgetMB, backends: map[string]*managedBackend{}}
}

// SetOnEvict registers a callback invoked, outside the Governor's lock,
// every time a backend is evicted (by Admit's budget-driven eviction) or
// removed (by an explicit Release). Phase 13+ backends that keep their
// own side-table of admitted backends (e.g. cloudsql keeps a map from
// governor ID to its live *postgres.Backend, so it knows which engine to
// route a CREATE DATABASE/ROLE statement to) should register this to
// stay in sync rather than risk talking to a backend the Governor already
// stopped. Safe to call with nil to clear it.
//
// Known limitation, deliberately not solved here: only one callback is
// supported, registered once. Fine while cloudsql is the only real-backend
// consumer; revisit (e.g. a slice of callbacks) if/when a second one
// (Cloud Run/Functions via Docker) needs its own.
func (g *Governor) SetOnEvict(fn func(id string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onEvict = fn
}

func notifyEvicted(fn func(id string), ids []string) {
	if fn == nil {
		return
	}
	for _, id := range ids {
		fn(id)
	}
}

func (g *Governor) usedMBLocked() int {
	total := 0
	for _, m := range g.backends {
		total += m.footprint
	}
	return total
}

// Admit tries to register backend under id. If admitting it would exceed
// the budget, Admit first evicts idle backends (oldest lastUsed first),
// calling Stop on each, until there's room or nothing left to evict. It
// returns admitted=false without changing any state if id is already
// admitted, or if backend's own footprint alone exceeds the total budget
// (no amount of evicting other backends would ever make room for it).
func (g *Governor) Admit(id string, backend Backend) (admitted bool, evictedIDs []string) {
	g.mu.Lock()

	footprint := backend.FootprintMB()
	if footprint > g.budgetMB {
		g.mu.Unlock()
		return false, nil
	}
	if _, exists := g.backends[id]; exists {
		g.mu.Unlock()
		return false, nil
	}

	for g.usedMBLocked()+footprint > g.budgetMB {
		victim := g.oldestIDLocked()
		if victim == "" {
			fn := g.onEvict
			g.mu.Unlock()
			notifyEvicted(fn, evictedIDs)
			return false, evictedIDs
		}
		g.evictLocked(victim)
		evictedIDs = append(evictedIDs, victim)
	}

	g.backends[id] = &managedBackend{id: id, backend: backend, footprint: footprint, lastUsed: time.Now()}
	fn := g.onEvict
	g.mu.Unlock()
	notifyEvicted(fn, evictedIDs)
	return true, evictedIDs
}

func (g *Governor) oldestIDLocked() string {
	var oldestID string
	var oldestTime time.Time
	for id, m := range g.backends {
		if oldestID == "" || m.lastUsed.Before(oldestTime) {
			oldestID = id
			oldestTime = m.lastUsed
		}
	}
	return oldestID
}

func (g *Governor) evictLocked(id string) {
	m := g.backends[id]
	delete(g.backends, id)
	if m != nil {
		_ = m.backend.Stop()
	}
}

// Release removes id from the governor and stops its backend — call this
// when the resource the backend was serving is itself deleted.
func (g *Governor) Release(id string) {
	g.mu.Lock()
	_, ok := g.backends[id]
	if ok {
		g.evictLocked(id)
	}
	fn := g.onEvict
	g.mu.Unlock()
	if ok {
		notifyEvicted(fn, []string{id})
	}
}

// Touch resets id's idle clock. Concrete backends (Phase 13+) should call
// this on every active use so an actively-used backend is never picked as
// the LRU eviction victim ahead of one that's merely older but inactive.
func (g *Governor) Touch(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.backends[id]; ok {
		m.lastUsed = time.Now()
	}
}

// minIdleTimeout/maxIdleTimeout bound IdleTimeout's adaptive range.
const (
	minIdleTimeout = 5 * time.Minute
	maxIdleTimeout = 30 * time.Minute
)

// IdleTimeout scales with current budget pressure: tight (minIdleTimeout)
// when the budget is nearly exhausted, loose (maxIdleTimeout) when
// there's slack, per the roadmap's "idle timeout itself scales with
// budget pressure" note. Concrete backends (Phase 13+) are expected to
// poll this rather than use one hardcoded number.
func (g *Governor) IdleTimeout() time.Duration {
	g.mu.Lock()
	used := g.usedMBLocked()
	budget := g.budgetMB
	g.mu.Unlock()

	if budget <= 0 {
		return minIdleTimeout
	}
	pressure := float64(used) / float64(budget)
	if pressure > 1 {
		pressure = 1
	}
	span := maxIdleTimeout - minIdleTimeout
	return maxIdleTimeout - time.Duration(pressure*float64(span))
}

// BackendSnapshot is the introspection-friendly view of one admitted
// backend.
type BackendSnapshot struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	FootprintMB int       `json:"footprintMb"`
	LastUsed    time.Time `json:"lastUsed"`
}

// GovernorSnapshot is the full introspection view served by
// GET /admin/real-backends.
type GovernorSnapshot struct {
	BudgetMB int               `json:"budgetMb"`
	UsedMB   int               `json:"usedMb"`
	Backends []BackendSnapshot `json:"backends"`
}

// Snapshot returns the governor's current state for introspection.
// Backends is always non-nil (possibly empty) so it serializes as `[]`
// rather than `null`.
func (g *Governor) Snapshot() GovernorSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	snap := GovernorSnapshot{BudgetMB: g.budgetMB, UsedMB: g.usedMBLocked(), Backends: []BackendSnapshot{}}
	for _, m := range g.backends {
		snap.Backends = append(snap.Backends, BackendSnapshot{
			ID:          m.id,
			Kind:        m.backend.Kind(),
			FootprintMB: m.footprint,
			LastUsed:    m.lastUsed,
		})
	}
	sort.Slice(snap.Backends, func(i, j int) bool { return snap.Backends[i].ID < snap.Backends[j].ID })
	return snap
}
