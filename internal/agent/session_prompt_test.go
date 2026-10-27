package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// agentWithMemory returns an Agent with a memory store, a prompt cache, and a new session.
func agentWithMemory(t *testing.T) (*Agent, *sessions.Session) {
	t.Helper()
	a, sess := newAgent(t, scripted())
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	a.Memory = m
	a.Prompts = NewPromptCache(a.SessionPrompt)
	return a, sess
}

// keyFor returns the prompt key of sess after n compactions.
func keyFor(sess *sessions.Session, n int) PromptKey {
	return PromptKey{SessionID: sess.ID, ModelOverride: sess.ModelOverride, PromptOverride: sess.PromptOverride, Compactions: n}
}

// writeAgentSkill writes the skill name with the description desc under dir.
func writeAgentSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\nDo the steps.\n"
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPromptFrozenWithinSession(t *testing.T) {
	a, sess := agentWithMemory(t)
	p1, f1 := a.Prompts.Get(ctx, sess, keyFor(sess, 0), "cli")
	if _, err := a.Memory.Apply(memory.Op{Action: "add", Target: "memory", Content: "VPS = hetzner"}); err != nil {
		t.Fatal(err)
	}
	p2, f2 := a.Prompts.Get(ctx, sess, keyFor(sess, 0), "cli")
	if p1 != p2 || !f1.Equal(f2) || strings.Contains(p2, "hetzner") {
		t.Fatal("frozen within session")
	}
}

func TestRebuildAfterCompactionReadsMemoryAgain(t *testing.T) {
	a, sess := agentWithMemory(t)
	a.Prompts.Get(ctx, sess, keyFor(sess, 0), "cli")
	if _, err := a.Memory.Apply(memory.Op{Action: "add", Target: "memory", Content: "VPS = hetzner"}); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Prompts.Get(ctx, sess, keyFor(sess, 1), "cli") // compactions 0 → 1
	if !strings.Contains(p, "hetzner") {
		t.Fatal("rebuild must re-read memory")
	}
}

func TestEachKeyFieldRebuilds(t *testing.T) {
	a, sess := agentWithMemory(t)
	builds := 0
	a.Prompts = NewPromptCache(func(_ context.Context, s *sessions.Session, ch string) (string, time.Time) {
		builds++
		return "P", time.Now()
	})
	base := keyFor(sess, 0)
	a.Prompts.Get(ctx, sess, base, "cli")
	a.Prompts.Get(ctx, sess, base, "cli")
	if builds != 1 {
		t.Fatal("a plain new turn must not rebuild", builds)
	}
	changed := []PromptKey{base, base, base, base}
	changed[0].ModelOverride = "cheap"
	changed[1].PromptOverride = "x"
	changed[2].Compactions = 2
	changed[3].MCPGeneration = 1
	for i, k := range changed {
		a.Prompts.Get(ctx, sess, k, "cli")
		if builds != i+2 {
			t.Fatal("key field", i, "must rebuild", builds)
		}
	}
}

func TestNewSessionSeesMemory(t *testing.T) {
	a, sessA := agentWithMemory(t)
	pa, _ := a.Prompts.Get(ctx, sessA, keyFor(sessA, 0), "cli")
	if _, err := a.Memory.Apply(memory.Op{Action: "add", Target: "memory", Content: "VPS = hetzner"}); err != nil {
		t.Fatal(err)
	}
	sessB, err := a.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := a.Prompts.Get(ctx, sessB, keyFor(sessB, 0), "cli")
	if strings.Contains(pa, "hetzner") || !strings.Contains(pb, "## MEMORY\nVPS = hetzner") {
		t.Fatal(pb)
	}
}

func TestPromptOverrideReplacesBase(t *testing.T) {
	a, sess := agentWithMemory(t)
	sess.PromptOverride = "Answer in Indonesian."
	p, _ := a.Prompts.Get(ctx, sess, keyFor(sess, 0), "cli")
	if !strings.HasPrefix(p, "Answer in Indonesian.") || strings.Contains(p, "You are Tether Agent") {
		t.Fatal(p)
	}
}

