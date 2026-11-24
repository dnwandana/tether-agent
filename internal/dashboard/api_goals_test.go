package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goalsFixture is a GoalsAPI over a new state.db with four sessions. The dashboard session is
// the active session of the dashboard key.
type goalsFixture struct {
	mux                 *http.ServeMux
	dash, tg, cli, done string
}

func newGoalsFixture(t *testing.T) *goalsFixture {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ss := sessions.New(d)
	gs := &goals.Store{DB: d}
	mk := func(key, src, title string) string {
		t.Helper()
		sess, err := ss.Create(ctx, sessions.Session{Key: key, Source: src, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}
	f := &goalsFixture{
		dash: mk(SessionKey, ChannelName, "Ship the docs sk-secret"),
		tg:   mk("telegram:dm:42", "telegram", "Phone chat"),
		cli:  mk("cli", "cli", "Build"),
		done: mk("cli", "cli", "Old work"),
	}
	if err := ss.SetActive(ctx, SessionKey, f.dash); err != nil {
		t.Fatal(err)
	}
	one, verdict, reason, budget := 1, "continue", "two tests still fail", "budget"
	waiting := goals.State{Goal: "Wait for the build", Status: goals.StatusActive, MaxTurns: 20}
	waiting.WaitOnPID(4242, "build", time.Now())
	for id, st := range map[string]goals.State{
		f.dash: {Goal: "Fix the flaky test sk-secret", Status: goals.StatusActive, TurnsUsed: 3, MaxTurns: 20, LastTurnAt: 1_800_000_000,
			Subgoals: []string{"no sleeps"}, Contract: goals.Contract{Verification: "go test ./..."},
			LastVerdict: &verdict, LastReason: &reason,
			Gates: []goals.Gate{{Command: "go vet ./...", TimeoutSeconds: 300, MaxRetries: 3, Attempts: 2,
				LastExitCode: &one, LastOutputTail: "token sk-secret"}}},
		f.tg:   {Goal: "Plan the trip", Status: goals.StatusPaused, PausedReason: &budget, MaxTurns: 20},
		f.cli:  waiting,
		f.done: {Goal: "Write the guide", Status: goals.StatusDone, MaxTurns: 20},
	} {
		if _, err := gs.Put(ctx, id, st, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE goals SET updated_at = ? WHERE session_id = ?`,
		time.Now().Add(-10*24*time.Hour).UnixMilli(), f.done); err != nil {
		t.Fatal(err)
	}
	api := NewGoalsAPI(gs, ss, func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }, time.Now)
	f.mux = http.NewServeMux()
	api.Register(f.mux)
	return f
}

func (f *goalsFixture) get(t *testing.T, path string, wantStatus int, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if rec.Code != wantStatus {
		t.Fatalf("%s: status %d, want %d: %s", path, rec.Code, wantStatus, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v: %s", path, err, rec.Body.String())
	}
}

func TestGoalsListOpenGoals(t *testing.T) {
	f := newGoalsFixture(t)
	var rows []GoalRowView
	f.get(t, "/api/goals", 200, &rows)
	by := map[string]GoalRowView{}
	for _, r := range rows {
		by[r.SessionID] = r
	}
	if len(rows) != 3 {
		t.Fatalf("%+v", rows)
	}
	if d := by[f.dash]; !d.CanStop || !d.CanResume || d.Goal != "Fix the flaky test [REDACTED]" || d.Title != "Ship the docs [REDACTED]" ||
		d.Source != "dashboard" || d.Status != "active" || d.TurnsUsed != 3 || d.MaxTurns != 20 || d.Waiting ||
		d.LastVerdict != "continue" || d.LastTurnAt == nil || !d.LastTurnAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("%+v", d)
	}
	if g := by[f.tg]; !g.CanStop || g.CanResume || g.Status != "paused" || g.PausedReason != "budget" ||
		g.Title != "Phone chat" || g.LastTurnAt != nil || g.LastVerdict != "" {
		t.Fatalf("%+v", g)
	}
	if c := by[f.cli]; c.CanStop || c.CanResume || !c.Waiting || c.WaitTarget != "pid 4242" {
		t.Fatalf("a CLI goal is view only: %+v", c)
	}
}

func TestGoalsListFinished(t *testing.T) {
	f := newGoalsFixture(t)
	var rows []GoalRowView
	f.get(t, "/api/goals?finished=1", 200, &rows)
	if len(rows) != 1 || rows[0].SessionID != f.done || rows[0].Status != "done" || rows[0].Title != "Old work" {
		t.Fatalf("%+v", rows)
	}
}

func TestGoalsDetail(t *testing.T) {
	f := newGoalsFixture(t)
	var det GoalDetail
	f.get(t, "/api/goals/"+f.dash, 200, &det)
	if !det.Row.CanResume || det.Row.Goal != "Fix the flaky test [REDACTED]" || det.LastVerdict != "continue" ||
		det.LastReason != "two tests still fail" || det.Verification != "go test ./..." ||
		!reflect.DeepEqual(det.Subgoals, []string{"no sleeps"}) || strings.Contains(det.StatusLine, "sk-secret") {
		t.Fatalf("%+v", det)
	}
	if det.Row.Title != "Ship the docs [REDACTED]" || det.Row.UpdatedAt.IsZero() || time.Since(det.Row.UpdatedAt) > time.Minute {
		t.Fatalf("title %q, updated %v", det.Row.Title, det.Row.UpdatedAt)
	}
	if len(det.Gates) != 1 {
		t.Fatalf("%+v", det.Gates)
	}
	g := det.Gates[0]
	if g.Command != "go vet ./..." || g.Attempts != 2 || g.LastExitCode == nil || *g.LastExitCode != 1 ||
		g.LastOutputTail != "token [REDACTED]" || g.TimeoutSeconds != 300 || g.MaxRetries != 3 {
		t.Fatalf("%+v", g)
	}
	if !reflect.DeepEqual(det.SubgoalHashes, []string{"c97edf6d"}) || !reflect.DeepEqual(det.GateHashes, []string{"3d4b424f"}) {
		t.Fatalf("the hashes of %q and %q: %v %v", "no sleeps", "go vet ./...", det.SubgoalHashes, det.GateHashes)
	}
	var wait GoalDetail
	f.get(t, "/api/goals/"+f.cli, 200, &wait)
	if wait.WaitingOnPID != 4242 || wait.WaitingReason != "build" || wait.WaitingSince.IsZero() || wait.Row.CanStop {
		t.Fatalf("%+v", wait)
	}
	if wait.SubgoalHashes == nil || wait.GateHashes == nil {
		t.Fatal("empty hash lists are [] and not null")
	}
	var e struct {
		Error string `json:"error"`
	}
	f.get(t, "/api/goals/nope", 404, &e)
	if e.Error != "No goal for this session." {
		t.Fatalf("%q", e.Error)
	}
}
