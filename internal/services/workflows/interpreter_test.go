package workflows

import (
	"encoding/json"
	"testing"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/testutil"
)

func jsonWorkflowBody(def map[string]any) map[string]any {
	raw, _ := json.Marshal(def)
	return map[string]any{"sourceContents": string(raw)}
}

// TestExecutionInterpretsAssignAndReturn covers sequential steps with
// ordered assign and an expression-valued return.
func TestExecutionInterpretsAssignAndReturn(t *testing.T) {
	srv := newTestServer(t)
	def := map[string]any{
		"main": map[string]any{
			"steps": []any{
				map[string]any{
					"compute": map[string]any{
						"assign": []any{
							map[string]any{"x": 2},
							map[string]any{"y": "${x * 3}"},
						},
					},
				},
				map[string]any{
					"done": map[string]any{"return": "${y}"},
				},
			},
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		jsonWorkflowBody(def), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "{}"}, &exec)
	if status != 200 || exec.State != "SUCCEEDED" || exec.Result != "6" {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}
}

// TestExecutionInterpretsSwitch covers conditional branching: the switch
// in "check" picks "positive" or falls through to the step's own next
// ("negative") depending on the input argument.
func TestExecutionInterpretsSwitch(t *testing.T) {
	srv := newTestServer(t)
	def := map[string]any{
		"main": map[string]any{
			"params": []any{"input"},
			"steps": []any{
				map[string]any{
					"check": map[string]any{
						"switch": []any{
							map[string]any{"condition": "${input > 0}", "return": "positive"},
						},
						"next": "negative",
					},
				},
				map[string]any{
					"negative": map[string]any{"return": "negative"},
				},
			},
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		jsonWorkflowBody(def), nil)

	var positive Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "5"}, &positive)
	if status != 200 || positive.Result != `"positive"` {
		t.Fatalf("positive branch: status=%d exec=%+v", status, positive)
	}

	var negative Execution
	status = testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "-5"}, &negative)
	if status != 200 || negative.Result != `"negative"` {
		t.Fatalf("negative branch: status=%d exec=%+v", status, negative)
	}
}

// TestExecutionInterpretsCallToSubworkflow covers call into another
// subworkflow defined in the same document, with args and a result
// binding.
func TestExecutionInterpretsCallToSubworkflow(t *testing.T) {
	srv := newTestServer(t)
	def := map[string]any{
		"main": map[string]any{
			"params": []any{"input"},
			"steps": []any{
				map[string]any{
					"invoke": map[string]any{
						"call":   "double",
						"args":   map[string]any{"n": "${input}"},
						"result": "r",
					},
				},
				map[string]any{
					"done": map[string]any{"return": "${r}"},
				},
			},
		},
		"double": map[string]any{
			"params": []any{"n"},
			"steps": []any{
				map[string]any{
					"compute": map[string]any{"return": "${n * 2}"},
				},
			},
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		jsonWorkflowBody(def), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "21"}, &exec)
	if status != 200 || exec.State != "SUCCEEDED" || exec.Result != "42" {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}
}

// TestExecutionCallsSysLog covers the sys.log builtin actually recording
// an activity log entry, the same sink every other Phase 11 real-dispatch
// path writes to.
func TestExecutionCallsSysLog(t *testing.T) {
	srv := newTestServer(t)
	def := map[string]any{
		"main": map[string]any{
			"steps": []any{
				map[string]any{
					"log": map[string]any{
						"call": "sys.log",
						"args": map[string]any{"text": "hello from workflow"},
					},
				},
				map[string]any{
					"done": map[string]any{"return": "ok"},
				},
			},
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		jsonWorkflowBody(def), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "{}"}, &exec)
	if status != 200 || exec.State != "SUCCEEDED" || exec.Result != `"ok"` {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}

	found := false
	for _, e := range activity.ListLogs("proj1") {
		if e.TextPayload == "hello from workflow" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sys.log call to record an activity log entry")
	}
}

// TestExecutionFailsOnUnknownNext covers the error path: a next pointing
// at a step that doesn't exist fails the execution instead of panicking
// or silently succeeding.
func TestExecutionFailsOnUnknownNext(t *testing.T) {
	srv := newTestServer(t)
	def := map[string]any{
		"main": map[string]any{
			"steps": []any{
				map[string]any{
					"bad": map[string]any{"next": "nowhere"},
				},
			},
		},
	}
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		jsonWorkflowBody(def), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": "{}"}, &exec)
	if status != 200 || exec.State != "FAILED" || exec.Error == nil || exec.Error.Payload == "" {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}
}

// TestExecutionFallsBackForNonJSONSourceContents locks in the documented
// boundary of support: a non-JSON (e.g. YAML) sourceContents isn't
// interpreted at all, and the execution keeps the original echo behavior.
func TestExecutionFallsBackForNonJSONSourceContents(t *testing.T) {
	srv := newTestServer(t)
	testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows?workflowId=wf",
		validWorkflowBody(), nil)

	var exec Execution
	status := testutil.DoJSON(t, "POST", srv.URL+"/v1/projects/proj1/locations/us-central1/workflows/wf/executions",
		map[string]any{"argument": `{"foo":"bar"}`}, &exec)
	if status != 200 || exec.State != "SUCCEEDED" || exec.Result != `{"foo":"bar"}` {
		t.Fatalf("create execution: status=%d exec=%+v", status, exec)
	}
}

// TestEvalExprArithmeticAndLogic is a focused unit test on the expression
// evaluator itself, independent of the HTTP surface.
func TestEvalExprArithmeticAndLogic(t *testing.T) {
	vars := map[string]any{
		"x": 5.0,
		"obj": map[string]any{
			"name": "alice",
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{"1 + 2 * 3", 7.0},
		{"(1 + 2) * 3", 9.0},
		{"x >= 5 && x < 10", true},
		{"x > 10 || x == 5", true},
		{"not (x == 5)", false},
		{"obj.name == 'alice'", true},
		{"'foo' + 'bar'", "foobar"},
		{"-x + 10", 5.0},
	}
	for _, c := range cases {
		got, err := evalExpr(c.expr, vars)
		if err != nil {
			t.Errorf("evalExpr(%q): unexpected error: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("evalExpr(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}
