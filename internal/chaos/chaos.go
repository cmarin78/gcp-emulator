package chaos

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
)

// Rule represents a chaos injection condition and action.
type Rule struct {
	ID           string  `json:"id"`
	Service      string  `json:"service,omitempty"`      // e.g. "storage", "pubsub", "compute" (or empty for any)
	PathPrefix   string  `json:"pathPrefix,omitempty"`   // e.g. "/storage/v1/b/my-bucket"
	Method       string  `json:"method,omitempty"`       // "GET", "POST", etc. (empty for any)
	Probability  float64 `json:"probability"`             // 0.0 to 1.0 (defaults to 1.0 if <= 0)
	DelayMs      int     `json:"delayMs,omitempty"`      // artificial base latency
	JitterMs     int     `json:"jitterMs,omitempty"`     // random additional delay up to JitterMs
	HTTPStatus   int     `json:"httpStatus,omitempty"`   // e.g. 503, 500, 429 (0 = only latency, no failure)
	ErrorCode    string  `json:"errorCode,omitempty"`    // e.g. "UNAVAILABLE", "INTERNAL"
	ErrorMessage string  `json:"errorMessage,omitempty"` // message returned to client
}

// Stats captures metrics about chaos injections.
type Stats struct {
	TotalRequestsEvaluated int64 `json:"totalRequestsEvaluated"`
	InjectedFailures       int64 `json:"injectedFailures"`
	InjectedDelays         int64 `json:"injectedDelays"`
	TotalDelayMs           int64 `json:"totalDelayMs"`
}

// Manager manages chaos rules and evaluates incoming requests.
type Manager struct {
	rules map[string]Rule
	mu    sync.RWMutex
	seq   int64

	totalRequests   int64
	injectedFails   int64
	injectedDelays  int64
	accumDelayMs    int64
	rng             *rand.Rand
	rngMu           sync.Mutex
}

// New creates a new chaos Manager.
func New() *Manager {
	return &Manager{
		rules: make(map[string]Rule),
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// RegisterRoutes registers the /admin/chaos REST endpoints.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/chaos/rules", m.handleCreateRule)
	mux.HandleFunc("GET /admin/chaos/rules", m.handleListRules)
	mux.HandleFunc("GET /admin/chaos/rules/{id}", m.handleGetRule)
	mux.HandleFunc("DELETE /admin/chaos/rules/{id}", m.handleDeleteRule)
	mux.HandleFunc("DELETE /admin/chaos/rules", m.handleClearRules)
	mux.HandleFunc("GET /admin/chaos/stats", m.handleGetStats)
	mux.HandleFunc("POST /admin/chaos/stats:reset", m.handleResetStats)
}

func (m *Manager) nextID() string {
	seq := atomic.AddInt64(&m.seq, 1)
	return fmt.Sprintf("chaos-rule-%d", seq)
}

// AddRule registers a new rule.
func (m *Manager) AddRule(rule Rule) (Rule, error) {
	if rule.ID == "" {
		rule.ID = m.nextID()
	}
	if rule.Probability <= 0 || rule.Probability > 1.0 {
		rule.Probability = 1.0
	}
	if rule.HTTPStatus > 0 {
		if rule.ErrorCode == "" {
			switch rule.HTTPStatus {
			case 503:
				rule.ErrorCode = "UNAVAILABLE"
			case 429:
				rule.ErrorCode = "RESOURCE_EXHAUSTED"
			case 500:
				rule.ErrorCode = "INTERNAL"
			case 404:
				rule.ErrorCode = "NOT_FOUND"
			default:
				rule.ErrorCode = "FAULT_INJECTED"
			}
		}
		if rule.ErrorMessage == "" {
			rule.ErrorMessage = fmt.Sprintf("Simulated failure injected by chaos engine (status %d)", rule.HTTPStatus)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[rule.ID] = rule
	return rule, nil
}

// GetRule retrieves a rule by ID.
func (m *Manager) GetRule(id string) (Rule, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rules[id]
	return r, ok
}

// ListRules lists all active rules.
func (m *Manager) ListRules() []Rule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Rule, 0, len(m.rules))
	for _, r := range m.rules {
		items = append(items, r)
	}
	return items
}

// DeleteRule deletes a rule by ID.
func (m *Manager) DeleteRule(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rules[id]; ok {
		delete(m.rules, id)
		return true
	}
	return false
}

// ClearRules removes all active chaos rules.
func (m *Manager) ClearRules() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = make(map[string]Rule)
}

// GetStats returns current chaos injection statistics.
func (m *Manager) GetStats() Stats {
	return Stats{
		TotalRequestsEvaluated: atomic.LoadInt64(&m.totalRequests),
		InjectedFailures:       atomic.LoadInt64(&m.injectedFails),
		InjectedDelays:         atomic.LoadInt64(&m.injectedDelays),
		TotalDelayMs:           atomic.LoadInt64(&m.accumDelayMs),
	}
}

