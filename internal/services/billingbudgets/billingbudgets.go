// Package billingbudgets emulates a subset of the Cloud Billing Budget API
// (billingbudgets.googleapis.com/v1): budgets scoped to a billing account
// (google_billing_budget in Terraform). Budget CRUD is synchronous in the
// real API (no Operation wrapper). As of Phase 11, getBudget/listBudgets
// also estimate real spend from actual Compute usage in the budget's
// filtered projects and emit a real Cloud Logging entry the first time a
// thresholdRule is crossed -- see spend.go -- instead of ThresholdRules
// being metadata that's never evaluated.
package billingbudgets

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/storage"
)

const bucketBudgets = "billingbudgets.budgets"

// BudgetAmount mirrors billingbudgets#BudgetAmount.
type BudgetAmount struct {
	SpecifiedAmount  *Money         `json:"specifiedAmount,omitempty"`
	LastPeriodAmount map[string]any `json:"lastPeriodAmount,omitempty"`
}

// Money mirrors google.type.Money.
type Money struct {
	CurrencyCode string `json:"currencyCode,omitempty"`
	Units        string `json:"units,omitempty"`
	Nanos        int64  `json:"nanos,omitempty"`
}

// ThresholdRule mirrors billingbudgets#ThresholdRule.
type ThresholdRule struct {
	ThresholdPercent float64 `json:"thresholdPercent"`
	SpendBasis       string  `json:"spendBasis,omitempty"`
}

// Filter mirrors billingbudgets#Filter (subset).
type Filter struct {
	Projects             []string `json:"projects,omitempty"`
	CreditTypesTreatment string   `json:"creditTypesTreatment,omitempty"`
}

// Budget mirrors the relevant subset of billingbudgets#GoogleCloudBillingBudgetsV1Budget.
type Budget struct {
	Name           string          `json:"name"` // billingAccounts/{account}/budgets/{budget}
	DisplayName    string          `json:"displayName,omitempty"`
	BudgetFilter   *Filter         `json:"budgetFilter,omitempty"`
	Amount         *BudgetAmount   `json:"amount,omitempty"`
	ThresholdRules []ThresholdRule `json:"thresholdRules,omitempty"`
	Etag           string          `json:"etag,omitempty"`
}

type Service struct {
	db  *storage.DB
	seq int64

	// mu/notified track which (budget, thresholdPercent) pairs have already
	// produced a Cloud Logging entry, so re-evaluating spend on every
	// get/list doesn't spam a new log line each time -- in-memory only,
	// same tradeoff internal/activity already accepts elsewhere in this
	// phase (no persistence across a restart).
	mu       sync.Mutex
	notified map[string]map[float64]bool
}

func New(db *storage.DB) *Service { return &Service{db: db} }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/billingAccounts/{account}/budgets", s.createBudget)
	mux.HandleFunc("GET /v1/billingAccounts/{account}/budgets", s.listBudgets)
	mux.HandleFunc("GET /v1/billingAccounts/{account}/budgets/{budget}", s.getBudget)
	mux.HandleFunc("PATCH /v1/billingAccounts/{account}/budgets/{budget}", s.updateBudget)
	mux.HandleFunc("DELETE /v1/billingAccounts/{account}/budgets/{budget}", s.deleteBudget)
}

func budgetKey(account, budget string) string { return account + "/" + budget }

func budgetName(account, budget string) string {
	return fmt.Sprintf("billingAccounts/%s/budgets/%s", account, budget)
}

func (s *Service) nextID() int64 {
	s.seq++
	return s.seq
}

func (s *Service) createBudget(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	var budget Budget
	if err := json.NewDecoder(r.Body).Decode(&budget); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if budget.DisplayName == "" {
		server.WriteError(w, 400, "INVALID_ARGUMENT", "displayName is required")
		return
	}
	budgetID := fmt.Sprintf("budget-%d", s.nextID())
	budget.Name = budgetName(account, budgetID)
	if budget.Etag == "" {
		budget.Etag = fmt.Sprintf("etag-%d", s.nextID())
	}
	if err := s.db.Put(bucketBudgets, budgetKey(account, budgetID), budget); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, budget)
}

func (s *Service) listBudgets(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	items := []Budget{}
	err := s.db.List(bucketBudgets, account+"/", func(key string, raw []byte) error {
		var b Budget
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		s.evaluateSpend(key, b)
		items = append(items, b)
		return nil
	})
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{"budgets": items})
}

func (s *Service) getBudget(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	budgetID := r.PathValue("budget")
	var b Budget
	found, err := s.db.Get(bucketBudgets, budgetKey(account, budgetID), &b)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "budget not found")
		return
	}
	s.evaluateSpend(budgetKey(account, budgetID), b)
	server.WriteJSON(w, 200, b)
}

func (s *Service) updateBudget(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	budgetID := r.PathValue("budget")
	var existing Budget
	found, err := s.db.Get(bucketBudgets, budgetKey(account, budgetID), &existing)
	if err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	if !found {
		server.WriteError(w, 404, "NOT_FOUND", "budget not found")
		return
	}
	var body Budget
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteError(w, 400, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	if body.BudgetFilter != nil {
		existing.BudgetFilter = body.BudgetFilter
	}
	if body.Amount != nil {
		existing.Amount = body.Amount
	}
	if body.ThresholdRules != nil {
		existing.ThresholdRules = body.ThresholdRules
	}
	if err := s.db.Put(bucketBudgets, budgetKey(account, budgetID), existing); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, existing)
}

func (s *Service) deleteBudget(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	budgetID := r.PathValue("budget")
	if err := s.db.Delete(bucketBudgets, budgetKey(account, budgetID)); err != nil {
		server.WriteError(w, 500, "INTERNAL", err.Error())
		return
	}
	server.WriteJSON(w, 200, map[string]any{})
}
