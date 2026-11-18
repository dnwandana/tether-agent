package gateway

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const (
	modeHelpWant    = "Set the approval mode of this chat or topic: /mode [read-only | guarded | full]"
	guardedManual   = "the approval gate asks for dangerous commands (approvals.mode: manual)."
	modeConfirmWant = "⚠️ **Full mode** runs dangerous commands in this topic with no question. Confirm?"
	modeCLILine     = "\n\nSend /mode read-only, /mode guarded, or /mode full.\n"
)

// modeText returns the /mode reply for the current mode and the guarded line.
func modeText(current, guarded string) string {
	return "🧭 **Mode of this topic: " + current + "**\n\n" +
		"**read-only**: the agent reads, searches, and answers. It cannot write files, run commands, or change memory, skills, or cron jobs.\n" +
		"**guarded**: " + guarded + "\n" +
		"**full**: no approval prompts in this topic. Hardline and human-only rules still apply."
}

// modeButtons returns the button row of the /mode reply. The current mode gets " ✓".
func modeButtons(current string) [][]Button {
	row := []Button{
		{Text: "👁 Read-only", Data: "/mode read-only"},
		{Text: "🛡 Guarded", Data: "/mode guarded"},
		{Text: "⚡ Full", Data: "/mode full"},
	}
	for i, m := range []string{"read-only", "guarded", "full"} {
		if m == current {
			row[i].Text += " ✓"
		}
	}
	return [][]Button{row}
}

// topicSession returns a session of the topic 7 of the group -100.
func topicSession(id string) *sessions.Session {
	return &sessions.Session{ID: id, Key: "telegram:group:-100:7", Source: "telegram", ChatID: "-100", ThreadID: "7"}
}

func TestModeCommandTelegram(t *testing.T) {
	ctx := context.Background()
	const scope = "telegram:-100:7"
	gate := &security.Gate{DB: openTestDB(t)}
	registerTestCommand(t, ModeCommand(gate))
	c, sent := interactiveCtx(t)
	c.Session = topicSession("s1")
	check := func(text string, buttons [][]Button) {
		t.Helper()
		r := lastSent(t, sent)
		if r.Text != text || !reflect.DeepEqual(r.Buttons, buttons) || !r.Edit {
			t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
		}
	}

	run(t, c, "/mode")
	check(modeText("guarded", guardedManual), modeButtons("guarded"))

	run(t, c, "/mode read-only")
	check(modeText("read-only", guardedManual), modeButtons("read-only"))
	if got := gate.ModeOf(ctx, scope); got != security.ModeReadOnly {
		t.Fatal(got)
	}

	// The mode belongs to the topic, so a new session of the topic keeps it.
	c.Session = topicSession("s2")
	run(t, c, "/mode")
	check(modeText("read-only", guardedManual), modeButtons("read-only"))

	// Full needs a confirm tap. Cancel is alone on the top row.
	run(t, c, "/mode full")
	check(modeConfirmWant, [][]Button{
		{{Text: "❌ Cancel", Data: "/mode"}},
		{{Text: "⚡ Confirm full", Data: "/mode full confirm"}},
	})
	if got := gate.ModeOf(ctx, scope); got != security.ModeReadOnly {
		t.Fatal("/mode full must wait for the confirm tap:", got)
	}
	run(t, c, "/mode full confirm")
	check(modeText("full", guardedManual), modeButtons("full"))
	if got := gate.ModeOf(ctx, scope); got != security.ModeFull {
		t.Fatal(got)
	}

	run(t, c, "/mode guarded")
	check(modeText("guarded", guardedManual), modeButtons("guarded"))
	if got := gate.ModeOf(ctx, scope); got != "" {
		t.Fatal("/mode guarded deletes the row:", got)
	}

	run(t, c, "/mode loud")
	if r := lastSent(t, sent); r.Text != modeHelpWant || r.Buttons != nil {
		t.Fatalf("%q %+v", r.Text, r.Buttons)
	}
}

func TestModeCommandCLI(t *testing.T) {
	ctx := context.Background()
	gate := &security.Gate{DB: openTestDB(t)}
	registerTestCommand(t, ModeCommand(gate))
	c, out := cmdCtx(t, "cli:t")
	c.Session = &sessions.Session{ID: "s1", Key: "cli:t", Source: "cli"}

	run(t, c, "/mode")
	if want := modeText("guarded", guardedManual) + modeCLILine; out.String() != want {
		t.Fatalf("got:\n%s", out)
	}

	// With no Confirm, the reply gives the typed command.
	out.Reset()
	run(t, c, "/mode full")
	if want := modeConfirmWant + "\nSend /mode full confirm to confirm.\n"; out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	if got := gate.ModeOf(ctx, "cli"); got != "" {
		t.Fatal(got)
	}

	// The y/N question: no keeps the mode, yes sets full.
	var asked []string
	answer := false
	c.Confirm = func(p string) bool { asked = append(asked, p); return answer }
	out.Reset()
	run(t, c, "/mode full")
	if got := gate.ModeOf(ctx, "cli"); got != "" || out.String() != modeText("guarded", guardedManual)+modeCLILine {
		t.Fatalf("%q\n%s", got, out)
	}
	answer = true
	out.Reset()
	run(t, c, "/mode full")
	if got := gate.ModeOf(ctx, "cli"); got != security.ModeFull || out.String() != modeText("full", guardedManual)+modeCLILine {
		t.Fatalf("%q\n%s", got, out)
	}
	const q = "Full mode runs dangerous commands in this topic with no question. Confirm?"
	if !slices.Equal(asked, []string{q, q}) {
		t.Fatalf("%q", asked)
	}
}

func TestModeGuardedLineFollowsApprovalsMode(t *testing.T) {
	registerTestCommand(t, ModeCommand(&security.Gate{DB: openTestDB(t)}))
	for mode, line := range map[string]string{
		"manual": "the approval gate asks for dangerous commands (approvals.mode: manual).",
		"smart":  "the smart judge checks dangerous commands first, then asks you (approvals.mode: smart).",
		"off":    "dangerous commands run with no question (approvals.mode: off). Hardline and human-only rules still apply.",
	} {
		c, sent := interactiveCtx(t)
		c.Session = topicSession("s1")
		c.Deps.Cfg.Approvals.Mode = mode
		run(t, c, "/mode")
		if r := lastSent(t, sent); r.Text != modeText("guarded", line) {
			t.Fatalf("%s:\n%s", mode, r.Text)
		}
	}
}

func TestModeOwnerOnlyAndWhileBusy(t *testing.T) {
	cmd := ModeCommand(nil)
	if cmd.Name != "mode" || cmd.MemberOK || cmd.MemberOwnSession || !cmd.WhileBusy || cmd.Help != modeHelpWant {
		t.Fatalf("%+v", cmd)
	}
	registerTestCommand(t, ModeCommand(&security.Gate{DB: openTestDB(t)}))
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	r := &recRenderer{}
	_ = g.Handle(context.Background(), member("k", "/mode full confirm"), func() Renderer { return r })
	if len(r.acks) != 1 || r.acks[0] != "⛔ Only the owner can use /mode." {
		t.Fatalf("%q", r.acks)
	}

	// A nil gate cannot read or set a mode.
	registerTestCommand(t, ModeCommand(nil))
	c, sent := interactiveCtx(t)
	c.Session = topicSession("s1")
	run(t, c, "/mode read-only")
	if r := lastSent(t, sent); r.Text != "⚠️ /mode is not available here." || r.Buttons != nil {
		t.Fatalf("%q %+v", r.Text, r.Buttons)
	}
}
