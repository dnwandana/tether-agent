package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
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

// The owner note of a masked cron delivery has no sound when the delivery has no sound.
func TestCronNoteNoSound(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":99,"is_bot":true,"username":"tether_bot"}}`))
		case "sendMessage":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":5}}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	a := gatewayApp(t, srv.URL, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	for _, quiet := range []bool{false, true} {
		n := cron.Note{Job: cron.Job{ID: "j1", Name: "prices"}, Target: cron.Target{Source: "telegram", ChatID: "-100"},
			TargetTitle: "Pricing team", Findings: []security.Finding{{Kind: "card", Label: "[card ••••1111]"}}, NoSound: quiet}
		if err := a.sendCronNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		last := bodies[len(bodies)-1]
		mu.Unlock()
		if (last["disable_notification"] == true) != quiet {
			t.Fatalf("quiet %v: %v", quiet, last)
		}
	}
}

// The dashboard resolves a held send with the shared function, and the Telegram card changes.
func TestResolveHeldEditsTelegramCard(t *testing.T) {
	url, calls, mu := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := a.Cron.Create(ctx, cron.Job{Name: "n", Schedule: "every 2h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Sched.Deliver.Held.Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID,
		Target: "telegram:-100", TargetTitle: "Family", Text: "report", Reason: "r", CardChat: "5", CardMsgID: 7,
		CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.resolveHeld(ctx, "h1", "drop"); err != nil || got != "Dropped." {
		t.Fatal(got, err)
	}
	mu.Lock()
	sent := strings.Join(*calls, " ")
	mu.Unlock()
	if !strings.Contains(sent, "editMessageText") {
		t.Fatal(sent)
	}
	if _, err := a.resolveHeld(ctx, "h1", "drop"); !errors.Is(err, cron.ErrHeldGone) {
		t.Fatal(err)
	}
}

// With Telegram off, Post is refused before the claim, and Drop still works.
func TestResolveHeldPostNeedsTelegram(t *testing.T) {
	a := gatewayApp(t, "", "")
	j, err := a.Cron.Create(ctx, cron.Job{Name: "n", Schedule: "every 2h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&cron.HeldStore{DB: a.DB}).Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID,
		Target: "telegram:-100", TargetTitle: "Family", Text: "report", Reason: "r", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.resolveHeld(ctx, "h1", "post"); err == nil ||
		err.Error() != "Telegram is off, so the text cannot be posted. Drop it, or turn Telegram on." {
		t.Fatal(err)
	}
	if got, err := a.resolveHeld(ctx, "h1", "drop"); err != nil || got != "Dropped." {
		t.Fatal(got, err)
	}
}

// resolveHeld sends held, so a page updates after a resolve that is not the dashboard route.
func TestResolveHeldPublishesHeld(t *testing.T) {
	a := gatewayApp(t, "", dashOn)
	if _, err := a.AddDashboard(ctx, DashboardFuncs{}); err != nil {
		t.Fatal(err)
	}
	j, err := a.Cron.Create(ctx, cron.Job{Name: "n", Schedule: "every 2h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&cron.HeldStore{DB: a.DB}).Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID,
		Target: "telegram:-100", TargetTitle: "Family", Text: "report", Reason: "r", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ev := dashEvents(t, a)
	if got, err := a.resolveHeld(ctx, "h1", "drop"); err != nil || got != "Dropped." {
		t.Fatal(got, err)
	}
	if e := waitDashEvent(t, ev, "held"); e.data != `{"id":"h1"}` {
		t.Fatal(e.data)
	}
}

// A new held send sends held after its card is posted.
func TestHoldDeliveryPublishesHeld(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG+dashOn)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddDashboard(ctx, DashboardFuncs{}); err != nil {
		t.Fatal(err)
	}
	j, err := a.Cron.Create(ctx, cron.Job{Name: "n", Schedule: "every 2h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := cron.HeldSend{ID: "h2", Kind: "hold", JobID: j.ID, Target: "telegram:-100", TargetTitle: "Family",
		Text: "report", Reason: "r", CreatedAt: time.Now()}
	if err := a.Sched.Deliver.Held.Insert(ctx, h); err != nil {
		t.Fatal(err)
	}
	ev := dashEvents(t, a)
	if err := a.holdDelivery(ctx, h, cron.HeldCardInfo{JobName: "n", TargetTitle: "Family", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if e := waitDashEvent(t, ev, "held"); e.data != `{"id":"h2"}` {
		t.Fatal(e.data)
	}
}

func TestChatTitlesPut(t *testing.T) {
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	c := &chatTitles{TTL: time.Hour, Now: func() time.Time { return now }}
	c.put(telegram.Chat{ID: 7}, 0, "", true) // no name: skipped, and the nil map is safe
	if _, _, ok := c.names(ctx, 7, 0); ok {
		t.Fatal("a chat with no name must not go into the cache")
	}
	c.put(telegram.Chat{ID: -100, Type: "supergroup", Title: "Ops"}, 9, "Alerts", true)
	c.put(telegram.Chat{ID: -100, Type: "supergroup", Title: "Ops"}, 9, "", true) // an icon-only rename keeps the name
	if chat, topic, ok := c.names(ctx, -100, 9); !ok || chat != "Ops" || topic != "Alerts" {
		t.Fatalf("%q %q %v", chat, topic, ok)
	}
	if chat, topic, ok := c.names(ctx, -100, 0); !ok || chat != "Ops" || topic != "" {
		t.Fatalf("no thread: %q %q %v", chat, topic, ok)
	}
	if _, _, ok := c.names(ctx, 5, 0); ok {
		t.Fatal("an unknown chat")
	}
	// The map holds at most 1000 chats. A put into a full map removes the oldest entry.
	for i := int64(1); i <= 1000; i++ {
		now = now.Add(time.Second)
		c.put(telegram.Chat{ID: 1000 + i, FirstName: "U"}, 0, "", true)
	}
	if len(c.m) != 1000 {
		t.Fatalf("%d chats, want 1000", len(c.m))
	}
	if _, _, ok := c.names(ctx, -100, 0); ok {
		t.Fatal("the oldest chat must go")
	}
	if chat, _, ok := c.names(ctx, 2000, 0); !ok || chat != "U" {
		t.Fatal("the newest chat must stay")
	}
}

func TestChatTitlesKV(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	kv := func(key string) string {
		t.Helper()
		v, _, err := d.KVGet(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	c := &chatTitles{TTL: time.Hour, DB: d}
	c.put(telegram.Chat{ID: -100, Title: "Ops"}, 9, "Alerts", true)
	if kv("tg.chat.-100") != "Ops" || kv("tg.topic.-100.9") != "Alerts" {
		t.Fatalf("%q %q", kv("tg.chat.-100"), kv("tg.topic.-100.9"))
	}
	// The same name makes no write.
	if err := d.KVDelete(ctx, "tg.chat.-100"); err != nil {
		t.Fatal(err)
	}
	c.put(telegram.Chat{ID: -100, Title: "Ops"}, 0, "", true)
	if v := kv("tg.chat.-100"); v != "" {
		t.Fatalf("the same name wrote %q", v)
	}
	c.put(telegram.Chat{ID: -100, Title: "Ops 2"}, 0, "", true)
	if v := kv("tg.chat.-100"); v != "Ops 2" {
		t.Fatalf("a new name must be written: %q", v)
	}
	// After a restart, a new cache with the same database reads the names.
	c2 := &chatTitles{TTL: time.Hour, DB: d}
	if chat, topic, ok := c2.names(ctx, -100, 9); !ok || chat != "Ops 2" || topic != "Alerts" {
		t.Fatalf("%q %q %v", chat, topic, ok)
	}
	if _, _, ok := c2.names(ctx, -300, 0); ok {
		t.Fatal("an unknown chat")
	}
}

// A message in a topic replies to the service message that created the topic, so it holds the
// first topic name. That name fills a gap, but it must not undo a rename, also after a restart.
func TestChatTitlesReplyNameKeepsRename(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ops := telegram.Chat{ID: -100, Type: "supergroup", Title: "Ops"}
	c := &chatTitles{TTL: time.Hour, DB: d}
	c.put(ops, 9, "Alerts", false) // no name yet: the reply name fills the gap
	if _, topic, _ := c.names(ctx, -100, 9); topic != "Alerts" {
		t.Fatalf("gap: %q", topic)
	}
	c.put(ops, 9, "Ops alerts", true) // the rename
	c.put(ops, 9, "Alerts", false)    // the next message in the topic
	if _, topic, _ := c.names(ctx, -100, 9); topic != "Ops alerts" {
		t.Fatalf("memory: %q", topic)
	}
	if v, _, _ := d.KVGet(ctx, "tg.topic.-100.9"); v != "Ops alerts" {
		t.Fatalf("kv: %q", v)
	}
	// After a restart, the memory is empty and the KV table holds the rename.
	c2 := &chatTitles{TTL: time.Hour, DB: d}
	c2.put(ops, 9, "Alerts", false)
	if _, topic, _ := c2.names(ctx, -100, 9); topic != "Ops alerts" {
		t.Fatalf("restart memory: %q", topic)
	}
	if v, _, _ := d.KVGet(ctx, "tg.topic.-100.9"); v != "Ops alerts" {
		t.Fatalf("restart kv: %q", v)
	}
}

// A getChat answer fills the memory only. A later message with the same name must still write
// the name to the KV table, so the name stays after a restart.
func TestChatTitlesPutWritesNameFromGetChat(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ops := telegram.Chat{ID: -100, Type: "supergroup", Title: "Ops"}
	c := &chatTitles{TTL: time.Hour, DB: d}
	c.mu.Lock()
	c.storeChat(ops.ID, titleEntry{chat: ops, at: time.Now()}) // what get stores
	c.mu.Unlock()
	c.put(ops, 0, "", true)
	if v, _, _ := d.KVGet(ctx, "tg.chat.-100"); v != "Ops" {
		t.Fatalf("kv: %q", v)
	}
}

// names keeps a miss for a short time, so a list of unknown chats makes no KV read for each
// row. A put clears the miss at once.
func TestChatTitlesNamesCachesMiss(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	c := &chatTitles{TTL: time.Hour, DB: d, Now: func() time.Time { return now }}
	if _, _, ok := c.names(ctx, -100, 0); ok {
		t.Fatal("unknown chat")
	}
	if err := d.KVSet(ctx, "tg.chat.-100", "Ops"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.names(ctx, -100, 0); ok {
		t.Fatal("a cached miss must skip the KV read")
	}
	now = now.Add(2 * time.Minute)
	if chat, _, ok := c.names(ctx, -100, 0); !ok || chat != "Ops" {
		t.Fatalf("after the miss time: %q %v", chat, ok)
	}
	// A topic miss, then a put of the topic name.
	if _, topic, _ := c.names(ctx, -100, 9); topic != "" {
		t.Fatalf("topic: %q", topic)
	}
	c.put(telegram.Chat{ID: -100, Title: "Ops"}, 9, "Alerts", true)
	if _, topic, _ := c.names(ctx, -100, 9); topic != "Alerts" {
		t.Fatalf("a put must clear the miss: %q", topic)
	}
	// A chat miss, then a put of the chat.
	if _, _, ok := c.names(ctx, -300, 0); ok {
		t.Fatal("unknown chat")
	}
	c.put(telegram.Chat{ID: -300, Title: "Dev"}, 0, "", true)
	if chat, _, ok := c.names(ctx, -300, 0); !ok || chat != "Dev" {
		t.Fatalf("a put must clear the chat miss: %q %v", chat, ok)
	}
}

func TestBootChatTitleFromChatSeen(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, outboundTG)
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Telegram.ChatSeen == nil {
		t.Fatal("AddTelegram must set ChatSeen")
	}
	if _, _, ok := a.bootChatTitle("-100", "7"); ok {
		t.Fatal("no name yet")
	}
	a.Telegram.ChatSeen(telegram.Chat{ID: -100, Type: "supergroup", Title: "Ops"}, 7, "Alerts", true)
	for thread, want := range map[string]string{"7": "Alerts", "0": "", "": ""} {
		if chat, topic, ok := a.bootChatTitle("-100", thread); !ok || chat != "Ops" || topic != want {
			t.Errorf("thread %q: %q %q %v", thread, chat, topic, ok)
		}
	}
	// A restart: a new cache with the same database.
	a.titles = &chatTitles{C: a.titles.C, TTL: time.Hour, DB: a.DB}
	if chat, topic, ok := a.bootChatTitle("-100", "7"); !ok || chat != "Ops" || topic != "Alerts" {
		t.Fatalf("after a restart: %q %q %v", chat, topic, ok)
	}
}
