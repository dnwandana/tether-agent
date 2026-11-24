package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// dashOn is the config text that turns the dashboard on. Port 0 is never used, because
// AddDashboard starts no listener.
const dashOn = "dashboard:\n  enabled: true\n  listen: 127.0.0.1:0\n"

// dashApp returns a gateway App with the dashboard channel added.
func dashApp(t *testing.T) *App {
	t.Helper()
	a := gatewayApp(t, "", dashOn)
	ch, err := a.AddDashboard(ctx, DashboardFuncs{})
	if err != nil || ch == nil {
		t.Fatal(ch, err)
	}
	return a
}

// dashSession creates the active session of dashboard:owner.
func dashSession(t *testing.T, a *App) *sessions.Session {
	t.Helper()
	s, err := a.Store.Create(ctx, sessions.Session{Key: dashboard.SessionKey, Source: "dashboard",
		ChatID: dashboard.OwnerChatID, Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetActive(ctx, s.Key, s.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAddDashboardOff(t *testing.T) {
	a := gatewayApp(t, "", "")
	ch, err := a.AddDashboard(ctx, DashboardFuncs{})
	if ch != nil || err != nil || a.Dashboard != nil {
		t.Fatal(ch, err)
	}
	if _, ok := a.Gateway().Channels["dashboard"]; ok {
		t.Fatal("dashboard.enabled false must add no channel")
	}
}

func TestAddDashboardRegisters(t *testing.T) {
	a := dashApp(t)
	got, ok := a.Gateway().Channels[dashboard.ChannelName]
	if !ok || got != gateway.Channel(a.Dashboard) || a.Dashboard.Name() != "dashboard" {
		t.Fatalf("%v %v", got, ok)
	}
}

// A bad dashboard value is not a load error. AddDashboard refuses it and adds no channel.
func TestAddDashboardBadValue(t *testing.T) {
	a := gatewayApp(t, "", "dashboard:\n  enabled: true\n  listen: 0.0.0.0:8787\n")
	ch, err := a.AddDashboard(ctx, DashboardFuncs{})
	if err == nil || ch != nil || a.Dashboard != nil {
		t.Fatal(ch, err)
	}
	if _, ok := a.Gateway().Channels["dashboard"]; ok {
		t.Fatal("a bad value must add no channel")
	}
}

func TestNewEnvDashboardPrompter(t *testing.T) {
	a := dashApp(t)
	sess := &sessions.Session{ID: "s1", Key: dashboard.SessionKey, Source: "dashboard", ChatID: dashboard.OwnerChatID, Kind: "chat"}
	env := a.NewTurnEnv(sess, "dashboard", "owner")
	cards := a.Dashboard.Cards()
	ga, ok := env.Gate.(tools.GateApprover)
	if !ok || ga.Gate != a.Gate || ga.Prompter != security.Prompter(cards) ||
		ga.OwnerPrompter != security.Prompter(cards) || env.Ask != tools.Asker(cards) {
		t.Fatalf("%+v", env)
	}
	if _, ok := env.Ask.(tools.SecretAsker); !ok {
		t.Fatal("the dashboard Ask must ask for secret values")
	}
	if env.Progress == nil {
		t.Fatal("a dashboard turn needs a progress function")
	}
	// No page is open, so the event goes nowhere. It must not block or panic.
	env.Progress(1, "1/2", "terminal", "ls")
}

func TestSendNoticeRoutesBySource(t *testing.T) {
	a := dashApp(t)
	send := a.Gateway().Deps.SendNotice
	if send == nil {
		t.Fatal("SendNotice must be set also when Telegram is off")
	}
	dash := &sessions.Session{ID: "s1", Key: dashboard.SessionKey, Source: "dashboard", ChatID: dashboard.OwnerChatID}
	if err := send(ctx, dash.Key, dash, gateway.Reply{Text: "↻ Goal 8/20: reason"}); err != nil {
		t.Fatal(err)
	}
	if n := a.Dashboard.Hub().Notices(); len(n) != 1 || n[0].Text != "↻ Goal 8/20: reason" {
		t.Fatalf("%+v", n)
	}
	tg := &sessions.Session{ID: "s2", Key: "telegram:dm:1", Source: "telegram", ChatID: "1"}
	if err := send(ctx, tg.Key, tg, gateway.Reply{Text: "x"}); err == nil {
		t.Fatal("Telegram is off, so a Telegram notice must fail")
	}
	if err := send(ctx, "dashboard:owner", nil, gateway.Reply{Text: "x"}); err == nil {
		t.Fatal("a notice with no session must fail")
	}
}

func TestDashboardStatus(t *testing.T) {
	a := dashApp(t)
	if err := a.Gate.SetMode(ctx, "dashboard", security.ModeFull); err != nil {
		t.Fatal(err)
	}
	sess := dashSession(t, a)
	asst := provider.Message{Role: provider.RoleAssistant, Content: "ok"}
	for _, m := range []sessions.MessageMeta{
		{Model: "m", Provider: "primary", CostUSD: 0.25},
		{Model: "m", Provider: "primary"}, // no price
	} {
		if _, err := a.Store.Append(ctx, sess.ID, asst, m); err != nil {
			t.Fatal(err)
		}
	}
	// A row of 25 h ago is out of the range.
	old, err := a.Store.Append(ctx, sess.ID, asst, sessions.MessageMeta{Model: "m", Provider: "primary", CostUSD: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.ExecContext(ctx, "UPDATE messages SET timestamp=? WHERE id=?",
		time.Now().Add(-25*time.Hour).UnixMilli(), old); err != nil {
		t.Fatal(err)
	}
	want := dashboard.Status{Model: "m", Entry: "primary", Mode: security.ModeFull, Spend24h: 0.25, Unpriced: true}
	if got := a.dashboardStatus(ctx); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	got := a.dashboardStatus(ctx)
	if got.Running != got.RunningBatches+got.RunningACP+got.RunningShell || got.BrowserOn {
		t.Fatalf("Running is the sum of the kinds, and the browser is off: %+v", got)
	}
	if err := a.Gate.SetMode(ctx, "dashboard", security.ModeGuarded); err != nil {
		t.Fatal(err)
	}
	if got := a.dashboardStatus(ctx); got.Mode != security.ModeGuarded {
		t.Fatalf("a scope with no row is guarded: %q", got.Mode)
	}
}

// With no active session, the header still shows the model and the entry.
func TestDashboardSessionNoActive(t *testing.T) {
	a := dashApp(t)
	ms := a.Agent.CurrentModel(nil)
	if v := a.dashboardSession(ctx); v.ID != "" || v.Model != ms.Model || v.Entry != ms.Name || v.Entry == "" {
		t.Fatalf("got %+v, want the model %q of the entry %q", v, ms.Model, ms.Name)
	}
	sess := dashSession(t, a)
	if v := a.dashboardSession(ctx); v.ID != sess.ID || v.Entry != a.Agent.CurrentModel(sess).Name {
		t.Fatalf("%+v", v)
	}
}

func TestDashboardTurnCost(t *testing.T) {
	a := dashApp(t)
	sess := dashSession(t, a)
	early, err := a.Store.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "before"},
		sessions.MessageMeta{Model: "m", Provider: "primary", CostUSD: 9})
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	if _, err := a.DB.ExecContext(ctx, "UPDATE messages SET timestamp=? WHERE id=?",
		since.Add(-time.Minute).UnixMilli(), early); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		msg  provider.Message
		meta sessions.MessageMeta
	}{
		{provider.Message{Role: provider.RoleUser, Content: "hi"}, sessions.MessageMeta{}},
		{provider.Message{Role: provider.RoleAssistant, Content: "a"}, sessions.MessageMeta{
			Usage: provider.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 400, TotalMS: 1500},
			Model: "m", Provider: "primary", CostUSD: 0.01}},
		{provider.Message{Role: provider.RoleAssistant, Content: "b"}, sessions.MessageMeta{
			Usage: provider.Usage{InputTokens: 500, OutputTokens: 100, TotalMS: 500},
			Model: "m2", Provider: "backup"}},
	}
	for _, r := range rows {
		if _, err := a.Store.Append(ctx, sess.ID, r.msg, r.meta); err != nil {
			t.Fatal(err)
		}
	}
	want := dashboard.CostView{Model: "m2", Provider: "backup", Calls: 2, In: 1900, Cached: 400, Out: 300,
		USD: 0.01, Unpriced: true, Seconds: 2, AnsweredBy: "backup"}
	got, err := a.dashboardTurnCost(ctx, sess.ID, since)
	if err != nil || got != want {
		t.Fatalf("got %+v %v, want %+v", got, err, want)
	}
	if got, err := a.dashboardTurnCost(ctx, "no-such-session", since); err != nil || got != (dashboard.CostView{}) {
		t.Fatalf("%+v %v", got, err)
	}
}

// fakeSignInCards records the call of SendSignInCard and taps Allow at once.
type fakeSignInCards struct {
	label, ip, code string
	expires         time.Time
}

func (f *fakeSignInCards) SendSignInCardState(_ context.Context, label, ip, code string, expires time.Time, answer func(allow bool), _ func() string) error {
	f.label, f.ip, f.code, f.expires = label, ip, code, expires
	answer(true)
	return nil
}

func TestSignInNotifierForwards(t *testing.T) {
	f := &fakeSignInCards{}
	exp := time.Date(2026, 11, 20, 9, 5, 0, 0, time.UTC)
	var n dashboard.SignInNotifier = signInNotifier{cards: f, log: slog.New(slog.DiscardHandler)}
	var got []bool
	n.NotifySignIn(ctx, dashboard.SignInRequest{ID: "r1", Code: "4821", Label: "Chrome on Android",
		UserAgent: "Mozilla/5.0", IP: "10.8.0.5", Expires: exp}, func(allow bool) { got = append(got, allow) })
	if f.label != "Chrome on Android" || f.ip != "10.8.0.5" || f.code != "4821" || !f.expires.Equal(exp) ||
		len(got) != 1 || !got[0] {
		t.Fatalf("%+v %v", f, got)
	}
}

// The session header has the goal line of an active goal, and no line without a goal.
func TestDashboardSessionGoalLine(t *testing.T) {
	a := dashApp(t)
	sess := dashSession(t, a)
	if v := a.dashboardSession(ctx); v.ID != sess.ID || v.GoalLine != "" {
		t.Fatalf("%+v", v)
	}
	if _, err := a.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState("ship the docs", goals.Contract{}, 20, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := "⊙ Goal (active, 0/20 turns): ship the docs"
	if v := a.dashboardSession(ctx); v.GoalLine != want {
		t.Fatalf("goal line %q, want %q", v.GoalLine, want)
	}
}
