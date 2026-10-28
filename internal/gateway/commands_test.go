package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// cmdCtx returns a command context for key over a new store, and the buffer of its replies.
func cmdCtx(t *testing.T, key string) (*CommandCtx, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	d := testDeps(t, &fakeProv{})
	return &CommandCtx{Deps: d, Key: key, Reply: func(s string) { fmt.Fprintln(&out, s) }}, &out
}

// run runs one command line in c.
func run(t *testing.T, c *CommandCtx, text string) {
	t.Helper()
	name, args, ok := IsCommand(text)
	if !ok {
		t.Fatalf("%q is not a command", text)
	}
	cmd, ok := Commands()[name]
	if !ok {
		t.Fatalf("no command %s", name)
	}
	if err := cmd.Run(context.Background(), c, args); err != nil {
		t.Fatal(err)
	}
}

// active returns the id of the active session of the key of c.
func active(t *testing.T, c *CommandCtx) string {
	t.Helper()
	s, err := c.Deps.Store.Active(context.Background(), c.Key)
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	return s.ID
}

func TestNewResumeRenameArchiveDelete(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/new")
	first := c.Session.ID
	run(t, c, "/new")
	if c.Session.ID == first || active(t, c) != c.Session.ID {
		t.Fatal("new")
	}
	run(t, c, "/sessions")
	if !strings.Contains(out.String(), first) || !strings.Contains(out.String(), "**▸ New session** · now _(current)_") {
		t.Fatal(out)
	}
	run(t, c, "/resume "+first)
	if active(t, c) != first || c.Session.ID != first {
		t.Fatal("resume")
	}
	run(t, c, "/rename scrape   allsaints")
	if s, _ := c.Deps.Store.Get(ctx, first); s.Title != "scrape allsaints" {
		t.Fatal(s.Title)
	}
	run(t, c, "/archive")
	out.Reset()
	run(t, c, "/sessions")
	if strings.Contains(out.String(), first) {
		t.Fatal("archived session listed:", out)
	}
	c.Deps.Agent.Prompts = agent.NewPromptCache(func(context.Context, *sessions.Session, string) (string, time.Time) {
		return "p", time.Now()
	})
	c.Deps.Agent.Prompts.Get(ctx, &sessions.Session{ID: first}, agent.PromptKey{SessionID: first}, "cli")
	c.Confirm = func(string) bool { return true }
	run(t, c, "/delete "+first)
	if _, err := c.Deps.Store.Get(ctx, first); err == nil {
		t.Fatal("deleted")
	}
	if c.Deps.Agent.Prompts.Len() != 0 {
		t.Fatal("the prompt of the deleted session must be forgotten")
	}
	if c.Session != nil {
		t.Fatal("the deleted session must not stay current")
	}
}

func TestCommandsActOnOwnKeyOnly(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "cli:t")
	other, _ := c.Deps.Store.Create(ctx, sessions.Session{Key: "telegram:dm:9", Source: "telegram"})
	c.Confirm = func(string) bool { return true }
	run(t, c, "/resume "+other.ID)
	run(t, c, "/delete "+other.ID)
	if strings.Count(out.String(), "This chat has no session `"+other.ID+"`.") != 2 {
		t.Fatal(out)
	}
	if _, err := c.Deps.Store.Get(ctx, other.ID); err != nil {
		t.Fatal("the session of another key must stay")
	}
}

func TestDeleteWithoutConfirmNeedsConfirmWord(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "telegram:dm:1")
	s, _ := c.Deps.Store.Create(ctx, sessions.Session{Key: c.Key})
	run(t, c, "/delete "+s.ID)
	want := "🗑 **Delete \"New session\"?**\nThis deletes 0 messages. You cannot undo this.\n" +
		"To delete it, send: /delete " + s.ID + " confirm\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	if _, err := c.Deps.Store.Get(ctx, s.ID); err != nil {
		t.Fatal("deleted without a confirm step")
	}
	run(t, c, "/delete "+s.ID+" confirm")
	if _, err := c.Deps.Store.Get(ctx, s.ID); err == nil {
		t.Fatal("not deleted")
	}
	c.Confirm = func(string) bool { return false }
	s2, _ := c.Deps.Store.Create(ctx, sessions.Session{Key: c.Key})
	run(t, c, "/delete "+s2.ID)
	if _, err := c.Deps.Store.Get(ctx, s2.ID); err != nil || !strings.Contains(out.String(), "Not deleted.") {
		t.Fatal("a no answer must keep the session")
	}
}

func TestNewCallsBeforeNewHook(t *testing.T) {
	c, _ := cmdCtx(t, "telegram:dm:1")
	run(t, c, "/new")
	old := c.Session
	var got *sessions.Session
	c.Hooks.BeforeNew = func(_ context.Context, s *sessions.Session) { got = s }
	run(t, c, "/reset")
	if got == nil || got.ID != old.ID || c.Session.ID == old.ID {
		t.Fatal(got, old)
	}
	if c.Session.Source != "telegram" {
		t.Fatalf("%+v", c.Session)
	}
}

