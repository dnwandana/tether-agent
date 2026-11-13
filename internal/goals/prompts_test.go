package goals

import (
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

var promptNow = time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)

func TestJudgeSystemPromptPidForm(t *testing.T) {
	p := JudgeSystemPrompt
	if !strings.HasPrefix(p, "You are a strict judge evaluating whether an autonomous agent has achieved a user's stated goal.") ||
		!strings.HasSuffix(p, `accepted (true=done, false=continue).`) {
		t.Fatal("the prompt must start and end like Hermes JUDGE_SYSTEM_PROMPT")
	}
	if !strings.Contains(p, "(e.g. a CI poller, build, test run, deploy). Return its pid in ``wait_on_pid`` (releases on exit only).\n- The agent says it is rate-limited") {
		t.Fatal("the WAIT rule must have the pid form")
	}
	if strings.Contains(p, "watch_patterns") || strings.Contains(p, "has a session id") {
		t.Fatal("the prompt must not describe watch patterns or session ids")
	}
	if !strings.Contains(p, `{"verdict": "wait", "wait_on_session": "<id>", "reason": "<one sentence>"}`) {
		t.Fatal("the shape lines stay verbatim")
	}
}

func TestDraftContractSystemPrompt(t *testing.T) {
	p := DraftContractSystemPrompt
	if !strings.HasPrefix(p, "You turn a user's plain-language objective into a structured completion contract") ||
		!strings.HasSuffix(p, `{"outcome": "...", "verification": "...", "constraints": "...", "boundaries": "...", "stop_when": "..."}`) {
		t.Fatalf("%q", p)
	}
}

func TestJudgeUserPromptPlain(t *testing.T) {
	got := JudgeUserPrompt(JudgeInput{Goal: "Fix the flaky cron test", Response: "I changed the clock.", Now: promptNow})
	want := "Goal:\nFix the flaky cron test\n\nAgent's most recent response:\nI changed the clock.\n\n" +
		"Current time: 2026-11-01 09:30:00 UTC\n\nIs the goal satisfied — done, blocked, continue, or wait?"
	if got != want {
		t.Fatalf("%q", got)
	}
}

func TestJudgeUserPromptBlocks(t *testing.T) {
	got := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Delegations: 2,
		Processes: []ProcLine{
			{PID: 4242, Command: "go test ./...\n-count=20", Running: 95 * time.Second, Tail: "ok  cron\n"},
			{PID: 7, Command: strings.Repeat("c", 130), Running: 1500 * time.Millisecond},
		},
		TodoBlock: "Open items of the agent's task list:\n- [ ] 3. Run the cron tests 20 times (pending)"})
	want := "Agent's most recent response:\nr\n\n" +
		"Background processes the agent currently has running (it may be waiting on one of these):\n" +
		"- pid 4242: go test ./... -count=20 (running 95s) | recent output: ok  cron\n" +
		"- pid 7: " + strings.Repeat("c", 120) + "… [truncated] (running 1s)\n\n" +
		"Active delegations: the agent has 2 delegated subagent batch(es) still running; " +
		"their results are delivered to it automatically when they finish.\n\n" +
		"Open items of the agent's task list:\n- [ ] 3. Run the cron tests 20 times (pending)\n\n" +
		"Current time: 2026-11-01 09:30:00 UTC"
	if !strings.Contains(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestJudgeUserPromptTemplateChoice(t *testing.T) {
	sub := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Subgoals: []string{"a", " ", "b"}})
	if !strings.Contains(sub, "Additional criteria the user added mid-loop (all must also be satisfied for the goal to be DONE):\n- 1. a\n- 2. b\n\nAgent's most recent response:") ||
		!strings.HasSuffix(sub, "Is the goal AND every additional criterion satisfied?") {
		t.Fatalf("%q", sub)
	}
	con := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Subgoals: []string{"no sleeps"},
		Contract: Contract{Verification: "go test ./internal/cron/ -count=20"}})
	if !strings.Contains(con, "Completion contract (the authoritative definition of done):\n- Verification: go test ./internal/cron/ -count=20\n- Extra criterion 1: no sleeps\n\nAgent's most recent response:") ||
		!strings.HasSuffix(con, "Is the goal satisfied per its completion contract — done, blocked, continue, or wait?") {
		t.Fatalf("%q", con)
	}
}

func TestJudgeUserPromptCuts(t *testing.T) {
	got := JudgeUserPrompt(JudgeInput{Goal: strings.Repeat("g", 2100), Response: strings.Repeat("r", 4100),
		TodoBlock: strings.Repeat("t", 2100), Now: promptNow})
	for _, part := range []string{
		"Goal:\n" + strings.Repeat("g", 2000) + "… [truncated]\n\n",
		"response:\n" + strings.Repeat("r", 4000) + "… [truncated]\n\n",
		"\n\n" + strings.Repeat("t", 2000) + "… [truncated]\n\nCurrent time:",
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing cut %q", part[:20])
		}
	}
	con := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Contract: Contract{Outcome: strings.Repeat("o", 2600)}})
	if !strings.Contains(con, "- Outcome: "+strings.Repeat("o", 2489)+"… [truncated]\n\n") {
		t.Fatal("the contract block must be cut at 2500 chars")
	}
	sub := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Subgoals: []string{strings.Repeat("s", 2100)}})
	if !strings.Contains(sub, "- 1. "+strings.Repeat("s", 1995)+"… [truncated]\n\n") {
		t.Fatal("the subgoal block must be cut at 2000 chars")
	}
	// The cut counts characters, not bytes.
	if got := JudgeUserPrompt(JudgeInput{Goal: strings.Repeat("é", 2000), Response: "r", Now: promptNow}); strings.Contains(got, "[truncated]") {
		t.Fatal("2000 characters must not be cut")
	}
}

