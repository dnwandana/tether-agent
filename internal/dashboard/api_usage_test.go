package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// usageFixture holds the parts of one usage test.
type usageFixture struct {
	mux  *http.ServeMux
	d    *db.DB
	sess *sessions.Store
	cron *cron.Store
	t0   time.Time
}

// usageInsert writes one assistant message with a NULL cost when cost is nil.
func usageInsert(t *testing.T, d *db.DB, session, model, prov string, in, cache, out int, cost any, at time.Time) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO messages (session_id, role, content, input_tokens, output_tokens,
		cache_read_tokens, latency_ms, latency_first_ms, cost_usd, model, provider, timestamp)
		VALUES (?, 'assistant', 'x', ?, ?, ?, 2000, 500, ?, ?, ?, ?)`,
		session, in, out, cache, cost, model, prov, at.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	t0 := time.Unix(1_800_000_000, 0)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"primary":    {Preset: "zai", Format: "openai", BaseURL: "https://api.example/v1?key=sk-secret-url", APIKey: "sk-secret-zai", Model: "glm-4.7", ContextWindow: 200000, Effort: "high"},
		"openrouter": {Format: "openai", Model: "z-ai/glm-4.7", Vision: true},
		"local":      {Format: "openai", Model: "qwen3", Unusable: "provider local: no API key", UnusableCause: config.UnusableNoKey},
		"backup":     {Preset: "anthropic", Format: "anthropic", Model: "claude-x"},
	}
	cfg.Agent.Fallback = []string{"openrouter", "local"}
	cfg.Agent.DelegationProvider = "openrouter"
	cfg.Goals.JudgeProvider = "backup"
	cfg.Outbound.GuardProvider = "local"
	f := &usageFixture{d: d, sess: sessions.New(d), cron: &cron.Store{DB: d}, t0: t0}
	api := NewUsageAPI(UsageDeps{
		DB: d, Sessions: f.sess, Cron: f.cron, Cfg: &cfg,
		Health: func(now time.Time) []provider.HealthRow {
			return []provider.HealthRow{
				{Entry: "backup", Model: "claude-x", Cause: "no connection", FailedAt: t0.Add(-5 * time.Minute)},
				{Entry: "openrouter", Model: "z-ai/glm-4.7", Cause: "HTTP 503", FailedAt: t0, SkipUntil: t0.Add(time.Minute)},
			}
		},
		Fallback: func() provider.FallbackStatus {
			return provider.FallbackStatus{FirstName: "primary", PrimaryModel: "glm-4.7", FallbackName: "primary", FallbackModel: "glm-4.7"}
		},
		Now:    time.Now,
		Redact: func(s string) string { return strings.ReplaceAll(s, "sk-secret-url", "[REDACTED]") },
	})
	f.mux = http.NewServeMux()
	api.Register(f.mux)
	return f
}

// usageGet sends a GET to the mux and decodes a 200 body into v.
func usageGet(t *testing.T, f *usageFixture, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code == 200 && v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
	}
	return w
}

func TestUsageAPIRanges(t *testing.T) {
	ctx := context.Background()
	f := newUsageFixture(t)
	s, err := f.sess.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sess.SetActive(ctx, SessionKey, s.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	usageInsert(t, f.d, s.ID, "glm-4.7", "primary", 1000, 200, 100, 0.5, now)
	usageInsert(t, f.d, s.ID, "glm-4.7", "primary", 1000, 200, 100, 0.5, now)
	usageInsert(t, f.d, "other", "mystery", "local", 10, 0, 10, nil, now)

	var v UsageView
	if w := usageGet(t, f, "/api/usage", &v); w.Code != 200 || v.Range != "today" || len(v.Rows) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	r := v.Rows[0]
	if r.Model != "glm-4.7" || r.Provider != "primary" || r.Calls != 2 || r.In != 2400 || r.Cached != 400 || r.Out != 200 ||
		r.HitPct != 16 || !r.HitKnown || r.USD != 1.0 || !r.Priced || r.TokPerSec < 66 || r.TokPerSec > 67 {
		t.Fatalf("%+v", r)
	}
	if v.Rows[1].Model != "mystery" || v.Rows[1].Priced || v.TotalUSD != 1.0 || !v.Unpriced {
		t.Fatalf("%+v", v)
	}
	if w := usageGet(t, f, "/api/usage?range=session", &v); w.Code != 200 || v.SessionID != s.ID || len(v.Rows) != 1 || v.Unpriced {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w := usageGet(t, f, "/api/usage?range=week", nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `unknown range \"week\"; use today, 7d, 30d, all, or session`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}

	var dv DailyView
	day := now.Format("2006-01-02")
	if w := usageGet(t, f, "/api/usage/daily?range=7d", &dv); w.Code != 200 || len(dv.Days) != 2 ||
		dv.Days[0] != (DayRow{Day: day, Model: "glm-4.7", USD: 1.0, In: 2400, Out: 200}) ||
		dv.Days[1] != (DayRow{Day: day, Model: "mystery", Unpriced: true, In: 10, Out: 10}) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := usageGet(t, f, "/api/usage/daily?range=session", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"days":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestUsageAPICron(t *testing.T) {
	ctx := context.Background()
	f := newUsageFixture(t)
	j, err := f.cron.Create(ctx, cron.Job{Name: "digest", Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := f.sess.Create(ctx, sessions.Session{Key: "cron:" + j.ID, Source: "cron", Kind: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	usageInsert(t, f.d, cs.ID, "glm-4.7", "primary", 100, 0, 10, 0.3, time.Now())
	var cv CronUsageView
	w := usageGet(t, f, "/api/usage/cron?range=7d", &cv)
	if w.Code != 200 || len(cv.Jobs) != 1 || cv.Jobs[0] != (JobCostRow{JobID: j.ID, JobName: "digest", USD: 0.3}) ||
		!strings.Contains(w.Body.String(), `"flagged":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestUsageAPIModels(t *testing.T) {
	f := newUsageFixture(t)
	var mv ModelsView
	w := usageGet(t, f, "/api/models", &mv)
	if w.Code != 200 || strings.Contains(w.Body.String(), "sk-secret") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var names, states []string
	for _, e := range mv.Entries {
		names, states = append(names, e.Name), append(states, e.Status)
	}
	if !slices.Equal(names, []string{"primary", "openrouter", "local", "backup"}) ||
		!slices.Equal(states, []string{"ok", "skipped", "unusable", "failed"}) {
		t.Fatalf("%q %q", names, states)
	}
	p := mv.Entries[0]
	if p.Preset != "zai" || p.Format != "openai" || p.Model != "glm-4.7" || p.ContextWindow != 200000 || p.Effort != "high" || p.Vision {
		t.Fatalf("%+v", p)
	}
	if o := mv.Entries[1]; !o.Vision || o.Cause != "HTTP 503" || !o.SkipUntil.Equal(f.t0.Add(time.Minute)) {
		t.Fatalf("%+v", o)
	}
	if l := mv.Entries[2]; l.Cause != config.UnusableNoKey {
		t.Fatalf("%+v", l)
	}
	if b := mv.Entries[3]; b.Cause != "no connection" || !b.FailedAt.Equal(f.t0.Add(-5*time.Minute)) || !b.SkipUntil.IsZero() {
		t.Fatalf("%+v", b)
	}
	if !slices.Equal(mv.Chain, []string{"primary", "openrouter", "local"}) || mv.Fallback.Active || mv.Fallback.FirstName != "primary" {
		t.Fatalf("%+v", mv)
	}
	roles := map[string]string{}
	for _, r := range mv.Roles {
		roles[r.Role] = r.Value
	}
	if len(mv.Roles) != 7 || roles["delegation"] != "openrouter" || roles["goal_judge"] != "backup" || roles["vision"] != "" {
		t.Fatalf("%+v", mv.Roles)
	}
	urls := map[string]string{}
	for _, e := range mv.Entries {
		urls[e.Name] = e.BaseURL
	}
	if urls["primary"] != "https://api.example/v1?key=[REDACTED]" || urls["backup"] != "https://api.anthropic.com" ||
		urls["openrouter"] != "" {
		t.Fatalf("the config URL, else the preset URL, through the redactor: %v", urls)
	}
	usable := map[string]bool{}
	for _, r := range mv.Roles {
		usable[r.Role] = r.EntryUsable
	}
	if !usable["delegation"] || !usable["goal_judge"] || !usable["tts"] || usable["outbound_guard"] {
		t.Fatalf("only the role on the unusable entry local is not usable: %v", usable)
	}
}
