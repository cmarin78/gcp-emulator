package gcs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

func TestNotificationConfigsCRUD(t *testing.T) {
	srv := newTestServer(t)

	// Create bucket
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b", map[string]string{"name": "notif-bucket"}, nil)

	// List initially empty
	var listResp struct {
		Kind  string               `json:"kind"`
		Items []NotificationConfig `json:"items"`
	}
	status := testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs", nil, &listResp)
	if status != 200 {
		t.Fatalf("list notificationConfigs: want 200, got %d", status)
	}
	if len(listResp.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(listResp.Items))
	}

	// Create notification config
	createBody := map[string]any{
		"topic":              "//pubsub.googleapis.com/projects/my-proj/topics/gcs-topic",
		"event_types":        []string{"OBJECT_FINALIZE", "OBJECT_DELETE"},
		"payload_format":     "JSON_API_V1",
		"object_name_prefix": "uploads/",
	}
	var created NotificationConfig
	status = testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs", createBody, &created)
	if status != 200 {
		t.Fatalf("create notificationConfig: want 200, got %d", status)
	}
	if created.ID == "" || created.Topic != createBody["topic"] || created.ObjectNamePrefix != "uploads/" {
		t.Fatalf("created unexpected notificationConfig: %+v", created)
	}

	// Get by ID
	var fetched NotificationConfig
	status = testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs/"+created.ID, nil, &fetched)
	if status != 200 || fetched.ID != created.ID {
		t.Fatalf("get notificationConfig: status=%d config=%+v", status, fetched)
	}

	// List now contains 1 item
	status = testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs", nil, &listResp)
	if status != 200 || len(listResp.Items) != 1 {
		t.Fatalf("list after create: want 1 item, got %d", len(listResp.Items))
	}

	// Delete
	status = testutil.DoJSON(t, "DELETE", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs/"+created.ID, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete notificationConfig: want 204, got %d", status)
	}

	// Get after delete returns 404
	status = testutil.DoJSON(t, "GET", srv.URL+"/storage/v1/b/notif-bucket/notificationConfigs/"+created.ID, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestGCSObjectLifecycleTriggersCallbacks(t *testing.T) {
	db := testutil.NewDB(t)
	svc := New(db)
	mux := http.NewServeMux()
	svc.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mu sync.Mutex
	type pubsubCall struct {
		Project    string
		Topic      string
		Data       []byte
		Attributes map[string]string
	}
	var pubsubCalls []pubsubCall
	var cloudevents []CloudEvent

	svc.SetPubSubPublisher(func(project, topic string, data []byte, attributes map[string]string) error {
		mu.Lock()
		defer mu.Unlock()
		pubsubCalls = append(pubsubCalls, pubsubCall{
			Project:    project,
			Topic:      topic,
			Data:       data,
			Attributes: attributes,
		})
		return nil
	})

	svc.SetCloudEventPublisher(func(e CloudEvent) error {
		mu.Lock()
		defer mu.Unlock()
		cloudevents = append(cloudevents, e)
		return nil
	})

	// Create bucket
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b", map[string]string{"name": "test-bkt"}, nil)

	// Configure notification config
	testutil.DoJSON(t, "POST", srv.URL+"/storage/v1/b/test-bkt/notificationConfigs", map[string]any{
		"topic":              "//pubsub.googleapis.com/projects/my-proj/topics/gcs-topic",
		"event_types":        []string{"OBJECT_FINALIZE", "OBJECT_DELETE"},
		"payload_format":     "JSON_API_V1",
		"object_name_prefix": "data/",
	}, nil)

	// 1. Upload object that does NOT match prefix -> should not trigger Pub/Sub, but still triggers CloudEvents
	uploadURL := srv.URL + "/upload/storage/v1/b/test-bkt/o?name=other.txt"
	req, _ := http.NewRequest("POST", uploadURL, strings.NewReader("other content"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload other.txt failed: resp=%+v err=%v", resp, err)
	}
	resp.Body.Close()

	mu.Lock()
	if len(pubsubCalls) != 0 {
		t.Fatalf("expected 0 pubsub calls for mismatched prefix, got %d", len(pubsubCalls))
	}
	if len(cloudevents) != 1 || cloudevents[0].Type != "google.cloud.storage.object.v1.finalized" {
		t.Fatalf("expected 1 cloudevent for upload, got %+v", cloudevents)
	}
	mu.Unlock()

	// 2. Upload object that matches prefix -> should trigger Pub/Sub and Eventarc
	uploadURL = srv.URL + "/upload/storage/v1/b/test-bkt/o?name=data/file.csv"
	req, _ = http.NewRequest("POST", uploadURL, strings.NewReader("a,b,c"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload data/file.csv failed: resp=%+v err=%v", resp, err)
	}
	resp.Body.Close()

	mu.Lock()
	if len(pubsubCalls) != 1 {
		t.Fatalf("expected 1 pubsub call, got %d", len(pubsubCalls))
	}
	call := pubsubCalls[0]
	if call.Project != "my-proj" || call.Topic != "gcs-topic" {
		t.Fatalf("unexpected pubsub target: project=%s topic=%s", call.Project, call.Topic)
	}
	if call.Attributes["eventType"] != "OBJECT_FINALIZE" || call.Attributes["objectId"] != "data/file.csv" {
		t.Fatalf("unexpected pubsub attributes: %+v", call.Attributes)
	}
	var uploadedObj Object
	if err := json.Unmarshal(call.Data, &uploadedObj); err != nil || uploadedObj.Name != "data/file.csv" {
		t.Fatalf("unexpected payload data: err=%v obj=%+v", err, uploadedObj)
	}
	if len(cloudevents) != 2 || cloudevents[1].Attributes["object"] != "data/file.csv" {
		t.Fatalf("unexpected cloudevents count: got %d", len(cloudevents))
	}
	mu.Unlock()

	// 3. Delete object -> should trigger OBJECT_DELETE
	delReq, _ := http.NewRequest("DELETE", srv.URL+"/storage/v1/b/test-bkt/o/data/file.csv", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete data/file.csv failed: resp=%+v err=%v", delResp, err)
	}
	delResp.Body.Close()

	mu.Lock()
	if len(pubsubCalls) != 2 {
		t.Fatalf("expected 2 pubsub calls after delete, got %d", len(pubsubCalls))
	}
	delCall := pubsubCalls[1]
	if delCall.Attributes["eventType"] != "OBJECT_DELETE" || delCall.Attributes["objectId"] != "data/file.csv" {
		t.Fatalf("unexpected delete pubsub attributes: %+v", delCall.Attributes)
	}
	if len(cloudevents) != 3 || cloudevents[2].Type != "google.cloud.storage.object.v1.deleted" {
		t.Fatalf("expected 3 cloudevents ending with deleted, got %+v", cloudevents)
	}
	mu.Unlock()
}
