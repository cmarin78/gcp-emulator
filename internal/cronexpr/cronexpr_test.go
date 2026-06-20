package cronexpr

import (
	"testing"
	"time"
)

func TestNextEveryMinute(t *testing.T) {
	after := time.Date(2026, 6, 20, 10, 0, 30, 0, time.UTC)
	next, err := Next("* * * * *", after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, 6, 20, 10, 1, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestNextHourly(t *testing.T) {
	after := time.Date(2026, 6, 20, 10, 30, 0, 0, time.UTC)
	next, err := Next("0 * * * *", after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestNextDailyAtFixedTime(t *testing.T) {
	after := time.Date(2026, 6, 20, 23, 0, 0, 0, time.UTC)
	next, err := Next("30 9 * * *", after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestNextStepAndList(t *testing.T) {
	after := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	next, err := Next("*/15 * * * *", after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, 6, 20, 10, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestNextWeekday(t *testing.T) {
	// Saturday June 20 2026 -> next Monday (dow=1) at 08:00
	after := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	next, err := Next("0 8 * * 1", after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestParseInvalid(t *testing.T) {
	if _, err := Parse("* * * *"); err == nil {
		t.Fatal("expected error for 4-field expression")
	}
	if _, err := Parse("60 * * * *"); err == nil {
		t.Fatal("expected error for out-of-range minute")
	}
}
