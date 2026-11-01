package goals

import "testing"

func TestParseGoalTextFields(t *testing.T) {
	head, c := ParseGoalText("Fix the flaky cron test\nverify: go test ./internal/cron/ -count=20\r\n" +
		"Must not: change the public API\nscope: internal/cron\ngive up when: the test needs a real clock\n" +
		"done when: 20 runs pass\nproof: the test output")
	if head != "Fix the flaky cron test" {
		t.Fatalf("%q", head)
	}
	want := Contract{
		Outcome:      "20 runs pass",
		Verification: "go test ./internal/cron/ -count=20 the test output",
		Constraints:  "change the public API",
		Boundaries:   "internal/cron",
		StopWhen:     "the test needs a real clock",
	}
	if c != want {
		t.Fatalf("%+v", c)
	}
}

func TestParseGoalTextEachAlias(t *testing.T) {
	aliases := map[string]string{
		"outcome": "outcome", "goal": "outcome", "done": "outcome", "done when": "outcome",
		"verification": "verification", "verify": "verification", "verified by": "verification",
		"evidence": "verification", "proof": "verification",
		"constraints": "constraints", "constraint": "constraints", "preserve": "constraints",
		"must not": "constraints", "do not change": "constraints",
		"boundaries": "boundaries", "boundary": "boundaries", "scope": "boundaries",
		"allowed": "boundaries", "files": "boundaries",
		"stop when": "stop_when", "stop_when": "stop_when", "blocked": "stop_when",
		"stop if blocked": "stop_when", "give up when": "stop_when",
	}
	for alias, field := range aliases {
		_, c := ParseGoalText("head\n " + alias + " :  v ")
		got := map[string]string{"outcome": c.Outcome, "verification": c.Verification,
			"constraints": c.Constraints, "boundaries": c.Boundaries, "stop_when": c.StopWhen}[field]
		if got != "v" {
			t.Fatalf("alias %q: %+v", alias, c)
		}
	}
}

func TestParseGoalTextHeadlineRules(t *testing.T) {
	cases := []struct{ in, head string }{
		// "fix bug" is not an alias, so the line stays in the headline.
		{"Fix bug: the cron test", "Fix bug: the cron test"},
		// The first line can be a field too.
		{"verify: make test\nShip the release", "Ship the release"},
		// A field with no value stays in the headline.
		{"Ship it\nverify:", "Ship it verify:"},
		// Blank lines are skipped, and the other lines join with one space.
		{"  Ship  \n\n  the release ", "Ship the release"},
		// An empty headline gives the raw text.
		{"verify: make test\nscope: cmd/", "verify: make test\nscope: cmd/"},
		{"", ""},
	}
	for _, tc := range cases {
		if head, _ := ParseGoalText(tc.in); head != tc.head {
			t.Errorf("%q: headline %q, want %q", tc.in, head, tc.head)
		}
	}
	if _, c := ParseGoalText("verify: make test\nscope: cmd/"); c.Verification != "make test" || c.Boundaries != "cmd/" {
		t.Fatalf("the raw text keeps its contract: %+v", c)
	}
}

func TestContractRender(t *testing.T) {
	c := Contract{Outcome: " 20 runs pass ", Verification: "go test ./internal/cron/ -count=20", StopWhen: "a real clock is needed"}
	want := "- Outcome: 20 runs pass\n- Verification: go test ./internal/cron/ -count=20\n- Stop when blocked: a real clock is needed"
	if got := c.Render(nil); got != want {
		t.Fatalf("%q", got)
	}
	want += "\n- Extra criterion 1: no sleeps\n- Extra criterion 2: keep the logs"
	if got := c.Render([]string{"no sleeps", "  ", " keep the logs"}); got != want {
		t.Fatalf("%q", got)
	}
	if got := (Contract{Constraints: "x", Boundaries: "y"}).Render(nil); got != "- Constraints: x\n- Boundaries: y" {
		t.Fatalf("%q", got)
	}
	if got := (Contract{}).Render(nil); got != "" {
		t.Fatalf("an empty contract must render as empty text: %q", got)
	}
}
