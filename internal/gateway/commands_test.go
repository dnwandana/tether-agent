package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// cmdCtx returns a command context for key over a new store, and the buffer of its replies.
func cmdCtx(t *testing.T, key string) (*CommandCtx, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	d := testDeps(t, &fakeProv{})
	return &CommandCtx{Deps: d, Key: key, Reply: func(s string) { out.WriteString(s + "\n") }}, &out
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
	if !strings.Contains(out.String(), first) || !strings.Contains(out.String(), "* "+c.Session.ID) {
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
	if strings.Count(out.String(), "no session "+other.ID+" in this chat") != 2 {
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
	if !strings.Contains(out.String(), "/delete "+s.ID+" confirm") {
		t.Fatal(out)
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
	if !strings.Contains(out.String(), "unknown provider: nope") {
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
	if f == orig || c.Session.ID != f || !strings.Contains(out.String(), "Forked into "+f+". The original session is unchanged.") {
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
	for _, want := range []string{"today", "glm-4.7", "188k", "flat plan", "z-ai/glm-4.7", "$0.07"} {
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
	if !strings.Contains(out.String(), "Usage: /usage [today|7d|30d|session]") {
		t.Fatal(out)
	}
}

func TestUsageCommandTelegramCodeBlock(t *testing.T) {
	c, out := cmdCtxDB(t, "telegram:dm:1")
	run(t, c, "/usage 7d")
	if !strings.Contains(out.String(), "```\n") {
		t.Fatal(out)
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
	if time.Since(start) > 200*time.Millisecond || !strings.Contains(out.String(), "New session "+c.Session.ID) {
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
	if !strings.Contains(out.String(), "Compaction is not available.") {
		t.Fatal(out)
	}
	c.Deps.Agent.Compactor = &ctxmgr.Compactor{Store: c.Deps.Store, DB: c.Deps.Agent.DB, Cfg: c.Deps.Cfg.Context}
	out.Reset()
	run(t, c, "/new")
	run(t, c, "/compact")
	if !strings.Contains(out.String(), "Nothing to compact.") {
		t.Fatal(out)
	}
	seedTurns(t, c.Deps.Store, c.Session.ID, 15)
	out.Reset()
	run(t, c, "/compact")
	if !strings.Contains(out.String(), "Compacted: 7 messages → 1 summary.") {
		t.Fatal(out)
	}
}

func TestReloadMCPCommand(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	defer Register(ReloadMCPCommand(nil))
	run(t, c, "/reload-mcp")
	if !strings.Contains(out.String(), "No MCP servers are configured.") {
		t.Fatal(out)
	}
	m := &mcp.Manager{CacheDir: t.TempDir()}
	if err := m.Init(context.Background(), c.Deps.Agent.Tools); err != nil {
		t.Fatal(err)
	}
	Register(ReloadMCPCommand(m))
	out.Reset()
	run(t, c, "/reload-mcp")
	want := "MCP servers reloaded.\nNo MCP servers are enabled.\nThe changes apply from the next message. That request has no prompt cache.\n"
	if out.String() != want || m.Generation() != 1 {
		t.Fatalf("%q %d", out, m.Generation())
	}
}

func TestReloadMCPReply(t *testing.T) {
	res := mcp.ReloadResult{Reloaded: []string{"fs", "gh"}, Added: []string{"web"}, Removed: []string{"old"},
		Servers: 3, Tools: 7, Skipped: []string{"mcp_gh_read_file"}}
	got := reloadMCPReply(res, errors.New("mcp web: connection refused"))
	want := "MCP servers reloaded.\n" +
		"Reloaded: fs, gh\n" +
		"Added: web\n" +
		"Removed: old\n" +
		"7 tools available from 3 servers.\n" +
		"Not added, because a different tool has the same name: mcp_gh_read_file\n" +
		"Some servers did not reload. They keep their last tool lists: mcp web: connection refused\n" +
		"The changes apply from the next message. That request has no prompt cache."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	got = reloadMCPReply(mcp.ReloadResult{}, fmt.Errorf("%w: bad yaml", mcp.ErrServerList))
	if got != "MCP reload failed: mcp: cannot read the server list: bad yaml\nThe MCP tools did not change." {
		t.Fatal(got)
	}
	got = reloadMCPReply(mcp.ReloadResult{Removed: []string{"gh"}}, nil)
	want = "MCP servers reloaded.\nRemoved: gh\nNo MCP servers are enabled.\n" +
		"The changes apply from the next message. That request has no prompt cache."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
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