func TestSkillsIndexInPrompt(t *testing.T) {
	a, sess := agentWithMemory(t)
	dir := t.TempDir()
	writeAgentSkill(t, dir, "deploy", "Deploy the app. Use when the user asks for a deploy.")
	a.Skills = &tools.SkillCatalog{Dir: dir}
	p, _ := a.Prompts.Get(ctx, sess, keyFor(sess, 0), "cli")
	if strings.Contains(p, "## SKILLS") {
		t.Fatal("no index when skill_view is not visible")
	}
	a.Tools.Register(tools.SkillViewTool{Catalog: a.Skills})
	p, _ = a.Prompts.Get(ctx, sess, keyFor(sess, 1), "cli")
	if !strings.Contains(p, "## SKILLS\n- deploy: Deploy the app. Use when the user asks for a deploy.") {
		t.Fatal(p)
	}
}

func TestPromptContextAndToolNotes(t *testing.T) {
	fp := scripted()
	a, sess := newAgent(t, fp, echoTool{})
	a.WorkDir = "/srv/work"
	a.ChatInfo = func(s *sessions.Session) string { return "group, topic 7" }
	a.Prompts = NewPromptCache(a.SessionPrompt)
	p, _ := a.Prompts.Get(ctx, sess, keyFor(sess, 0), "telegram")
	for _, want := range []string{"- Working directory: /srv/work", "- Channel: telegram", "- Chat: group, topic 7"} {
		if !strings.Contains(p, want) {
			t.Fatal(want, "\n", p)
		}
	}
}

func TestNowPrefixNextDay(t *testing.T) {
	f := time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC)
	if NowPrefix(f, f.Add(30*time.Minute)) != "" {
		t.Fatal("same day → no prefix")
	}
	if NowPrefix(f, f.Add(2*time.Hour)) != "[now: 2026-10-28 01:00 +00] " {
		t.Fatal(NowPrefix(f, f.Add(2*time.Hour)))
	}
	if NowPrefix(time.Time{}, f) != "" {
		t.Fatal("no frozen time → no prefix")
	}
}

func TestRunTurnUsesFrozenPromptAndNowPrefix(t *testing.T) {
	fp := scripted(text("one"), text("two"), text("three"))
	a, sess := newAgent(t, fp)
	frozen := time.Now().Add(-48 * time.Hour)
	builds := 0
	a.Prompts = NewPromptCache(func(_ context.Context, s *sessions.Session, ch string) (string, time.Time) {
		builds++
		return "FROZEN " + ch, frozen
	})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "again", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || fp.requests[0].System != "FROZEN cli" || fp.requests[1].System != "FROZEN cli" {
		t.Fatal(builds, fp.requests[0].System)
	}
	msgs := messages(t, a, sess.ID)
	if !strings.HasPrefix(msgs[0].Msg.Content, "[now: ") || !strings.HasSuffix(msgs[0].Msg.Content, "] hi") {
		t.Fatal("the stored user text gets the now prefix:", msgs[0].Msg.Content)
	}
	// An explicit Turn.System wins over the cache.
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", System: "SYS", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if fp.requests[2].System != "SYS" {
		t.Fatal(fp.requests[2].System)
	}
}

func TestPromptCacheForgetAndBound(t *testing.T) {
	builds := 0
	c := NewPromptCache(func(_ context.Context, s *sessions.Session, ch string) (string, time.Time) {
		builds++
		return s.ID, time.Now()
	})
	s := &sessions.Session{ID: "a"}
	c.Get(ctx, s, PromptKey{SessionID: "a"}, "cli")
	c.Forget("a")
	c.Get(ctx, s, PromptKey{SessionID: "a"}, "cli")
	if builds != 2 {
		t.Fatal("Forget must drop the entry", builds)
	}
	for i := range promptCacheMax + 10 {
		id := string(rune('A'+i%26)) + strings.Repeat("x", i)
		c.Get(ctx, &sessions.Session{ID: id}, PromptKey{SessionID: id}, "cli")
	}
	if n := c.Len(); n > promptCacheMax {
		t.Fatal("the cache must stay bounded", n)
	}
}

func TestPromptCacheConcurrentGet(t *testing.T) {
	c := NewPromptCache(func(_ context.Context, s *sessions.Session, ch string) (string, time.Time) {
		return "P-" + s.ID, time.Now()
	})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('a' + i%5))
			if p, _ := c.Get(ctx, &sessions.Session{ID: id}, PromptKey{SessionID: id}, "cli"); p != "P-"+id {
				t.Error(p)
			}
		}()
	}
	wg.Wait()
}
