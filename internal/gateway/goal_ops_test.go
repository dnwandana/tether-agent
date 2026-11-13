package gateway

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
)

// resetGateConfirms empties the waiting gate confirms now and at the end of the test.
func resetGateConfirms(t *testing.T) {
	t.Helper()
	reset := func() {
		gateConfirmsMu.Lock()
		gateConfirms, gateConfirmSeq = map[string]map[int]gateConfirm{}, map[string]int{}
		gateConfirmsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestGoalWait(t *testing.T) {
	c, rec := goalCtx(t, false)
	pid := os.Getpid()
	run(t, c, fmt.Sprintf("/goal wait %d build", pid))
	if got := rec.lastText(t); got != "/goal wait: no active goal to park" {
		t.Fatalf("%q", got)
	}
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	for text, want := range map[string]string{
		"/goal wait 0":          "/goal wait: pid must be a positive integer",
		"/goal wait 1073741824": "/goal wait: pid is not alive on this host",
		"/goal wait abc":        EscapeMarkdown(goals.ParseCommand("wait abc").Usage),
	} {
		run(t, c, text)
		if got := rec.lastText(t); got != want {
			t.Fatalf("%s: got %q, want %q", text, got, want)
		}
	}
	if st := loadGoal(t, c.Deps, c.Session.ID); st.WaitingOnPID != nil {
		t.Fatalf("a refused wait must not park: %+v", st)
	}
	run(t, c, fmt.Sprintf("/goal wait %d build", pid))
	want := fmt.Sprintf("⏳ Goal parked on pid %d (build). Loop pauses until it exits.", pid)
	st := loadGoal(t, c.Deps, c.Session.ID)
	if rec.lastText(t) != want || st.WaitingOnPID == nil || *st.WaitingOnPID != pid ||
		st.WaitingReason == nil || *st.WaitingReason != "build" {
		t.Fatalf("%q %+v", rec.lastText(t), st)
	}
}

func TestGoalWaitArmsTheWaker(t *testing.T) {
	c, _ := goalCtx(t, false)
	type armCall struct {
		sessionID string
		st        goals.State
	}
	var calls []armCall
	c.Hooks.ArmWake = func(sessionID string, st goals.State) { calls = append(calls, armCall{sessionID, st}) }
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal wait 0")
	if len(calls) != 0 {
		t.Fatalf("a refused wait must not arm the waker: %+v", calls)
	}
	pid := os.Getpid()
	run(t, c, fmt.Sprintf("/goal wait %d", pid))
	if len(calls) != 1 || calls[0].sessionID != c.Session.ID ||
		calls[0].st.WaitingOnPID == nil || *calls[0].st.WaitingOnPID != pid {
		t.Fatalf("%+v", calls)
	}
}

func TestGoalUnwait(t *testing.T) {
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal unwait")
	if rec.lastText(t) != "No wait barrier set." || len(rec.starts) != 0 {
		t.Fatalf("%q %+v", rec.lastText(t), rec.starts)
	}
	run(t, c, fmt.Sprintf("/goal wait %d", os.Getpid()))
	run(t, c, "/goal unwait")
	st := loadGoal(t, c.Deps, c.Session.ID)
	if rec.lastText(t) != "▶ Wait barrier cleared — goal loop resumes." || st.WaitingOnPID != nil {
		t.Fatalf("%q %+v", rec.lastText(t), st)
	}
	if len(rec.starts) != 1 || rec.starts[0].Text != goals.ContinuationPrompt(st, nil) || rec.starts[0].Epoch != st.Epoch {
		t.Fatalf("an active goal must get one continuation: %+v", rec.starts)
	}
}

func TestGoalGateAddChecks(t *testing.T) {
	resetGateConfirms(t)
	c, rec := goalCtx(t, false)
	run(t, c, "/goal gate add go test ./internal/cron/")
	if got := rec.lastText(t); got != "/goal gate add: no active goal" {
		t.Fatalf("%q", got)
	}
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal gate add go test ./internal/cron/")
	if got := rec.lastText(t); got != "⚿ Gate added: $ go test ./internal/cron/ (3 retries, 300s timeout). It must pass before the goal can complete." {
		t.Fatalf("%q", got)
	}
	for _, cmd := range []string{"mkfs.ext4 /dev/sdb1", "echo pw | sudo -S ls"} {
		run(t, c, "/goal gate add "+cmd)
		if got := rec.lastText(t); !strings.Contains(got, "cannot be a gate") {
			t.Fatalf("%s: %q", cmd, got)
		}
	}
	if st := loadGoal(t, c.Deps, c.Session.ID); len(st.Gates) != 1 {
		t.Fatalf("a refused gate must not be added: %+v", st.Gates)
	}
}

func TestGoalGateConfirmButtons(t *testing.T) {
	resetGateConfirms(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	setNow(t, now)
	c, rec := goalCtx(t, true)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	ask := "⚠️ This gate matches a dangerous pattern: world/other-writable permissions. " +
		"The gate runs before each judge call, with no approval prompt. Add it?"
	run(t, c, "/goal gate add chmod 777 build")
	rep := rec.sent[len(rec.sent)-1]
	want := [][]Button{{{Text: "Add gate", Data: "/goal gate ok 1"}, {Text: "Cancel", Data: "/goal gate no 1"}}}
	if rep.Text != ask || !reflect.DeepEqual(rep.Buttons, want) {
		t.Fatalf("%+v", rep)
	}
	if st := loadGoal(t, c.Deps, c.Session.ID); len(st.Gates) != 0 {
		t.Fatal("the gate must wait for the confirm")
	}
	run(t, c, "/goal gate ok 1")
	rep = rec.sent[len(rec.sent)-1]
	if !rep.Edit || rep.Text != "⚿ Gate added: $ chmod 777 build (3 retries, 300s timeout). It must pass before the goal can complete." {
		t.Fatalf("%+v", rep)
	}
	run(t, c, "/goal gate add chmod 777 dist")
	run(t, c, "/goal gate no 2")
	if rep := rec.sent[len(rec.sent)-1]; !rep.Edit || rep.Text != "Gate not added." {
		t.Fatalf("%+v", rep)
	}
	run(t, c, "/goal gate add chmod 777 out")
	setNow(t, now.Add(time.Duration(c.Deps.Cfg.Approvals.TimeoutS+1)*time.Second))
	run(t, c, "/goal gate ok 3")
	if rep := rec.sent[len(rec.sent)-1]; rep.Text != "This confirm is not valid now. Send /goal gate add <command> again." {
		t.Fatalf("%+v", rep)
	}
	run(t, c, "/goal gate ok 1")
	if st := loadGoal(t, c.Deps, c.Session.ID); len(st.Gates) != 1 || st.Gates[0].Command != "chmod 777 build" {
		t.Fatalf("a used, cancelled, or expired confirm adds nothing: %+v", st.Gates)
	}
	for _, r := range rec.sent {
		for _, row := range r.Buttons {
			for _, b := range row {
				if len(b.Data) >= 64 || !strings.HasPrefix(b.Data, "/") {
					t.Fatalf("%q", b.Data)
				}
			}
		}
	}
}

func TestGoalGateConfirmCLI(t *testing.T) {
	resetGateConfirms(t)
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	var prompts []string
	answer := true
	c.Confirm = func(p string) bool { prompts = append(prompts, p); return answer }
	run(t, c, "/goal gate add chmod 777 build")
	answer = false
	run(t, c, "/goal gate add chmod 777 dist")
	if len(prompts) != 2 || !strings.HasPrefix(prompts[0], "⚠️ This gate matches a dangerous pattern: world/other-writable permissions.") {
		t.Fatalf("%q", prompts)
	}
	if rec.lastText(t) != "Gate not added." {
		t.Fatalf("%q", rec.lastText(t))
	}
	if st := loadGoal(t, c.Deps, c.Session.ID); len(st.Gates) != 1 || st.Gates[0].Command != "chmod 777 build" {
		t.Fatalf("%+v", st.Gates)
	}
}

func TestGoalGateListRemoveClear(t *testing.T) {
	resetGateConfirms(t)
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal gate list")
	if got := rec.lastText(t); got != EscapeMarkdown(goals.RenderGates(nil)) {
		t.Fatalf("%q", got)
	}
	run(t, c, "/goal gate add go test ./...")
	run(t, c, "/goal gate add go vet ./...")
	run(t, c, "/goal gate")
	st := loadGoal(t, c.Deps, c.Session.ID)
	if got := rec.lastText(t); got != EscapeMarkdown(goals.RenderGates(st.Gates)) {
		t.Fatalf("%q", got)
	}
	run(t, c, "/goal gate rm 1")
	if got := rec.lastText(t); got != "✓ Gate removed: $ go test ./..." {
		t.Fatalf("%q", got)
	}
	run(t, c, "/goal gate remove 5")
	if got := rec.lastText(t); !strings.HasPrefix(got, "/goal gate remove: ") {
		t.Fatalf("%q", got)
	}
	run(t, c, "/goal gate clear")
	if got := rec.lastText(t); got != "✓ Cleared 1 gate." {
		t.Fatalf("%q", got)
	}
	before := loadGoal(t, c.Deps, c.Session.ID).Epoch
	run(t, c, "/goal gate add go test ./...")
	run(t, c, "/goal gate add go vet ./...")
	run(t, c, "/goal gate clear")
	if got := rec.lastText(t); got != "✓ Cleared 2 gates." {
		t.Fatalf("%q", got)
	}
	if after := loadGoal(t, c.Deps, c.Session.ID).Epoch; after != before {
		t.Fatalf("a gate edit must not change the epoch: %d to %d", before, after)
	}
}

func TestSubgoalCommand(t *testing.T) {
	cmd, ok := Commands()["subgoal"]
	if !ok || !cmd.WhileBusy || cmd.Help != "Add a criterion to the goal: /subgoal <text> | remove <n> | clear" {
		t.Fatalf("%+v", cmd)
	}
	c, rec := goalCtx(t, false)
	run(t, c, "/subgoal check the logs")
	if got := rec.lastText(t); got != goals.NoGoalText {
		t.Fatalf("%q", got)
	}
	st := setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/subgoal")
	if got := rec.lastText(t); got != "⊙ Goal (active, 0/20 turns): ship it\n(no subgoals — use /subgoal <text> to add criteria)" {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal check the *error* log")
	if got := rec.lastText(t); got != "✓ Added subgoal 1: check the \\*error\\* log" {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal run go vet")
	run(t, c, "/subgoal")
	if got := rec.lastText(t); got != "⊙ Goal (active, 0/20 turns, 2 subgoals): ship it\n- 1. check the \\*error\\* log\n- 2. run go vet" {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal remove 1")
	if got := rec.lastText(t); got != "✓ Removed subgoal 1: check the \\*error\\* log" {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal clear")
	if got := rec.lastText(t); got != "✓ Cleared 1 subgoal." {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal clear")
	if got := rec.lastText(t); got != "No subgoals to clear." {
		t.Fatalf("%q", got)
	}
	run(t, c, "/subgoal remove x")
	if got := rec.lastText(t); got != EscapeMarkdown(goals.ParseSubgoal("remove x").Usage) {
		t.Fatalf("%q", got)
	}
	if after := loadGoal(t, c.Deps, c.Session.ID); after.Epoch != st.Epoch || len(rec.starts) != 0 {
		t.Fatalf("a subgoal edit must not change the epoch or start a turn: %+v %+v", after, rec.starts)
	}
}

// /goal wait keeps the epoch, so it must drop the queued goal turns itself.
func TestGoalWaitDropsTheQueuedGoalTurns(t *testing.T) {
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal wait 0")
	if rec.drops != 0 {
		t.Fatal("a refused wait must not drop the goal turns")
	}
	run(t, c, fmt.Sprintf("/goal wait %d", os.Getpid()))
	if rec.drops != 1 {
		t.Fatalf("drops %d", rec.drops)
	}
}

func TestGoalGateAddUsesTheConfig(t *testing.T) {
	resetGateConfirms(t)
	c, rec := goalCtx(t, false)
	c.Deps.Cfg.Goals.GateTimeoutS, c.Deps.Cfg.Goals.GateMaxRetries = 600, 5
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal gate add make test")
	if got := rec.lastText(t); got != "⚿ Gate added: $ make test (5 retries, 600s timeout). It must pass before the goal can complete." {
		t.Fatalf("%q", got)
	}
	if g := loadGoal(t, c.Deps, c.Session.ID).Gates[0]; g.TimeoutSeconds != 600 || g.MaxRetries != 5 {
		t.Fatalf("the new gate must hold the config values: %+v", g)
	}
}

func TestGateValuesNilConfig(t *testing.T) {
	if s, r := gateValues(&Deps{}); s != 0 || r != 0 {
		t.Fatal("a nil Cfg gives 0, so AddGateWith uses the consts:", s, r)
	}
}
