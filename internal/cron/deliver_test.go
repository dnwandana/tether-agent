package cron

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func noHome(context.Context, bool) (Target, bool) { return Target{}, false }

// homeAt returns a home function. For cron it returns the cron thread; for other uses it
// returns the general topic of the home chat.
func homeAt(chat, cronThread string) func(context.Context, bool) (Target, bool) {
	return func(_ context.Context, forCron bool) (Target, bool) {
		if forCron {
			return Target{Source: "telegram", ChatID: chat, ThreadID: cronThread}, true
		}
		return Target{Source: "telegram", ChatID: chat}, true
	}
}

type sent struct {
	to   Target
	text string
}

func recorder(home func(context.Context, bool) (Target, bool)) (*Deliverer, *[]sent) {
	var calls []sent
	return &Deliverer{
		Send: func(_ context.Context, to Target, text string, _ []string) error {
			calls = append(calls, sent{to, text})
			return nil
		},
		Home: home,
	}, &calls
}

func TestOriginChatAndTopic(t *testing.T) {
	tgt, local, _ := resolveTarget(ctx, Job{Origin: Origin{Source: "telegram", ChatID: "-100", ThreadID: "17585"}}, noHome)
	if local || tgt.ChatID != "-100" || tgt.ThreadID != "17585" || tgt.Source != "telegram" {
		t.Fatal(tgt)
	}
	tgt, _, _ = resolveTarget(ctx, Job{Deliver: "origin", Origin: Origin{Source: "telegram", ChatID: "42"}}, noHome)
	if tgt.ChatID != "42" || tgt.ThreadID != "" {
		t.Fatal(tgt)
	}
}

func TestCLIOriginGoesHomeWithCronThread(t *testing.T) {
	tgt, _, _ := resolveTarget(ctx, Job{Origin: Origin{Source: "cli"}}, homeAt("-100", "99"))
	if tgt.ChatID != "-100" || tgt.ThreadID != "99" {
		t.Fatal(tgt)
	}
	// A job with no stored origin also goes to the home channel.
	tgt, _, err := resolveTarget(ctx, Job{}, homeAt("-100", "99"))
	if err != nil || tgt.ChatID != "-100" || tgt.ThreadID != "99" {
		t.Fatal(tgt, err)
	}
}

func TestExplicitTelegramTarget(t *testing.T) {
	tgt, local, err := resolveTarget(ctx, Job{Deliver: "telegram:-100:5", Origin: Origin{Source: "telegram", ChatID: "7"}}, noHome)
	if err != nil || local || tgt != (Target{Source: "telegram", ChatID: "-100", ThreadID: "5"}) {
		t.Fatal(tgt, err)
	}
	// Without a thread, the target has no topic (Hermes).
	tgt, _, err = resolveTarget(ctx, Job{Deliver: "telegram:@chan"}, noHome)
	if err != nil || tgt != (Target{Source: "telegram", ChatID: "@chan"}) {
		t.Fatal(tgt, err)
	}
	if _, _, err := resolveTarget(ctx, Job{Deliver: "slack:x"}, noHome); err == nil {
		t.Fatal("unknown deliver value must fail")
	}
}

func TestLocalNeverSends(t *testing.T) {
	d, calls := recorder(homeAt("-100", ""))
	j := Job{Name: "n", Deliver: "local", Origin: Origin{Source: "telegram", ChatID: "1"}}
	if _, local, _ := resolveTarget(ctx, j, d.Home); !local {
		t.Fatal("local")
	}
	if got, err := d.Deliver(ctx, j, RunOutcome{Text: "hi", Send: true}); err != nil || got != DeliveryLocal || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
}

func TestNoHomeErrorText(t *testing.T) {
	_, _, err := resolveTarget(ctx, Job{Origin: Origin{Source: "cli"}}, noHome)
	if err == nil || err.Error() != "no delivery target: run /sethome in a Telegram chat, or set deliver" {
		t.Fatal(err)
	}
	if _, _, err := resolveTarget(ctx, Job{Origin: Origin{Source: "cli"}}, nil); err == nil {
		t.Fatal("nil home must fail")
	}
	var logs bytes.Buffer
	d, calls := recorder(noHome)
	d.Log = slog.New(slog.NewTextHandler(&logs, nil))
	if got, err := d.Deliver(ctx, Job{ID: "j1", Name: "n"}, RunOutcome{Text: "x", Send: true}); err == nil || got != DeliveryFailed || len(*calls) != 0 {
		t.Fatal(got, err)
	}
	if !strings.Contains(logs.String(), "j1") {
		t.Fatal("delivery failure not logged:", logs.String())
	}
}

