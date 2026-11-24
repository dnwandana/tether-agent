package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/brief"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// JobsDeps holds what the Jobs page reads and runs. Tool is the Run method of the
// cronjob tool. PostHeld sends a held text to its Telegram target, and EditCard edits
// its Telegram card. TelegramOn is false when the gateway has no Telegram channel.
// Publish is Hub.Publish.
type JobsDeps struct {
	Jobs       *cron.Store
	Held       *cron.HeldStore
	Goals      *goals.Store
	Sessions   *sessions.Store
	DB         *db.DB
	Tool       func(ctx context.Context, raw json.RawMessage, env *tools.Env) (string, error)
	TelegramOn bool
	PostHeld   func(ctx context.Context, h cron.HeldSend) error
	EditCard   func(h cron.HeldSend, result string)
	Publish    func(e Event)
	Redact     func(string) string
	Now        func() time.Time
	Log        *slog.Logger
	// ChatTitle reads the title cache of the Telegram chats. It never calls Telegram. Nil gives no label.
	ChatTitle func(chatID, threadID string) (chat, topic string, ok bool)
}

// JobsAPI serves the Jobs page.
type JobsAPI struct{ d JobsDeps }

type JobRow struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Icon          string    `json:"icon"`
	Schedule      string    `json:"schedule"`
	Enabled       bool      `json:"enabled"`
	PauseReason   string    `json:"pause_reason"`
	NextRun       time.Time `json:"next_run"`
	LastRun       time.Time `json:"last_run"`
	LastStatus    string    `json:"last_status"`
	FailStreak    int       `json:"fail_streak"`
	Watch         bool      `json:"watch"`
	DraftOnly     bool      `json:"draft_only"`
	OK            int       `json:"ok"` // ok runs in the newest 7 runs
	LastCostUSD   float64   `json:"last_cost_usd"`
	LastCostKnown bool      `json:"last_cost_known"`
	HistoryOff    bool      `json:"history_off"`
	Last7         []string  `json:"last7"` // run statuses of the newest 7 runs, oldest first
}

