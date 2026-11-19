package storage

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSnapshotExportAndImport(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Put("test.bucket", "item1", map[string]string{"foo": "bar"}); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// 1. Binary snapshot
	var buf bytes.Buffer
	if err := db.WriteSnapshot(&buf); err != nil {
		t.Fatalf("WriteSnapshot failed: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty snapshot buffer")
	}

	// 2. Export JSON
	exported, err := db.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}
	if len(exported["test.bucket"]) != 1 {
		t.Fatalf("expected 1 item in test.bucket, got %d", len(exported["test.bucket"]))
	}

	// 3. Import JSON into another db
	db2, err := Open(filepath.Join(dir, "test2.db"))
	if err != nil {
		t.Fatalf("Open db2 failed: %v", err)
	}
	defer db2.Close()

	if err := db2.ImportJSON(exported); err != nil {
		t.Fatalf("ImportJSON failed: %v", err)
	}

	var res map[string]string
	found, err := db2.Get("test.bucket", "item1", &res)
	if err != nil || !found {
		t.Fatalf("expected item1 in db2: found=%v, err=%v", found, err)
	}
	if res["foo"] != "bar" {
		t.Fatalf("expected bar, got %s", res["foo"])
	}
}
