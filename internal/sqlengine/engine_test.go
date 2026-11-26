package sqlengine

import (
	"testing"
)

func TestEngineBasicCRUD(t *testing.T) {
	eng := New()

	// 1. CREATE TABLE
	res, err := eng.Execute("CREATE TABLE users (id STRING, name STRING, age INT64, active BOOL)")
	if err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil ExecResult")
	}

	// 2. INSERT INTO
	insertSQL := "INSERT INTO users (id, name, age, active) VALUES ('u1', 'Alice', 30, true), ('u2', 'Bob', 25, false), ('u3', 'Charlie', 35, true)"
	res, err = eng.Execute(insertSQL)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if res.RowsAffected != 3 {
		t.Fatalf("want 3 rows affected, got %d", res.RowsAffected)
	}

	// 3. SELECT *
	qr, err := eng.Query("SELECT * FROM users")
	if err != nil {
		t.Fatalf("select * failed: %v", err)
	}
	if qr.TotalRows != 3 {
		t.Fatalf("want 3 rows, got %d", qr.TotalRows)
	}

	// 4. SELECT with WHERE and projection
	qr, err = eng.Query("SELECT name, age FROM users WHERE age > 26 AND active = true")
	if err != nil {
		t.Fatalf("select where failed: %v", err)
	}
	if qr.TotalRows != 2 {
		t.Fatalf("want 2 rows, got %d", qr.TotalRows)
	}
	maps := qr.ToMaps()
	if maps[0]["name"] != "Alice" || maps[1]["name"] != "Charlie" {
		t.Fatalf("unexpected results: %+v", maps)
	}

	// 5. UPDATE
	updateRes, err := eng.Execute("UPDATE users SET age = 26, active = true WHERE id = 'u2'")
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if updateRes.RowsAffected != 1 {
		t.Fatalf("want 1 row updated, got %d", updateRes.RowsAffected)
	}

	// Verify update
	qr, err = eng.Query("SELECT age, active FROM users WHERE id = 'u2'")
	if err != nil || qr.TotalRows != 1 {
		t.Fatalf("query after update failed: %v qr=%+v", err, qr)
	}
	m := qr.ToMaps()[0]
	if m["age"] != int64(26) || m["active"] != true {
		t.Fatalf("expected updated age=26 active=true, got %+v", m)
	}

	// 6. DELETE
	delRes, err := eng.Execute("DELETE FROM users WHERE id = 'u1'")
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if delRes.RowsAffected != 1 {
		t.Fatalf("want 1 row deleted, got %d", delRes.RowsAffected)
	}

	qr, err = eng.Query("SELECT COUNT(*) as total FROM users")
	if err != nil || qr.TotalRows != 1 {
		t.Fatalf("count failed: %v", err)
	}
	if qr.Rows[0][0] != int64(2) {
		t.Fatalf("want count=2, got %v", qr.Rows[0][0])
	}

	// 7. DROP TABLE
	_, err = eng.Execute("DROP TABLE users")
	if err != nil {
		t.Fatalf("drop table failed: %v", err)
	}
	_, err = eng.Query("SELECT * FROM users")
	if err == nil {
		t.Fatal("expected error querying dropped table, got nil")
	}
}

func TestEngineAggregatesAndGroupBy(t *testing.T) {
	eng := New()
	_ = eng.CreateTable("sales", []Column{
		{Name: "region", Type: "STRING"},
		{Name: "product", Type: "STRING"},
		{Name: "amount", Type: "FLOAT64"},
	})

	_ = eng.InsertRows("sales", []map[string]any{
		{"region": "US", "product": "Widget", "amount": 100.0},
		{"region": "US", "product": "Gadget", "amount": 150.0},
		{"region": "EU", "product": "Widget", "amount": 200.0},
		{"region": "EU", "product": "Gadget", "amount": 300.0},
		{"region": "US", "product": "Widget", "amount": 50.0},
	})

	// Global Aggregates
	qr, err := eng.Query("SELECT COUNT(*) as cnt, SUM(amount) as total, AVG(amount) as avg_amt, MIN(amount) as min_amt, MAX(amount) as max_amt FROM sales")
	if err != nil {
		t.Fatalf("global aggregates failed: %v", err)
	}
	if qr.TotalRows != 1 {
		t.Fatalf("expected 1 row, got %d", qr.TotalRows)
	}
	m := qr.ToMaps()[0]
	if m["cnt"] != int64(5) {
		t.Fatalf("want cnt=5, got %v", m["cnt"])
	}
	if m["total"] != int64(800) && m["total"] != 800.0 {
		t.Fatalf("want total=800, got %v", m["total"])
	}
	if m["avg_amt"] != 160.0 {
		t.Fatalf("want avg=160, got %v", m["avg_amt"])
	}

	// GROUP BY region with ORDER BY total DESC
	qr, err = eng.Query("SELECT region, SUM(amount) as total FROM sales GROUP BY region ORDER BY total DESC")
	if err != nil {
		t.Fatalf("group by failed: %v", err)
	}
	if qr.TotalRows != 2 {
		t.Fatalf("expected 2 groups, got %d", qr.TotalRows)
	}
	groups := qr.ToMaps()
	if groups[0]["region"] != "EU" || (groups[0]["total"] != int64(500) && groups[0]["total"] != 500.0) {
		t.Fatalf("expected EU with 500 first, got %+v", groups[0])
	}
	if groups[1]["region"] != "US" || (groups[1]["total"] != int64(300) && groups[1]["total"] != 300.0) {
		t.Fatalf("expected US with 300 second, got %+v", groups[1])
	}
}

func TestEngineConditionsAndOperators(t *testing.T) {
	eng := New()
	_ = eng.CreateTable("items", []Column{
		{Name: "code", Type: "STRING"},
		{Name: "name", Type: "STRING"},
		{Name: "qty", Type: "INT64"},
	})

	_ = eng.InsertRows("items", []map[string]any{
		{"code": "A10", "name": "Apple", "qty": 10},
		{"code": "B20", "name": "Banana", "qty": 0},
		{"code": "C30", "name": "Cherry", "qty": 50},
		{"code": "D40", "name": "Dragonfruit", "qty": nil},
	})

	// LIKE
	qr, err := eng.Query("SELECT code, name FROM items WHERE name LIKE 'A%' OR name LIKE '%fruit'")
	if err != nil {
		t.Fatalf("like query failed: %v", err)
	}
	if qr.TotalRows != 2 {
		t.Fatalf("expected 2 rows for LIKE, got %d", qr.TotalRows)
	}

	// IN
	qr, err = eng.Query("SELECT code FROM items WHERE code IN ('A10', 'C30')")
	if err != nil || qr.TotalRows != 2 {
		t.Fatalf("IN query failed: %v, total=%d", err, qr.TotalRows)
	}

	// IS NULL / IS NOT NULL
	qr, err = eng.Query("SELECT code FROM items WHERE qty IS NULL")
	if err != nil || qr.TotalRows != 1 || qr.ToMaps()[0]["code"] != "D40" {
		t.Fatalf("IS NULL failed: %v, qr=%+v", err, qr)
	}

	qr, err = eng.Query("SELECT code FROM items WHERE qty IS NOT NULL ORDER BY qty DESC LIMIT 2")
	if err != nil || qr.TotalRows != 2 {
		t.Fatalf("IS NOT NULL with LIMIT failed: %v", err)
	}
	if qr.ToMaps()[0]["code"] != "C30" || qr.ToMaps()[1]["code"] != "A10" {
		t.Fatalf("unexpected order: %+v", qr.ToMaps())
	}
}
