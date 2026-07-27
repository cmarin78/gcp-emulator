package firestore

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in firestore_test.go (same package).

// createTestDoc creates a document with typed Firestore fields.
func createTestDoc(t *testing.T, srvURL, collection, docID string, fields map[string]any) {
	t.Helper()
	url := srvURL + "/v1/projects/proj1/databases/(default)/documents/" + collection + "?documentId=" + docID
	status := testutil.DoJSON(t, "POST", url, map[string]any{"fields": fields}, nil)
	if status != 200 {
		t.Fatalf("createTestDoc %s/%s: got %d", collection, docID, status)
	}
}

// ── runQuery tests ────────────────────────────────────────────────────────────

func TestRunQueryReturnsAllDocuments(t *testing.T) {
	srv := newTestServer(t)

	// Create 3 users.
	for _, id := range []string{"u1", "u2", "u3"} {
		createTestDoc(t, srv.URL, "users", id, map[string]any{
			"name": map[string]any{"stringValue": "user-" + id},
		})
	}

	var results []QueryResult
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents:runQuery",
		map[string]any{
			"structuredQuery": map[string]any{
				"from": []map[string]any{{"collectionId": "users"}},
			},
		}, &results)
	if status != 200 {
		t.Fatalf("runQuery: want 200, got %d", status)
	}
	if len(results) != 3 {
		t.Fatalf("runQuery: want 3 results, got %d", len(results))
	}
	if results[0].Document == nil {
		t.Fatalf("runQuery: first result has no document")
	}
}

func TestRunQueryWithFieldFilter(t *testing.T) {
	srv := newTestServer(t)

	createTestDoc(t, srv.URL, "orders", "o1", map[string]any{
		"status": map[string]any{"stringValue": "active"},
	})
	createTestDoc(t, srv.URL, "orders", "o2", map[string]any{
		"status": map[string]any{"stringValue": "closed"},
	})
	createTestDoc(t, srv.URL, "orders", "o3", map[string]any{
		"status": map[string]any{"stringValue": "active"},
	})

	var results []QueryResult
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents:runQuery",
		map[string]any{
			"structuredQuery": map[string]any{
				"from": []map[string]any{{"collectionId": "orders"}},
				"where": map[string]any{
					"fieldFilter": map[string]any{
						"field": map[string]any{"fieldPath": "status"},
						"op":    "EQUAL",
						"value": map[string]any{"stringValue": "active"},
					},
				},
			},
		}, &results)
	if status != 200 {
		t.Fatalf("runQuery filter: want 200, got %d", status)
	}
	if len(results) != 2 {
		t.Fatalf("runQuery EQUAL filter: want 2 results (active), got %d", len(results))
	}
}

func TestRunQueryWithLimit(t *testing.T) {
	srv := newTestServer(t)

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		createTestDoc(t, srv.URL, "items", id, map[string]any{
			"v": map[string]any{"stringValue": id},
		})
	}

	limit := 3
	var results []QueryResult
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents:runQuery",
		map[string]any{
			"structuredQuery": map[string]any{
				"from":  []map[string]any{{"collectionId": "items"}},
				"limit": limit,
			},
		}, &results)
	if status != 200 {
		t.Fatalf("runQuery limit: want 200, got %d", status)
	}
	if len(results) != 3 {
		t.Fatalf("runQuery LIMIT 3: want 3 results, got %d", len(results))
	}
}

func TestRunQueryMissingFromReturns400(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST",
		srv.URL+"/v1/projects/proj1/databases/(default)/documents:runQuery",
		map[string]any{"structuredQuery": map[string]any{}}, nil)
	if status != 400 {
		t.Fatalf("runQuery no from: want 400, got %d", status)
	}
}

// ── subcollection tests ───────────────────────────────────────────────────────

func TestSubcollectionCRUD(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/databases/(default)/documents"

	// Create parent document.
	createTestDoc(t, srv.URL, "orders", "order-1", map[string]any{
		"total": map[string]any{"integerValue": "100"},
	})

	// Create subcollection document: orders/order-1/items/item-1.
	var item Document
	status := testutil.DoJSON(t, "POST", base+"/orders/order-1/items?documentId=item-1",
		map[string]any{
			"fields": map[string]any{
				"sku": map[string]any{"stringValue": "ABC"},
			},
		}, &item)
	if status != 200 || item.Name == "" {
		t.Fatalf("create subitem: status=%d doc=%+v", status, item)
	}

	// Get the subdocument.
	var got Document
	status = testutil.DoJSON(t, "GET", base+"/orders/order-1/items/item-1", nil, &got)
	if status != 200 || got.Name != item.Name {
		t.Fatalf("get subitem: status=%d doc=%+v", status, got)
	}

	// List the subcollection.
	var list struct {
		Documents []Document `json:"documents"`
	}
	status = testutil.DoJSON(t, "GET", base+"/orders/order-1/items", nil, &list)
	if status != 200 || len(list.Documents) != 1 {
		t.Fatalf("list subitems: status=%d docs=%+v", status, list.Documents)
	}

	// Patch the subdocument.
	status = testutil.DoJSON(t, "PATCH", base+"/orders/order-1/items/item-1",
		map[string]any{"fields": map[string]any{"sku": map[string]any{"stringValue": "XYZ"}}}, nil)
	if status != 200 {
		t.Fatalf("patch subitem: want 200, got %d", status)
	}

	// Delete the subdocument.
	status = testutil.DoJSON(t, "DELETE", base+"/orders/order-1/items/item-1", nil, nil)
	if status != 200 {
		t.Fatalf("delete subitem: want 200, got %d", status)
	}

	// Verify it's gone.
	status = testutil.DoJSON(t, "GET", base+"/orders/order-1/items/item-1", nil, nil)
	if status != 404 {
		t.Fatalf("get deleted subitem: want 404, got %d", status)
	}
}

func TestSubcollectionDoesNotConflictWithTopLevel(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/databases/(default)/documents"

	// Top-level document.
	createTestDoc(t, srv.URL, "products", "p1", map[string]any{
		"name": map[string]any{"stringValue": "widget"},
	})

	// Top-level list should only return top-level docs.
	var list struct {
		Documents []Document `json:"documents"`
	}
	testutil.DoJSON(t, "GET", base+"/products", nil, &list)
	if len(list.Documents) != 1 {
		t.Fatalf("top-level list: want 1 doc, got %d", len(list.Documents))
	}

	// Create subcollection doc under p1.
	testutil.DoJSON(t, "POST", base+"/products/p1/reviews?documentId=r1",
		map[string]any{"fields": map[string]any{"score": map[string]any{"integerValue": "5"}}}, nil)

	// Top-level list still 1.
	testutil.DoJSON(t, "GET", base+"/products", nil, &list)
	if len(list.Documents) != 1 {
		t.Fatalf("top-level list after subcollection create: want 1, got %d", len(list.Documents))
	}
}
