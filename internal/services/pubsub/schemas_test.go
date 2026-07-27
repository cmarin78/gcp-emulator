package pubsub

import (
	"testing"

	"github.com/cesar/gcp-emulator/internal/testutil"
)

// newTestServer is defined in pubsub_test.go (same package).

func TestSchemaLifecycle(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/schemas"

	var created Schema
	status := testutil.DoJSON(t, "POST", base+"?schemaId=my-schema", map[string]any{
		"type":       "AVRO",
		"definition": `{"type":"record","name":"Msg","fields":[{"name":"id","type":"string"}]}`,
	}, &created)
	if status != 200 || created.Name == "" || created.Type != "AVRO" {
		t.Fatalf("create: status=%d schema=%+v", status, created)
	}

	var list struct {
		Schemas []Schema `json:"schemas"`
	}
	status = testutil.DoJSON(t, "GET", base, nil, &list)
	if status != 200 || len(list.Schemas) != 1 {
		t.Fatalf("list: status=%d schemas=%+v", status, list.Schemas)
	}

	var got Schema
	status = testutil.DoJSON(t, "GET", base+"/my-schema", nil, &got)
	if status != 200 || got.Type != "AVRO" {
		t.Fatalf("get: status=%d schema=%+v", status, got)
	}

	status = testutil.DoJSON(t, "DELETE", base+"/my-schema", nil, nil)
	if status != 200 {
		t.Fatalf("delete: want 200, got %d", status)
	}
	status = testutil.DoJSON(t, "GET", base+"/my-schema", nil, nil)
	if status != 404 {
		t.Fatalf("get after delete: want 404, got %d", status)
	}
}

func TestSchemaDuplicateConflicts(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/schemas"
	body := map[string]any{"type": "PROTOCOL_BUFFER", "definition": "syntax = \"proto3\";"}
	testutil.DoJSON(t, "POST", base+"?schemaId=dup", body, nil)
	status := testutil.DoJSON(t, "POST", base+"?schemaId=dup", body, nil)
	if status != 409 {
		t.Fatalf("duplicate schema: want 409, got %d", status)
	}
}

func TestSchemaValidate(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/v1/projects/proj1/schemas"
	testutil.DoJSON(t, "POST", base+"?schemaId=v-schema", map[string]any{"type": "AVRO", "definition": "{}"}, nil)
	status := testutil.DoJSON(t, "POST", base+"/v-schema:validate", map[string]any{"schema": map[string]any{"type": "AVRO"}}, nil)
	if status != 200 {
		t.Fatalf("validate: want 200, got %d", status)
	}
}

func TestSchemaRequiresType(t *testing.T) {
	srv := newTestServer(t)
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/schemas?schemaId=no-type",
		map[string]any{"definition": "x"}, nil)
	if status != 400 {
		t.Fatalf("schema without type: want 400, got %d", status)
	}
}

func TestTopicWithSchemaSettings(t *testing.T) {
	srv := newTestServer(t)
	// Create schema first.
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/schemas?schemaId=msg-schema",
		map[string]any{"type": "AVRO", "definition": "{}"}, nil)

	// Create topic referencing the schema.
	var topic Topic
	status := testutil.DoJSON(t, "PUT", srv.URL+"/v1/projects/proj1/topics/my-topic",
		map[string]any{
			"schemaSettings": map[string]any{
				"schema":   "projects/proj1/schemas/msg-schema",
				"encoding": "JSON",
			},
		}, &topic)
	if status != 200 || topic.SchemaSettings == nil || topic.SchemaSettings.Encoding != "JSON" {
		t.Fatalf("topic with schemaSettings: status=%d topic=%+v", status, topic)
	}
}
