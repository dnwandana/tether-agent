package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// UsageDeps holds what the usage and models routes read. Health is provider.Health.Snapshot
// of the gateway, and Fallback is the FallbackStatus of the default chain.
type UsageDeps struct {
	DB       *db.DB
	Sessions *sessions.Store
	Cron     *cron.Store
	Cfg      *config.Config
	Health   func(now time.Time) []provider.HealthRow
	Fallback func() provider.FallbackStatus
	Now      func() time.Time
	// Redact masks the base URLs. Nil keeps them.
	Redact func(string) string
}

// UsageModelRow is one row of the usage table. Priced is false when no call has a stored cost.
type UsageModelRow struct {
	Model     string  `json:"model"`
	Provider  string  `json:"provider"`
	Calls     int     `json:"calls"`
	In        int64   `json:"in"`
	Cached    int64   `json:"cached"`
	Out       int64   `json:"out"`
	HitPct    int     `json:"hit_pct"`
	HitKnown  bool    `json:"hit_known"`
	TokPerSec float64 `json:"tok_per_sec"`
	USD       float64 `json:"usd"`
	Priced    bool    `json:"priced"`
}

// UsageView is the Usage page for one range. TotalUSD adds the priced rows only, and
// Unpriced is true when a row has no price ("+" on the page).
type UsageView struct {
	Range       string          `json:"range"`
	SessionID   string          `json:"session_id"`
	Rows        []UsageModelRow `json:"rows"`
	Compactions int             `json:"compactions"`
	TotalUSD    float64         `json:"total_usd"`
	Unpriced    bool            `json:"unpriced"`
}

// DayRow is one bar segment of the daily chart.
type DayRow struct {
	Day      string  `json:"day"`
	Model    string  `json:"model"`
	USD      float64 `json:"usd"`
	Unpriced bool    `json:"unpriced"`
	In       int64   `json:"in"`
	Out      int64   `json:"out"`
}

// DailyView is the daily cost series of one range.
type DailyView struct {
	Range string   `json:"range"`
	Days  []DayRow `json:"days"`
}

// JobCostRow is the cost of the cron sessions of one job. JobName is "" for a deleted job.
type JobCostRow struct {
	JobID   string  `json:"job_id"`
	JobName string  `json:"job_name"`
	USD     float64 `json:"usd"`
}

// FlaggedRow is a run that cost more than 3 times the median of its job.
type FlaggedRow struct {
	JobID   string    `json:"job_id"`
	JobName string    `json:"job_name"`
	At      time.Time `json:"at"`
	USD     float64   `json:"usd"`
	Median  float64   `json:"median"`
}

// CronUsageView is the cron cost by job and the flagged runs of one range.
type CronUsageView struct {
	Range   string       `json:"range"`
	Jobs    []JobCostRow `json:"jobs"`
	Flagged []FlaggedRow `json:"flagged"`
}

// ModelEntryRow is one provider entry. Status is ok, skipped (in the skip time), failed (the
// last failure, skip ended), or unusable (Cause is Provider.UnusableCause).
type ModelEntryRow struct {
	Name          string    `json:"name"`
	Preset        string    `json:"preset"`
	Format        string    `json:"format"`
	Model         string    `json:"model"`
	BaseURL       string    `json:"base_url"`
	Vision        bool      `json:"vision"`
	ContextWindow int       `json:"context_window"`
	Effort        string    `json:"effort"`
	Status        string    `json:"status"`
	Cause         string    `json:"cause"`
	FailedAt      time.Time `json:"failed_at"`
	SkipUntil     time.Time `json:"skip_until"`
}

// HealthView is one row of the Health snapshot, also for a pinned model.
type HealthView struct {
	Entry     string    `json:"entry"`
	Model     string    `json:"model"`
	Cause     string    `json:"cause"`
	FailedAt  time.Time `json:"failed_at"`
	SkipUntil time.Time `json:"skip_until"`
}

// FallbackView is provider.FallbackStatus of the default chain.
type FallbackView struct {
	Active        bool      `json:"active"`
	FirstName     string    `json:"first_name"`
	PrimaryModel  string    `json:"primary_model"`
	FallbackName  string    `json:"fallback_name"`
	FallbackModel string    `json:"fallback_model"`
	FailedAt      time.Time `json:"failed_at"`
	Cause         string    `json:"cause"`
}

// ModelRoleRow is one role. Key is the config key; Value is its value, "" when not set.
type ModelRoleRow struct {
	Role  string `json:"role"`
	Key   string `json:"key"`
	Value string `json:"value"`
	// EntryUsable is false when Value names a provider entry that is unusable.
	EntryUsable bool `json:"entry_usable"`
}

