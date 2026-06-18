package pubsub

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
