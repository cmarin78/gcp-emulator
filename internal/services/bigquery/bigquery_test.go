package bigquery

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

// TestDatasetLifecycle covers create -> get -> list -> patch -> delete.
func TestDatasetLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var ds Dataset
	status := testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets", map[string]any{
		"datasetReference": map[string]string{"datasetId": "my_ds"},
	}, &ds)
	if status != 200 || ds.DatasetReference.DatasetID != "my_ds" || ds.Location != "US" {
		t.Fatalf("create: status=%d ds=%+v", status, ds)
	}

	var got Dataset
	status = testutil.DoJSON(t, "GET", srv.URL+"/bigquery/v2/projects/proj1/datasets/my_ds", nil, &got)
	if status != 200 || got.DatasetReference.DatasetID != "my_ds" {
		t.Fatalf("get: status=%d ds=%+v", status, got)
	}

	var list struct {
		Datasets []Dataset `json:"datasets"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/bigquery/v2/projects/proj1/datasets", nil, &list)
	if status != 200 || len(list.Datasets) != 1 {
		t.Fatalf("list: status=%d datasets=%+v", status, list.Datasets)
	}

	var patched Dataset
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/bigquery/v2/projects/proj1/datasets/my_ds", map[string]string{
		"friendlyName": "My Dataset",
	}, &patched)
	if status != 200 || patched.FriendlyName != "My Dataset" {
		t.Fatalf("patch: status=%d ds=%+v", status, patched)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/bigquery/v2/projects/proj1/datasets/my_ds", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestTableLifecycle covers create -> get -> list -> delete under a dataset.
func TestTableLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets", map[string]any{
		"datasetReference": map[string]string{"datasetId": "ds1"},
	}, nil)

	var table Table
	status := testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables", map[string]any{
		"tableReference": map[string]string{"tableId": "my_table"},
		"schema": map[string]any{
			"fields": []map[string]string{{"name": "id", "type": "INTEGER"}},
		},
	}, &table)
	if status != 200 || table.TableReference.TableID != "my_table" || len(table.Schema.Fields) != 1 {
		t.Fatalf("create table: status=%d table=%+v", status, table)
	}

	var got Table
	status = testutil.DoJSON(t, "GET", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables/my_table", nil, &got)
	if status != 200 || got.TableReference.TableID != "my_table" {
		t.Fatalf("get table: status=%d table=%+v", status, got)
	}

	var list struct {
		Tables []Table `json:"tables"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables", nil, &list)
	if status != 200 || len(list.Tables) != 1 {
		t.Fatalf("list tables: status=%d tables=%+v", status, list.Tables)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables/my_table", nil, nil)
	if status != 200 {
		t.Fatalf("delete table: want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating a dataset or table whose
// client-specified ID already exists returns 409 instead of overwriting.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets", map[string]any{
		"datasetReference": map[string]string{"datasetId": "ds1"},
	}, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets", map[string]any{
		"datasetReference": map[string]string{"datasetId": "ds1"},
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate dataset: want 409, got %d", status)
	}

	testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables", map[string]any{
		"tableReference": map[string]string{"tableId": "my_table"},
	}, nil)
	status = testutil.DoJSON(t, "POST", srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables", map[string]any{
		"tableReference": map[string]string{"tableId": "my_table"},
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate table: want 409, got %d", status)
	}
}
