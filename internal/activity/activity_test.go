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