// ModelsView is the Models page.
type ModelsView struct {
	Fallback FallbackView    `json:"fallback"`
	Entries  []ModelEntryRow `json:"entries"`
	Chain    []string        `json:"chain"`
	Health   []HealthView    `json:"health"`
	Roles    []ModelRoleRow  `json:"roles"`
}

// UsageAPI serves the Usage and Models pages.
type UsageAPI struct{ d UsageDeps }

// NewUsageAPI returns the usage and models routes.
func NewUsageAPI(d UsageDeps) *UsageAPI {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Redact == nil {
		d.Redact = func(s string) string { return s }
	}
	return &UsageAPI{d: d}
}

// Register adds the routes to the mux.
func (a *UsageAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/usage", a.usage)
	mux.HandleFunc("GET /api/usage/daily", a.daily)
	mux.HandleFunc("GET /api/usage/cron", a.cron)
	mux.HandleFunc("GET /api/models", a.models)
}

func usageJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func usageErr(w http.ResponseWriter, status int, msg string) {
	usageJSON(w, status, map[string]string{"error": msg})
}

// usageScopeOf resolves the range of the request. sessionID is set for "session" with an
// active session. empty is true for "session" with no active session. ok is false when the
// range is unknown: the function has then written the 400 reply.
func (a *UsageAPI) usageScopeOf(w http.ResponseWriter, r *http.Request) (name string, since time.Time, sessionID string, empty, ok bool) {
	name = r.URL.Query().Get("range")
	if name == "" {
		name = "today"
	}
	if name == "session" {
		s, err := a.d.Sessions.Active(r.Context(), SessionKey)
		if err != nil {
			usageErr(w, http.StatusInternalServerError, err.Error())
			return name, since, "", false, false
		}
		if s == nil {
			return name, since, "", true, true
		}
		return name, since, s.ID, false, true
	}
	since, found := obs.UsageSince(name, a.d.Now())
	if !found {
		usageErr(w, http.StatusBadRequest, fmt.Sprintf("unknown range %q; use today, 7d, 30d, all, or session", name))
		return name, since, "", false, false
	}
	return name, since, "", false, true
}

