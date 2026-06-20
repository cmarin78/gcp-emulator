package pubsub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
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

// TestTopicSubscribePublishPullAck covers the full Pub/Sub happy path:
// create topic -> create subscription -> publish -> pull -> acknowledge.
func TestTopicSubscribePublishPullAck(t *testing.T) {
	srv := newTestServer(t)

	var topic Topic
	status := testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/my-topic", nil, &topic)
	if status != 200 || topic.Name != "projects/proj1/topics/my-topic" {
		t.Fatalf("create topic: status=%d topic=%+v", status, topic)
	}

	var sub Subscription
	status = testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/my-sub", map[string]any{
		"topic": topic.Name,
	}, &sub)
	if status != 200 || sub.Topic != topic.Name {
		t.Fatalf("create subscription: status=%d sub=%+v", status, sub)
	}

	var publishResp struct {
		MessageIDs []string `json:"messageIds"`
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/topics/my-topic:publish", map[string]any{
		"messages": []map[string]string{{"data": "aGVsbG8="}},
	}, &publishResp)
	if status != 200 || len(publishResp.MessageIDs) != 1 {
		t.Fatalf("publish: status=%d resp=%+v", status, publishResp)
	}

	var pullResp struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/my-sub:pull", map[string]any{
		"maxMessages": 10,
	}, &pullResp)
	if status != 200 || len(pullResp.ReceivedMessages) != 1 {
		t.Fatalf("pull: status=%d resp=%+v", status, pullResp)
	}
	if pullResp.ReceivedMessages[0].Message.Data != "aGVsbG8=" {
		t.Fatalf("pull: unexpected message data %q", pullResp.ReceivedMessages[0].Message.Data)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/my-sub:acknowledge", map[string]any{
		"ackIds": []string{pullResp.ReceivedMessages[0].AckID},
	}, nil)
	if status != 200 {
		t.Fatalf("acknowledge: want 200, got %d", status)
	}

	// Queue should be empty now.
	var secondPull struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/my-sub:pull", nil, &secondPull)
	if len(secondPull.ReceivedMessages) != 0 {
		t.Fatalf("expected empty queue after ack, got %+v", secondPull.ReceivedMessages)
	}
}

func TestTopicAndSubscriptionDelete(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/t1", nil, nil)
	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/s1", map[string]any{
		"topic": "projects/proj1/topics/t1",
	}, nil)

	status := testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/subscriptions/s1", nil, nil)
	if status != 200 {
		t.Fatalf("delete subscription: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/v1/projects/proj1/topics/t1", nil, nil)
	if status != 200 {
		t.Fatalf("delete topic: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", srv.URL+"/v1/projects/proj1/topics/t1", nil, nil)
	if status != 404 {
		t.Fatalf("get deleted topic: want 404, got %d", status)
	}
}

// TestPushSubscriptionDeliversRealHTTP asserts that publishing to a topic
// with a push subscription configured actually delivers a real HTTP POST
// to the pushEndpoint, using the standard Pub/Sub push wire format — the
// Phase 11 behavioral upgrade over pull-only delivery.
func TestPushSubscriptionDeliversRealHTTP(t *testing.T) {
	hit := make(chan map[string]any, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		hit <- body
	}))
	t.Cleanup(endpoint.Close)

	srv := newTestServer(t)
	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/push-topic", nil, nil)

	var sub Subscription
	status := testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/push-sub", map[string]any{
		"topic": "projects/proj1/topics/push-topic",
		"pushConfig": map[string]any{
			"pushEndpoint": endpoint.URL,
		},
	}, &sub)
	if status != 200 || sub.PushConfig == nil || sub.PushConfig.PushEndpoint != endpoint.URL {
		t.Fatalf("create push subscription: status=%d sub=%+v", status, sub)
	}

	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/topics/push-topic:publish", map[string]any{
		"messages": []map[string]string{{"data": "aGVsbG8="}},
	}, nil)

	select {
	case body := <-hit:
		if body["subscription"] != sub.Name {
			t.Fatalf("push body subscription = %v, want %v", body["subscription"], sub.Name)
		}
		msg, ok := body["message"].(map[string]any)
		if !ok || msg["data"] != "aGVsbG8=" {
			t.Fatalf("push body message = %+v", body["message"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for real push delivery")
	}

	// A push subscription should NOT also queue for pull.
	var pullResp struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/push-sub:pull", nil, &pullResp)
	if len(pullResp.ReceivedMessages) != 0 {
		t.Fatalf("push subscription unexpectedly queued for pull: %+v", pullResp.ReceivedMessages)
	}

	// activity.RecordLog/IncrCounter run right after the push HTTP call
	// completes (Fase 11 Logging/Monitoring wiring); poll briefly since it's
	// not synchronized with the channel receive above.
	deadline := time.Now().Add(2 * time.Second)
	for {
		series := activity.ListTimeSeries("proj1", "pubsub.googleapis.com/subscription/push_request_count")
		if len(series) == 1 && len(series[0].Points) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for recorded push_request_count series")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestModifyPushConfigSwitchesDeliveryMode asserts that modifyPushConfig
// can both set and clear a subscription's push delivery.
func TestModifyPushConfigSwitchesDeliveryMode(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/mpc-topic", nil, nil)
	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/mpc-sub", map[string]any{
		"topic": "projects/proj1/topics/mpc-topic",
	}, nil)

	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/mpc-sub:modifyPushConfig", map[string]any{
		"pushConfig": map[string]any{"pushEndpoint": "http://example.invalid/push"},
	}, nil)
	if status != 200 {
		t.Fatalf("modifyPushConfig (set): want 200, got %d", status)
	}

	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/subscriptions/mpc-sub:modifyPushConfig", map[string]any{
		"pushConfig": map[string]any{},
	}, nil)
	if status != 200 {
		t.Fatalf("modifyPushConfig (clear): want 200, got %d", status)
	}
}

// TestDuplicateCreateConflicts asserts that creating a topic or subscription
// whose client-specified name already exists returns 409 ALREADY_EXISTS.
func TestDuplicateCreateConflicts(t *testing.T) {
	srv := newTestServer(t)

	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/dup-topic", nil, nil)
	status := testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/dup-topic", nil, nil)
	if status != 409 {
		t.Fatalf("duplicate topic: want 409, got %d", status)
	}

	testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/dup-sub", map[string]any{
		"topic": "projects/proj1/topics/dup-topic",
	}, nil)
	status = testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/subscriptions/dup-sub", map[string]any{
		"topic": "projects/proj1/topics/dup-topic",
	}, nil)
	if status != 409 {
		t.Fatalf("duplicate subscription: want 409, got %d", status)
	}
}
