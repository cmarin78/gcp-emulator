package bigquery

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in bigquery_test.go (same package).

// ── helpers ───────────────────────────────────────────────────────────────────

func createDatasetAndTable(t *testing.T, srvURL, project, dataset, tableID string, fields []map[string]any) {
	t.Helper()
	base := srvURL + "/bigquery/v2/projects/" + project

	testutil.DoJSON(t, "POST", base+"/datasets", map[string]any{
		"datasetReference": map[string]any{"datasetId": dataset},
	}, nil)

	testutil.DoJSON(t, "POST", base+"/datasets/"+dataset+"/tables", map[string]any{
		"tableReference": map[string]any{"tableId": tableID},
		"schema":         map[string]any{"fields": fields},
	}, nil)
}

// ── insertAll / tabledata.list ─────────────────────────────────────────────────

func TestTabledataInsertAllAndList(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/bigquery/v2/projects/proj1"

	fields := []map[string]any{
		{"name": "id", "type": "STRING"},
		{"name": "val", "type": "INTEGER"},
	}
	createDatasetAndTable(t, srv.URL,"proj1", "ds1", "tbl1", fields)

	// Insert rows.
	status := testutil.DoJSON(t, "POST", base+"/datasets/ds1/tables/tbl1/insertAll", map[string]any{
		"rows": []map[string]any{
			{"insertId": "r1", "json": map[string]any{"id": "a", "val": 1}},
			{"insertId": "r2", "json": map[string]any{"id": "b", "val": 2}},
		},
	}, nil)
	if status != 200 {
		t.Fatalf("insertAll: want 200, got %d", status)
	}

	// List rows.
	var listResp struct {
		TotalRows string           `json:"totalRows"`
		Rows      []QueryResultRow `json:"rows"`
	}
	status = testutil.DoJSON(t, "GET", base+"/datasets/ds1/tables/tbl1/data", nil, &listResp)
	if status != 200 {
		t.Fatalf("tabledata.list: want 200, got %d", status)
	}
	if listResp.TotalRows != "2" {
		t.Fatalf("totalRows: want 2, got %q", listResp.TotalRows)
	}
	if len(listResp.Rows) != 2 {
		t.Fatalf("rows count: want 2, got %d", len(listResp.Rows))
	}
	// Each row should have 2 cells (id, val in schema order).
	if len(listResp.Rows[0].F) != 2 {
		t.Fatalf("row[0] cells: want 2, got %d", len(listResp.Rows[0].F))
	}
}

func TestTabledataInsertAllUnknownTableReturns404(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/bigquery/v2/projects/proj1/datasets/ds1/tables/nosuch/insertAll",
		map[string]any{"rows": []any{}}, nil)
	if status != 404 {
		t.Fatalf("insertAll missing table: want 404, got %d", status)
	}
}

// ── jobs.insert + jobs.get ────────────────────────────────────────────────────

func TestJobsInsertAndGet(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/bigquery/v2/projects/proj1"

	fields := []map[string]any{{"name": "name", "type": "STRING"}}
	createDatasetAndTable(t, srv.URL,"proj1", "ds2", "events", fields)
	testutil.DoJSON(t, "POST", base+"/datasets/ds2/tables/events/insertAll", map[string]any{
		"rows": []map[string]any{
			{"json": map[string]any{"name": "click"}},
		},
	}, nil)

	// Insert job.
	var job Job
	status := testutil.DoJSON(t, "POST", base+"/jobs", map[string]any{
		"configuration": map[string]any{
			"jobType": "QUERY",
			"query": map[string]any{
				"query":          "SELECT * FROM ds2.events",
				"defaultDataset": map[string]any{"datasetId": "ds2"},
				"useLegacySql":   false,
			},
		},
	}, &job)
	if status != 200 || job.Status.State != "DONE" {
		t.Fatalf("jobs.insert: status=%d job=%+v", status, job)
	}

	// Get job.
	var got Job
	status = testutil.DoJSON(t, "GET", base+"/jobs/"+job.JobReference.JobID, nil, &got)
	if status != 200 || got.Status.State != "DONE" {
		t.Fatalf("jobs.get: status=%d job=%+v", status, got)
	}
}

// ── jobs.query (synchronous) ──────────────────────────────────────────────────

func TestJobsQuerySynchronous(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/bigquery/v2/projects/proj1"

	fields := []map[string]any{
		{"name": "k", "type": "STRING"},
		{"name": "v", "type": "INTEGER"},
	}
	createDatasetAndTable(t, srv.URL,"proj1", "ds3", "metrics", fields)
	testutil.DoJSON(t, "POST", base+"/datasets/ds3/tables/metrics/insertAll", map[string]any{
		"rows": []map[string]any{
			{"json": map[string]any{"k": "cpu", "v": 42}},
			{"json": map[string]any{"k": "mem", "v": 80}},
			{"json": map[string]any{"k": "disk", "v": 60}},
		},
	}, nil)

	var qr struct {
		JobComplete bool             `json:"jobComplete"`
		TotalRows   string           `json:"totalRows"`
		Rows        []QueryResultRow `json:"rows"`
	}
	status := testutil.DoJSON(t, "POST", base+"/queries", map[string]any{
		"query":          "SELECT * FROM ds3.metrics LIMIT 2",
		"defaultDataset": map[string]any{"datasetId": "ds3"},
		"useLegacySql":   false,
	}, &qr)
	if status != 200 || !qr.JobComplete {
		t.Fatalf("jobs.query: status=%d resp=%+v", status, qr)
	}
	if qr.TotalRows != "2" {
		t.Fatalf("jobs.query LIMIT 2: want totalRows=2, got %q", qr.TotalRows)
	}
	if len(qr.Rows) != 2 {
		t.Fatalf("jobs.query LIMIT 2: want 2 rows, got %d", len(qr.Rows))
	}
}

func TestJobsQueryMissingQueryReturns400(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/bigquery/v2/projects/proj1/queries",
		map[string]any{}, nil)
	if status != 400 {
		t.Fatalf("jobs.query no query: want 400, got %d", status)
	}
}

// ── jobs.getQueryResults ──────────────────────────────────────────────────────

func TestJobsGetQueryResults(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/bigquery/v2/projects/proj1"

	fields := []map[string]any{{"name": "x", "type": "INTEGER"}}
	createDatasetAndTable(t, srv.URL,"proj1", "ds4", "nums", fields)
	testutil.DoJSON(t, "POST", base+"/datasets/ds4/tables/nums/insertAll", map[string]any{
		"rows": []map[string]any{
			{"json": map[string]any{"x": 10}},
			{"json": map[string]any{"x": 20}},
		},
	}, nil)

	// Create a job whose results we'll retrieve.
	var job Job
	testutil.DoJSON(t, "POST", base+"/jobs", map[string]any{
		"configuration": map[string]any{
			"query": map[string]any{
				"query": "SELECT * FROM ds4.nums",
			},
		},
	}, &job)

	var qr struct {
		JobComplete bool             `json:"jobComplete"`
		TotalRows   string           `json:"totalRows"`
		Rows        []QueryResultRow `json:"rows"`
	}
	status := testutil.DoJSON(t, "GET", base+"/queries/"+job.JobReference.JobID, nil, &qr)
	if status != 200 || !qr.JobComplete {
		t.Fatalf("getQueryResults: status=%d resp=%+v", status, qr)
	}
	if qr.TotalRows != "2" {
		t.Fatalf("getQueryResults: want totalRows=2, got %q", qr.TotalRows)
	}
}