func TestModelAndPromptOverrides(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/model cheap:glm-4.7-flash")
	run(t, c, "/prompt set Answer in Indonesian.")
	s, _ := c.Deps.Store.Get(ctx, c.Session.ID)
	if s.ModelOverride != "cheap:glm-4.7-flash" || s.PromptOverride != "Answer in Indonesian." {
		t.Fatal(s)
	}
	run(t, c, "/prompt show")
	if !strings.Contains(out.String(), "Answer in Indonesian.") {
		t.Fatal(out)
	}
	run(t, c, "/prompt clear")
	run(t, c, "/model clear")
	s, _ = c.Deps.Store.Get(ctx, c.Session.ID)
	if s.PromptOverride != "" || s.ModelOverride != "" {
		t.Fatal(s)
	}
	run(t, c, "/model cheap")
	if s, _ = c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "cheap" {
		t.Fatal(s)
	}
}

func TestModelUnknownProviderRejected(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/model nope")
	if out.String() != "⚠️ **Unknown provider: nope**\nThe providers are: cheap, primary.\n" {
		t.Fatal(out)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s)
	}
}

func TestIsCommandStripsBotName(t *testing.T) {
	if n, a, ok := IsCommand("/new@tether_bot now"); !ok || n != "new" || a != "now" {
		t.Fatal(n, a, ok)
	}
	for _, in := range []string{"hello", "/etc/hosts is odd", "/", "/@bot", " /"} {
		if _, _, ok := IsCommand(in); ok {
			t.Fatal(in)
		}
	}
	if n, a, ok := IsCommand("/Prompt set\nline two"); !ok || n != "prompt" || a != "set\nline two" {
		t.Fatalf("%q %q %v", n, a, ok)
	}
}

func TestForkCommandSwitchesKey(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/new")
	orig := c.Session.ID
	if _, err := c.Deps.Store.Append(ctx, orig, provider.Message{Role: provider.RoleUser, Content: "hi"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/fork try B")
	f := active(t, c)
	want := "🍴 **Copied to \"try B\"**\nYou are in the copy now. \"New session\" did not change.\n" +
		"Send /resume " + orig + " to go back."
	if f == orig || c.Session.ID != f || !strings.Contains(out.String(), want) {
		t.Fatal(out)
	}
	s, _ := c.Deps.Store.Get(ctx, f)
	if s.ParentID != orig || s.Title != "try B" {
		t.Fatalf("%+v", s)
	}
	if msgs, _ := c.Deps.Store.Messages(ctx, f); len(msgs) != 1 {
		t.Fatal(len(msgs))
	}
}

// cmdCtxDB returns cmdCtx with Agent.DB set to the database of the session store.
func cmdCtxDB(t *testing.T, key string) (*CommandCtx, *bytes.Buffer) {
	t.Helper()
	c, out := cmdCtx(t, key)
	d := openTestDB(t)
	c.Deps.Store = sessions.New(d)
	c.Deps.Agent.Store = c.Deps.Store
	c.Deps.Agent.DB = d
	return c, out
}

func TestUsageCommand(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtxDB(t, "cli:t")
	run(t, c, "/new")
	st := c.Deps.Store
	st.Append(ctx, c.Session.ID, provider.Message{Role: provider.RoleAssistant, Content: "a"},
		sessions.MessageMeta{Model: "glm-4.7", Provider: "primary", Usage: provider.Usage{InputTokens: 188000, OutputTokens: 10}})
	other, _ := st.Create(ctx, sessions.Session{Key: "cli:other"})
	st.Append(ctx, other.ID, provider.Message{Role: provider.RoleAssistant, Content: "b"},
		sessions.MessageMeta{Model: "z-ai/glm-4.7", Provider: "openrouter", CostUSD: 0.07})
	flat := c.Deps.Cfg.Providers["primary"]
	flat.FlatRate = true
	c.Deps.Cfg.Providers["primary"] = flat

	run(t, c, "/usage")
	for _, want := range []string{"📊 **Usage: today**\n", "glm-4.7", "188k", "flat plan", "z-ai/glm-4.7", "$0.07", "tok/s"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, "\n", out)
		}
	}
	out.Reset()
	run(t, c, "/usage session")
	if strings.Contains(out.String(), "z-ai") || !strings.Contains(out.String(), "glm-4.7") || !strings.Contains(out.String(), "Compactions: 0") {
		t.Fatal(out)
	}
	out.Reset()
	run(t, c, "/usage 1y")
	if out.String() != "⚠️ **Unknown range: 1y**\nThe ranges are: today, 7d, 30d, session.\n" {
		t.Fatal(out)
	}
}

// seedUsage writes the calls of the /usage examples to the session of c and returns the
// usage rows of today.
func seedUsage(t *testing.T, c *CommandCtx) []obs.UsageRow {
	t.Helper()
	ctx := context.Background()
	s, err := c.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []sessions.MessageMeta{
		{Model: "anthropic/claude-sonnet-5", Provider: "openrouter", CostUSD: 1.62, Usage: provider.Usage{InputTokens: 412300, OutputTokens: 21700}},
		{Model: "zai/glm-4.6", Provider: "primary", Usage: provider.Usage{InputTokens: 88100, OutputTokens: 4200}},
	} {
		if _, err := c.Deps.Store.Append(ctx, s.ID, provider.Message{Role: provider.RoleAssistant, Content: "a"}, m); err != nil {
			t.Fatal(err)
		}
	}
	flat := c.Deps.Cfg.Providers["primary"]
	flat.FlatRate = true
	c.Deps.Cfg.Providers["primary"] = flat
	since, _ := obs.UsageSince("today", time.Now())
	rows, _, err := obs.UsageReport(ctx, c.Deps.Agent.DB, since, "", FlatProviders(c.Deps.Cfg))
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	return rows
}

