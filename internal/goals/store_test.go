package goals

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
)

// openGoalsDB opens a new state.db in a temporary folder. The test closes it at the end.
func openGoalsDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// putRaw writes a row with the JSON text raw, as an older program or Hermes wrote it.
func putRaw(t *testing.T, d *db.DB, sessionID, raw string, rev int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO goals (session_id, state, revision, updated_at) VALUES (?, ?, ?, 0)`,
		sessionID, raw, rev); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRoundTripAndConflict(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: openGoalsDB(t)}
	code := 1
	st := State{Goal: "Fix the flaky cron test", Status: StatusActive, MaxTurns: 20, CreatedAt: 1761955200.25,
		Subgoals: []string{"no sleeps"}, Contract: Contract{Verification: "go test ./internal/cron/ -count=20"},
		Gates: []Gate{{Command: "go vet ./...", TimeoutSeconds: 300, MaxRetries: 3, LastExitCode: &code}},
		Epoch: 1, JudgeSessionID: "j1"}
	rev, err := s.Put(ctx, "s1", st, 0)
	if err != nil || rev != 1 {
		t.Fatal(rev, err)
	}
	got, gotRev, err := s.Get(ctx, "s1")
	if err != nil || gotRev != 1 || !reflect.DeepEqual(got, st) {
		t.Fatalf("%+v %d %v", got, gotRev, err)
	}
	st.TurnsUsed = 1
	if rev, err = s.Put(ctx, "s1", st, 1); err != nil || rev != 2 {
		t.Fatal(rev, err)
	}
	stale := st
	stale.TurnsUsed = 9
	if _, err := s.Put(ctx, "s1", stale, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale revision must give ErrConflict, got %v", err)
	}
	if _, err := s.Put(ctx, "s1", stale, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("an INSERT over a row must give ErrConflict, got %v", err)
	}
	if _, err := s.Put(ctx, "missing", stale, 5); !errors.Is(err, ErrConflict) {
		t.Fatalf("an UPDATE of no row must give ErrConflict, got %v", err)
	}
	got, gotRev, _ = s.Get(ctx, "s1")
	if gotRev != 2 || got.TurnsUsed != 1 {
		t.Fatalf("a conflict must change nothing: %d %+v", gotRev, got)
	}
}

func TestStoreGetNoRow(t *testing.T) {
	got, rev, err := (&Store{DB: openGoalsDB(t)}).Get(context.Background(), "none")
	if err != nil || rev != 0 || !reflect.DeepEqual(got, State{}) {
		t.Fatalf("%+v %d %v", got, rev, err)
	}
}

func TestStoreLoadsHermesRow(t *testing.T) {
	d := openGoalsDB(t)
	putRaw(t, d, "h1", `{"goal": "Ship the release", "status": "paused", "turns_used": 3, "max_turns": 20,
		"created_at": 1761955200.123456, "last_turn_at": 1761955800.5, "last_verdict": null,
		"last_reason": null, "paused_reason": "user-paused", "consecutive_parse_failures": 0,
		"consecutive_transport_failures": 0, "subgoals": [], "waiting_on_pid": null,
		"waiting_on_session": null, "waiting_until": 0.0, "waiting_on_delegations": 0,
		"waiting_reason": null, "waiting_since": 0.0,
		"contract": {"outcome": "", "verification": "make test", "constraints": "", "boundaries": "", "stop_when": ""},
		"gates": [{"command": "make test", "timeout_seconds": 300, "max_retries": 3, "attempts": 1,
		"last_exit_code": null, "last_output_tail": ""}]}`, 7)
	st, rev, err := (&Store{DB: d}).Get(context.Background(), "h1")
	if err != nil || rev != 7 {
		t.Fatal(rev, err)
	}
	if st.Goal != "Ship the release" || st.Status != StatusPaused || st.TurnsUsed != 3 ||
		st.CreatedAt != 1761955200.123456 || st.LastTurnAt != 1761955800.5 {
		t.Fatalf("%+v", st)
	}
	if st.LastVerdict != nil || st.LastReason != nil || st.WaitingOnPID != nil || st.WaitingReason != nil {
		t.Fatalf("a null field must load as nil: %+v", st)
	}
	if st.PausedReason == nil || *st.PausedReason != "user-paused" || st.Contract.Verification != "make test" {
		t.Fatalf("%+v", st)
	}
	if len(st.Gates) != 1 || st.Gates[0].LastExitCode != nil || st.Gates[0].Attempts != 1 {
		t.Fatalf("%+v", st.Gates)
	}
	if st.Epoch != 0 || st.JudgeSessionID != "" {
		t.Fatalf("the Tether Agent fields must be zero: %+v", st)
	}
}

func TestStoreLoadsOldAndUnknownRows(t *testing.T) {
	d := openGoalsDB(t)
	putRaw(t, d, "old", `{"goal": "g", "status": "active", "turns_used": 1, "max_turns": 20}`, 1)
	putRaw(t, d, "new", `{"goal": "g", "status": "active", "max_turns": 20, "watch_patterns": ["x"], "future": {"a": 1}}`, 1)
	putRaw(t, d, "bare", `{"goal": "g", "subgoals": ["a", "  ", "b"], "gates": [{"command": " "}, {"command": "make"}]}`, 1)
	s := &Store{DB: d}
	ctx := context.Background()
	old, _, err := s.Get(ctx, "old")
	if err != nil || len(old.Subgoals) != 0 || !old.Contract.Empty() || len(old.Gates) != 0 {
		t.Fatalf("%+v %v", old, err)
	}
	if _, _, err := s.Get(ctx, "new"); err != nil {
		t.Fatalf("an unknown key must load: %v", err)
	}
	bare, _, err := s.Get(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	// The Hermes defaults of GoalState.from_json and GoalGate.from_dict.
	if bare.Status != StatusActive || bare.MaxTurns != 20 || !reflect.DeepEqual(bare.Subgoals, []string{"a", "b"}) {
		t.Fatalf("%+v", bare)
	}
	if len(bare.Gates) != 1 || bare.Gates[0].Command != "make" || bare.Gates[0].TimeoutSeconds != 300 || bare.Gates[0].MaxRetries != 3 {
		t.Fatalf("%+v", bare.Gates)
	}
}

func TestStoreDeleteAndActive(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: openGoalsDB(t)}
	for _, r := range []struct{ id, status string }{{"s3", StatusActive}, {"s2", StatusPaused}, {"s1", StatusActive}} {
		if _, err := s.Put(ctx, r.id, State{Goal: "g " + r.id, Status: r.status, MaxTurns: 20}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, "s3", State{Goal: "g s3", Status: StatusActive, MaxTurns: 20, TurnsUsed: 2}, 1); err != nil {
		t.Fatal(err)
	}
	act, err := s.Active(ctx)
	if err != nil || len(act) != 2 || act[0].SessionID != "s1" || act[1].SessionID != "s3" ||
		act[1].Revision != 2 || act[1].State.TurnsUsed != 2 {
		t.Fatalf("%+v %v", act, err)
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, rev, _ := s.Get(ctx, "s1"); rev != 0 {
		t.Fatal("Delete must remove the row")
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("a delete of no row is not an error: %v", err)
	}
}
