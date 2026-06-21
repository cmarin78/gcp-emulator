package eventarc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// httpEndpointTriggerBody builds a trigger pointed at an httpEndpoint
// destination (an arbitrary URL, the same real mechanism Cloud
// Scheduler/Tasks use), which is what makes real delivery deterministically
// testable without depending on cloudrun's synthesized, non-resolvable
// a.run.app-style URI.
func httpEndpointTriggerBody(filterValue, destURL string) map[string]any {
	return map[string]any{
		"eventFilters": []map[string]any{
			{"attribute": "type", "value": filterValue},
		},
		"destination": map[string]any{
			"httpEndpoint": map[string]any{"uri": destURL},
		},
	}
}

// TestPublishEventDeliversOnMatch covers the core Phase 11 behavior: a
// CloudEvent whose "type" matches the trigger's eventFilters results in a
// real, observable HTTP POST to the resolved destination.
func TestPublishEventDeliversOnMatch(t *testing.T) {
	var mu sync.Mutex
	var gotBody map[string]any
	var gotHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(204)
	}))
	defer backend.Close()

	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=t1",
		httpEndpointTriggerBody("google.cloud.storage.object.v1.finalized", backend.URL), nil)

	var result PublishResult
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/t1:publishEvent",
		map[string]any{
			"id":     "evt-1",
			"source": "//storage.googleapis.com/projects/_/buckets/my-bucket",
			"type":   "google.cloud.storage.object.v1.finalized",
			"data":   map[string]any{"bucket": "my-bucket", "name": "object.txt"},
		}, &result)
	if status != 200 {
		t.Fatalf("publishEvent: want 200, got %d", status)
	}
	if !result.Matched || !result.Delivered {
		t.Fatalf("expected matched+delivered, got %+v", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotBody["bucket"] != "my-bucket" {
		t.Fatalf("backend did not receive expected data payload, got %+v", gotBody)
	}
	if gotHeaders.Get("ce-type") != "google.cloud.storage.object.v1.finalized" {
		t.Fatalf("expected ce-type CloudEvents header, got %q", gotHeaders.Get("ce-type"))
	}
	if gotHeaders.Get("ce-id") != "evt-1" {
		t.Fatalf("expected ce-id CloudEvents header, got %q", gotHeaders.Get("ce-id"))
	}
}

// TestPublishEventSkipsDeliveryWhenFiltersDontMatch asserts that an event
// whose "type" doesn't match the trigger's eventFilters never reaches the
// destination at all.
func TestPublishEventSkipsDeliveryWhenFiltersDontMatch(t *testing.T) {
	delivered := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = true
	}))
	defer backend.Close()

	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=t1",
		httpEndpointTriggerBody("google.cloud.storage.object.v1.finalized", backend.URL), nil)

	var result PublishResult
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/t1:publishEvent",
		map[string]any{
			"id":     "evt-2",
			"source": "//pubsub.googleapis.com/...",
			"type":   "google.cloud.pubsub.topic.v1.messagePublished",
		}, &result)
	if status != 200 {
		t.Fatalf("publishEvent: want 200, got %d", status)
	}
	if result.Matched {
		t.Fatalf("expected matched=false for a non-matching event, got %+v", result)
	}
	if delivered {
		t.Fatal("backend should not have received a request for a non-matching event")
	}
}

// TestPublishEventUnknownTriggerNotFound covers the 404 path.
func TestPublishEventUnknownTriggerNotFound(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/missing:publishEvent",
		map[string]any{"type": "x"}, nil)
	if status != 404 {
		t.Fatalf("publishEvent on missing trigger: want 404, got %d", status)
	}
}

// TestMatchPathPatternOperator covers the match-path-pattern operator
// against the literal "*"/"**" segment semantics Eventarc uses.
func TestMatchPathPatternOperator(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"projects/*/buckets/*", "projects/p1/buckets/b1", true},
		{"projects/*/buckets/*", "projects/p1/buckets/b1/objects/o1", false},
		{"projects/p1/buckets/**", "projects/p1/buckets/b1/objects/o1", true},
		{"projects/p2/buckets/*", "projects/p1/buckets/b1", false},
	}
	for _, c := range cases {
		if got := matchPathPattern(c.pattern, c.value); got != c.want {
			t.Errorf("matchPathPattern(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

// TestPublishEventDeliveryFailureReportedNotDelivered covers a destination
// that's unreachable (closed server): the publish call itself still
// succeeds (200), but reports delivered=false, matching Pub/Sub push's
// "no retries, report what happened" precedent.
func TestPublishEventDeliveryFailureReportedNotDelivered(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := backend.URL
	backend.Close() // ya cerrado: nadie escucha en esa URL

	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers?triggerId=t1",
		httpEndpointTriggerBody("my.event.type", deadURL), nil)

	var result PublishResult
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/triggers/t1:publishEvent",
		map[string]any{"type": "my.event.type"}, &result)
	if status != 200 {
		t.Fatalf("publishEvent: want 200, got %d", status)
	}
	if !result.Matched || result.Delivered {
		t.Fatalf("expected matched=true, delivered=false for an unreachable destination, got %+v", result)
	}
}
