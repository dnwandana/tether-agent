package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// GoalsAPI serves the Goals page.
type GoalsAPI struct {
	goals    *goals.Store
	sessions *sessions.Store
	redact   func(string) string
	now      func() time.Time
}

// NewGoalsAPI returns the routes of the Goals page. The routes only read.
func NewGoalsAPI(gs *goals.Store, ss *sessions.Store, redact func(string) string, now func() time.Time) *GoalsAPI {
	return &GoalsAPI{goals: gs, sessions: ss, redact: redact, now: now}
}

type GoalRowView struct {
	SessionID    string    `json:"session_id"`
	Title        string    `json:"title"`
	Source       string    `json:"source"`
	Goal         string    `json:"goal"`
	Status       string    `json:"status"`
	Waiting      bool      `json:"waiting"`
	WaitTarget   string    `json:"wait_target"`
	PausedReason string    `json:"paused_reason"`
	TurnsUsed    int       `json:"turns_used"`
	MaxTurns     int       `json:"max_turns"`
	UpdatedAt    time.Time `json:"updated_at"`
	Actionable   bool      `json:"actionable"`
}

type GoalGateView struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxRetries     int    `json:"max_retries"`
	Attempts       int    `json:"attempts"`
	LastExitCode   *int   `json:"last_exit_code"`
	LastOutputTail string `json:"last_output_tail"`
}

type GoalDetail struct {
	Row                  GoalRowView    `json:"row"`
	StatusLine           string         `json:"status_line"`
	LastVerdict          string         `json:"last_verdict"`
	LastReason           string         `json:"last_reason"`
	Outcome              string         `json:"outcome"`
	Verification         string         `json:"verification"`
	Constraints          string         `json:"constraints"`
	Boundaries           string         `json:"boundaries"`
	StopWhen             string         `json:"stop_when"`
	Subgoals             []string       `json:"subgoals"`
	Gates                []GoalGateView `json:"gates"`
	WaitingReason        string         `json:"waiting_reason"`
	WaitingSince         time.Time      `json:"waiting_since"`
	WaitingUntil         time.Time      `json:"waiting_until"`
	WaitingOnPID         int            `json:"waiting_on_pid"`
	WaitingOnSession     string         `json:"waiting_on_session"`
	WaitingOnDelegations int            `json:"waiting_on_delegations"`
	ParseFailures        int            `json:"parse_failures"`
	TransportFailures    int            `json:"transport_failures"`
	TurnFailures         int            `json:"turn_failures"`
}

func init() { registerAPI(GoalRowView{}, GoalGateView{}, GoalDetail{}) }

func goalsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func goalsErr(w http.ResponseWriter, status int, msg string) {
	goalsJSON(w, status, map[string]string{"error": msg})
}

// goalsTime turns Unix seconds into a time. 0 gives the zero time.
func goalsTime(secs float64) time.Time {
	if secs == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(secs*1e9))
}