// wantUsageButtons returns the range buttons of /usage with a check mark on the button at i.
func wantUsageButtons(i int) [][]Button {
	row := []Button{{Text: "Today", Data: "/usage today"}, {Text: "7 days", Data: "/usage 7d"},
		{Text: "30 days", Data: "/usage 30d"}, {Text: "Session", Data: "/usage session"}}
	row[i].Text = "✓ " + row[i].Text
	return [][]Button{row}
}

func TestUsageCommandRichReply(t *testing.T) {
	c, sent := interactiveCtx(t)
	rows := seedUsage(t, c)
	run(t, c, "/usage")
	r := lastSent(t, sent)
	if want := "📊 **Usage: today**\n\n" + obs.FormatUsageMarkdown(rows); r.Rich != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Rich, want)
	}
	if want := "📊 **Usage: today**\n```\n" + obs.FormatUsageNarrow(rows) + "\n```"; r.Text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
	if !reflect.DeepEqual(r.Buttons, wantUsageButtons(0)) || r.Edit {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/usage 7d")
	r = lastSent(t, sent)
	if !strings.HasPrefix(r.Rich, "📊 **Usage: 7 days**\n\n| Model |") || !strings.HasPrefix(r.Text, "📊 **Usage: 7 days**\n```\n") ||
		!r.Edit || !reflect.DeepEqual(r.Buttons, wantUsageButtons(1)) {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/usage 30d")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "📊 **Usage: 30 days**\n") || !reflect.DeepEqual(r.Buttons, wantUsageButtons(2)) {
		t.Fatalf("%+v", r)
	}
}

func TestUsageCommandSessionReply(t *testing.T) {
	c, sent := interactiveCtx(t)
	c.Session = titled(t, c, "my_trip", 0)
	rows := seedUsage(t, c)
	run(t, c, "/usage session")
	r := lastSent(t, sent)
	head := "📊 **Usage: \"my\\_trip\"**"
	if want := head + "\n\n" + obs.FormatUsageMarkdown(rows) + "\n\nCompactions: 0"; r.Rich != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Rich, want)
	}
	if want := head + "\n```\n" + obs.FormatUsageNarrow(rows) + "\n```\nCompactions: 0"; r.Text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
	if !reflect.DeepEqual(r.Buttons, wantUsageButtons(3)) || !r.Edit {
		t.Fatalf("%+v", r)
	}
}

