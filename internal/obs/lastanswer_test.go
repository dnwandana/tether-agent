package obs

import (
	"context"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

func insertSession(t *testing.T, d *db.DB, id, pin string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions (id, model_override) VALUES (?, ?)`, id, pin); err != nil {
		t.Fatal(err)
	}
}

func TestReadLastAnswerNewest(t *testing.T) {
	d := openUsageDB(t)
	insertSession(t, d, "s1", "")
	now := time.Now()
	insertUsage(t, d, usageRow{session: "s1", model: "glm-5.3", prov: "primary", at: now.Add(-10 * time.Minute)})
	insertUsage(t, d, usageRow{session: "s1", model: "deepseek", prov: "fallback", at: now.Add(-2 * time.Minute)})
	// A newer user row and a newer assistant row with no model do not count.
	if _, err := d.Exec(`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('s1', 'user', 'hi', ?)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('s1', 'assistant', 'x', ?)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	l, ok, err := ReadLastAnswer(context.Background(), d)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if l.Provider != "fallback" || l.Model != "deepseek" {
		t.Fatalf("%+v", l)
	}
	if diff := l.At.Sub(now.Add(-2 * time.Minute)).Abs(); diff > time.Millisecond {
		t.Fatalf("At = %v, diff %v", l.At, diff)
	}
	if l.Pin != "" || l.PinsFallback() {
		t.Fatalf("pin %q", l.Pin)
	}
	// The connection is free after the call.
	var n int
	if err := d.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestReadLastAnswerPin(t *testing.T) {
	d := openUsageDB(t)
	insertSession(t, d, "s1", "")
	insertSession(t, d, "s2", "fallback")
	now := time.Now()
	insertUsage(t, d, usageRow{session: "s1", model: "glm-5.3", prov: "primary", at: now.Add(-5 * time.Minute)})
	insertUsage(t, d, usageRow{session: "s2", model: "deepseek", prov: "fallback", at: now.Add(-time.Minute)})
	l, ok, err := ReadLastAnswer(context.Background(), d)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if l.Pin != "fallback" || !l.PinsFallback() {
		t.Fatalf("%+v", l)
	}
	for pin, want := range map[string]bool{
		"fallback":          true,
		"fallback:deepseek": true,
		"primary:glm-4.7":   false,
		"":                  false,
		"fallbacks":         false,
	} {
		if got := (LastAnswer{Pin: pin}).PinsFallback(); got != want {
			t.Errorf("PinsFallback(%q) = %v, want %v", pin, got, want)
		}
	}
}

func TestReadLastAnswerNoSessionRow(t *testing.T) {
	d := openUsageDB(t)
	insertSession(t, d, "s1", "fallback")
	now := time.Now()
	insertUsage(t, d, usageRow{session: "s1", model: "deepseek", prov: "fallback", at: now.Add(-time.Hour)})
	insertUsage(t, d, usageRow{session: "orphan", model: "glm-5.3", prov: "primary", at: now})
	l, ok, err := ReadLastAnswer(context.Background(), d)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if l.Provider != "primary" || l.Model != "glm-5.3" || l.Pin != "" {
		t.Fatalf("%+v", l)
	}
}

func TestReadLastAnswerEmpty(t *testing.T) {
	d := openUsageDB(t)
	l, ok, err := ReadLastAnswer(context.Background(), d)
	if err != nil || ok {
		t.Fatalf("l=%+v ok=%v err=%v", l, ok, err)
	}
}
