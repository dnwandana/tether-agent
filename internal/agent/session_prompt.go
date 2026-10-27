package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// promptCacheMax limits the number of cached prompts. A long-running gateway sees many
// sessions, so the least recently used entry goes out first.
const promptCacheMax = 256

// nowPrefixFormat is the time format of the now prefix on a user message.
const nowPrefixFormat = "[now: 2006-01-02 15:04 -07] "

// PromptKey identifies one frozen system prompt. When a field changes, the prompt is
// rebuilt. Nothing else invalidates a prompt.
type PromptKey struct {
	SessionID, ModelOverride, PromptOverride string
	Compactions                              int
	MCPGeneration                            uint64
}

// promptEntry is the cached prompt of one session.
type promptEntry struct {
	key      PromptKey
	prompt   string
	frozenAt time.Time
	used     uint64
}

// PromptCache holds the frozen system prompt of each session, so the prompt cache of the
// provider can hit on every turn. It is safe for concurrent use.
type PromptCache struct {
	build   func(ctx context.Context, s *sessions.Session, channel string) (string, time.Time)
	mu      sync.Mutex
	entries map[string]*promptEntry
	tick    uint64
}

// NewPromptCache returns a cache that calls build to make a prompt. build returns the
// prompt and the time at which the prompt was frozen.
func NewPromptCache(build func(ctx context.Context, s *sessions.Session, channel string) (string, time.Time)) *PromptCache {
	return &PromptCache{build: build, entries: map[string]*promptEntry{}}
}

// Get returns the frozen prompt of the session and the time at which it was frozen. It
// builds the prompt only when the session has no entry or when key changed.
func (c *PromptCache) Get(ctx context.Context, s *sessions.Session, key PromptKey, channel string) (prompt string, frozenAt time.Time) {
	if e, ok := c.lookup(key); ok {
		return e.prompt, e.frozenAt
	}
	// The build reads files, so it runs without the lock.
	prompt, frozenAt = c.build(ctx, s, channel)
	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent Get can store the same key first. Keep that entry, so the two callers
	// get the same bytes.
	if e, ok := c.entries[key.SessionID]; ok && e.key == key {
		return e.prompt, e.frozenAt
	}
	c.tick++
	c.entries[key.SessionID] = &promptEntry{key: key, prompt: prompt, frozenAt: frozenAt, used: c.tick}
	c.evict()
	return prompt, frozenAt
}

// lookup returns the entry of key.SessionID when its key is the same as key.
func (c *PromptCache) lookup(key PromptKey) (promptEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key.SessionID]
	if !ok || e.key != key {
		return promptEntry{}, false
	}
	c.tick++
	e.used = c.tick
	return *e, true
}

// evict removes the least recently used entries above promptCacheMax. The caller holds mu.
func (c *PromptCache) evict() {
	for len(c.entries) > promptCacheMax {
		var (
			oldest string
			min    uint64
		)
		for id, e := range c.entries {
			if oldest == "" || e.used < min {
				oldest, min = id, e.used
			}
		}
		delete(c.entries, oldest)
	}
}

// Forget removes the entry of the session. Call it when the session is deleted.
func (c *PromptCache) Forget(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, sessionID)
}

// Len returns the number of cached prompts.
func (c *PromptCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// NowPrefix returns the prefix for a user message that arrives on a later calendar day
// than frozenAt, for example "[now: 2026-10-28 09:14 +07] ". It returns "" on the same
// day and when frozenAt is zero. The day is the day in the location of now.
func NowPrefix(frozenAt, now time.Time) string {
	if frozenAt.IsZero() {
		return ""
	}
	fy, fm, fd := frozenAt.In(now.Location()).Date()
	ny, nm, nd := now.Date()
	if fy == ny && fm == nm && fd == nd {
		return ""
	}
	return now.Format(nowPrefixFormat)
}

// SessionPrompt builds the system prompt of the session. It reads memory and the
// skills from disk. A session prompt override replaces the base prompt. The second result
// is the time at which the prompt is frozen. Use it as the build function of NewPromptCache.
func (a *Agent) SessionPrompt(_ context.Context, s *sessions.Session, channel string) (string, time.Time) {
	now := time.Now()
	in := PromptInput{Base: BasePrompt(), Now: now, TZ: zoneName(now), WorkDir: a.workDir(), Channel: channel}
	if s != nil && strings.TrimSpace(s.PromptOverride) != "" {
		in.Base = s.PromptOverride
	}
	if a.Memory != nil {
		in.Memory, in.Profile = a.Memory.Memory(), a.Memory.Profile()
	}
	var visible map[string]bool
	if a.Tools != nil {
		visible, _ = a.Tools.Visible(tools.Resolve(a.config().Toolsets[channel], nil, nil), nil)
		in.ToolNotes = DefaultToolNotes(visible)
	}
	// The index is useful only when the model can load a skill.
	if a.Skills != nil && (a.Tools == nil || visible["skill_view"]) {
		list, warnings, err := a.Skills.Load()
		if err != nil {
			a.logger().Warn("agent: cannot load the skills; the prompt has no skills index", "err", err.Error())
		}
		for _, w := range warnings {
			a.logger().Warn("agent: skill skipped", "path", w.Path, "reason", w.Text)
		}
		in.SkillsIndex = SkillLines(skills.Index(list))
	}
	if a.ChatInfo != nil && s != nil {
		in.ChatInfo = a.ChatInfo(s)
	}
	return BuildSystemPrompt(in), now
}

// workDir returns Agent.WorkDir, or the working directory of the process.
func (a *Agent) workDir() string {
	if a.WorkDir != "" {
		return a.WorkDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// zoneName returns the name of the local time zone, for example "Asia/Jakarta". When the
// name is not known, it returns the zone abbreviation of now.
func zoneName(now time.Time) string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	abbr, _ := now.Zone()
	return abbr
}

// turnSystem returns the system prompt of the turn and the time at which it was frozen.
// An explicit Turn.System wins. With no prompt cache, the prompt is empty.
func (a *Agent) turnSystem(ctx context.Context, t Turn, channel string) (string, time.Time) {
	// A review run sends the system prompt of the parent, also when it is empty.
	if t.System != "" || a.Prompts == nil || t.review != nil {
		return t.System, time.Time{}
	}
	return a.Prompts.Get(ctx, t.Session, a.promptKey(ctx, t.Session), channel)
}

// promptKey returns the prompt key of the session from its row, the number of its
// compactions, and the MCP generation.
func (a *Agent) promptKey(ctx context.Context, s *sessions.Session) PromptKey {
	k := PromptKey{SessionID: s.ID, ModelOverride: s.ModelOverride, PromptOverride: s.PromptOverride}
	k.Compactions = a.compactionCount(ctx, s.ID)
	if a.MCPGen != nil {
		k.MCPGeneration = a.MCPGen()
	}
	return k
}

// compactionCount returns the number of compactions of the session. With no database, or
// after a read error, it returns 0.
func (a *Agent) compactionCount(ctx context.Context, sessionID string) int {
	if a.DB == nil {
		return 0
	}
	var n int
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM compactions WHERE session_id = ?`, sessionID).Scan(&n); err != nil {
		a.logger().Warn("agent: cannot count the compactions", "session", sessionID, "err", err.Error())
		return 0
	}
	return n
}
