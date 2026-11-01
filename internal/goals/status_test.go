package goals

import (
	"reflect"
	"testing"
	"time"
)

var statusNow = time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)

func TestNewState(t *testing.T) {
	c := Contract{Verification: "make test"}
	st := NewState("Fix the flaky cron test", c, 20, statusNow)
	want := State{Goal: "Fix the flaky cron test", Status: StatusActive, MaxTurns: 20,
		CreatedAt: float64(statusNow.UnixMilli()) / 1000, Contract: c, Epoch: 1}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("%+v", st)
	}
	if NewState("g", Contract{}, 0, statusNow).MaxTurns != 20 {
		t.Fatal("a budget of 0 must give the Hermes default of 20")
	}
}

func TestStatusLineForms(t *testing.T) {
	nowS := float64(statusNow.UnixMilli()) / 1000
	pid, sess := 4242, "4242"
	base := State{Goal: "Fix the flaky cron test", Status: StatusActive, TurnsUsed: 3, MaxTurns: 20}
	cases := []struct {
		name string
		mut  func(*State)
		want string
	}{
		{"active", func(*State) {}, "⊙ Goal (active, 3/20 turns): Fix the flaky cron test"},
		{"meta one", func(s *State) {
			s.Subgoals = []string{"a"}
			s.Contract = Contract{Outcome: "x"}
			s.Gates = []Gate{{Command: "make"}}
		}, "⊙ Goal (active, 3/20 turns, 1 subgoal, contract, 1 gate): Fix the flaky cron test"},
		{"meta many", func(s *State) {
			s.Subgoals = []string{"a", "b"}
			s.Gates = []Gate{{Command: "make"}, {Command: "vet"}}
		}, "⊙ Goal (active, 3/20 turns, 2 subgoals, 2 gates): Fix the flaky cron test"},
		{"paused", func(s *State) { s.Status, s.PausedReason = StatusPaused, new("stopped by /stop") },
			"⏸ Goal (paused, 3/20 turns — stopped by /stop): Fix the flaky cron test"},
		{"paused no reason", func(s *State) { s.Status = StatusPaused },
			"⏸ Goal (paused, 3/20 turns): Fix the flaky cron test"},
		{"done", func(s *State) { s.Status = StatusDone }, "✓ Goal done (3/20 turns): Fix the flaky cron test"},
		{"process", func(s *State) { s.WaitingOnSession = &sess },
			"⏳ Goal (parked on session 4242, 3/20 turns): Fix the flaky cron test"},
		{"process reason", func(s *State) { s.WaitingOnSession, s.WaitingReason = &sess, new("the CI run") },
			"⏳ Goal (parked on the CI run, 3/20 turns): Fix the flaky cron test"},
		{"pid", func(s *State) { s.WaitingOnPID = &pid },
			"⏳ Goal (parked on pid 4242, 3/20 turns): Fix the flaky cron test"},
		{"timed", func(s *State) { s.WaitingUntil = nowS + 90.5 },
			"⏳ Goal (parked 90s — 90s, 3/20 turns): Fix the flaky cron test"},
		{"timed reason", func(s *State) { s.WaitingUntil, s.WaitingReason = nowS+600, new("2 batches run") },
			"⏳ Goal (parked 600s — 2 batches run, 3/20 turns): Fix the flaky cron test"},
		{"timed over", func(s *State) { s.WaitingUntil = nowS - 1 },
			"⊙ Goal (active, 3/20 turns): Fix the flaky cron test"},
		{"other", func(s *State) { s.Status = "odd" }, "Goal (odd, 3/20 turns): Fix the flaky cron test"},
		{"cleared", func(s *State) { s.Status = StatusCleared }, NoGoalText},
		{"none", func(s *State) { *s = State{} }, NoGoalText},
	}
	for _, tc := range cases {
		st := base
		tc.mut(&st)
		if got := StatusLine(st, statusNow); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestShowText(t *testing.T) {
	st := State{Goal: "g", Status: StatusActive, TurnsUsed: 0, MaxTurns: 20, Subgoals: []string{"no sleeps"}}
	want := "⊙ Goal (active, 0/20 turns, 1 subgoal): g\n" +
		"(no completion contract — set one with /goal draft <objective> or inline field: value lines)"
	if got := ShowText(st, statusNow); got != want {
		t.Fatalf("%q", got)
	}
	st.Contract = Contract{Verification: "make test"}
	want = "⊙ Goal (active, 0/20 turns, 1 subgoal, contract): g\n- Verification: make test"
	if got := ShowText(st, statusNow); got != want {
		t.Fatalf("the show block has no extra criteria, like Hermes render_contract: %q", got)
	}
	if got := ShowText(State{}, statusNow); got != NoGoalText {
		t.Fatalf("%q", got)
	}
}

func TestSubgoalEdits(t *testing.T) {
	st := State{Goal: "g", Status: StatusPaused, Epoch: 3}
	if n, err := st.AddSubgoal("  no sleeps "); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := st.AddSubgoal("keep the logs"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := st.AddSubgoal("   "); err == nil || err.Error() != "subgoal text is empty" {
		t.Fatal(err)
	}
	if _, err := st.RemoveSubgoal(3); err == nil || err.Error() != "index out of range (1..2)" {
		t.Fatal(err)
	}
	if got, err := st.RemoveSubgoal(1); err != nil || got != "no sleeps" || !reflect.DeepEqual(st.Subgoals, []string{"keep the logs"}) {
		t.Fatal(got, err, st.Subgoals)
	}
	st.ClearSubgoals()
	if len(st.Subgoals) != 0 || st.Epoch != 3 {
		t.Fatalf("a subgoal edit must not change the epoch: %+v", st)
	}
	for _, status := range []string{"", StatusDone, StatusCleared} {
		none := State{Goal: "g", Status: status}
		if _, err := none.AddSubgoal("x"); err == nil || err.Error() != "no active goal" {
			t.Fatalf("%q: %v", status, err)
		}
		if _, err := none.RemoveSubgoal(1); err == nil || err.Error() != "no active goal" {
			t.Fatalf("%q: %v", status, err)
		}
	}
}
