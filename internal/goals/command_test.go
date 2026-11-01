package goals

import "testing"

func TestParseCommandVerbs(t *testing.T) {
	cases := []struct {
		in   string
		want Command
	}{
		{"", Command{Verb: VerbStatus}},
		{"  status ", Command{Verb: VerbStatus}},
		{"show", Command{Verb: VerbShow}},
		{"PAUSE", Command{Verb: VerbPause}},
		{"resume", Command{Verb: VerbResume}},
		{"continue", Command{Verb: VerbResume}},
		{"unpause", Command{Verb: VerbResume}},
		{"clear", Command{Verb: VerbClear}},
		{"stop", Command{Verb: VerbClear}},
		{"done", Command{Verb: VerbClear}},
		{"unwait", Command{Verb: VerbUnwait}},
		{"Fix the flaky cron test", Command{Verb: VerbSet, Text: "Fix the flaky cron test"}},
		// clear, stop, and done with more words set a new goal, like Hermes.
		{"clear the old one", Command{Verb: VerbSet, Text: "clear the old one"}},
		{"done when tests pass", Command{Verb: VerbSet, Text: "done when tests pass"}},
		{"-- pause the cron jobs", Command{Verb: VerbSet, Text: "pause the cron jobs"}},
		{"--", Command{Verb: VerbSet, Usage: "Invalid goal: goal text is empty"}},
		{"draft speed up the cron tests", Command{Verb: VerbDraft, Text: "speed up the cron tests"}},
		{"draft", Command{Verb: VerbDraft, Usage: "Usage: /goal draft <objective in plain language>"}},
		// draft must be a whole word.
		{"drafting docs", Command{Verb: VerbSet, Text: "drafting docs"}},
	}
	for _, tc := range cases {
		if got := ParseCommand(tc.in); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseCommandBareControlIgnoresRest(t *testing.T) {
	for verb, want := range map[string]string{"resume": VerbResume, "continue": VerbResume, "unpause": VerbResume,
		"pause": VerbPause, "status": VerbStatus, "show": VerbShow, "unwait": VerbUnwait} {
		got := ParseCommand(verb + " last goal")
		if got != (Command{Verb: want, Ignored: "last goal"}) {
			t.Errorf("%s: %+v", verb, got)
		}
	}
}

func TestIgnoredNote(t *testing.T) {
	want := "(ignored 'last goal': a control command never sets goal text — use /goal -- <text> " +
		"when the goal really starts with a control word.)"
	if got := IgnoredNote("last goal"); got != want {
		t.Fatalf("%q", got)
	}
	// The Python repr of a text with a single quote uses double quotes.
	if got := IgnoredNote("it's done"); got[:20] != `(ignored "it's done"` {
		t.Fatalf("%q", got)
	}
}

func TestParseCommandWait(t *testing.T) {
	cases := []struct {
		in   string
		want Command
	}{
		{"wait 4242", Command{Verb: VerbWait, PID: 4242}},
		{"wait 4242 the CI run", Command{Verb: VerbWait, PID: 4242, Reason: "the CI run"}},
		{"wait", Command{Verb: VerbWait, Usage: "Usage: /goal wait <pid> [reason]"}},
		{"wait abc", Command{Verb: VerbWait, Usage: "/goal wait: <pid> must be an integer process id."}},
	}
	for _, tc := range cases {
		if got := ParseCommand(tc.in); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseCommandGate(t *testing.T) {
	const usage = "Usage: /goal gate [list | add <command> | remove <N> | clear]"
	const notInt = "/goal gate remove: <N> must be an integer (1-based index)."
	cases := []struct {
		in   string
		want Command
	}{
		{"gate", Command{Verb: VerbGate, GateOp: "list"}},
		{"gate LIST", Command{Verb: VerbGate, GateOp: "list"}},
		{"gate add go test ./...", Command{Verb: VerbGate, GateOp: "add", GateArg: "go test ./..."}},
		{"gate remove 2", Command{Verb: VerbGate, GateOp: "remove", GateIndex: 2}},
		{"gate rm 1", Command{Verb: VerbGate, GateOp: "remove", GateIndex: 1}},
		{"gate clear", Command{Verb: VerbGate, GateOp: "clear"}},
		{"gate ok 3", Command{Verb: VerbGate, GateOp: "ok", GateIndex: 3}},
		{"gate no 3", Command{Verb: VerbGate, GateOp: "no", GateIndex: 3}},
		{"gate add", Command{Verb: VerbGate, Usage: usage}},
		{"gate clear all", Command{Verb: VerbGate, Usage: usage}},
		{"gate run make", Command{Verb: VerbGate, Usage: usage}},
		{"gate remove x", Command{Verb: VerbGate, Usage: notInt}},
		{"gate ok x", Command{Verb: VerbGate, Usage: usage}},
	}
	for _, tc := range cases {
		if got := ParseCommand(tc.in); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseSubgoal(t *testing.T) {
	cases := []struct {
		in   string
		want SubgoalCommand
	}{
		{"", SubgoalCommand{Op: "list"}},
		{"no sleeps in the test", SubgoalCommand{Op: "add", Text: "no sleeps in the test"}},
		{"remove 2", SubgoalCommand{Op: "remove", Index: 2}},
		{"REMOVE 2 extra", SubgoalCommand{Op: "remove", Index: 2}},
		{"remove", SubgoalCommand{Op: "remove", Usage: "Usage: /subgoal remove <n>"}},
		{"remove two", SubgoalCommand{Op: "remove", Usage: "/subgoal remove: <n> must be an integer (1-based index)."}},
		{"clear", SubgoalCommand{Op: "clear"}},
	}
	for _, tc := range cases {
		if got := ParseSubgoal(tc.in); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
}
