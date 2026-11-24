package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// DashboardFuncs holds the functions of the dashboard that live in cmd/tether, because
// internal/setup imports internal/app.
type DashboardFuncs struct {
	Doctor      func(ctx context.Context) ([]dashboard.DoctorCheck, time.Time, error)
	UpdateCheck func(ctx context.Context) (dashboard.UpdateInfo, error)
	UpdateRun   func(ctx context.Context) error
	Pairing     dashboard.PairingFuncs
	SetupState  func(ctx context.Context) []dashboard.SetupStep
}

// AddDashboard checks the dashboard values, builds the dashboard channel, and adds it to
// the gateway. It returns nil, nil when dashboard.enabled is false. It does no network
// work and starts no listener. Channel.Run starts the listener.
func (a *App) AddDashboard(ctx context.Context, f DashboardFuncs) (*dashboard.Channel, error) {
	if !a.Cfg.Dashboard.Enabled {
		return nil, nil
	}
	if err := a.Cfg.Dashboard.Check(); err != nil {
		a.Log.Error("dashboard: cannot start, so the gateway runs without it", "err", err.Error())
		return nil, fmt.Errorf("dashboard: %w", err)
	}
	d := dashboard.Deps{
		Cfg:         a.Cfg.Dashboard,
		DB:          a.DB,
		Sessions:    a.Store,
		Redact:      a.Redactor.Redact,
		Partial:     a.Redactor.PartialExact,
		Log:         a.Log,
		Doctor:      f.Doctor,
		UpdateCheck: f.UpdateCheck,
		UpdateRun:   f.UpdateRun,
		Pairing:     f.Pairing,
		SetupState:  f.SetupState,
		SaveSecret:  a.SaveSecret,
		Status:      a.dashboardStatus,
		TurnCost:    a.dashboardTurnCost,
		Session:     a.dashboardSession,
		HeldResolve: a.resolveHeld,
	}
	ch, err := dashboard.NewChannel(d)
	if err != nil {
		a.Log.Error("dashboard: cannot start, so the gateway runs without it", "err", err.Error())
		return nil, fmt.Errorf("dashboard: %w", err)
	}
	ch.Cards().SetRedact(a.Redactor.Redact)
	ch.AddRoutes(a.dashboardRoutes(ch, f)...)
	a.Dashboard = ch
	a.Gateway().Channels[ch.Name()] = ch
	if a.Telegram != nil {
		a.Telegram.SignInHours = a.Cfg.Dashboard.SessionHours
		ch.SignIn().AddNotifier(signInNotifier{cards: a.Telegram, log: a.Log})
	}
	return ch, nil
}

// signInCards is the part of telegram.Channel that sends the sign-in card.
type signInCards interface {
	SendSignInCardState(ctx context.Context, label, ip, code string, expires time.Time, answer func(allow bool), state func() string) error
}

// signInNotifier sends a dashboard sign-in request to the owners' Telegram chats.
type signInNotifier struct {
	cards signInCards
	log   *slog.Logger
}

// NotifySignIn sends the card. A send error gives a warning only: the request expires
// by itself, and the tray card of a signed-in page can still answer it.
func (n signInNotifier) NotifySignIn(ctx context.Context, req dashboard.SignInRequest, answer func(allow bool)) {
	if err := n.cards.SendSignInCardState(ctx, req.Label, req.IP, req.Code, req.Expires, answer, req.State); err != nil && n.log != nil {
		n.log.Warn("cannot send the dashboard sign-in card to Telegram", "err", err.Error())
	}
}

// dashboardSession gives the header data of the active dashboard session.
func (a *App) dashboardSession(ctx context.Context) dashboard.SessionView {
	sess, err := a.Store.Active(ctx, dashboard.SessionKey)
	if err != nil {
		return dashboard.SessionView{}
	}
	if sess == nil {
		ms := a.Agent.CurrentModel(nil)
		return dashboard.SessionView{Model: ms.Model, Entry: ms.Name}
	}
	ms := a.Agent.CurrentModel(sess)
	v := dashboard.SessionView{ID: sess.ID, Title: sess.Title, Model: ms.Model, Entry: ms.Name, Effort: sess.Effort}
	if tokens, window, threshold, err := a.Agent.ContextEstimate(ctx, sess.ID); err == nil {
		v.Context = &dashboard.ContextView{Tokens: tokens, Window: window, Threshold: threshold}
	}
	if a.Goals != nil {
		if st, ok, err := a.Goals.Load(ctx, sess.ID); err == nil && ok &&
			(st.Status == goals.StatusActive || st.Status == goals.StatusPaused) {
			v.GoalLine = a.Redactor.Redact(goals.StatusLine(st, time.Now()))
		}
	}
	return v
}

