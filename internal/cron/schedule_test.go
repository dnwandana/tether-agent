package cron

import (
	"strings"
	"testing"
	"time"
)

func TestInterval(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for spec, d := range map[string]time.Duration{
		"every 2h": 2 * time.Hour, "30m": 30 * time.Minute, "1d": 24 * time.Hour,
		"every 45 minutes": 45 * time.Minute, "EVERY 1d": 24 * time.Hour,
	} {
		s, err := Parse(spec, now, time.UTC)
		if err != nil {
			t.Fatal(spec, err)
		}
		if s.OneShot() || !s.Next(now).Equal(now.Add(d)) || !s.Next(now.Add(d)).Equal(now.Add(2*d)) {
			t.Fatal(spec, s.Next(now))
		}
	}
}

func TestIntervalString(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for spec, want := range map[string]string{"30m": "every 30m", "every 120m": "every 2h", "every 2 days": "every 2d", "90m": "every 90m"} {
		s, _ := Parse(spec, now, time.UTC)
		if s.String() != want {
			t.Fatal(spec, s.String())
		}
	}
}

func TestCronDaily0100(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, loc)
	s, err := Parse("0 1 * * *", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Next(now) != time.Date(2026, 10, 28, 1, 0, 0, 0, loc) || s.OneShot() || s.String() != "0 1 * * *" {
		t.Fatal(s.Next(now))
	}
}

// The schedule uses loc even when the caller passes a time in another zone.
func TestCronUsesLocNotInputZone(t *testing.T) {
	loc := time.FixedZone("UTC+7", 7*3600)
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC) // 17:00 in loc
	s, err := Parse("0 1 * * *", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 28, 1, 0, 0, 0, loc); !s.Next(now).Equal(want) {
		t.Fatal(s.Next(now), want)
	}
}

func TestISOOneShot(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	s, err := Parse("2026-11-01T09:00", now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !s.OneShot() || s.Next(now) != time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC) || !s.Next(s.Next(now)).IsZero() {
		t.Fatal(s.Next(now))
	}
}

func TestISOFormats(t *testing.T) {
	loc := time.FixedZone("UTC+7", 7*3600)
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for spec, want := range map[string]time.Time{
		"2026-11-01T09:00":          time.Date(2026, 11, 1, 9, 0, 0, 0, loc),
		"2026-11-01T09:00:30":       time.Date(2026, 11, 1, 9, 0, 30, 0, loc),
		"2026-11-01 09:00":          time.Date(2026, 11, 1, 9, 0, 0, 0, loc),
		"2026-11-01T09:00:00Z":      time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		"2026-11-01T09:00:00+02:00": time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC),
		"2026-11-01T09:00+02:00":    time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC),
		"2026-11-01":                time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
	} {
		s, err := Parse(spec, now, loc)
		if err != nil {
			t.Fatal(spec, err)
		}
		if !s.OneShot() || !s.Next(now).Equal(want) {
			t.Fatal(spec, s.Next(now), want)
		}
	}
}

// Parse refuses a one-shot time at or before now, with or without an offset.
func TestISOInPastRejected(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, spec := range []string{"2026-10-01T09:00", "2026-10-27T10:00", "2026-10-27T16:00+07:00"} {
		_, err := Parse(spec, now, time.UTC)
		if err == nil || !strings.Contains(err.Error(), "in the past") {
			t.Fatal(spec, err)
		}
	}
}

// A naive timestamp is read in loc, so the past check uses loc too.
func TestISOPastCheckUsesLoc(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) // 07:00 in loc
	if _, err := Parse("2026-10-27T08:00", now, loc); err != nil {
		t.Fatal("08:00 in UTC-5 is after 12:00 UTC", err)
	}
	if _, err := Parse("2026-10-27T06:00", now, loc); err == nil {
		t.Fatal("06:00 in UTC-5 is before 12:00 UTC")
	}
}

func TestCronAcrossDSTChange(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin") // DST ends 2026-10-25 03:00 → 02:00
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, loc) // n1 = Oct 24 09:00 CEST, n2 = Oct 25 09:00 CET
	s, err := Parse("0 9 * * *", now, loc)
	if err != nil {
		t.Fatal(err)
	}
	n1 := s.Next(now)
	n2 := s.Next(n1)
	if n1.Hour() != 9 || n2.Hour() != 9 || n2.Sub(n1) != 25*time.Hour {
		t.Fatal(n1, n2)
	}
}

func TestCronAcrossDSTStart(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin") // DST starts 2027-03-28 02:00 → 03:00
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2027, 3, 27, 8, 0, 0, 0, loc)
	s, _ := Parse("0 9 * * *", now, loc)
	n1 := s.Next(now)
	n2 := s.Next(n1)
	if n1.Hour() != 9 || n2.Hour() != 9 || n2.Sub(n1) != 23*time.Hour {
		t.Fatal(n1, n2)
	}
}

func TestBadSpecError(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, spec := range []string{"sometimes", "", "every", "every 0m", "0m", "61 * * * *", "* * * *", "0 0 30 2 *",
		"CRON_TZ=UTC 0 1 * * *", "2026-13-01T09:00", "@daily"} {
		_, err := Parse(spec, now, time.UTC)
		if err == nil || !strings.Contains(err.Error(), "use 30m, every 2h, a 5-field cron expression, or an ISO time") {
			t.Fatal(spec, err)
		}
	}
}