// ResetStats zeroes out injection statistics.
func (m *Manager) ResetStats() {
	atomic.StoreInt64(&m.totalRequests, 0)
	atomic.StoreInt64(&m.injectedFails, 0)
	atomic.StoreInt64(&m.injectedDelays, 0)
	atomic.StoreInt64(&m.accumDelayMs, 0)
}

func (m *Manager) randFloat() float64 {
	m.rngMu.Lock()
	defer m.rngMu.Unlock()
	return m.rng.Float64()
}

func (m *Manager) randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	m.rngMu.Lock()
	defer m.rngMu.Unlock()
	return m.rng.Intn(n)
}

// matches determines whether a request matches a chaos rule.
func (r *Rule) matches(req *http.Request) bool {
	if r.Method != "" && !strings.EqualFold(r.Method, req.Method) {
		return false
	}
	if r.PathPrefix != "" && !strings.HasPrefix(req.URL.Path, r.PathPrefix) {
		return false
	}
	if r.Service != "" {
		p := strings.ToLower(req.URL.Path)
		svc := strings.ToLower(r.Service)
		// Check common GCP URL prefixes
		switch svc {
		case "storage", "gcs":
			if !strings.HasPrefix(p, "/storage/") && !strings.HasPrefix(p, "/upload/storage/") {
				return false
			}
		case "pubsub":
			if !strings.HasPrefix(p, "/v1/projects/") || (!strings.Contains(p, "/topics") && !strings.Contains(p, "/subscriptions")) {
				return false
			}
		case "spanner":
			if !strings.Contains(p, "/instances") || !strings.Contains(p, "/databases") {
				return false
			}
		case "bigquery":
			if !strings.HasPrefix(p, "/bigquery/") {
				return false
			}
		case "compute":
			if !strings.HasPrefix(p, "/compute/") {
				return false
			}
		default:
			if !strings.Contains(p, svc) {
				return false
			}
		}
	}
	return true
}

// Intercept evaluates whether an incoming HTTP request should be delayed or failed.
func (m *Manager) Intercept(req *http.Request) (delay time.Duration, shouldFail bool, status int, code, msg string) {
	// Never inject chaos into admin, console, or health endpoints
	p := req.URL.Path
	if strings.HasPrefix(p, "/admin/") || p == "/healthz" || strings.HasPrefix(p, "/web/") || req.Method == http.MethodOptions {
		return 0, false, 0, "", ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.rules) == 0 {
		return 0, false, 0, "", ""
	}

	atomic.AddInt64(&m.totalRequests, 1)

	for _, rule := range m.rules {
		if !rule.matches(req) {
			continue
		}

		// Probability check
		if rule.Probability < 1.0 {
			roll := m.randFloat()
			if roll > rule.Probability {
				continue
			}
		}

		// Calculate Delay
		totalDelayMs := rule.DelayMs
		if rule.JitterMs > 0 {
			totalDelayMs += m.randIntn(rule.JitterMs)
		}
		if totalDelayMs > 0 {
			delay = time.Duration(totalDelayMs) * time.Millisecond
			atomic.AddInt64(&m.injectedDelays, 1)
			atomic.AddInt64(&m.accumDelayMs, int64(totalDelayMs))
		}

		// Calculate Failure
		if rule.HTTPStatus > 0 {
			shouldFail = true
			status = rule.HTTPStatus
			code = rule.ErrorCode
			msg = rule.ErrorMessage
			atomic.AddInt64(&m.injectedFails, 1)
		}

		return delay, shouldFail, status, code, msg
	}

	return 0, false, 0, "", ""
}

// ── HTTP Handlers ─────────────────────────────────────────────────────────────

func (m *Manager) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var body Rule
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	created, err := m.AddRule(body)
	if err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	server.WriteJSON(w, 201, created)
}

func (m *Manager) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules := m.ListRules()
	server.WriteJSON(w, 200, map[string]any{
		"rules": rules,
		"count": len(rules),
	})
}

func (m *Manager) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, found := m.GetRule(id)
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "chaos rule not found")
		return
	}
	server.WriteJSON(w, 200, rule)
}

func (m *Manager) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !m.DeleteRule(id) {
		server.WriteError(w, 404, "NOT_FOUND", "chaos rule not found")
		return
	}
	server.WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

func (m *Manager) handleClearRules(w http.ResponseWriter, r *http.Request) {
	m.ClearRules()
	server.WriteJSON(w, 200, map[string]string{"status": "cleared"})
}

func (m *Manager) handleGetStats(w http.ResponseWriter, r *http.Request) {
	server.WriteJSON(w, 200, m.GetStats())
}

func (m *Manager) handleResetStats(w http.ResponseWriter, r *http.Request) {
	m.ResetStats()
	server.WriteJSON(w, 200, map[string]string{"status": "reset"})
}
