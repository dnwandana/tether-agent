package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const outboundTG = "telegram:\n  enabled: true\n  bot_token: T\n  allowed_users: [\"5\"]\n" +
	"outbound:\n  private_chats: [\"-1007\"]\n"

func TestCronDelivererWiring(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	d := a.Sched.Deliver
	if d.IsPrivate == nil || d.Scan == nil || d.Hold == nil || d.Note == nil || d.TargetInfo == nil ||
		d.LogDecision == nil || d.Held == nil || a.Sched.Sweep == nil || d.Guard == nil || a.Gate.Guard == nil {
		t.Fatalf("%+v", d)
	}
	// SendGuard holds a func field, so the two interface values cannot use ==.
	if reflect.TypeOf(d.Guard) != reflect.TypeOf(a.Gate.Guard) {
		t.Fatalf("guard %T, gate %T", d.Guard, a.Gate.Guard)
	}
	for _, tc := range []struct {
		t    cron.Target
		want bool
	}{
		{cron.Target{Source: "telegram", ChatID: "5"}, true},
		{cron.Target{Source: "telegram", ChatID: "6"}, false},
		{cron.Target{Source: "telegram", ChatID: "-100", ThreadID: "9"}, false},
		{cron.Target{Source: "telegram", ChatID: "-1007"}, true},
		{cron.Target{}, true},
	} {
		if got := d.IsPrivate(ctx, tc.t); got != tc.want {
			t.Errorf("deliverer %+v: %v", tc.t, got)
		}
		if got := a.Cron.IsPrivate(ctx, tc.t); got != tc.want {
			t.Errorf("store %+v: %v", tc.t, got)
		}
	}
	masked, f := d.Scan("Card 4111 1111 1111 1111 for the hotel.", nil)
	if len(f) != 1 || f[0].Kind != "card" || !strings.Contains(masked, "[card ••••1111]") || strings.Contains(masked, "4111 1111") {
		t.Fatal(masked, f)
	}
	// The fake API answers getChat with "true", so the title is the chat id.
	if got := d.TargetInfo(ctx, cron.Target{Source: "telegram", ChatID: "-100", ThreadID: "9"}); got != (cron.TargetInfo{Kind: "topic", Title: "-100"}) {
		t.Fatalf("%+v", got)
	}
	if got := d.TargetInfo(ctx, cron.Target{Source: "telegram", ChatID: "6"}); got != (cron.TargetInfo{Kind: "private chat of a member", Title: "6"}) {
		t.Fatalf("%+v", got)
	}
}

func TestTelegramOutboundWiring(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  allowed_users: [\"5\"]\n"+
		"outbound:\n  private_chats: [\"-1001234567890\"]\n")
	ch, err := a.AddTelegram(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Outbound == nil || ch.Partial == nil || ch.HeldCards == nil {
		t.Fatalf("%+v", ch)
	}
	masked, f := ch.Outbound("", -100, "Card 4111 1111 1111 1111.")
	if len(f) != 1 || !strings.Contains(masked, "[card ••••1111]") {
		t.Fatal(masked, f)
	}
	if !ch.PrivateChat(-1001234567890) || !ch.PrivateChat(5) || ch.PrivateChat(-100) {
		t.Fatal("private chat rule")
	}
	off := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  allowed_users: [\"5\"]\noutbound:\n  scan: false\n")
	ch2, err := off.AddTelegram(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch2.Outbound != nil {
		t.Fatal("outbound.scan false needs a nil Outbound")
	}
}

func TestCronDelivererScanOff(t *testing.T) {
	a := gatewayApp(t, "", "outbound:\n  scan: false\n  guard: false\n")
	if a.Sched.Deliver.Scan != nil || a.Sched.Deliver.Guard != nil {
		t.Fatalf("%+v", a.Sched.Deliver)
	}
}

func TestStoreIsPrivateInChatRole(t *testing.T) {
	a := newApp(t)
	if a.Cron.IsPrivate == nil {
		t.Fatal("the chat role needs the draft-only check too")
	}
	if !a.Cron.IsPrivate(ctx, cron.Target{Source: "cli"}) || !a.Cron.IsPrivate(ctx, cron.Target{}) ||
		a.Cron.IsPrivate(ctx, cron.Target{Source: "telegram", ChatID: "-100"}) {
		t.Fatal("cli and local are private; a group is not")
	}
}

func TestChatTitlesCache(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":-100,"type":"supergroup","title":"Family"}}`))
	}))
	t.Cleanup(srv.Close)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c := &chatTitles{C: &telegram.Client{Token: "T", BaseURL: srv.URL}, TTL: time.Hour, Now: func() time.Time { return now }}
	for range 2 {
		if got := targetInfo(ctx, cron.Target{Source: "telegram", ChatID: "-100"}, c); got != (cron.TargetInfo{Kind: "group", Title: "Family"}) {
			t.Fatalf("%+v", got)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("%d getChat calls in 1 h, want 1", n.Load())
	}
	now = now.Add(61 * time.Minute)
	targetInfo(ctx, cron.Target{Source: "telegram", ChatID: "-100"}, c)
	if n.Load() != 2 {
		t.Fatalf("an old title must be fetched again: %d calls", n.Load())
	}
}