func goalsText(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// goalsRow builds the row view of one goal. Each free text goes through the redact func.
func (a *GoalsAPI) goalsRow(id, title, source string, st goals.State, upd time.Time, activeID string) GoalRowView {
	now := a.now()
	v := GoalRowView{
		SessionID:    id,
		Title:        a.redact(title),
		Source:       source,
		Goal:         a.redact(st.Goal),
		Status:       st.Status,
		PausedReason: a.redact(goalsText(st.PausedReason)),
		TurnsUsed:    st.TurnsUsed,
		MaxTurns:     st.MaxTurns,
		UpdatedAt:    upd,
		Actionable:   id != "" && id == activeID,
	}
	if st.Status == goals.StatusActive {
		if t := st.WaitTarget(now); t != "" {
			v.Waiting = true
			v.WaitTarget = t
		}
	}
	return v
}

// goalsActiveID returns the ID of the active dashboard session, or "".
func (a *GoalsAPI) goalsActiveID(r *http.Request) string {
	sess, err := a.sessions.Active(r.Context(), SessionKey)
	if err != nil || sess == nil {
		return ""
	}
	return sess.ID
}

// Register adds the goals routes to mux.
func (a *GoalsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/goals", a.list)
	mux.HandleFunc("GET /api/goals/{session}", a.detail)
}

func (a *GoalsAPI) list(w http.ResponseWriter, r *http.Request) {
	finished := r.URL.Query().Get("finished") == "1"
	activeID := a.goalsActiveID(r)
	all, err := a.goals.ListAll(r.Context(), a.now().Add(-30*24*time.Hour))
	if err != nil {
		goalsErr(w, http.StatusInternalServerError, a.redact(err.Error()))
		return
	}
	out := []GoalRowView{}
	for _, g := range all {
		open := g.State.Status == goals.StatusActive || g.State.Status == goals.StatusPaused
		if open == finished {
			continue
		}
		out = append(out, a.goalsRow(g.SessionID, g.Title, g.Source, g.State, g.UpdatedAt, activeID))
	}
	goalsJSON(w, http.StatusOK, out)
}

func (a *GoalsAPI) detail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("session")
	st, rev, err := a.goals.Get(ctx, id)
	if err != nil {
		goalsErr(w, http.StatusInternalServerError, a.redact(err.Error()))
		return
	}
	if rev == 0 {
		goalsErr(w, http.StatusNotFound, "No goal for this session.")
		return
	}
	var title, source string
	sess, err := a.sessions.Get(ctx, id)
	switch {
	case err == nil:
		title, source = sess.Title, sess.Source
	case !errors.Is(err, sessions.ErrNotFound):
		goalsErr(w, http.StatusInternalServerError, a.redact(err.Error()))
		return
	}
	upd, err := a.goals.UpdatedAt(ctx, id)
	if err != nil {
		goalsErr(w, http.StatusInternalServerError, a.redact(err.Error()))
		return
	}
	now := a.now()
	d := GoalDetail{
		Row:                  a.goalsRow(id, title, source, st, upd, a.goalsActiveID(r)),
		StatusLine:           a.redact(goals.StatusLine(st, now)),
		LastVerdict:          goalsText(st.LastVerdict),
		LastReason:           a.redact(goalsText(st.LastReason)),
		Outcome:              a.redact(st.Contract.Outcome),
		Verification:         a.redact(st.Contract.Verification),
		Constraints:          a.redact(st.Contract.Constraints),
		Boundaries:           a.redact(st.Contract.Boundaries),
		StopWhen:             a.redact(st.Contract.StopWhen),
		Subgoals:             []string{},
		Gates:                []GoalGateView{},
		WaitingReason:        a.redact(goalsText(st.WaitingReason)),
		WaitingSince:         goalsTime(st.WaitingSince),
		WaitingUntil:         goalsTime(st.WaitingUntil),
		WaitingOnSession:     goalsText(st.WaitingOnSession),
		WaitingOnDelegations: st.WaitingOnDelegations,
		ParseFailures:        st.ConsecutiveParseFailures,
		TransportFailures:    st.ConsecutiveTransportFailures,
		TurnFailures:         st.ConsecutiveTurnFailures,
	}
	if st.WaitingOnPID != nil {
		d.WaitingOnPID = *st.WaitingOnPID
	}
	for _, sg := range st.Subgoals {
		d.Subgoals = append(d.Subgoals, a.redact(sg))
	}
	for _, g := range st.Gates {
		d.Gates = append(d.Gates, GoalGateView{
			Command:        a.redact(g.Command),
			TimeoutSeconds: g.TimeoutSeconds,
			MaxRetries:     g.MaxRetries,
			Attempts:       g.Attempts,
			LastExitCode:   g.LastExitCode,
			LastOutputTail: a.redact(g.LastOutputTail),
		})
	}
	goalsJSON(w, http.StatusOK, d)
}
