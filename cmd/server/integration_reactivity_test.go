package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cesar/gcp-emulator/internal/server"
	"github.com/cesar/gcp-emulator/internal/services/eventarc"
	"github.com/cesar/gcp-emulator/internal/services/gcs"
	"github.com/cesar/gcp-emulator/internal/services/pubsub"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

// TestGCSUploadTriggersPubSubPushAndEventarc verifies the full end-to-end reactive pipeline:
// 1. Uploading an object to GCS triggers a Pub/Sub notification to a topic.
// 2. The topic dispatches a real HTTP POST to a push subscription endpoint.
// 3. Uploading also triggers Eventarc, which dispatches a real CloudEvents HTTP POST to an Eventarc trigger endpoint.
func TestGCSUploadTriggersPubSubPushAndEventarc(t *testing.T) {
	db := testutil.NewDB(t)
	srv := server.New()
	mux := srv.Mux()

	gcsSvc := gcs.New(db)
	gcsSvc.Register(mux)
	pubsubSvc := pubsub.New(db)
	pubsubSvc.Register(mux)
	eventarcSvc := eventarc.New(db)
	eventarcSvc.Register(mux)

	// Wire inter-service reactivity
	gcsSvc.SetPubSubPublisher(pubsubSvc.Publish)
	gcsSvc.SetCloudEventPublisher(func(e gcs.CloudEvent) error {
		_, err := eventarcSvc.DispatchCloudEvent(eventarc.CloudEvent{
			ID:              e.ID,
			Source:          e.Source,
			Type:            e.Type,
			DataContentType: e.DataContentType,
			Attributes:      e.Attributes,
			Data:            e.Data,
		})
		return err
	})

	serverHTTP := httptest.NewServer(mux)
	defer serverHTTP.Close()

	// 1. Set up target HTTP server for Pub/Sub push
	pubsubPushHit := make(chan map[string]any, 2)
	pubsubPushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		pubsubPushHit <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer pubsubPushServer.Close()

	// 2. Set up target HTTP server for Eventarc
	eventarcHit := make(chan string, 2)
	eventarcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ceType := r.Header.Get("ce-type")
		eventarcHit <- ceType
		w.WriteHeader(http.StatusOK)
	}))
	defer eventarcServer.Close()

	// 3. Create GCS bucket
	testutil.DoJSON(t, "POST", serverHTTP.URL+"/storage/v1/b", map[string]string{"name": "reactive-bucket"}, nil)

	// 4. Create Pub/Sub topic and push subscription
	testutil.DoJSON(t, "PUT", serverHTTP.URL+"/v1/projects/proj-1/topics/storage-events", nil, nil)
	testutil.DoJSON(t, "PUT", serverHTTP.URL+"/v1/projects/proj-1/subscriptions/storage-sub", map[string]any{
		"topic": "projects/proj-1/topics/storage-events",
		"pushConfig": map[string]any{
			"pushEndpoint": pubsubPushServer.URL,
		},
	}, nil)

	// 5. Configure GCS bucket notification to the Pub/Sub topic
	testutil.DoJSON(t, "POST", serverHTTP.URL+"/storage/v1/b/reactive-bucket/notificationConfigs", map[string]any{
		"topic":          "//pubsub.googleapis.com/projects/proj-1/topics/storage-events",
		"payload_format": "JSON_API_V1",
	}, nil)

	// 6. Create Eventarc trigger listening for storage object finalized events
	testutil.DoJSON(t, "POST", serverHTTP.URL+"/v1/projects/proj-1/locations/us-central1/triggers?triggerId=gcs-trigger", map[string]any{
		"eventFilters": []map[string]any{
			{"attribute": "type", "value": "google.cloud.storage.object.v1.finalized"},
			{"attribute": "bucket", "value": "reactive-bucket"},
		},
		"destination": map[string]any{
			"httpEndpoint": map[string]any{
				"uri": eventarcServer.URL,
			},
		},
	}, nil)

	// 7. Perform GCS object upload
	uploadURL := serverHTTP.URL + "/upload/storage/v1/b/reactive-bucket/o?name=invoices/2026.pdf"
	req, _ := http.NewRequest("POST", uploadURL, strings.NewReader("%PDF-sample-content%"))
	req.Header.Set("Content-Type", "application/pdf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload object failed: resp=%+v err=%v", resp, err)
	}
	resp.Body.Close()

	// 8. Assert Pub/Sub push received the event
	select {
	case msg := <-pubsubPushHit:
		messageObj, ok := msg["message"].(map[string]any)
		if !ok {
			t.Fatalf("expected 'message' key in pubsub push envelope, got %+v", msg)
		}
		attrs, _ := messageObj["attributes"].(map[string]any)
		if attrs["eventType"] != "OBJECT_FINALIZE" || attrs["objectId"] != "invoices/2026.pdf" {
			t.Fatalf("unexpected pubsub message attributes: %+v", attrs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Pub/Sub push HTTP delivery")
	}

	// 9. Assert Eventarc received the CloudEvent
	select {
	case ceType := <-eventarcHit:
		if ceType != "google.cloud.storage.object.v1.finalized" {
			t.Fatalf("unexpected Eventarc ce-type: got %q", ceType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Eventarc CloudEvent HTTP delivery")
	}
}
