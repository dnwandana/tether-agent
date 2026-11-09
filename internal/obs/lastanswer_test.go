package obs

import (
	"context"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
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
	if l.Pin != "" || l.PinnedEntry() != "primary" {
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
	if l.Pin != "fallback" || l.PinnedEntry() != "fallback" {
		t.Fatalf("%+v", l)
	}
	for pin, want := range map[string]string{
		"":                "primary",
		"deepseek:m":      "deepseek",
		"fallback":        "fallback",
		"primary:glm-4.7": "primary",
	} {
		if got := (LastAnswer{Pin: pin}).PinnedEntry(); got != want {
			t.Errorf("PinnedEntry(%q) = %q, want %q", pin, got, want)
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

func TestReadLastAnswerSkipsHiddenSessions(t *testing.T) {
	d := openUsageDB(t)
	insertSession(t, d, "s1", "")
	now := time.Now()
	insertUsage(t, d, usageRow{session: "s1", model: "glm-5.3", prov: "primary", at: now.Add(-time.Hour)})
	// A newer row in each hidden child session does not count.
	for i, kind := range sessions.HiddenChildKinds {
		id := "hidden-" + kind
		if _, err := d.Exec(`INSERT INTO sessions (id, kind, parent_id) VALUES (?, ?, 's1')`, id, kind); err != nil {
			t.Fatal(err)
		}
		insertUsage(t, d, usageRow{session: id, model: "kimi-k2.6", prov: "vision", at: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	l, ok, err := ReadLastAnswer(context.Background(), d)
	if err != nil || !ok || l.Provider != "primary" || l.Model != "glm-5.3" {
		t.Fatalf("%+v ok=%v err=%v", l, ok, err)
	}
	// A row with no session row still counts.
	insertUsage(t, d, usageRow{session: "orphan", model: "deepseek", prov: "fallback", at: now.Add(-30 * time.Minute)})
	l, ok, err = ReadLastAnswer(context.Background(), d)
	if err != nil || !ok || l.Provider != "fallback" || l.Model != "deepseek" || l.Pin != "" {
		t.Fatalf("%+v ok=%v err=%v", l, ok, err)
	}
}