type RunRow struct {
	ID          int64     `json:"id"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationMS  int64     `json:"duration_ms"`
	Status      string    `json:"status"`
	Delivery    string    `json:"delivery"`
	Output      string    `json:"output"`
	DeliveryErr string    `json:"delivery_err"`
	SessionID   string    `json:"session_id"`
	CostUSD     float64   `json:"cost_usd"`
	CostKnown   bool      `json:"cost_known"`
	CostFlag    bool      `json:"cost_flag"`
}

type JobDetail struct {
	Job         JobRow   `json:"job"`
	Prompt      string   `json:"prompt"`
	Script      string   `json:"script"`
	Workdir     string   `json:"workdir"`
	Deliver     string   `json:"deliver"`
	Model       string   `json:"model"` // "provider:model" or ""
	Effort      string   `json:"effort"`
	Skills      []string `json:"skills"`
	Toolsets    []string `json:"toolsets"`
	NoAgent     bool     `json:"no_agent"`
	Continuity  bool     `json:"continuity"`
	RepeatLeft  int      `json:"repeat_left"`
	LastOutput  string   `json:"last_output"`
	LastError   string   `json:"last_error"`
	WatchOutput string   `json:"watch_output"`
	Last7       []RunRow `json:"last7"`
	WeekOK      int      `json:"week_ok"`
	WeekTotal   int      `json:"week_total"`
	WeekCostUSD float64  `json:"week_cost_usd"` // from cron.CostByJob for the last 7 days
	RunsTotal   int      `json:"runs_total"`
	FailLimit   int      `json:"fail_limit"` // cron.fail_streak_limit; 0 is off

	WatchLimitHit bool   `json:"watch_limit_hit"` // a watch job used its model runs of the last 24 h
	PromptMasked  bool   `json:"prompt_masked"`   // the prompt holds a secret that the page does not show
	DeliverLabel  string `json:"deliver_label"`   // the cached chat title of a Telegram target, or ""
}

type HeldView struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	JobID       string    `json:"job_id"`
	JobName     string    `json:"job_name"`
	Target      string    `json:"target"`
	TargetTitle string    `json:"target_title"`
	Text        string    `json:"text"`
	Reason      string    `json:"reason"`
	Sources     []string  `json:"sources"`
	Created     time.Time `json:"created"`
	Actions     []Button  `json:"actions"`
	CanPost     bool      `json:"can_post"`
}

// BriefLineView is one line of the brief. Kind is failing, paused_job, bad_schedule,
// held, goal, or cost.
type BriefLineView struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Detail     string  `json:"detail"`
	Count      int     `json:"count"`
	AgeSeconds int64   `json:"age_seconds"`
	CostUSD    float64 `json:"cost_usd"`
	Ratio      float64 `json:"ratio"`
	JobID      string  `json:"job_id"`
	SessionID  string  `json:"session_id"`
}

type BriefView struct {
	Lines          []BriefLineView `json:"lines"`
	RunsOK         int             `json:"runs_ok"`
	RunsTotal      int             `json:"runs_total"`
	HistoryOff     bool            `json:"history_off"`
	SpendUSD       float64         `json:"spend_usd"`
	SpendUnpriced  bool            `json:"spend_unpriced"`
	DefaultDeliver string          `json:"default_deliver"`
}

func init() { registerAPI(JobRow{}, RunRow{}, JobDetail{}, HeldView{}, BriefLineView{}, BriefView{}) }

const (
	jobsLastRuns   = 7
	jobsRunsLimit  = 20
	jobsRunsMax    = 100
	jobsFormMax    = 64 << 10
	jobsWeek       = 7 * 24 * time.Hour
	jobsTextNoJob  = "No job with this id."
	jobsTextForm   = "The job form can only create or update a job."
	jobsTextNoTG   = "Telegram is off, so the text cannot be posted. Drop it, or turn Telegram on."
	jobsTextAction = `Use "post" or "drop".`
)

// NewJobsAPI returns the routes of the Jobs page.
func NewJobsAPI(d JobsDeps) *JobsAPI {
	if d.Redact == nil {
		d.Redact = func(s string) string { return s }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Publish == nil {
		d.Publish = func(Event) {}
	}
	return &JobsAPI{d: d}
}

// Register adds the routes to mux.
func (a *JobsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/brief", a.brief)
	mux.HandleFunc("GET /api/jobs", a.list)
	mux.HandleFunc("GET /api/jobs/{id}", a.detail)
	mux.HandleFunc("GET /api/jobs/{id}/runs", a.runs)
	mux.HandleFunc("POST /api/jobs", a.form)
	mux.HandleFunc("GET /api/held", a.heldList)
	mux.HandleFunc("POST /api/held/{id}", a.heldResolve)
}

func jobsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jobsErr(w http.ResponseWriter, status int, msg string) {
	jobsJSON(w, status, map[string]string{"error": msg})
}

// jobsFail writes a failure with the redacted error text.
func (a *JobsAPI) jobsFail(w http.ResponseWriter, status int, err error) {
	jobsErr(w, status, a.d.Redact(err.Error()))
}

// jobsIcon returns the state icon of the job j. It follows jobIcon of the gateway.
func jobsIcon(j cron.Job) string {
	switch {
	case !j.Enabled && j.RepeatLeft == 0:
		return "✔"
	case !j.Enabled:
		return "⏸"
	case j.FailStreak > 0:
		return "⚠"
	case j.Watch:
		return "👁"
	case j.LastStatus == "ok":
		return "✅"
	}
	return "⏳"
}

func (a *JobsAPI) historyOff() bool { return a.d.Jobs.RunHistory == 0 }

// jobsRow builds the row of the job j. It reads the newest runs first.
func (a *JobsAPI) jobsRow(ctx context.Context, j cron.Job) (JobRow, error) {
	row := JobRow{ID: j.ID, Name: j.Name, Icon: jobsIcon(j), Schedule: j.Schedule, Enabled: j.Enabled,
		PauseReason: j.PauseReason, NextRun: j.NextRun, LastRun: j.LastRun, LastStatus: j.LastStatus,
		FailStreak: j.FailStreak, Watch: j.Watch, DraftOnly: j.DraftOnly, HistoryOff: a.historyOff(), Last7: []string{}}
	if row.HistoryOff {
		return row, nil
	}
	runs, err := a.d.Jobs.Runs(ctx, j.ID, jobsLastRuns, 0)
	if err != nil {
		return row, err
	}
	for _, r := range runs {
		if r.Status == "ok" {
			row.OK++
		}
		row.Last7 = append([]string{r.Status}, row.Last7...) // newest first in, oldest first out
	}
	if len(runs) > 0 && runs[0].CostUSD != nil {
		row.LastCostUSD, row.LastCostKnown = *runs[0].CostUSD, true
	}
	return row, nil
}

func (a *JobsAPI) runView(r cron.Run) RunRow {
	v := RunRow{ID: r.ID, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		DurationMS: r.FinishedAt.Sub(r.StartedAt).Milliseconds(), Status: r.Status, Delivery: r.Delivery,
		Output: a.d.Redact(r.Output), DeliveryErr: a.d.Redact(r.DeliveryErr), SessionID: r.SessionID,
		CostFlag: r.CostFlag}
	if r.CostUSD != nil {
		v.CostUSD, v.CostKnown = *r.CostUSD, true
	}
	return v
}

func jobsKind(k brief.Kind) string {
	switch k {
	case brief.KindFailing:
		return "failing"
	case brief.KindPausedJob:
		return "paused_job"
	case brief.KindBadSchedule:
		return "bad_schedule"
	case brief.KindHeld:
		return "held"
	case brief.KindGoal:
		return "goal"
	}
	return "cost"
}

func (a *JobsAPI) brief(w http.ResponseWriter, r *http.Request) {
	st, err := brief.Build(r.Context(), brief.Deps{Jobs: a.d.Jobs, Held: a.d.Held, Goals: a.d.Goals,
		Sessions: a.d.Sessions, DB: a.d.DB}, a.historyOff(), a.d.Now())
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	v := BriefView{Lines: []BriefLineView{}, RunsOK: st.RunsOK, RunsTotal: st.RunsTotal, HistoryOff: st.HistoryOff,
		SpendUSD: st.Spend, SpendUnpriced: st.SpendUnpriced, DefaultDeliver: "local"}
	if a.d.TelegramOn {
		v.DefaultDeliver = "origin"
	}
	for _, l := range st.Lines {
		v.Lines = append(v.Lines, BriefLineView{Kind: jobsKind(l.Kind), Name: a.d.Redact(l.Name), Detail: a.d.Redact(l.Detail),
			Count: l.Count, AgeSeconds: int64(l.Age.Seconds()), CostUSD: l.CostUSD, Ratio: l.Ratio,
			JobID: l.JobID, SessionID: l.SessionID})
	}
	jobsJSON(w, http.StatusOK, v)
}

func (a *JobsAPI) list(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.d.Jobs.List(r.Context())
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]JobRow, 0, len(jobs))
	for _, j := range jobs {
		row, err := a.jobsRow(r.Context(), j)
		if err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		rows = append(rows, row)
	}
	jobsJSON(w, http.StatusOK, rows)
}

func (a *JobsAPI) detail(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	j, err := a.d.Jobs.Get(ctx, id)
	if errors.Is(err, cron.ErrNotFound) {
		jobsErr(w, http.StatusNotFound, jobsTextNoJob)
		return
	}
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	row, err := a.jobsRow(ctx, j)
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	model := ""
	if j.Model != nil && (j.Model.Provider != "" || j.Model.Model != "") {
		model = strings.Trim(j.Model.Provider+":"+j.Model.Model, ":")
	}
	v := JobDetail{Job: row, Prompt: a.d.Redact(j.Prompt), Script: j.Script, Workdir: j.Workdir, Deliver: j.Deliver,
		Model: model, Effort: j.Effort, Skills: nonNil(j.Skills), Toolsets: nonNil(j.Toolsets), NoAgent: j.NoAgent,
		Continuity: j.Continuity, RepeatLeft: j.RepeatLeft, LastOutput: a.d.Redact(j.LastOutput),
		LastError: a.d.Redact(j.LastError), WatchOutput: a.d.Redact(j.WatchOutput), Last7: []RunRow{}, FailLimit: a.d.Jobs.FailStreakLimit,
		PromptMasked: a.d.Redact(j.Prompt) != j.Prompt, DeliverLabel: a.deliverLabel(j.Deliver)}
	if !row.HistoryOff {
		runs, err := a.d.Jobs.Runs(ctx, id, jobsLastRuns, 0)
		if err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		for _, run := range runs {
			v.Last7 = append(v.Last7, a.runView(run))
		}
		since := a.d.Now().Add(-jobsWeek)
		if v.WeekOK, v.WeekTotal, err = a.d.Jobs.RunsOK(ctx, id, since); err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		if v.RunsTotal, err = a.d.Jobs.RunsTotal(ctx, id); err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		costs, err := cron.CostByJob(ctx, a.d.DB, since)
		if err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		v.WeekCostUSD = costs[id]
	}
	if j.Watch {
		n, err := a.d.Jobs.ModelRunsSince(ctx, id, a.d.Now().Add(-24*time.Hour))
		if err != nil {
			a.jobsFail(w, http.StatusInternalServerError, err)
			return
		}
		v.WatchLimitHit = n >= cron.WatchModelRunsPerDay
	}
	jobsJSON(w, http.StatusOK, v)
}

// deliverLabel returns the cached title of the Telegram chat of a deliver target.
// It returns "" for another form, for no cache, or for a title that is not cached.
func (a *JobsAPI) deliverLabel(deliver string) string {
	rest, ok := strings.CutPrefix(deliver, "telegram:")
	if !ok || a.d.ChatTitle == nil {
		return ""
	}
	chatID, threadID, _ := strings.Cut(rest, ":")
	chat, topic, ok := a.d.ChatTitle(chatID, threadID)
	if !ok {
		return ""
	}
	label := a.d.Redact(chat)
	if topic != "" {
		label += " › " + a.d.Redact(topic)
	}
	return label
}

// jobsInt reads a non-negative integer query value. It returns def when the value is absent or bad.
func jobsInt(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n < 0 {
		return def
	}
	return n
}

func (a *JobsAPI) runs(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if _, err := a.d.Jobs.Get(ctx, id); errors.Is(err, cron.ErrNotFound) {
		jobsErr(w, http.StatusNotFound, jobsTextNoJob)
		return
	} else if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	limit := jobsInt(r, "limit", jobsRunsLimit)
	if limit == 0 {
		limit = jobsRunsLimit
	}
	if limit > jobsRunsMax {
		limit = jobsRunsMax
	}
	runs, err := a.d.Jobs.Runs(ctx, id, limit, jobsInt(r, "offset", 0))
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]RunRow, 0, len(runs))
	for _, run := range runs {
		out = append(out, a.runView(run))
	}
	jobsJSON(w, http.StatusOK, out)
}

// form runs the cronjob tool for the job form. Only create and update pass.
func (a *JobsAPI) form(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, jobsFormMax))
	if err != nil {
		jobsErr(w, http.StatusBadRequest, "The request body is not valid.")
		return
	}
	var head struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		jobsErr(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	if head.Action != "create" && head.Action != "update" {
		jobsErr(w, http.StatusBadRequest, jobsTextForm)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	env := &tools.Env{SessionKey: SessionKey, Channel: ChannelName}
	s, err := a.d.Sessions.Active(ctx, SessionKey)
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	if s != nil {
		env.SessionID = s.ID
	}
	out, err := a.d.Tool(ctx, json.RawMessage(raw), env)
	if err != nil {
		a.jobsFail(w, http.StatusBadRequest, err)
		return
	}
	jobsJSON(w, http.StatusOK, ReplyView{Text: a.d.Redact(out)})
}

// jobsActions returns the buttons of a held send. They follow its kind.
func jobsActions(h cron.HeldRow) []Button {
	if h.Kind == "original" {
		return []Button{{Text: "✅ Post the full text there", Data: "post"}}
	}
	title := h.TargetTitle
	if title == "" {
		title = h.Target
	}
	return []Button{{Text: "❌ Drop", Data: "drop"}, {Text: `✅ Send to "` + title + `"`, Data: "post"}}
}

func (a *JobsAPI) heldList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.d.Jobs.HeldList(r.Context(), a.d.Now())
	if err != nil {
		a.jobsFail(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]HeldView, 0, len(rows))
	for _, h := range rows {
		out = append(out, HeldView{ID: h.ID, Kind: h.Kind, JobID: h.JobID, JobName: h.JobName, Target: h.Target,
			TargetTitle: h.TargetTitle, Text: a.d.Redact(h.Text), Reason: h.Reason, Sources: h.Sources,
			Created: h.Created, Actions: jobsActions(h), CanPost: a.d.TelegramOn})
	}
	jobsJSON(w, http.StatusOK, out)
}

func (a *JobsAPI) heldResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jobsErr(w, http.StatusBadRequest, "Bad held send id.")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jobsFormMax)).Decode(&body); err != nil ||
		(body.Action != "post" && body.Action != "drop") {
		jobsErr(w, http.StatusBadRequest, jobsTextAction)
		return
	}
	if body.Action == "post" && !a.d.TelegramOn {
		jobsErr(w, http.StatusConflict, jobsTextNoTG)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	res, err := cron.ResolveHeld(ctx, a.d.Jobs, id, body.Action, a.d.PostHeld, a.d.EditCard)
	if err != nil {
		// A run-mark error can follow a send or a drop that worked. The result text shows that.
		if res == "" || errors.Is(err, cron.ErrHeldNotSent) {
			a.jobsFail(w, http.StatusConflict, err)
			return
		}
		if a.d.Log != nil {
			a.d.Log.Warn("dashboard: held send resolved, but the run mark failed", "id", id, "err", err)
		}
	}
	a.d.Publish(Event{Name: "held", Data: map[string]any{"id": id}})
	jobsJSON(w, http.StatusOK, ReplyView{Text: a.d.Redact(res)})
}

// nonNil returns s, or an empty list when s is nil, so the JSON has [] and not null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