func TestSilentOutcomeNotSent(t *testing.T) {
	d, calls := recorder(homeAt("-100", ""))
	j := Job{Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	if got, err := d.Deliver(ctx, j, RunOutcome{Send: false}); err != nil || got != DeliveryNone || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
}

func TestDeliverSendsText(t *testing.T) {
	d, calls := recorder(noHome)
	d.Redact = func(s string) string { return strings.ReplaceAll(s, "sk-secret", "***") }
	j := Job{Name: "n", Origin: Origin{Source: "telegram", ChatID: "-100", ThreadID: "3"}}
	if got, err := d.Deliver(ctx, j, RunOutcome{Text: "key sk-secret", Send: true}); err != nil || got != DeliverySent {
		t.Fatal(got, err)
	}
	if len(*calls) != 1 || (*calls)[0].to.ThreadID != "3" || (*calls)[0].text != "key ***" {
		t.Fatal(*calls)
	}
}

func TestSendErrorReturnedNotPanic(t *testing.T) {
	j := Job{ID: "j2", Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	d := &Deliverer{Send: func(context.Context, Target, string, []string) error { return errors.New("flood wait") }}
	if got, err := d.Deliver(ctx, j, RunOutcome{Text: "x", Send: true}); err == nil || got != DeliveryFailed || !strings.Contains(err.Error(), "flood wait") {
		t.Fatal(got, err)
	}
	d.Send = func(context.Context, Target, string, []string) error { panic("bad channel") }
	if got, err := d.Deliver(ctx, j, RunOutcome{Text: "x", Send: true}); err == nil || got != DeliveryFailed {
		t.Fatal("a panic in Send must become an error")
	}
	if got, err := (&Deliverer{}).Deliver(ctx, j, RunOutcome{Text: "x", Send: true}); err == nil || got != DeliveryFailed {
		t.Fatal("nil Send must fail")
	}
}

// Deliver returns the delivery value of each case. Silent comes before Send.
func TestDeliverReturnsDelivery(t *testing.T) {
	var sends int
	okSend := func(context.Context, Target, string, []string) error { sends++; return nil }
	fail := func(context.Context, Target, string, []string) error { return errors.New("flood wait") }
	chat := Job{ID: "j3", Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	local := Job{ID: "j4", Name: "n", Deliver: "local"}
	for _, c := range []struct {
		name    string
		d       *Deliverer
		j       Job
		o       RunOutcome
		want    string
		wantErr bool
	}{
		{"sent", &Deliverer{Send: okSend}, chat, RunOutcome{Text: "x", Send: true, Status: "ok"}, DeliverySent, false},
		{"silent", &Deliverer{Send: okSend}, chat, RunOutcome{Text: "[SILENT]", Status: "ok", Silent: true}, DeliverySilent, false},
		{"silent before send", &Deliverer{Send: okSend}, chat, RunOutcome{Text: "[SILENT]", Send: true, Status: "ok", Silent: true}, DeliverySilent, false},
		{"none", &Deliverer{Send: okSend}, chat, RunOutcome{Status: "ok"}, DeliveryNone, false},
		{"local", &Deliverer{Send: okSend, Home: homeAt("-100", "")}, local, RunOutcome{Text: "x", Send: true, Status: "ok"}, DeliveryLocal, false},
		{"failed send", &Deliverer{Send: fail}, chat, RunOutcome{Text: "x", Send: true, Status: "error"}, DeliveryFailed, true},
		{"failed no target", &Deliverer{Send: okSend, Home: noHome}, Job{ID: "j5", Origin: Origin{Source: "cli"}}, RunOutcome{Text: "x", Send: true}, DeliveryFailed, true},
	} {
		got, err := c.d.Deliver(ctx, c.j, c.o)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if sends != 1 {
		t.Fatalf("only the sent case sends, got %d sends", sends)
	}
}

// The pause alert of a local job goes to the home channel, with the cron thread.
func TestLocalPauseGoesHome(t *testing.T) {
	d, calls := recorder(homeAt("-100", "99"))
	j := Job{ID: "j6", Name: "n", Deliver: "local"}
	o := RunOutcome{Text: "⚠ alert\n\n⏸ paused", Send: true, Status: "error", Pause: true}
	got, err := d.Deliver(ctx, j, o)
	if err != nil || got != DeliverySent || len(*calls) != 1 ||
		(*calls)[0].to != (Target{Source: "telegram", ChatID: "-100", ThreadID: "99"}) || (*calls)[0].text != o.Text {
		t.Fatal(got, err, *calls)
	}
	d.Send = func(context.Context, Target, string, []string) error { return errors.New("flood wait") }
	if got, err := d.Deliver(ctx, j, o); err == nil || got != DeliveryFailed {
		t.Fatal(got, err)
	}
}

// With no home channel, a nil Home, or a nil Send, the pause alert of a local job stays local.
// That is not an error.
func TestLocalPauseWithoutHomeStaysLocal(t *testing.T) {
	j := Job{ID: "j7", Name: "n", Deliver: "local"}
	o := RunOutcome{Text: "alert", Send: true, Status: "error", Pause: true}
	noHomeD, noHomeCalls := recorder(noHome)
	nilHomeD, nilHomeCalls := recorder(nil)
	for name, d := range map[string]*Deliverer{
		"no home channel": noHomeD,
		"nil Home":        nilHomeD,
		"nil Send":        {Home: homeAt("-100", "")},
	} {
		if got, err := d.Deliver(ctx, j, o); err != nil || got != DeliveryLocal {
			t.Errorf("%s: got %q, %v; want %q", name, got, err, DeliveryLocal)
		}
	}
	if len(*noHomeCalls) != 0 || len(*nilHomeCalls) != 0 {
		t.Fatal(*noHomeCalls, *nilHomeCalls)
	}
}
