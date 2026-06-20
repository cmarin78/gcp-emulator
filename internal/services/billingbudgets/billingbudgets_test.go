package billingbudgets

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.NewDB(t)
	mux := http.NewServeMux()
	New(db).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func validBudgetBody() map[string]any {
	return map[string]any{
		"displayName": "my-budget",
		"budgetFilter": map[string]any{
			"projects": []string{"projects/proj1"},
		},
		"amount": map[string]any{
			"specifiedAmount": map[string]any{
				"currencyCode": "USD",
				"units":        "1000",
			},
		},
		"thresholdRules": []map[string]any{
			{"thresholdPercent": 0.9},
		},
	}
}

func TestBudgetLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var created Budget
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/billingAccounts/ACC123/budgets", validBudgetBody(), &created)
	if status != 200 || created.Name == "" || created.DisplayName != "my-budget" {
		t.Fatalf("create: status=%d budget=%+v", status, created)
	}

	var list struct {
		Budgets []Budget `json:"budgets"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/billingAccounts/ACC123/budgets", nil, &list)
	if status != 200 || len(list.Budgets) != 1 {
		t.Fatalf("list: status=%d budgets=%+v", status, list.Budgets)
	}

	budgetPath := "/v1/" + created.Name
	var got Budget
	status = testutil.DoJSON(t, "GET", srv.URL+budgetPath, nil, &got)
	if status != 200 || got.Name != created.Name {
		t.Fatalf("get: status=%d budget=%+v", status, got)
	}

	var updated Budget
	status = testutil.DoJSON(t, "PATCH", srv.URL+budgetPath, map[string]any{"displayName": "renamed"}, &updated)
	if status != 200 || updated.DisplayName != "renamed" {
		t.Fatalf("update: status=%d budget=%+v", status, updated)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+budgetPath, nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}

	status = testutil.DoJSON(t, "GET", srv.URL+budgetPath, nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestCreateBudgetRequiresDisplayName(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/billingAccounts/ACC123/budgets", map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("create without displayName: want 400, got %d", status)
	}
}

func TestUpdateBudgetNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "PATCH", srv.URL+"/v1/billingAccounts/ACC123/budgets/missing", map[string]any{}, nil)
	if status != 404 {
		t.Fatalf("update missing budget: want 404, got %d", status)
	}
}
