package activity

import "testing"

func TestProjectOf(t *testing.T) {
	cases := map[string]string{
		"projects/proj1/locations/us-central1/jobs/my-job": "proj1",
		"projects/proj1": "proj1",
		"proj1":          "",
		"":               "",
	}
	for in, want := range cases {
		if got := ProjectOf(in); got != want {
			t.Errorf("ProjectOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecordAndListLogsCap(t *testing.T) {
	const project = "test-record-and-list-logs-cap"
	for i := 0; i < maxLogEntriesPerProject+10; i++ {
		RecordLog(project, LogEntry{Severity: "INFO", TextPayload: "x"})
	}
	got := ListLogs(project)
	if len(got) != maxLogEntriesPerProject {
		t.Fatalf("len(ListLogs) = %d, want %d", len(got), maxLogEntriesPerProject)
	}
}

func TestRecordLogIgnoresEmptyProject(t *testing.T) {
	RecordLog("", LogEntry{Severity: "INFO"})
	if got := ListLogs(""); len(got) != 0 {
		t.Fatalf("expected no entries recorded for empty project, got %d", len(got))
	}
}

func TestIncrCounterAccumulatesAndFilters(t *testing.T) {
	const project = "test-incr-counter"
	IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", map[string]string{"job_name": "j1"})
	IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", map[string]string{"job_name": "j1"})
	IncrCounter(project, "cloudtasks.googleapis.com/task/dispatch_count", map[string]string{"task_name": "t1"})

	all := ListTimeSeries(project, "")
	if len(all) != 2 {
		t.Fatalf("ListTimeSeries(all) returned %d series, want 2", len(all))
	}

	scheduler := ListTimeSeries(project, "cloudscheduler")
	if len(scheduler) != 1 {
		t.Fatalf("ListTimeSeries(filtered) returned %d series, want 1", len(scheduler))
	}
	pts := scheduler[0].Points
	if len(pts) != 2 || pts[len(pts)-1].Value != 2 {
		t.Fatalf("scheduler series points = %+v, want cumulative [1,2]", pts)
	}

	other := ListTimeSeries(project, "no-such-metric")
	if len(other) != 0 {
		t.Fatalf("expected no series for unmatched filter, got %+v", other)
	}
}

// TestRecordGaugeOverwritesRatherThanAccumulates confirms Phase 15's
// RecordGauge's defining difference from IncrCounter: each call stores
// the given value directly, not last+1, since a CPU%/connection-count
// measurement can go down between samples (unlike a monotonic event
// count).
func TestRecordGaugeOverwritesRatherThanAccumulates(t *testing.T) {
	const project = "test-record-gauge"
	const metricType = "cloudsql.googleapis.com/database/postgresql/num_backends"

	RecordGauge(project, metricType, nil, 3)
	RecordGauge(project, metricType, nil, 1)
	RecordGauge(project, metricType, nil, 7)

	series := ListTimeSeries(project, metricType)
	if len(series) != 1 {
		t.Fatalf("expected exactly one series, got %d: %+v", len(series), series)
	}
	ts := series[0]
	if ts.Kind != "GAUGE" {
		t.Fatalf("expected kind GAUGE, got %q", ts.Kind)
	}
	want := []float64{3, 1, 7}
	if len(ts.Points) != len(want) {
		t.Fatalf("expected %d points, got %d: %+v", len(want), len(ts.Points), ts.Points)
	}
	for i, p := range ts.Points {
		if p.Value != want[i] {
			t.Fatalf("point %d: want %v, got %v (gauge values must not accumulate)", i, want[i], p.Value)
		}
	}
}

// TestIncrCounterStillReportsCumulativeKind confirms Phase 15's addition
// of a Kind field doesn't change IncrCounter-produced series' kind —
// every pre-Phase-15 caller's series must still read back as CUMULATIVE.
func TestIncrCounterStillReportsCumulativeKind(t *testing.T) {
	const project = "test-counter-kind"
	IncrCounter(project, "cloudscheduler.googleapis.com/job/execution_count", nil)
	series := ListTimeSeries(project, "")
	if len(series) != 1 || series[0].Kind != "CUMULATIVE" {
		t.Fatalf("expected one CUMULATIVE series, got %+v", series)
	}
}
