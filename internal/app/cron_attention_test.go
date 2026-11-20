package app

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
)

// The cron keys set the no-sound window, the time of the daily status, and its function.
func TestSchedulerQuietAndBriefFromConfig(t *testing.T) {
	a := gatewayApp(t, "", "cron:\n  quiet_hours: \"22:00-07:00\"\n  brief_at: \"08:30\"\n")
	s := a.Sched
	at := func(h, m int) time.Time { return time.Date(2026, 11, 19, h, m, 0, 0, time.Local) }
	if s.Quiet == nil || !s.Quiet(at(23, 0)) || !s.Quiet(at(6, 59)) || s.Quiet(at(7, 0)) || s.Quiet(at(12, 0)) {
		t.Fatal("quiet_hours 22:00-07:00 must give a window that crosses midnight")
	}
	if s.BriefAt != 8*60+30 || s.Brief == nil {
		t.Fatalf("BriefAt %d, Brief nil %v", s.BriefAt, s.Brief == nil)
	}
	if s.Runner.Jobs != a.Cron || s.Runner.Cost == nil || s.Runner.Sleep != nil {
		t.Fatalf("%+v", s.Runner)
	}
}

// With the default config, nothing is quiet and no daily status is sent.
func TestSchedulerNoQuietNoBriefByDefault(t *testing.T) {
	a := gatewayApp(t, "", "")
	if a.Sched.Quiet != nil || a.Sched.Brief != nil {
		t.Fatal("the empty keys must leave Quiet and Brief nil")
	}
}

// Both roles register /jobs and /brief. Both work while a task runs.
func TestJobsAndBriefRegistered(t *testing.T) {
	keepCommandTable(t)
	newApp(t)
	for _, name := range []string{"jobs", "brief"} {
		if c, ok := gateway.Commands()[name]; !ok || !c.WhileBusy || c.MemberOK {
			t.Fatalf("chat role: /%s %+v", name, c)
		}
	}
	gatewayApp(t, "", "")
	for _, name := range []string{"jobs", "brief"} {
		if _, ok := gateway.Commands()[name]; !ok {
			t.Fatalf("gateway role: no /%s", name)
		}
	}
}

// The cost of a run is the sum of the priced calls of its sessions. No priced call gives nil.
func TestCronCostSumsSessions(t *testing.T) {
	a := gatewayApp(t, "", "")
	now := time.Now().UnixMilli()
	for _, r := range []struct {
		session string
		cost    any
	}{{"s1", 0.10}, {"s2", 0.25}, {"s3", nil}} {
		if _, err := a.DB.Exec(`INSERT INTO messages (session_id, role, content, input_tokens, output_tokens,
			cache_read_tokens, latency_ms, latency_first_ms, cost_usd, model, provider, timestamp)
			VALUES (?, 'assistant', 'x', 10, 10, 0, 0, 0, ?, 'm', 'primary', ?)`, r.session, r.cost, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Sched.Runner.Cost(ctx, []string{"s1", "s2"})
	if err != nil || got == nil || math.Abs(*got-0.35) > 1e-9 {
		t.Fatal(got, err)
	}
	for _, ids := range [][]string{{"s3"}, nil} {
		if got, err := a.Sched.Runner.Cost(ctx, ids); err != nil || got != nil {
			t.Fatal(ids, got, err)
		}
	}
}

// A new data dir has nothing that needs the owner.
func TestBriefTextNothingNeedsYou(t *testing.T) {
	a := newApp(t)
	text, empty, err := a.briefText(ctx)
	if err != nil || !empty || !strings.HasPrefix(text, "Nothing needs you.") {
		t.Fatal(text, empty, err)
	}
}

// The status masks a secret in a goal text before the Markdown escape changes its "_".
func TestBriefTextRedactsBeforeEscape(t *testing.T) {
	a := newApp(t)
	secret := "github_pat_ABCDEFGHIJ1234567890"
	st := goals.State{Goal: "use " + secret, Status: goals.StatusPaused, MaxTurns: 20}
	reason := "stopped by /stop"
	st.PausedReason = &reason
	st.PausedAt = float64(time.Now().Add(-time.Hour).Unix())
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(`INSERT INTO goals (session_id, state, revision, updated_at) VALUES (?, ?, 1, ?)`,
		"s1", string(raw), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	text, empty, err := a.briefText(ctx)
	if err != nil || empty || strings.Contains(text, "ABCDEFGHIJ1234567890") {
		t.Fatal(text, empty, err)
	}
}
