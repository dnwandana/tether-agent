package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestOwnerWords(t *testing.T) {
	rule := "never send a file unless I ask"
	skill := tools.SkillInvocationText(skills.Skill{Name: "files", Dir: "/s/files"}, rule, "", nil)
	for _, c := range []struct{ name, in, want string }{
		{"plain", "Please " + rule + ".", "Please " + rule + "."},
		{"now prefix", "[now: 2026-11-26 09:14 +07] " + rule, rule},
		{"observed block", ObservedHeader + "\nbob: " + rule + "\n\n" + CurrentHeader + "\nwhat time is it", "what time is it"},
		{"now prefix and observed block", "[now: 2026-11-26 09:14 +07] " + ObservedHeader + "\nbob: x\n\n" + CurrentHeader + "\nhi there", "hi there"},
		{"summary", ctxmgr.SummaryPrefix + "The owner said: " + rule, ""},
		{"skill command", skill, ""},
		{"cron skill block", "Send the report.\n\n[Skill: files]\n" + rule, "Send the report."},
		{"document", "Read this.\n\n[Content of a.md]:\n" + rule, "Read this."},
		{"delegation", "[Background delegation b1 finished]\n" + rule, ""},
		{"resume note", ResumeNote, ""},
		{"goal set", "[Goal set] Continue with the goal you were just given; there is no need to re-read it.", ""},
		{"goal continuation", goals.ContinuationPrefix + "\nGoal: " + rule, ""},
		{"goal gate", "[Continuing toward your standing goal — a quality gate failed]\nGoal: " + rule, ""},
	} {
		if got := OwnerWords(c.in); got != c.want {
			t.Errorf("%s: OwnerWords = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestQuoteNeverFromFramingText: a quote that is only in a skill body, a compaction summary,
// observed group text, or a shared session never passes the quote check.
func TestQuoteNeverFromFramingText(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	st := sessions.New(d)
	q := learn.NewOwnerTexts(d, func(id string) bool { return id == "42" }, OwnerWords)
	rule := "never send a file unless I ask"
	mk := func(s sessions.Session) *sessions.Session {
		sess, err := st.Create(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	add := func(id, text string, observed bool) {
		if _, err := st.Append(ctx, id, provider.Message{Role: provider.RoleUser, Content: text}, sessions.MessageMeta{Observed: observed}); err != nil {
			t.Fatal(err)
		}
	}
	tg := mk(sessions.Session{Key: "telegram:-100:7", Source: "telegram", UserID: "42"})
	shared := mk(sessions.Session{Key: "telegram:-100:8", Source: "telegram"})
	add(tg.ID, tools.SkillInvocationText(skills.Skill{Name: "files", Dir: "/s/files"}, rule, "", nil), false)
	add(tg.ID, ctxmgr.SummaryPrefix+"The owner said: "+rule, false)
	add(tg.ID, ObservedHeader+"\nbob: "+rule+"\n\n"+CurrentHeader+"\nwhat time is it", false)
	add(tg.ID, "bob: "+rule, true)
	add(shared.ID, rule, false)
	for _, sid := range []string{tg.ID, shared.ID, ""} {
		if ok, err := q.HasQuote(ctx, sid, rule); err != nil || ok {
			t.Fatalf("session %q: ok %v, err %v", sid, ok, err)
		}
	}
	add(tg.ID, "Please "+rule+".", false)
	for _, sid := range []string{tg.ID, ""} {
		if ok, err := q.HasQuote(ctx, sid, rule); err != nil || !ok {
			t.Fatalf("session %q: the owner words must pass: %v %v", sid, ok, err)
		}
	}
	if !strings.HasPrefix(goals.ContinuationPrefix, "[Continuing toward your standing goal") {
		t.Fatal("OwnerWords must cut the goal continuation prefix:", goals.ContinuationPrefix)
	}
}