func TestCronNoteText(t *testing.T) {
	n := cron.Note{Job: cron.Job{Name: "spend-report"}, Target: cron.Target{Source: "telegram", ChatID: "-100"},
		TargetTitle: "Family", Findings: []security.Finding{{Kind: "card", Label: "[card ••••1111]"}}}
	if got, want := cronNoteText(n), "🛡️ Job \"spend-report\": I masked 1 item in the delivery to \"Family\":\n• card ••••1111"; got != want {
		t.Fatalf("%q", got)
	}
	n.TargetTitle = ""
	n.Findings = append(n.Findings, security.Finding{Kind: "secret", Label: "«PYPI_TOKEN»"},
		security.Finding{Kind: "phone", Label: "[phone +94•••••4567]"})
	want := "🛡️ Job \"spend-report\": I masked 3 items in the delivery to \"-100\":\n" +
		"• card ••••1111\n• secret «PYPI_TOKEN»\n• phone +94•••••4567"
	if got := cronNoteText(n); got != want {
		t.Fatalf("%q", got)
	}
}

func TestCronLogDecision(t *testing.T) {
	a := gatewayApp(t, "", "")
	j := cron.Job{Name: "competitor-prices", Origin: cron.Origin{Source: "telegram", ChatID: "-100", ThreadID: "9"}}
	a.Sched.Deliver.LogDecision(ctx, j, security.DecisionBlocked, "the run read pastebin.com")
	rows, err := a.Gate.ListLog(ctx, "", true, 10)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	r := rows[0]
	if r.Source != "model-guard" || r.Decision != "blocked" || r.Tool != "cron_delivery" ||
		r.Preview != "the run read pastebin.com" || r.Scope != gateway.ScopeFor("telegram", "-100", "9") {
		t.Fatalf("%+v", r)
	}
}

func TestSweepHeldExpires(t *testing.T) {
	url, calls, mu := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := a.Cron.Create(ctx, cron.Job{Name: "n", Schedule: "every 2h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := a.Cron.MarkRun(ctx, j.ID, cron.RunRecord{Status: "ok", LastStatus: "ok", Delivery: cron.DeliveryHeld,
		RanAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	runs, err := a.Cron.Runs(ctx, j.ID, 1, 0)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	held := a.Sched.Deliver.Held
	if err := held.Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID, RunID: runs[0].ID,
		Target: "telegram:-100", TargetTitle: "Family", Text: "report", Reason: "r", CardChat: "5", CardMsgID: 7,
		CreatedAt: now.Add(-25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a.Sched.Sweep(context.Background())
	if _, ok, err := held.Claim(ctx, "h1"); ok || err != nil {
		t.Fatal("the sweep must claim the old row:", ok, err)
	}
	if runs, _ := a.Cron.Runs(ctx, j.ID, 1, 0); runs[0].Delivery != cron.DeliveryExpired {
		t.Fatalf("%+v", runs[0])
	}
	mu.Lock()
	sent := strings.Join(*calls, " ")
	mu.Unlock()
	if !strings.Contains(sent, "editMessageText") {
		t.Fatal(sent)
	}
}

func TestDraftOnlyAppWiring(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	to, ok := a.Sched.Deliver.OwnerHome(ctx)
	if !ok || to != (cron.Target{Source: "telegram", ChatID: "5"}) {
		t.Fatal(to, ok)
	}
	ct, _ := a.Registry.Get("cronjob")
	pt := ct.(tools.CronJobTool).PrivateTarget
	if pt == nil {
		t.Fatal("the cronjob tool has no PrivateTarget")
	}
	dm, err := a.Store.Create(ctx, sessions.Session{Key: "telegram:dm:5", Source: "telegram", ChatID: "5", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := a.Store.Create(ctx, sessions.Session{Key: "telegram:group:-100", Source: "telegram", ChatID: "-100", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sess    *sessions.Session
		deliver string
		want    bool
	}{
		{dm, "", true},
		{dm, "origin", true},
		{group, "", false},
		{group, "local", true},
		{dm, "telegram:-100", false},
		{group, "telegram:5", true},
	} {
		if got := pt(ctx, a.NewEnv(tc.sess, "telegram"), tc.deliver); got != tc.want {
			t.Errorf("%s %q: %v", tc.sess.Key, tc.deliver, got)
		}
	}
	if !pt(ctx, a.NewEnv(nil, "cli"), "") {
		t.Fatal("a CLI job is checked again at the run")
	}
}
