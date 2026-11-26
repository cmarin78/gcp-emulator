package spanner

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

// TestInstanceLifecycle covers create -> get -> list -> patch -> delete.
func TestInstanceLifecycle(t *testing.T) {
	srv := newTestServer(t)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{
		"instanceId": "my-instance",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create: status=%d op=%+v", status, createOp)
	}

	var got Instance
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, &got)
	if status != 200 || got.State != "READY" || got.Config != "regional-us-central1" || got.ProcessingUnits != 100 {
		t.Fatalf("get: status=%d inst=%+v", status, got)
	}

	var list struct {
		Instances []Instance `json:"instances"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances", nil, &list)
	if status != 200 || len(list.Instances) != 1 {
		t.Fatalf("list: status=%d instances=%+v", status, list.Instances)
	}

	var patchOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/instances/my-instance", map[string]any{
		"instance": map[string]any{"nodeCount": 3},
	}, &patchOp)
	if status != 200 || !patchOp.Done {
		t.Fatalf("patch: status=%d op=%+v", status, patchOp)
	}
	var patched Instance
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, &patched)
	if patched.NodeCount != 3 {
		t.Fatalf("expected nodeCount updated to 3, got %d", patched.NodeCount)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/instances/my-instance", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
}

// TestDatabaseLifecycle covers create (via CREATE DATABASE DDL statement) ->
// get -> list -> updateDdl -> delete, asserting the database name is parsed
// correctly out of the createStatement.
func TestDatabaseLifecycle(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)

	var createOp Operation
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "CREATE DATABASE `mydb`",
	}, &createOp)
	if status != 200 || !createOp.Done {
		t.Fatalf("create database: status=%d op=%+v", status, createOp)
	}

	var got Database
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, &got)
	if status != 200 || got.State != "READY" || got.DatabaseDialect != "GOOGLE_STANDARD_SQL" {
		t.Fatalf("get database: status=%d db=%+v", status, got)
	}

	var list struct {
		Databases []Database `json:"databases"`
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases", nil, &list)
	if status != 200 || len(list.Databases) != 1 {
		t.Fatalf("list databases: status=%d dbs=%+v", status, list.Databases)
	}

	var ddlOp Operation
	status = testutil.DoJSON(t, "PATCH", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb/ddl", map[string]any{
		"statements": []string{"ALTER TABLE foo ADD COLUMN bar STRING(MAX)"},
	}, &ddlOp)
	if status != 200 || !ddlOp.Done {
		t.Fatalf("update ddl: status=%d op=%+v", status, ddlOp)
	}
	var afterDdl Database
	testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, &afterDdl)
	if len(afterDdl.ExtraStatements) != 1 {
		t.Fatalf("expected extraStatements appended, got %+v", afterDdl.ExtraStatements)
	}

	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/instances/inst1/databases/mydb", nil, nil)
	if status != 200 {
		t.Fatalf("delete database: want 200, got %d", status)
	}
}

func TestCreateDatabaseRequiresValidCreateStatement(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "not a valid statement",
	}, nil)
	if status != 400 {
		t.Fatalf("invalid createStatement: want 400, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating an instance or database
// whose client-specified name already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances", map[string]any{"instanceId": "inst1"}, nil)
	if status != 409 {
		t.Fatalf("duplicate instance: want 409, got %d", status)
	}

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "CREATE DATABASE `mydb`",
	}, nil)
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/instances/inst1/databases", map[string]any{
		"createStatement": "CREATE DATABASE `mydb`",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate database: want 409, got %d", status)
	}
}

func TestSpannerDataPlaneAndExecuteSql(t *testing.T) {
	srv := newTestServer(t)

	// Create Instance and Database with initial DDL
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/p1/instances", map[string]any{"instanceId": "i1"}, nil)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/p1/instances/i1/databases", map[string]any{
		"createStatement": "CREATE DATABASE `musicdb`",
		"extraStatements": []string{
			"CREATE TABLE Singers (SingerId INT64, FirstName STRING, LastName STRING)",
		},
	}, nil)

	dbURL := srv.URL + "/v1/projects/p1/instances/i1/databases/musicdb"

	// 1. Create Session
	var sess Session
	status := testutil.DoJSON(t, "POST", dbURL+"/sessions", nil, &sess)
	if status != 200 || sess.Name == "" {
		t.Fatalf("create session failed: status=%d sess=%+v", status, sess)
	}

	// 2. Insert rows via session:executeSql (DML)
	insertSQL := "INSERT INTO Singers (SingerId, FirstName, LastName) VALUES (1, 'Marc', 'Richards'), (2, 'Catalina', 'Smith')"
	var dmlResp struct {
		Stats struct {
			RowCountExact string `json:"rowCountExact"`
		} `json:"stats"`
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/"+sess.Name+":executeSql", map[string]any{
		"sql": insertSQL,
	}, &dmlResp)
	if status != 200 || dmlResp.Stats.RowCountExact != "2" {
		t.Fatalf("executeSql DML failed: status=%d resp=%+v", status, dmlResp)
	}

	// 3. Query rows via session:executeSql (SELECT)
	var selectResp struct {
		Metadata struct {
			RowType struct {
				Fields []struct {
					Name string `json:"name"`
				} `json:"fields"`
			} `json:"rowType"`
		} `json:"metadata"`
		Rows [][]any `json:"rows"`
	}
	querySQL := "SELECT SingerId, FirstName, LastName FROM Singers WHERE SingerId = 1"
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/"+sess.Name+":executeSql", map[string]any{
		"sql": querySQL,
	}, &selectResp)
	if status != 200 {
		t.Fatalf("executeSql SELECT failed: status=%d", status)
	}
	if len(selectResp.Rows) != 1 || selectResp.Rows[0][1] != "Marc" {
		t.Fatalf("unexpected select results: %+v", selectResp)
	}

	// 4. Batch DML
	var batchResp struct {
		ResultSets []map[string]any `json:"resultSets"`
		Status     map[string]any   `json:"status"`
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/"+sess.Name+":executeBatchDml", map[string]any{
		"statements": []map[string]string{
			{"sql": "UPDATE Singers SET LastName = 'Johnson' WHERE SingerId = 1"},
		},
	}, &batchResp)
	if status != 200 || len(batchResp.ResultSets) != 1 {
		t.Fatalf("batch DML failed: status=%d resp=%+v", status, batchResp)
	}

	// 5. Transactions (beginTransaction, commit)
	var txResp map[string]any
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/"+sess.Name+":beginTransaction", nil, &txResp)
	if status != 200 || txResp["id"] == "" {
		t.Fatalf("beginTransaction failed: status=%d resp=%+v", status, txResp)
	}

	var commitResp map[string]any
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/"+sess.Name+":commit", nil, &commitResp)
	if status != 200 || commitResp["commitTimestamp"] == "" {
		t.Fatalf("commit failed: status=%d resp=%+v", status, commitResp)
	}

	// 6. Delete Session
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/"+sess.Name, nil, nil)
	if status != 200 {
		t.Fatalf("delete session failed: status=%d", status)
	}
}
