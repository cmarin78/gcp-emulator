// Package cronexpr implements a minimal standard 5-field unix-cron
// evaluator ("minute hour day-of-month month day-of-week"), enough to
// compute the next fire time for Cloud Scheduler jobs without pulling in
// an external dependency. It deliberately does not support seconds,
// "L"/"#" special characters, or named months/weekdays — Cloud Scheduler
// itself only documents the standard 5-field unix-cron format.
//
// Timezone handling: Next operates entirely in the location of the `after`
// time passed in; callers wanting a specific job TimeZone should convert
// `after` into that location first. This keeps the evaluator itself
// timezone-agnostic.
package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// field bounds, in order: minute, hour, day-of-month, month, day-of-week
var bounds = [5][2]int{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week (0 = Sunday)
}

// Schedule is a parsed cron expression: a set of allowed values per field.
type Schedule struct {
	allowed [5]map[int]bool
}

// Parse parses a standard 5-field unix-cron expression.
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cronexpr: expected 5 fields (minute hour dom month dow), got %d in %q", len(fields), expr)
	}
	var sch Schedule
	for i, f := range fields {
		set, err := parseField(f, bounds[i][0], bounds[i][1])
		if err != nil {
			return nil, fmt.Errorf("cronexpr: field %d (%q): %w", i, f, err)
		}
		sch.allowed[i] = set
	}
	return &sch, nil
}

func parseField(f string, lo, hi int) (map[int]bool, error) {
	set := make(map[int]bool)
	for _, part := range strings.Split(f, ",") {
		if err := parsePart(part, lo, hi, set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func parsePart(part string, lo, hi int, set map[int]bool) error {
	step := 1
	rangePart := part
	if idx := strings.IndexByte(part, '/'); idx >= 0 {
		var err error
		step, err = strconv.Atoi(part[idx+1:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step in %q", part)
		}
		rangePart = part[:idx]
	}

	start, end := lo, hi
	switch {
	case rangePart == "*":
		// start/end already lo/hi
	case strings.Contains(rangePart, "-"):
		segs := strings.SplitN(rangePart, "-", 2)
		a, err1 := strconv.Atoi(segs[0])
		b, err2 := strconv.Atoi(segs[1])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("invalid range %q", rangePart)
		}
		start, end = a, b
	default:
		v, err := strconv.Atoi(rangePart)
		if err != nil {
			return fmt.Errorf("invalid value %q", rangePart)
		}
		start, end = v, v
	}
	if start < lo || end > hi || start > end {
		return fmt.Errorf("value out of range [%d,%d] in %q", lo, hi, part)
	}
	for v := start; v <= end; v += step {
		set[v] = true
	}
	return nil
}

func (s *Schedule) matches(t time.Time) bool {
	minute, hour, dom, month, dow := t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())
	if !s.allowed[0][minute] || !s.allowed[1][hour] || !s.allowed[3][month] {
		return false
	}
	// Standard cron semantics: if both dom and dow are restricted (not "*"),
	// a match on EITHER is sufficient. We approximate "is restricted" as
	// "doesn't cover the full range".
	domRestricted := len(s.allowed[2]) < (bounds[2][1] - bounds[2][0] + 1)
	dowRestricted := len(s.allowed[4]) < (bounds[4][1] - bounds[4][0] + 1)
	domOK := s.allowed[2][dom]
	dowOK := s.allowed[4][dow]
	if domRestricted && dowRestricted {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Next returns the next time strictly after `after` at which the schedule
// fires, searching minute-by-minute up to two years out (cron schedules
// that never fire within two years are treated as an error, matching the
// practical limits of any cron implementation).
func (s *Schedule) Next(after time.Time) (time.Time, error) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.AddDate(2, 0, 0)
	for t.Before(limit) {
		if s.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cronexpr: no fire time found within 2 years")
}

// Next is a convenience wrapper: parse expr and compute the next fire
// time after `after` in one call.
func Next(expr string, after time.Time) (time.Time, error) {
	sch, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sch.Next(after)
}