// dashboardStatus returns the status bar of the dashboard.
func (a *App) dashboardStatus(ctx context.Context) dashboard.Status {
	sess, _ := a.Store.Active(ctx, dashboard.SessionKey)
	ms := a.Agent.CurrentModel(sess)
	st := dashboard.Status{
		Model:    ms.Model,
		Entry:    ms.Name,
		Fallback: ms.Fallback.Active,
		TLS:      a.Cfg.Dashboard.TLS(),
	}
	if st.Fallback {
		st.FallbackCause = ms.Fallback.Cause
	}
	st.Mode = a.Gate.ModeOf(ctx, "dashboard")
	if st.Mode == "" {
		st.Mode = security.ModeGuarded
	}
	if a.Dashboard != nil {
		st.Waiting = len(a.Dashboard.Cards().Open())
	}
	if a.Batches != nil {
		st.RunningBatches = a.Batches.Running(dashboard.SessionKey)
	}
	if a.ACP != nil {
		st.RunningACP = len(a.ACP.Busy())
	}
	if sess != nil && a.Procs != nil {
		st.RunningShell = len(a.Procs.Running(sess.ID))
	}
	st.Running = st.RunningBatches + st.RunningACP + st.RunningShell
	st.BrowserOn = a.Browser != nil && a.Browser.Running()
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	var calls, priced int
	var usd float64
	err := a.DB.QueryRowContext(ctx, "SELECT COUNT(*), COUNT(cost_usd), COALESCE(SUM(cost_usd), 0) "+
		"FROM messages WHERE role='assistant' AND model != '' AND timestamp >= ?", since).Scan(&calls, &priced, &usd)
	if err != nil {
		a.Log.Warn("dashboard: cannot read the spend of 24 hours", "err", err.Error())
		return st
	}
	st.Spend24h = usd
	st.Unpriced = calls > priced
	return st
}

// dashboardTurnCost returns the cost footer of a turn: the sum of the assistant rows of the
// session from since.
func (a *App) dashboardTurnCost(ctx context.Context, sessionID string, since time.Time) (dashboard.CostView, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT model, provider, input_tokens, cache_read_tokens, output_tokens, cost_usd, latency_ms "+
		"FROM messages WHERE session_id=? AND role='assistant' AND model != '' AND timestamp >= ? ORDER BY id",
		sessionID, since.UnixMilli())
	if err != nil {
		return dashboard.CostView{}, fmt.Errorf("dashboard: read the turn cost: %w", err)
	}
	var v dashboard.CostView
	var ms int64
	for rows.Next() {
		var model, prov string
		var in, cached, out, lat int64
		var cost sql.NullFloat64
		if err := rows.Scan(&model, &prov, &in, &cached, &out, &cost, &lat); err != nil {
			rows.Close()
			return dashboard.CostView{}, fmt.Errorf("dashboard: read the turn cost: %w", err)
		}
		v.Calls++
		v.In += in + cached
		v.Cached += cached
		v.Out += out
		if cost.Valid {
			v.USD += cost.Float64
		} else {
			v.Unpriced = true
		}
		ms += lat
		v.Model, v.Provider = model, prov
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return dashboard.CostView{}, fmt.Errorf("dashboard: read the turn cost: %w", err)
	}
	if v.Calls == 0 {
		return dashboard.CostView{}, nil
	}
	v.Seconds = float64(ms) / 1000
	first := "primary"
	if sess, err := a.Store.Get(ctx, sessionID); err == nil {
		if n := a.Agent.CurrentModel(sess).Fallback.FirstName; n != "" {
			first = n
		}
	}
	if v.Provider != first {
		v.AnsweredBy = v.Provider
	}
	return v, nil
}

// sendNotice sends a notice that has no turn. It routes by the source of sess at call
// time, so the order of AddTelegram and AddDashboard does not matter.
func (a *App) sendNotice(ctx context.Context, key string, sess *sessions.Session, r gateway.Reply) error {
	if sess == nil {
		return fmt.Errorf("notice for key %s: no session", key)
	}
	switch sess.Source {
	case "telegram":
		if a.Telegram == nil {
			return fmt.Errorf("telegram notice for key %s: Telegram is off", key)
		}
		return a.Telegram.SendNotice(ctx, key, sess, r)
	case dashboard.ChannelName:
		if a.Dashboard == nil {
			return fmt.Errorf("dashboard notice for key %s: the dashboard is off", key)
		}
		return a.Dashboard.Send(ctx, gateway.Target{Source: "dashboard", ChatID: sess.ChatID},
			gateway.Outbound{Text: r.Text, Keyboard: r.Buttons})
	}
	ch := a.Gateway().Channels[sess.Source]
	if ch == nil {
		return fmt.Errorf("notice for key %s: no channel for the source %q", key, sess.Source)
	}
	return ch.Send(ctx, gateway.Target{Source: sess.Source, ChatID: sess.ChatID, ThreadID: sess.ThreadID},
		gateway.Outbound{Text: r.Text})
}
