package goals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// gateGoal returns an active goal with the gates cmds.
func gateGoal(cmds ...string) State {
	st := State{Goal: "ship it", Status: StatusActive, MaxTurns: 20, Epoch: 1}
	for _, c := range cmds {
		st.AddGate(c)
	}
	return st
}

func TestRunGatesStopsAtTheFirstFailure(t *testing.T) {
	st := gateGoal("go vet ./...", "go test ./...", "make lint")
	var ran []string
	exec := func(_ context.Context, cmd string, timeout time.Duration) (string, int, error) {
		ran = append(ran, cmd)
		if timeout != 300*time.Second {
			t.Errorf("timeout %v, want 300s", timeout)
		}
		if cmd == "go test ./..." {
			return "FAIL: TestCron", 1, nil
		}
		return "ok", 0, nil
	}
	out := RunGates(context.Background(), &st, exec)
	want := GateOutcome{Failed: true, Index: 1, Failure: GateFailure{Command: "go test ./...", Attempt: 1, MaxRetries: 3, ExitCode: 1, Output: "FAIL: TestCron"}}
	if out != want {
		t.Fatalf("got %+v, want %+v", out, want)
	}
	if strings.Join(ran, "|") != "go vet ./...|go test ./..." {
		t.Fatal("the other gates do not run after a failure:", ran)
	}
	g0, g1, g2 := st.Gates[0], st.Gates[1], st.Gates[2]
	if g0.LastExitCode == nil || *g0.LastExitCode != 0 || g0.Attempts != 0 || g0.LastOutputTail != "ok" {
		t.Fatalf("%+v", g0)
	}
	if g1.LastExitCode == nil || *g1.LastExitCode != 1 || g1.Attempts != 1 || g1.LastOutputTail != "FAIL: TestCron" {
		t.Fatalf("%+v", g1)
	}
	if g2.LastExitCode != nil {
		t.Fatal("a gate that did not run keeps no result")
	}
}

func TestRunGatesFourthFailurePauses(t *testing.T) {
	st := gateGoal("exit 1")
	fail := func(context.Context, string, time.Duration) (string, int, error) { return "", 1, nil }
	for i := 1; i <= 3; i++ {
		if out := RunGates(context.Background(), &st, fail); !out.Failed || out.Exhausted || out.Failure.Attempt != i {
			t.Fatalf("failure %d: %+v", i, out)
		}
	}
	if out := RunGates(context.Background(), &st, fail); !out.Exhausted || out.Failure.Attempt != 4 || out.Failure.MaxRetries != 3 {
		t.Fatalf("the fourth failure uses the 3 retries: %+v", out)
	}
	pass := func(context.Context, string, time.Duration) (string, int, error) { return "ok", 0, nil }
	if out := RunGates(context.Background(), &st, pass); out != (GateOutcome{}) || st.Gates[0].Attempts != 0 {
		t.Fatalf("a pass sets the attempts to 0: %+v %+v", out, st.Gates[0])
	}
	empty := State{}
	if out := RunGates(context.Background(), &empty, fail); out != (GateOutcome{}) {
		t.Fatal("no gates give no failure")
	}
}

func TestRunGatesErrorsAndTail(t *testing.T) {
	st := gateGoal("make check")
	st.Gates[0].TimeoutSeconds = 42
	var got time.Duration
	exec := func(_ context.Context, _ string, timeout time.Duration) (string, int, error) {
		got = timeout
		return "", 0, errors.New("cannot start bash: no such file")
	}
	out := RunGates(context.Background(), &st, exec)
	if got != 42*time.Second || !out.Failed || out.Failure.ExitCode != -1 || out.Failure.Output != "cannot start bash: no such file" {
		t.Fatalf("a start failure is exit -1 with the error as the output: %+v", out)
	}
	exec = func(context.Context, string, time.Duration) (string, int, error) {
		return "partial", -1, errors.New("command stopped: context canceled")
	}
	if out := RunGates(context.Background(), &st, exec); out.Failure.Output != "partial\ncommand stopped: context canceled" {
		t.Fatalf("%q", out.Failure.Output)
	}
	exec = func(context.Context, string, time.Duration) (string, int, error) {
		return strings.Repeat("é", 5000) + "END", 2, nil
	}
	out = RunGates(context.Background(), &st, exec)
	if n := utf8.RuneCountInString(out.Failure.Output); n != 3000 || !strings.HasSuffix(out.Failure.Output, "END") || !utf8.ValidString(out.Failure.Output) {
		t.Fatalf("the tail is the last 3000 chars, got %d", n)
	}
	if st.Gates[0].LastOutputTail != out.Failure.Output {
		t.Fatal("the gate keeps the same tail")
	}
}

func TestRunGatesThroughTheTerminalTool(t *testing.T) {
	t.Setenv("GATE_SECRET", "leak-me")
	tt := &tools.TerminalTool{DotEnv: map[string]string{"GATE_SECRET": "leak-me"}}
	env := &tools.Env{WorkDir: t.TempDir()}
	exec := func(ctx context.Context, cmd string, timeout time.Duration) (string, int, error) {
		return tt.Exec(ctx, env, cmd, "", timeout)
	}
	st := gateGoal(`test -z "$GATE_SECRET"`)
	if out := RunGates(context.Background(), &st, exec); out.Failed {
		t.Fatalf("a gate does not see a .env name: %+v", out)
	}
	st = gateGoal("sleep 5")
	st.Gates[0].TimeoutSeconds = 1
	if out := RunGates(context.Background(), &st, exec); out.Failure.ExitCode != -1 || !strings.Contains(out.Failure.Output, "timed out after 1s") {
		t.Fatalf("a timeout is exit -1: %+v", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st = gateGoal("sleep 5")
	if out := RunGates(ctx, &st, exec); out.Failure.ExitCode != -1 || !strings.Contains(out.Failure.Output, "command stopped") {
		t.Fatalf("a cancel is exit -1: %+v", out)
	}
}