func TestUsageCommandSessionEmpty(t *testing.T) {
	c, sent := interactiveCtx(t)
	c.Session = titled(t, c, "Plan the Bali trip", 0)
	run(t, c, "/usage session")
	r := lastSent(t, sent)
	if r.Text != "📊 **Usage: \"Plan the Bali trip\"**\nNo model calls yet. 0 compactions." || r.Rich != "" {
		t.Fatalf("%+v", r)
	}
	if !reflect.DeepEqual(r.Buttons, wantUsageButtons(3)) {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestUsageCommandRangeEmpty(t *testing.T) {
	c, sent := interactiveCtx(t)
	run(t, c, "/usage")
	r := lastSent(t, sent)
	if r.Text != "📊 **Usage: today**\nNo model calls in this range." || r.Rich != "" || !reflect.DeepEqual(r.Buttons, wantUsageButtons(0)) {
		t.Fatalf("%+v", r)
	}
}

func TestUsageCommandCLIKeepsWideTable(t *testing.T) {
	c, out := cmdCtxDB(t, "cli:t")
	rows := seedUsage(t, c)
	run(t, c, "/usage")
	if want := "📊 **Usage: today**\n" + obs.FormatUsage(rows) + "\n"; out.String() != want || !strings.Contains(want, "tok/s") {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestUsageCommandNoAgent(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	c.Deps.Agent = nil
	run(t, c, "/usage")
	if out.String() != "📊 **Usage is not available**\n" {
		t.Fatalf("%q", out)
	}
}

// seedTurns appends n user and assistant pairs to the session.
func seedTurns(t *testing.T, st *sessions.Store, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		for _, role := range []provider.Role{provider.RoleUser, provider.RoleAssistant} {
			if _, err := st.Append(context.Background(), id, provider.Message{Role: role, Content: string(role) + " " + strings.Repeat("x", 20)}, sessions.MessageMeta{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNewStartsBackgroundReviewOfOldSession(t *testing.T) {
	ctx := context.Background()
	c, out := cmdCtxDB(t, "cli:t")
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	c.Deps.Agent.Tools.Register(tools.MemoryTool{Store: m})
	fp := c.Deps.Agent.Prov.(*fakeProv)
	fp.delay = 300 * time.Millisecond
	run(t, c, "/new")
	old := c.Session
	seedTurns(t, c.Deps.Store, old.ID, 6)
	start := time.Now()
	run(t, c, "/new")
	if time.Since(start) > 200*time.Millisecond || !strings.Contains(out.String(), "✨ Starting new session.") {
		t.Fatal("the reply must not wait for the review:", time.Since(start), out)
	}
	// The review runs in the background over the old session. With no skill_manage, it is
	// a memory review.
	var review string
	waitFor(t, func() bool {
		var n int
		row := c.Deps.Agent.DB.QueryRowContext(ctx, `SELECT id, (SELECT COUNT(*) FROM messages WHERE session_id = s.id)
			FROM sessions s WHERE parent_id = ? AND kind = 'review'`, old.ID)
		return row.Scan(&review, &n) == nil && n >= 2
	})
	texts := fp.lastUserTexts()
	if len(texts) != 1 || !strings.HasPrefix(texts[0], agent.MemoryReviewPrompt) {
		t.Fatal(texts)
	}
}

func TestNewReviewNeedsSixUserTurns(t *testing.T) {
	c, _ := cmdCtxDB(t, "cli:t")
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	c.Deps.Agent.Tools.Register(tools.MemoryTool{Store: m})
	run(t, c, "/new")
	seedTurns(t, c.Deps.Store, c.Session.ID, 5)
	run(t, c, "/new")
	time.Sleep(50 * time.Millisecond)
	if n := len(c.Deps.Agent.Prov.(*fakeProv).lastUserTexts()); n != 0 {
		t.Fatal("5 user turns must not start a review:", n)
	}
}

func TestCompactCommand(t *testing.T) {
	c, out := cmdCtxDB(t, "cli:t")
	run(t, c, "/compact")
	if out.String() != "🗜 **Compaction is not available**\n" {
		t.Fatal(out)
	}
	c.Deps.Agent.Compactor = &ctxmgr.Compactor{Store: c.Deps.Store, DB: c.Deps.Agent.DB, Cfg: c.Deps.Cfg.Context}
	out.Reset()
	run(t, c, "/new")
	run(t, c, "/compact")
	if !strings.Contains(out.String(), "🗜 **Nothing to compact**\nThis session is still short.\n") {
		t.Fatal(out)
	}
	seedTurns(t, c.Deps.Store, c.Session.ID, 15)
	out.Reset()
	run(t, c, "/compact")
	if out.String() != "🗜 **Compacted**\n7 messages → 1 summary. The session keeps the context.\n" {
		t.Fatal(out)
	}
}

func TestReloadMCPCommand(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	defer Register(ReloadMCPCommand(nil))
	run(t, c, "/reload-mcp")
	if out.String() != "🔌 **No MCP servers**\nAdd a server under `mcp.servers` in config.yaml.\n" {
		t.Fatal(out)
	}
	m := &mcp.Manager{CacheDir: t.TempDir()}
	if err := m.Init(context.Background(), c.Deps.Agent.Tools); err != nil {
		t.Fatal(err)
	}
	Register(ReloadMCPCommand(m))
	out.Reset()
	run(t, c, "/reload-mcp")
	want := "🔌 **MCP servers reloaded**\nNo MCP servers are enabled.\n" +
		"_The changes apply from your next message. That message does not use the prompt cache._\n"
	if out.String() != want || m.Generation() != 1 {
		t.Fatalf("%q %d", out, m.Generation())
	}
}

func TestReloadMCPReply(t *testing.T) {
	const last = "_The changes apply from your next message. That message does not use the prompt cache._"
	got := reloadMCPReply(mcp.ReloadResult{Reloaded: []string{"github"}, Added: []string{"notion"}, Servers: 2, Tools: 14}, nil)
	want := "🔌 **MCP servers reloaded**\n• Reloaded: github\n• Added: notion\n14 tools from 2 servers.\n" + last
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	res := mcp.ReloadResult{Reloaded: []string{"fs", "gh"}, Added: []string{"web"}, Removed: []string{"old"},
		Servers: 3, Tools: 7, Skipped: []string{"mcp_gh_read_file"}}
	got = reloadMCPReply(res, errors.New("mcp web: connection refused"))
	want = "🔌 **MCP servers reloaded**\n" +
		"• Reloaded: fs, gh\n" +
		"• Added: web\n" +
		"• Removed: old\n" +
		"7 tools from 3 servers.\n" +
		"• Not added, because another tool has the same name: mcp\\_gh\\_read\\_file\n" +
		"⚠️ Some servers did not reload. They keep their last tool lists: mcp web: connection refused\n" +
		last
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	got = reloadMCPReply(mcp.ReloadResult{}, fmt.Errorf("%w: bad yaml", mcp.ErrServerList))
	if got != "⚠️ **MCP reload failed**\nmcp: cannot read the server list: bad yaml\nThe MCP tools did not change." {
		t.Fatal(got)
	}
	got = reloadMCPReply(mcp.ReloadResult{Removed: []string{"gh"}}, nil)
	want = "🔌 **MCP servers reloaded**\n• Removed: gh\nNo MCP servers are enabled.\n" + last
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	got = reloadMCPReply(mcp.ReloadResult{Reloaded: []string{"gh"}, Servers: 1, Tools: 1}, nil)
	want = "🔌 **MCP servers reloaded**\n• Reloaded: gh\n1 tool from 1 server.\n" + last
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHelpTelegram(t *testing.T) {
	c, sent := interactiveCtx(t)
	want := "📖 **Commands**\n\n**Sessions**\n/new · start a new session\n/sessions · see and resume sessions\n" +
		"/rename · change the title\n/fork · copy this session\n/archive · hide this session\n/delete · delete a session\n\n" +
		"**This session**\n/model · change the model\n/prompt · change the prompt\n/compact · make the context shorter\n" +
		"/usage · see tokens and cost\n\n**The current task**\n/stop · stop it\n/steer · send it a note\n" +
		"/approve · /deny · answer an approval\n\n**Setup**\n/sethome · send cron results to this chat\n/reload_mcp · reload the MCP servers"
	run(t, c, "/help")
	if r := lastSent(t, sent); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
	if cmd := Commands()["help"]; !cmd.WhileBusy || cmd.Help != "Show all commands" {
		t.Fatalf("%+v", cmd)
	}
}

func TestHelpCLI(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/help")
	want := "📖 **Commands**\n\n**Sessions**\n/new · start a new session\n/sessions · see and resume sessions\n" +
		"/rename · change the title\n/fork · copy this session\n/archive · hide this session\n/delete · delete a session\n\n" +
		"**This session**\n/model · change the model\n/prompt · change the prompt\n/compact · make the context shorter\n" +
		"/usage · see tokens and cost\n\n**The current task**\n/stop · stop it\n/steer · send it a note\n\n" +
		"**Setup**\n/reload-mcp · reload the MCP servers\n/exit · leave the chat\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out.String(), "/approve") || strings.Contains(out.String(), "/sethome") {
		t.Fatal(out)
	}
}

func TestDeleteCallsAfterDeleteHook(t *testing.T) {
	c, _ := cmdCtx(t, "cli:t")
	var got []string
	c.Deps.AfterDelete = func(id string) { got = append(got, id) }
	run(t, c, "/new")
	id := c.Session.ID
	run(t, c, "/delete "+id+" "+confirmWord)
	if len(got) != 1 || got[0] != id {
		t.Fatal(got)
	}
	// A refused delete does not call the hook.
	run(t, c, "/delete nope "+confirmWord)
	if len(got) != 1 {
		t.Fatal(got)
	}
}

// interactiveCtx returns a command context for telegram:dm:1 that records each sent Reply.
func interactiveCtx(t *testing.T) (*CommandCtx, *[]Reply) {
	t.Helper()
	c, _ := cmdCtxDB(t, "telegram:dm:1")
	var sent []Reply
	c.Send = func(r Reply) { sent = append(sent, r) }
	return c, &sent
}

// setNow fixes the clock of the commands at now for the test.
func setNow(t *testing.T, now time.Time) {
	t.Helper()
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })
}

// lastSent returns the last Reply in sent.
func lastSent(t *testing.T, sent *[]Reply) Reply {
	t.Helper()
	if len(*sent) == 0 {
		t.Fatal("no reply")
	}
	return (*sent)[len(*sent)-1]
}

// titled creates a session of the key of c with the title and n messages.
func titled(t *testing.T, c *CommandCtx, title string, n int) *sessions.Session {
	t.Helper()
	ctx := context.Background()
	s, err := c.Deps.Store.Create(ctx, sessions.Session{Key: c.Key, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := c.Deps.Store.Append(ctx, s.ID, provider.Message{Role: provider.RoleUser, Content: "x"}, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSessionsCommandUsesList(t *testing.T) {
	c, sent := interactiveCtx(t)
	setNow(t, time.Now())
	titled(t, c, "One", 0)
	titled(t, c, "Two", 0)
	run(t, c, "/sessions")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "📋 **Your sessions** · 1–2 of 2\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/sessions archived")
	if r := lastSent(t, sent); r.Text != "🗄 **No archived sessions**" {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/sessions 2")
	if r := lastSent(t, sent); !r.Edit || !strings.HasPrefix(r.Text, "📋 **Your sessions** · 1–2 of 2\n") {
		t.Fatalf("%+v", r)
	}
}

func TestResumeByIDReply(t *testing.T) {
	c, sent := interactiveCtx(t)
	now := time.Date(2026, 10, 28, 22, 0, 0, 0, time.Local)
	setNow(t, now)
	s := titled(t, c, "Docker cleanup", 15)
	setLastActive(t, c, s, time.Date(2026, 10, 28, 21, 40, 0, 0, time.Local))
	run(t, c, "/resume "+s.ID)
	r := lastSent(t, sent)
	if want := "↩️ **Back to \"Docker cleanup\"**\n15 messages. Last message today at 21:40."; r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
	if active(t, c) != s.ID || c.Session.ID != s.ID {
		t.Fatal("resume must set the active session")
	}

	one := titled(t, c, "", 1)
	setLastActive(t, c, one, time.Date(2025, 10, 12, 9, 5, 0, 0, time.Local))
	run(t, c, "/resume "+one.ID)
	if r := lastSent(t, sent); r.Text != "↩️ **Back to \"New session\"**\n1 message. Last message Oct 12, 2025 at 09:05." {
		t.Fatalf("got:\n%s", r.Text)
	}
	empty := titled(t, c, "a_b", 0)
	run(t, c, "/resume "+empty.ID)
	if r := lastSent(t, sent); r.Text != "↩️ **Back to \"a\\_b\"**\nNo messages yet." {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestResumeClearsArchive(t *testing.T) {
	ctx := context.Background()
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Plan the Bali trip", 2)
	if err := c.Deps.Store.Archive(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/resume "+s.ID)
	if got, _ := c.Deps.Store.Get(ctx, s.ID); got.Archived || c.Session.Archived {
		t.Fatal("resume must clear the archive flag")
	}
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "↩️ **Back to \"Plan the Bali trip\"**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestResumeDeletedSessionNotFound(t *testing.T) {
	ctx := context.Background()
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Gone", 0)
	if err := c.Deps.Store.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/resume "+s.ID)
	want := "⚠️ **Session not found**\nThis chat has no session `" + s.ID + "`. Send /sessions to see your sessions."
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestResumeNoArgShowsResumeList(t *testing.T) {
	c, sent := interactiveCtx(t)
	titled(t, c, "Plan the Bali trip", 0)
	titled(t, c, "Fix nginx config", 0)
	run(t, c, "/resume")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "↩️ **Resume a session** · ") {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/resume bali")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "↩️ **Sessions with \"bali\"** · 1 found\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestDeleteNoArgShowsDeleteList(t *testing.T) {
	c, sent := interactiveCtx(t)
	titled(t, c, "Fix nginx config", 0)
	run(t, c, "/delete")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "🗑 **Delete a session** · ") {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/delete nginx")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "🗑 **Sessions with \"nginx\"** · 1 found\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestDeleteAsksWithButtons(t *testing.T) {
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Fix nginx config", 12)
	run(t, c, "/delete "+s.ID)
	r := lastSent(t, sent)
	if want := "🗑 **Delete \"Fix nginx config\"?**\nThis deletes 12 messages. You cannot undo this."; r.Text != want || r.Edit {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := [][]Button{{{Text: "🗑 Delete", Data: "/delete " + s.ID + " confirm"}, {Text: "Cancel", Data: "cancel"}}}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	if _, err := c.Deps.Store.Get(context.Background(), s.ID); err != nil {
		t.Fatal("the question must not delete")
	}
}

func TestDeleteConfirmEditsMessage(t *testing.T) {
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Fix nginx config", 12)
	run(t, c, "/delete "+s.ID+" confirm")
	r := lastSent(t, sent)
	if r.Text != "🗑 **Deleted \"Fix nginx config\"**" || !r.Edit || r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
	if _, err := c.Deps.Store.Get(context.Background(), s.ID); err == nil {
		t.Fatal("not deleted")
	}
	// A tap after the delete gets the not-found reply.
	run(t, c, "/delete "+s.ID+" confirm")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ **Session not found**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestDeleteCLIConfirmQuestion(t *testing.T) {
	c, out := cmdCtxDB(t, "cli:t")
	s := titled(t, c, "Fix nginx config", 12)
	var prompt string
	c.Confirm = func(p string) bool { prompt = p; return false }
	run(t, c, "/delete "+s.ID)
	if prompt != "Delete \"Fix nginx config\"? This deletes 12 messages. You cannot undo this." {
		t.Fatal(prompt)
	}
	if out.String() != "Not deleted.\n" {
		t.Fatalf("%q", out)
	}
	c.Confirm = func(string) bool { return true }
	out.Reset()
	run(t, c, "/delete "+s.ID)
	if out.String() != "🗑 **Deleted \"Fix nginx config\"**\n" {
		t.Fatalf("%q", out)
	}
}

func TestDeleteOtherKeyRefusedWithConfirm(t *testing.T) {
	ctx := context.Background()
	c, sent := interactiveCtx(t)
	other, _ := c.Deps.Store.Create(ctx, sessions.Session{Key: "telegram:dm:9"})
	run(t, c, "/delete "+other.ID+" confirm")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ **Session not found**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if _, err := c.Deps.Store.Get(ctx, other.ID); err != nil {
		t.Fatal("the session of another key must stay")
	}
}

func TestArchiveReplyHasUndo(t *testing.T) {
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Plan the Bali trip", 0)
	c.Session = s
	run(t, c, "/archive")
	r := lastSent(t, sent)
	if r.Text != "🗄 **Archived \"Plan the Bali trip\"**\nThe session list does not show it now." {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := [][]Button{{{Text: "↩️ Undo", Data: "/resume " + s.ID}, {Text: "🆕 New session", Data: "/new"}}}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	// The Undo button takes the session out of the archive.
	run(t, c, r.Buttons[0][0].Data)
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "↩️ **Back to \"Plan the Bali trip\"**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if got, _ := c.Deps.Store.Get(context.Background(), s.ID); got.Archived {
		t.Fatal("Undo must clear the archive flag")
	}
}

func TestArchiveCLIUndoHint(t *testing.T) {
	c, out := cmdCtxDB(t, "cli:t")
	s := titled(t, c, "Plan the Bali trip", 0)
	c.Session = s
	run(t, c, "/archive")
	want := "🗄 **Archived \"Plan the Bali trip\"**\nThe session list does not show it now.\nSend /resume " + s.ID + " to undo.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestForkReply(t *testing.T) {
	c, sent := interactiveCtx(t)
	s := titled(t, c, "Plan the Bali trip", 1)
	c.Session = s
	run(t, c, "/fork Bali plan B")
	r := lastSent(t, sent)
	if r.Text != "🍴 **Copied to \"Bali plan B\"**\nYou are in the copy now. \"Plan the Bali trip\" did not change." {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := [][]Button{{{Text: "↩️ Back to \"Plan the Bali trip\"", Data: "/resume " + s.ID}}}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	c.Session = s
	run(t, c, "/fork")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "🍴 **Copied to \"Plan the Bali trip (fork)\"**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestRenameReplies(t *testing.T) {
	c, sent := interactiveCtx(t)
	c.Session = titled(t, c, "Plan the Bali trip", 0)
	run(t, c, "/rename")
	if r := lastSent(t, sent); r.Text != "✏️ **Title: Plan the Bali trip**\nTo change it, send `/rename <new title>`." {
		t.Fatalf("got:\n%s", r.Text)
	}
	c.Session = titled(t, c, "", 0)
	run(t, c, "/rename")
	if r := lastSent(t, sent); r.Text != "✏️ **Title: none**\nTo change it, send `/rename <new title>`." {
		t.Fatalf("got:\n%s", r.Text)
	}
	// With no session, /rename shows the usage and does not create a session.
	c.Session = nil
	run(t, c, "/rename")
	if r := lastSent(t, sent); r.Text != "✏️ **Title: none**\nTo change it, send `/rename <new title>`." || c.Session != nil {
		t.Fatalf("got:\n%s\nsession %+v", r.Text, c.Session)
	}
	if list, total, err := c.store().ListPage(context.Background(), c.Key, sessions.ListQuery{}); err != nil || total != 2 {
		t.Fatalf("%+v %d %v", list, total, err)
	}
	c.Session = titled(t, c, "", 0)
	run(t, c, "/rename Trip   budget")
	if r := lastSent(t, sent); r.Text != "✏️ **Renamed to \"Trip budget\"**" {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/rename my_project")
	if r := lastSent(t, sent); r.Text != "✏️ **Renamed to \"my\\_project\"**" {
		t.Fatalf("got:\n%s", r.Text)
	}
}

// modelProv is fakeProv with another model name.
type modelProv struct {
	*fakeProv
	model string
}

func (m modelProv) Model() string { return m.model }

// fixTip makes pickTip return tip for the test.
func fixTip(t *testing.T, tip string) {
	t.Helper()
	old := pickTip
	pickTip = func() string { return tip }
	t.Cleanup(func() { pickTip = old })
}

func TestNewReplyFormat(t *testing.T) {
	fixTip(t, "T")
	c, sent := interactiveCtx(t)
	c.Deps.Agent.Prov = modelProv{fakeProv: &fakeProv{}, model: "anthropic/claude-sonnet-5"}
	pc := c.Deps.Cfg.Providers["primary"]
	pc.ContextWindow = 200000
	c.Deps.Cfg.Providers["primary"] = pc
	want := "✨ Starting new session.\nSend a message to begin.\n\nModel: anthropic/claude-sonnet-5\nContext: 200K tokens\n\n💡 Tip: T"
	for _, cmd := range []string{"/new", "/reset"} {
		run(t, c, cmd)
		if r := lastSent(t, sent); r.Text != want || r.Buttons != nil || r.Edit {
			t.Fatalf("%s: got:\n%s", cmd, r.Text)
		}
	}
	// With no agent, the reply has no model and no context lines.
	c.Deps.Agent = nil
	c.Deps.BeforeNew = func(context.Context, *sessions.Session) {}
	run(t, c, "/new")
	if r := lastSent(t, sent); r.Text != "✨ Starting new session.\nSend a message to begin.\n\n💡 Tip: T" {
		t.Fatalf("got:\n%s", r.Text)
	}
}

// modelCtx returns an interactive command context with the providers openrouter, primary,
// and zai. The model of primary is anthropic/claude-sonnet-5.
func modelCtx(t *testing.T) (*CommandCtx, *[]Reply) {
	t.Helper()
	c, sent := interactiveCtx(t)
	fp := &fakeProv{}
	prim := modelProv{fakeProv: fp, model: "anthropic/claude-sonnet-5"}
	c.Deps.Agent.Prov = prim
	c.Deps.Providers = map[string]provider.Provider{"openrouter": fp, "primary": prim, "zai": fp}
	return c, sent
}

func TestModelShowWithButtons(t *testing.T) {
	c, sent := modelCtx(t)
	run(t, c, "/model")
	r := lastSent(t, sent)
	if want := "🧠 **Model: default**\n`primary` · anthropic/claude-sonnet-5\nSelect a provider for this session:"; r.Text != want {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := [][]Button{{{Text: "openrouter", Data: "/model openrouter"}, {Text: "✓ primary", Data: "/model primary"}, {Text: "zai", Data: "/model zai"}}}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, c, "/model zai:glm_4.6")
	run(t, c, "/model")
	r = lastSent(t, sent)
	if want := "🧠 **Model: zai:glm\\_4.6**\n`zai` · glm\\_4.6\nSelect a provider for this session:"; r.Text != want {
		t.Fatalf("got:\n%s", r.Text)
	}
	if r.Buttons[0][2].Text != "✓ zai" || r.Buttons[0][1].Text != "primary" {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestModelButtonRowsOfThree(t *testing.T) {
	c, sent := modelCtx(t)
	fp := &fakeProv{}
	for _, n := range []string{"a", "b", "c", "d"} {
		c.Deps.Providers[n] = fp
	}
	run(t, c, "/model")
	r := lastSent(t, sent)
	if len(r.Buttons) != 3 || len(r.Buttons[0]) != 3 || len(r.Buttons[2]) != 1 || r.Buttons[2][0].Text != "zai" {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestModelShowCLI(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/model")
	want := "🧠 **Model: default**\n`primary` · fake-model\nSend /model <provider> to change it. The providers are: cheap, primary.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestModelSetAndClear(t *testing.T) {
	c, sent := modelCtx(t)
	run(t, c, "/model zai:glm-4.6")
	r := lastSent(t, sent)
	if r.Text != "🧠 **Model: zai:glm-4.6**\nThis session uses it from the next message." {
		t.Fatalf("got:\n%s", r.Text)
	}
	if want := [][]Button{{{Text: "↺ Use the default", Data: "/model clear"}}}; !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "zai:glm-4.6" {
		t.Fatal(s.ModelOverride)
	}
	run(t, c, "/model clear")
	r = lastSent(t, sent)
	if r.Text != "🧠 **Model: default**\nThis session uses the default model again." || r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
}

func TestModelErrors(t *testing.T) {
	c, sent := modelCtx(t)
	run(t, c, "/model foo")
	if r := lastSent(t, sent); r.Text != "⚠️ **Unknown provider: foo**\nThe providers are: openrouter, primary, zai." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/model my_x")
	if r := lastSent(t, sent); r.Text != "⚠️ **Unknown provider: my\\_x**\nThe providers are: openrouter, primary, zai." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/model zai:")
	if r := lastSent(t, sent); r.Text != "⚠️ **No model after the colon**\nSend /model <provider> or /model <provider>:<model>." {
		t.Fatalf("got:\n%s", r.Text)
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
}

func TestPromptReplies(t *testing.T) {
	c, sent := interactiveCtx(t)
	base := "📝 **Prompt: base prompt**\nTo replace it in this session, send `/prompt set <text>`."
	for _, cmd := range []string{"/prompt", "/prompt show"} {
		run(t, c, cmd)
		if r := lastSent(t, sent); r.Text != base || r.Buttons != nil {
			t.Fatalf("%s: got:\n%s", cmd, r.Text)
		}
	}
	useBase := [][]Button{{{Text: "🧹 Use the base prompt", Data: "/prompt clear"}}}
	run(t, c, "/prompt set Answer in Indonesian.")
	r := lastSent(t, sent)
	if r.Text != "📝 **Custom prompt set**\nIt replaces the base prompt in this session." || !reflect.DeepEqual(r.Buttons, useBase) {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/prompt")
	r = lastSent(t, sent)
	if r.Text != "📝 **Prompt: custom**\n> Answer in Indonesian." || !reflect.DeepEqual(r.Buttons, useBase) {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/prompt set Use *bold* and my_var.\n\nBe short.")
	run(t, c, "/prompt show")
	if r := lastSent(t, sent); r.Text != "📝 **Prompt: custom**\n> Use \\*bold\\* and my\\_var.\n>\n> Be short." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/prompt clear")
	if r := lastSent(t, sent); r.Text != "📝 **Base prompt again**\nThe custom prompt is removed." || r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.PromptOverride != "" {
		t.Fatal(s.PromptOverride)
	}
	help := "📝 **Prompt commands**\n`/prompt` shows the prompt.\n`/prompt set <text>` replaces the base prompt in this session.\n`/prompt clear` goes back to the base prompt."
	for _, cmd := range []string{"/prompt nope", "/prompt set"} {
		run(t, c, cmd)
		if r := lastSent(t, sent); r.Text != help {
			t.Fatalf("%s: got:\n%s", cmd, r.Text)
		}
	}
}

func TestCompactReplies(t *testing.T) {
	for _, tc := range []struct {
		n    int
		err  error
		want string
	}{
		{42, nil, "🗜 **Compacted**\n42 messages → 1 summary. The session keeps the context."},
		{0, nil, "🗜 **Nothing to compact**\nThis session is still short."},
		{0, errors.New("provider timeout"), "⚠️ **Compaction failed**\nprovider timeout. Try again later."},
		{0, errors.New("agent: the compaction did not complete. The log has the cause."), "⚠️ **Compaction failed**\nagent: the compaction did not complete. The log has the cause. Try again later."},
	} {
		if got := compactReply(tc.n, tc.err); got.Text != tc.want || got.Buttons != nil {
			t.Fatalf("got:\n%s\nwant:\n%s", got.Text, tc.want)
		}
	}
}
