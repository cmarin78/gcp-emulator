// Package activity is a small in-memory event recorder shared by the
// services that emit "this really happened" signals (Cloud Scheduler real
// fires, Cloud Tasks real dispatches, Pub/Sub real push deliveries) and the
// services that surface them back to the user: Cloud Logging (entries) and
// Cloud Monitoring (time series).
//
// It exists specifically to avoid an import cycle between producer
// services (cloudscheduler, cloudtasks, pubsub) and consumer services
// (logging, monitoring): both sides depend on this package instead of on
// each other.
//
// Deliberately in-memory and capped — this is not a real logging/metrics
// backend, just enough so that querying logs/metrics after a real dispatch
// shows real activity instead of an empty stub. State does not survive a
// restart, the same tradeoff already accepted for Pub/Sub's in-memory
// pending queue.
package activity

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	maxLogEntriesPerProject = 1000
	maxSeriesPointsPerKey   = 500
)

// LogEntry mirrors the relevant subset of logging.v2.LogEntry.
type LogEntry struct {
	LogName     string            `json:"logName,omitempty"`
	Timestamp   string            `json:"timestamp"`
	Severity    string            `json:"severity,omitempty"`
	TextPayload string            `json:"textPayload,omitempty"`
	Resource    map[string]any    `json:"resource,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	InsertID    string            `json:"insertId,omitempty"`
}

// Point is one (timestamp, value) sample for a metric series.
type Point struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

// TimeSeries is a queryable (metricType, points) tuple, shaped closely
// enough to monitoring.v3.TimeSeries for callers to adapt directly. Kind
// is either "CUMULATIVE" (IncrCounter-produced series: monotonically
// non-decreasing event counts) or "GAUGE" (RecordGauge-produced series:
// an instantaneous measurement like CPU% or connection count, which can
// go up or down between samples).
type TimeSeries struct {
	MetricType string  `json:"metricType"`
	Kind       string  `json:"kind"`
	Points     []Point `json:"points"`
}

type seriesKey struct {
	project    string
	metricType string
	labelsKey  string
}

var (
	mu     sync.Mutex
	logs   = map[string][]LogEntry{} // project -> entries (oldest first, capped)
	series = map[seriesKey][]Point{} // (project, metric, labels) -> points
	kinds  = map[seriesKey]string{}  // (project, metric, labels) -> "CUMULATIVE"|"GAUGE"
	seq    int64
)

func nextInsertID() string {
	mu.Lock()
	seq++
	id := seq
	mu.Unlock()
	return fmt.Sprintf("activity-%d", id)
}

// RecordLog appends a log entry for a project, evicting the oldest entry
// once the per-project cap is reached.
func RecordLog(project string, e LogEntry) {
	if project == "" {
		return
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if e.InsertID == "" {
		e.InsertID = nextInsertID()
	}
	mu.Lock()
	defer mu.Unlock()
	list := append(logs[project], e)
	if len(list) > maxLogEntriesPerProject {
		list = list[len(list)-maxLogEntriesPerProject:]
	}
	logs[project] = list
}

// ListLogs returns a copy of the recorded entries for a project, oldest
// first (matching insertion order).
func ListLogs(project string) []LogEntry {
	mu.Lock()
	defer mu.Unlock()
	src := logs[project]
	out := make([]LogEntry, len(src))
	copy(out, src)
	return out
}

func labelsKey(labels map[string]string) string {
	// Order-independent enough for our purposes: label sets here are small
	// and fixed per call site, so a simple sorted-free fmt is acceptable.
	return fmt.Sprintf("%v", labels)
}

// IncrCounter records one occurrence of a counter-style metric (e.g.
// "cloudscheduler.googleapis.com/job/execution_count") at the current
// time, for the given project/labels. Value is cumulative within the
// process lifetime, matching CUMULATIVE metric kind semantics.
func IncrCounter(project, metricType string, labels map[string]string) {
	if project == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	key := seriesKey{project: project, metricType: metricType, labelsKey: labelsKey(labels)}
	pts := series[key]
	var last float64
	if len(pts) > 0 {
		last = pts[len(pts)-1].Value
	}
	pts = append(pts, Point{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Value: last + 1})
	if len(pts) > maxSeriesPointsPerKey {
		pts = pts[len(pts)-maxSeriesPointsPerKey:]
	}
	series[key] = pts
	kinds[key] = "CUMULATIVE"
}

// RecordGauge records one instantaneous measurement of a gauge-style
// metric (e.g. "run.googleapis.com/container/cpu/utilization",
// "cloudsql.googleapis.com/database/postgresql/num_backends") at the
// current time, for the given project/labels. Unlike IncrCounter, value
// is stored as given — it is not added to the previous sample — matching
// real Cloud Monitoring's GAUGE metric kind: a Docker container's CPU% or
// a Postgres connection count can go up or down between polls, not just
// accumulate. This is Phase 15's primitive for real-backend-sourced
// metrics, feeding the exact same activity/listTimeSeries pipeline Phase
// 11 built for real trigger counts.
func RecordGauge(project, metricType string, labels map[string]string, value float64) {
	if project == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	key := seriesKey{project: project, metricType: metricType, labelsKey: labelsKey(labels)}
	pts := append(series[key], Point{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Value: value})
	if len(pts) > maxSeriesPointsPerKey {
		pts = pts[len(pts)-maxSeriesPointsPerKey:]
	}
	series[key] = pts
	kinds[key] = "GAUGE"
}

// ListTimeSeries returns all recorded series for a project whose metric
// type contains the given substring filter (empty filter = all series).
func ListTimeSeries(project, metricTypeContains string) []TimeSeries {
	mu.Lock()
	defer mu.Unlock()
	out := []TimeSeries{}
	for key, pts := range series {
		if key.project != project {
			continue
		}
		if metricTypeContains != "" && !strings.Contains(key.metricType, metricTypeContains) {
			continue
		}
		kind := kinds[key]
		if kind == "" {
			kind = "CUMULATIVE"
		}
		out = append(out, TimeSeries{MetricType: key.metricType, Kind: kind, Points: append([]Point{}, pts...)})
	}
	return out
}

// ProjectOf extracts the project ID out of a "projects/{project}/..."
// resource name. Returns "" if the name doesn't start with "projects/".
func ProjectOf(name string) string {
	const prefix = "projects/"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := name[len(prefix):]
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return rest[:idx]
	}
	return rest
}
