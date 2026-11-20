package cron

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
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
	to      Target
	text    string
	noSound bool
}

func recorder(home func(context.Context, bool) (Target, bool)) (*Deliverer, *[]sent) {
	var calls []sent
	return &Deliverer{
		Send: func(_ context.Context, to Target, text string, _ []string, noSound bool) error {
			calls = append(calls, sent{to, text, noSound})
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
	d := &Deliverer{Send: func(context.Context, Target, string, []string, bool) error { return errors.New("flood wait") }}
	if got, err := d.Deliver(ctx, j, RunOutcome{Text: "x", Send: true}); err == nil || got != DeliveryFailed || !strings.Contains(err.Error(), "flood wait") {
		t.Fatal(got, err)
	}
	d.Send = func(context.Context, Target, string, []string, bool) error { panic("bad channel") }
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
	okSend := func(context.Context, Target, string, []string, bool) error { sends++; return nil }
	fail := func(context.Context, Target, string, []string, bool) error { return errors.New("flood wait") }
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
	d.Send = func(context.Context, Target, string, []string, bool) error { return errors.New("flood wait") }
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

// draftDeliverer returns a recorder whose private targets are the chats in private, with the
// owner chat 5.
func draftDeliverer(private ...string) (*Deliverer, *[]sent) {
	d, calls := recorder(homeAt("-100", "99"))
	d.IsPrivate = func(_ context.Context, t Target) bool { return slices.Contains(private, t.ChatID) }
	d.OwnerHome = func(context.Context) (Target, bool) { return Target{Source: "telegram", ChatID: "5"}, true }
	return d, calls
}

func TestDraftOnlyReportToGroupFails(t *testing.T) {
	d, calls := draftDeliverer("5")
	j := Job{ID: "j1", Name: "n", Deliver: "telegram:-100", DraftOnly: true}
	got, err := d.Deliver(ctx, j, RunOutcome{Text: "drafts ready", Send: true, Status: "ok"})
	if got != DeliveryFailed || !errors.Is(err, ErrDraftDeliver) || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
}

func TestDraftOnlyReportToPrivateSends(t *testing.T) {
	d, calls := draftDeliverer("5")
	j := Job{ID: "j1", Name: "n", Deliver: "telegram:5", DraftOnly: true}
	got, err := d.Deliver(ctx, j, RunOutcome{Text: "drafts ready", Send: true, Status: "ok"})
	if got != DeliverySent || err != nil || len(*calls) != 1 || (*calls)[0].to.ChatID != "5" {
		t.Fatal(got, err, *calls)
	}
}

func TestDraftOnlyAlertsGoToOwner(t *testing.T) {
	alert := RunOutcome{Text: "⚠ Cron job n failed: boom", Send: true, Status: "error"}
	pause := RunOutcome{Text: "⚠ alert\n\n⏸ paused", Send: true, Status: "error", Pause: true}
	for name, tc := range map[string]struct {
		job Job
		o   RunOutcome
	}{
		"group alert":        {Job{ID: "j1", Name: "n", Deliver: "telegram:-100", DraftOnly: true}, alert},
		"cli origin to home": {Job{ID: "j2", Name: "n", Origin: Origin{Source: "cli"}, DraftOnly: true}, alert},
		"local pause":        {Job{ID: "j3", Name: "n", Deliver: "local", DraftOnly: true}, pause},
	} {
		d, calls := draftDeliverer("5")
		got, err := d.Deliver(ctx, tc.job, tc.o)
		if got != DeliverySent || err != nil || len(*calls) != 1 || (*calls)[0].to != (Target{Source: "telegram", ChatID: "5"}) {
			t.Errorf("%s: %v %v %v", name, got, err, *calls)
		}
	}
	// A local draft-only job with no pause sends nothing, as today.
	d, calls := draftDeliverer("5")
	if got, err := d.Deliver(ctx, Job{ID: "j4", Name: "n", Deliver: "local", DraftOnly: true}, alert); got != DeliveryLocal || err != nil || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
	// No owner chat: the group alert fails, and nothing goes to the group.
	d, calls = draftDeliverer("5")
	d.OwnerHome = nil
	if got, err := d.Deliver(ctx, Job{ID: "j5", Name: "n", Deliver: "telegram:-100", DraftOnly: true}, alert); got != DeliveryFailed || err == nil || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
}

const (
	recLine  = "✅ Cron job n works again after 2 failed runs."
	costLine = "💸 This run cost $0.50, 5.0× the usual $0.10 of this job."
)

// A repeat alert sends nothing. The pause alert always goes out, also on a repeat.
func TestDeliverRepeatSendsNothing(t *testing.T) {
	d, calls := recorder(homeAt("-100", ""))
	chat := Job{ID: "j1", Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	o := RunOutcome{Text: "⚠ Cron job n failed: provider down", Send: true, Status: "error", Repeat: true}
	if got, err := d.Deliver(ctx, chat, o); err != nil || got != DeliveryRepeat || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
	local := Job{ID: "j2", Name: "n", Deliver: "local"}
	if got, err := d.Deliver(ctx, local, o); err != nil || got != DeliveryRepeat || len(*calls) != 0 {
		t.Fatal(got, err, *calls)
	}
	o.Pause = true
	if got, err := d.Deliver(ctx, chat, o); err != nil || got != DeliverySent || len(*calls) != 1 {
		t.Fatal(got, err, *calls)
	}
}

// The recovered line goes after the result. A silent run and a local job send only the line.
// A draft-only job sends the line to the owner chat.
func TestDeliverRecoveredLine(t *testing.T) {
	chat := Job{ID: "j1", Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	local := Job{ID: "j2", Name: "n", Deliver: "local"}
	draftLocal := Job{ID: "j3", Name: "n", Deliver: "local", DraftOnly: true}
	ok := RunOutcome{Text: "report", Send: true, Status: "ok", RecoveredLine: recLine}
	silent := RunOutcome{Text: "[SILENT]", Status: "ok", Silent: true, RecoveredLine: recLine}
	origin := Target{Source: "telegram", ChatID: "1"}
	home := Target{Source: "telegram", ChatID: "-100", ThreadID: "99"}
	owner := Target{Source: "telegram", ChatID: "5"}
	for _, c := range []struct {
		name string
		j    Job
		o    RunOutcome
		want string
		to   Target
		text string
	}{
		{"result and line", chat, ok, DeliverySent, origin, "report\n\n" + recLine},
		{"silent sends only the line", chat, silent, DeliverySilent, origin, recLine},
		{"local sends only the line home", local, ok, DeliveryLocal, home, recLine},
		{"local silent", local, silent, DeliverySilent, home, recLine},
		{"draft-only line to the owner", draftLocal, ok, DeliveryLocal, owner, recLine},
	} {
		d, calls := draftDeliverer("1", "5")
		got, err := d.Deliver(ctx, c.j, c.o)
		if err != nil || got != c.want || len(*calls) != 1 || (*calls)[0].to != c.to || (*calls)[0].text != c.text {
			t.Errorf("%s: got %q, %v, %+v", c.name, got, err, *calls)
		}
	}
	// A failed send of the line only logs. The run keeps its delivery value.
	d, _ := recorder(homeAt("-100", "99"))
	d.Send = func(context.Context, Target, string, []string, bool) error { return errors.New("flood wait") }
	if got, err := d.Deliver(ctx, local, ok); err != nil || got != DeliveryLocal {
		t.Fatal(got, err)
	}
	if got, err := d.Deliver(ctx, chat, silent); err != nil || got != DeliverySilent {
		t.Fatal(got, err)
	}
}

// A message that is only the recovered line skips the scan and model_guard.
func TestDeliverLineSkipsGuard(t *testing.T) {
	r := outbound(t, false)
	g := &guardRec{allow: false, reason: "no"}
	r.d.Guard = g
	o := RunOutcome{Text: "[SILENT]", Status: "ok", Silent: true, Sources: []string{"web_fetch"}, RecoveredLine: recLine}
	got, err := r.d.Deliver(ctx, groupJob, o)
	if err != nil || got != DeliverySilent || len(g.got) != 0 || len(r.scanned) != 0 || len(r.holds) != 0 ||
		!reflect.DeepEqual(r.sent, []string{recLine}) {
		t.Fatal(got, err, g.got, r.scanned, r.sent)
	}
}

// The cost line goes only to a private target, after the recovered line. It never goes alone.
func TestDeliverCostLineOnlyPrivate(t *testing.T) {
	o := RunOutcome{Text: "report", Send: true, Status: "ok", RecoveredLine: recLine, CostLine: costLine}
	priv := outbound(t, true)
	if got, err := priv.d.Deliver(ctx, groupJob, o); err != nil || got != DeliverySent ||
		!reflect.DeepEqual(priv.sent, []string{"report\n\n" + recLine + "\n\n" + costLine}) {
		t.Fatal(got, err, priv.sent)
	}
	group := outbound(t, false)
	if got, err := group.d.Deliver(ctx, groupJob, o); err != nil || got != DeliverySent ||
		!reflect.DeepEqual(group.sent, []string{"report\n\n" + recLine}) {
		t.Fatal(got, err, group.sent)
	}
	alone := outbound(t, true)
	silent := RunOutcome{Text: "[SILENT]", Status: "ok", Silent: true, CostLine: costLine}
	if got, err := alone.d.Deliver(ctx, groupJob, silent); err != nil || got != DeliverySilent || len(alone.sent) != 0 {
		t.Fatal(got, err, alone.sent)
	}
	local := Job{ID: "j2", Name: "n", Deliver: "local"}
	localRun := RunOutcome{Text: "report", Send: true, Status: "ok", CostLine: costLine}
	if got, err := alone.d.Deliver(ctx, local, localRun); err != nil || got != DeliveryLocal || len(alone.sent) != 0 {
		t.Fatal(got, err, alone.sent)
	}
}

// NoSound reaches each send: the result, the line, and the alert to the owner chat. The owner
// note of a masked delivery gets the flag too.
func TestDeliverNoSound(t *testing.T) {
	d, calls := draftDeliverer("1", "5")
	chat := Job{ID: "j1", Name: "n", Origin: Origin{Source: "telegram", ChatID: "1"}}
	draft := Job{ID: "j2", Name: "n", Deliver: "telegram:-100", DraftOnly: true}
	for _, c := range []struct {
		j Job
		o RunOutcome
	}{
		{chat, RunOutcome{Text: "report", Send: true, Status: "ok", NoSound: true}},
		{chat, RunOutcome{Text: "[SILENT]", Status: "ok", Silent: true, RecoveredLine: recLine, NoSound: true}},
		{chat, RunOutcome{Text: "⚠ alert", Send: true, Status: "error", NoSound: true}},
		{draft, RunOutcome{Text: "⚠ alert", Send: true, Status: "error", NoSound: true}},
	} {
		if _, err := d.Deliver(ctx, c.j, c.o); err != nil {
			t.Fatal(err)
		}
	}
	if len(*calls) != 4 {
		t.Fatal(*calls)
	}
	for _, c := range *calls {
		if !c.noSound {
			t.Fatalf("%+v", c)
		}
	}
	if _, err := d.Deliver(ctx, chat, RunOutcome{Text: "report", Send: true, Status: "ok"}); err != nil || (*calls)[4].noSound {
		t.Fatal(err, *calls)
	}
	r := outbound(t, false)
	if _, err := r.d.Deliver(ctx, groupJob, RunOutcome{Text: cardText, Send: true, Status: "ok", NoSound: true}); err != nil ||
		len(r.notes) != 1 || !r.notes[0].NoSound {
		t.Fatal(err, r.notes)
	}
}

// A result that model_guard holds still sends the recovered line, so the owner learns that
// the job works again.
func TestDeliverHoldSendsRecoveredLine(t *testing.T) {
	r := outbound(t, false)
	r.d.Guard = &guardRec{allow: false, reason: "no"}
	o := RunOutcome{Text: "report", Send: true, Status: "ok", Sources: []string{"web_fetch"}, RecoveredLine: recLine}
	got, err := r.d.Deliver(ctx, groupJob, o)
	if err != nil || got != DeliveryHeld || len(r.holds) != 1 || !reflect.DeepEqual(r.sent, []string{recLine}) {
		t.Fatal(got, err, r.holds, r.sent)
	}
}