func (a *UsageAPI) usage(w http.ResponseWriter, r *http.Request) {
	name, since, sessionID, empty, ok := a.usageScopeOf(w, r)
	if !ok {
		return
	}
	v := UsageView{Range: name, SessionID: sessionID, Rows: []UsageModelRow{}}
	if !empty {
		rows, compactions, err := obs.UsageReport(r.Context(), a.d.DB, since, sessionID)
		if err != nil {
			usageErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		v.Compactions = compactions
		for _, u := range rows {
			row := UsageModelRow{Model: u.Model, Provider: u.Provider, Calls: u.Calls, In: int64(u.InTok),
				Cached: int64(u.CacheTok), Out: int64(u.OutTok), TokPerSec: u.TokPerSec, USD: u.CostUSD, Priced: u.CostKnown}
			row.HitPct, row.HitKnown = obs.HitRate(u.CacheTok, u.InTok)
			if u.CostKnown {
				v.TotalUSD += u.CostUSD
			} else {
				v.Unpriced = true
			}
			v.Rows = append(v.Rows, row)
		}
	}
	usageJSON(w, http.StatusOK, v)
}

func (a *UsageAPI) daily(w http.ResponseWriter, r *http.Request) {
	name, since, _, empty, ok := a.usageScopeOf(w, r)
	if !ok {
		return
	}
	v := DailyView{Range: name, Days: []DayRow{}}
	if !empty && name != "session" {
		days, err := obs.DailyCost(r.Context(), a.d.DB, since)
		if err != nil {
			usageErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, d := range days {
			v.Days = append(v.Days, DayRow(d))
		}
	}
	usageJSON(w, http.StatusOK, v)
}

func (a *UsageAPI) cron(w http.ResponseWriter, r *http.Request) {
	name, since, _, _, ok := a.usageScopeOf(w, r)
	if !ok {
		return
	}
	v := CronUsageView{Range: name, Jobs: []JobCostRow{}, Flagged: []FlaggedRow{}}
	if name != "session" {
		if err := a.cronFill(r.Context(), &v, since); err != nil {
			usageErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	usageJSON(w, http.StatusOK, v)
}

// cronFill reads the three cron queries one after the other.
func (a *UsageAPI) cronFill(ctx context.Context, v *CronUsageView, since time.Time) error {
	if since.IsZero() {
		since = time.Unix(0, 0)
	}
	costs, err := cron.CostByJob(ctx, a.d.DB, since)
	if err != nil {
		return err
	}
	jobs, err := a.d.Cron.List(ctx)
	if err != nil {
		return err
	}
	flagged, err := a.d.Cron.FlaggedRuns(ctx, since)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, j := range jobs {
		names[j.ID] = j.Name
	}
	for id, usd := range costs {
		v.Jobs = append(v.Jobs, JobCostRow{JobID: id, JobName: names[id], USD: usd})
	}
	slices.SortFunc(v.Jobs, func(x, y JobCostRow) int {
		switch {
		case x.USD > y.USD:
			return -1
		case x.USD < y.USD:
			return 1
		}
		return strings.Compare(x.JobID, y.JobID)
	})
	for _, f := range flagged {
		v.Flagged = append(v.Flagged, FlaggedRow{JobID: f.JobID, JobName: f.JobName, At: f.At, USD: f.CostUSD, Median: f.CostMedian})
	}
	return nil
}

// usageMediaValue returns the provider of a media role, else its backend.
func usageMediaValue(m config.MediaBackend) string {
	if m.Provider != "" {
		return m.Provider
	}
	return m.Backend
}

func (a *UsageAPI) models(w http.ResponseWriter, r *http.Request) {
	cfg := a.d.Cfg
	now := a.d.Now()
	v := ModelsView{Entries: []ModelEntryRow{}, Chain: []string{}, Health: []HealthView{}}
	if a.d.Fallback != nil {
		fb := a.d.Fallback()
		v.Fallback = FallbackView(fb)
	}
	var health []provider.HealthRow
	if a.d.Health != nil {
		health = a.d.Health(now)
	}
	type hkey struct{ entry, model string }
	byKey := map[hkey]provider.HealthRow{}
	for _, h := range health {
		byKey[hkey{h.Entry, h.Model}] = h
		v.Health = append(v.Health, HealthView(h))
	}
	fallback := cfg.FallbackList()
	v.Chain = append(v.Chain, "primary")
	v.Chain = append(v.Chain, fallback...)
	order := slices.Clone(v.Chain)
	for _, n := range slices.Sorted(maps.Keys(cfg.Providers)) {
		if !slices.Contains(order, n) {
			order = append(order, n)
		}
	}
	seen := map[string]bool{}
	for _, name := range order {
		p, found := cfg.Providers[name]
		if !found || seen[name] {
			continue
		}
		seen[name] = true
		u := p.BaseURL
		if u == "" {
			if pr, ok := config.PresetByID(p.Preset); ok {
				u = pr.BaseURL
			}
		}
		row := ModelEntryRow{Name: name, Preset: p.Preset, Format: p.Format, Model: p.Model, BaseURL: a.d.Redact(u), Vision: p.Vision,
			ContextWindow: p.ContextWindow, Effort: p.Effort, Status: "ok"}
		if p.Unusable != "" {
			row.Status, row.Cause = "unusable", p.UnusableCause
		} else if h, hit := byKey[hkey{name, p.Model}]; hit {
			row.Cause, row.FailedAt, row.SkipUntil = h.Cause, h.FailedAt, h.SkipUntil
			row.Status = "failed"
			if !h.SkipUntil.IsZero() {
				row.Status = "skipped"
			}
		}
		v.Entries = append(v.Entries, row)
	}
	usable := func(value string) bool {
		p, ok := cfg.Providers[value]
		return !ok || p.Unusable == ""
	}
	role := func(role, key, value string) ModelRoleRow {
		return ModelRoleRow{Role: role, Key: key, Value: value, EntryUsable: usable(value)}
	}
	v.Roles = []ModelRoleRow{
		role("delegation", "agent.delegation_provider", cfg.Agent.DelegationProvider),
		role("vision", "agent.vision_provider", cfg.Agent.VisionProvider),
		role("tts", "media.tts", usageMediaValue(cfg.Media.TTS)),
		role("stt", "media.stt", usageMediaValue(cfg.Media.STT)),
		role("image", "media.image", usageMediaValue(cfg.Media.Image)),
		role("goal_judge", "goals.judge_provider", cfg.Goals.JudgeProvider),
		role("outbound_guard", "outbound.guard_provider", cfg.Outbound.GuardProvider),
	}
	usageJSON(w, http.StatusOK, v)
}

func init() {
	registerAPI(UsageView{}, UsageModelRow{}, DailyView{}, DayRow{}, CronUsageView{}, JobCostRow{}, FlaggedRow{},
		ModelsView{}, ModelEntryRow{}, HealthView{}, FallbackView{}, ModelRoleRow{})
}
