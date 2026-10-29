package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

// sendRecorder is a Telegram channel that records the texts that it sends.
type sendRecorder struct {
	mu   sync.Mutex
	sent map[gateway.Target]string
	n    int
	err  error
}

func (*sendRecorder) Name() string                                { return "telegram" }
func (*sendRecorder) Run(context.Context, *gateway.Gateway) error { return nil }
func (r *sendRecorder) Send(_ context.Context, to gateway.Target, m gateway.Outbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	if r.err != nil {
		return r.err
	}
	if r.sent == nil {
		r.sent = map[gateway.Target]string{}
	}
	r.sent[to] = m.Text
	return nil
}

// bootApp returns a gateway App with Telegram on, a recording channel, and a home chat.
func bootApp(t *testing.T, withHome bool) (*App, *sendRecorder) {
	t.Helper()
	a := gatewayApp(t, "", "")
	a.Cfg.Telegram.Enabled = true
	rec := &sendRecorder{}
	a.Gateway().Channels["telegram"] = rec
	if withHome {
		if err := a.DB.KVSet(ctx, "telegram.home_chat", "100"); err != nil {
			t.Fatal(err)
		}
	}
	return a, rec
}

func setNotify(t *testing.T, a *App, to gateway.Target, age time.Duration) {
	t.Helper()
	b, err := json.Marshal(restartNotify{Source: to.Source, ChatID: to.ChatID, ThreadID: to.ThreadID,
		At: time.Now().Add(-age).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.KVSet(ctx, RestartNotifyKey, string(b)); err != nil {
		t.Fatal(err)
	}
}

func TestBootNoticeMatrix(t *testing.T) {
	home := gateway.Target{Source: "telegram", ChatID: "100"}
	req := gateway.Target{Source: "telegram", ChatID: "42", ThreadID: "7"}
	cases := []struct {
		name          string
		notify        *gateway.Target
		last, version string
		want          map[gateway.Target]string
	}{
		{"restart, same version", &req, "0.6.4", "0.6.4",
			map[gateway.Target]string{req: "♻ The gateway restarted. Your session continues."}},
		{"restart, new version", &req, "0.6.3", "0.6.4", map[gateway.Target]string{
			req:  "♻ The gateway restarted on tether 0.6.4 (before: 0.6.3). Your session continues.",
			home: "♻ Tether updated from 0.6.3 to 0.6.4 and restarted."}},
		{"restart from the home chat, new version", &home, "0.6.3", "0.6.4", map[gateway.Target]string{
			home: "♻ The gateway restarted on tether 0.6.4 (before: 0.6.3). Your session continues."}},
		{"update only", nil, "0.6.3", "0.6.4",
			map[gateway.Target]string{home: "♻ Tether updated from 0.6.3 to 0.6.4 and restarted."}},
		{"nothing", nil, "0.6.4", "0.6.4", map[gateway.Target]string{}},
		{"first start", nil, "", "0.6.4", map[gateway.Target]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, rec := bootApp(t, true)
			if c.notify != nil {
				setNotify(t, a, *c.notify, time.Minute)
			}
			if c.last != "" {
				if err := a.DB.KVSet(ctx, LastVersionKey, c.last); err != nil {
					t.Fatal(err)
				}
			}
			a.BootNotice(ctx, c.version)
			rec.mu.Lock()
			got := maps.Clone(rec.sent)
			n := rec.n
			rec.mu.Unlock()
			if got == nil {
				got = map[gateway.Target]string{}
			}
			if !maps.Equal(got, c.want) || n != len(c.want) {
				t.Fatalf("sent %d: %q, want %q", n, got, c.want)
			}
			if _, ok, _ := a.DB.KVGet(ctx, RestartNotifyKey); ok {
				t.Fatal("restart notify is still set")
			}
			if v, _, _ := a.DB.KVGet(ctx, LastVersionKey); v != c.version {
				t.Fatalf("last version %q", v)
			}
		})
	}
}

func TestBootNoticeOldRestartNotify(t *testing.T) {
	a, rec := bootApp(t, true)
	setNotify(t, a, gateway.Target{Source: "telegram", ChatID: "42"}, 11*time.Minute)
	if err := a.DB.KVSet(ctx, LastVersionKey, "0.6.4"); err != nil {
		t.Fatal(err)
	}
	a.BootNotice(ctx, "0.6.4")
	if rec.n != 0 {
		t.Fatalf("sent %q", rec.sent)
	}
	if _, ok, _ := a.DB.KVGet(ctx, RestartNotifyKey); ok {
		t.Fatal("restart notify is still set")
	}
}

func TestBootNoticeNoHomeChat(t *testing.T) {
	a, rec := bootApp(t, false)
	if err := a.DB.KVSet(ctx, LastVersionKey, "0.6.3"); err != nil {
		t.Fatal(err)
	}
	a.BootNotice(ctx, "0.6.4")
	if rec.n != 0 {
		t.Fatalf("sent %q", rec.sent)
	}
	if v, _, _ := a.DB.KVGet(ctx, LastVersionKey); v != "0.6.4" {
		t.Fatalf("last version %q", v)
	}
}

func TestBootNoticeSendFailure(t *testing.T) {
	a, rec := bootApp(t, true)
	rec.err = errors.New("boom")
	setNotify(t, a, gateway.Target{Source: "telegram", ChatID: "42"}, time.Minute)
	if err := a.DB.KVSet(ctx, LastVersionKey, "0.6.3"); err != nil {
		t.Fatal(err)
	}
	a.BootNotice(ctx, "0.6.4")
	if rec.n != 2 {
		t.Fatalf("sends %d", rec.n)
	}
	if _, ok, _ := a.DB.KVGet(ctx, RestartNotifyKey); ok {
		t.Fatal("restart notify is still set")
	}
	if v, _, _ := a.DB.KVGet(ctx, LastVersionKey); v != "0.6.4" {
		t.Fatalf("last version %q", v)
	}
}

func TestBootNoticeTelegramOff(t *testing.T) {
	a, rec := bootApp(t, true)
	a.Cfg.Telegram.Enabled = false
	if err := a.DB.KVSet(ctx, LastVersionKey, "0.6.3"); err != nil {
		t.Fatal(err)
	}
	a.BootNotice(ctx, "0.6.4")
	if rec.n != 0 {
		t.Fatalf("sent %q", rec.sent)
	}
}