func TestContinuationPromptVariants(t *testing.T) {
	plain := ContinuationPrompt(State{Goal: "Fix the flaky cron test", Status: StatusActive}, nil)
	want := "[Continuing toward your standing goal]\nGoal: Fix the flaky cron test\n\n" +
		"Continue working toward this goal. Take the next concrete step. If you believe the goal is complete, " +
		"state so explicitly and stop. If you are blocked and need input from the user, say so clearly and stop."
	if plain != want || !strings.HasPrefix(plain, ContinuationPrefix) {
		t.Fatalf("%q", plain)
	}
	con := ContinuationPrompt(State{Goal: "g", Contract: Contract{Outcome: "x"}, Subgoals: []string{"y"}}, nil)
	if !strings.Contains(con, "Goal: g\n\nCompletion contract:\n- Outcome: x\n- Extra criterion 1: y\n\nContinue working toward the outcome above.") {
		t.Fatalf("%q", con)
	}
	sub := ContinuationPrompt(State{Goal: "g", Subgoals: []string{"y"}}, nil)
	if !strings.Contains(sub, "Goal: g\n\nAdditional criteria the user added mid-loop:\n- 1. y\n\nContinue working toward the goal AND all additional criteria.") {
		t.Fatalf("%q", sub)
	}
	gate := ContinuationPrompt(State{Goal: "g", Contract: Contract{Outcome: "x"}},
		&GateFailure{Command: "go test ./...", Attempt: 2, MaxRetries: 3, ExitCode: 1})
	if !strings.HasPrefix(gate, "[Continuing toward your standing goal — a quality gate failed]\nGoal: g\n\n") ||
		!strings.Contains(gate, "(attempt 2/3):\n  $ go test ./...\nExit code: 1\nOutput (tail):\n```\n(no output)\n```\n\n") {
		t.Fatalf("%q", gate)
	}
	withOut := ContinuationPrompt(State{Goal: "g"}, &GateFailure{Command: "make", Attempt: 1, MaxRetries: 3, ExitCode: -1, Output: "FAIL cron"})
	if !strings.Contains(withOut, "Exit code: -1\nOutput (tail):\n```\nFAIL cron\n```") {
		t.Fatalf("%q", withOut)
	}
}

func TestKickPrompt(t *testing.T) {
	const seen = "[Goal set] Continue with the goal you were just given; there is no need to re-read it."
	long := strings.Repeat("word ", 80) // 400 chars, 399 after the fold
	long400 := strings.Repeat("a", 400)
	cases := []struct{ goal, last, want string }{
		{"Fix the flaky cron test", "Fix the flaky cron test", "Fix the flaky cron test"},
		{long400, long400, seen},
		{long400, "please:\n" + long400 + "\nthanks", seen},
		{long400, long400 + strings.Repeat(" more", 30), long400}, // 400 of 550 is less than 80%
		{strings.Repeat("a", 399), strings.Repeat("a", 399), strings.Repeat("a", 399)},
		{long, long, long}, // the fold makes it 399 chars
		{"line one\n\n" + long400, "line one " + long400, seen},
	}
	for i, tc := range cases {
		if got := KickPrompt(tc.goal, tc.last); got != tc.want {
			t.Errorf("case %d: %q", i, got)
		}
	}
}

func TestJudgeUserPromptCutsFromConfig(t *testing.T) {
	chars := config.GoalsJudgeChars{Goal: 10, Response: 20, Contract: 30, Subgoal: 15, Todo: 12, Process: 8}
	got := JudgeUserPrompt(JudgeInput{Goal: strings.Repeat("g", 11), Response: strings.Repeat("r", 21),
		TodoBlock: strings.Repeat("t", 13), Now: promptNow, Chars: chars,
		Processes: []ProcLine{{PID: 7, Command: strings.Repeat("c", 9)}}})
	for _, part := range []string{
		"Goal:\n" + strings.Repeat("g", 10) + "… [truncated]\n\n",
		"response:\n" + strings.Repeat("r", 20) + "… [truncated]\n\n",
		"\n\n" + strings.Repeat("t", 12) + "… [truncated]\n\nCurrent time:",
		"- pid 7: " + strings.Repeat("c", 8) + "… [truncated] (running 0s)",
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing cut %q in:\n%s", part, got)
		}
	}
	// "- Outcome: " has 11 runes, so a cut at 30 keeps 19 "o".
	con := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Chars: chars,
		Contract: Contract{Outcome: strings.Repeat("o", 100)}})
	if !strings.Contains(con, "- Outcome: "+strings.Repeat("o", 19)+"… [truncated]\n\n") {
		t.Fatal("the contract block must be cut at 30 chars")
	}
	// "- 1. " has 5 runes, so a cut at 15 keeps 10 "s".
	sub := JudgeUserPrompt(JudgeInput{Goal: "g", Response: "r", Now: promptNow, Chars: chars,
		Subgoals: []string{strings.Repeat("s", 20)}})
	if !strings.Contains(sub, "- 1. "+strings.Repeat("s", 10)+"… [truncated]\n\n") {
		t.Fatal("the subgoal block must be cut at 15 chars")
	}
}
