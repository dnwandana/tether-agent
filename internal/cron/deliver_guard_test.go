package cron

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

type guardRec struct {
	allow  bool
	reason string
	got    []security.GuardInput
}

func (g *guardRec) Check(_ context.Context, in security.GuardInput) (bool, string) {
	g.got = append(g.got, in)
	return g.allow, g.reason
}

// outboundRec is a Deliverer with every new field set. Each call goes into the fields of the rec.
type outboundRec struct {
	d        *Deliverer
	sent     []string
	scanned  []string
	redacted []string
	notes    []Note
	holds    []HeldCardInfo
	logs     []string
	holdErr  error
}

func outbound(t *testing.T, private bool) *outboundRec {
	t.Helper()
	r := &outboundRec{}
	r.d = &Deliverer{
		Send: func(_ context.Context, _ Target, text string, _ []string, _ bool) error {
			r.sent = append(r.sent, text)
			return nil
		},
		Home:      noHome,
		Redact:    func(s string) string { r.redacted = append(r.redacted, s); return s },
		IsPrivate: func(context.Context, Target) bool { return private },
		Scan: func(text string, handles []string) (string, []security.Finding) {
			r.scanned = append(r.scanned, text)
			f := security.Scan(text, nil, handles)
			return security.RedactFindings(text, f), f
		},
		Hold: func(_ context.Context, _ HeldSend, c HeldCardInfo) error {
			r.holds = append(r.holds, c)
			return r.holdErr
		},
		Note:        func(_ context.Context, n Note) error { r.notes = append(r.notes, n); return nil },
		TargetInfo:  func(context.Context, Target) TargetInfo { return TargetInfo{Kind: "group", Title: "Pricing team"} },
		LogDecision: func(_ context.Context, _ Job, decision, reason string) { r.logs = append(r.logs, decision+": "+reason) },
		Held:        heldStore(t),
	}
	return r
}

var groupJob = Job{ID: "j1", Name: "competitor-prices", Prompt: "Check the prices.", Deliver: "telegram:-100"}

const cardText = "pay 4111 1111 1111 1111 with tok-1234567890abcdef"

func TestDeliverPrivateRedactsOnly(t *testing.T) {
	r := outbound(t, true)
	got, err := r.d.Deliver(ctx, groupJob, RunOutcome{Text: cardText, Send: true, Status: "ok"})
	if err != nil || got != DeliverySent || len(r.scanned) != 0 || len(r.notes) != 0 || r.sent[0] != cardText {
		t.Fatal(got, err, r.scanned, r.sent)
	}
}

func TestDeliverScanBeforeRedactAndNote(t *testing.T) {
	r := outbound(t, false)
	o := RunOutcome{Text: cardText, Send: true, Status: "ok", Handles: []string{"tok-1234567890abcdef"}}
	got, err := r.d.Deliver(ctx, groupJob, o)
	if err != nil || got != DeliverySent {
		t.Fatal(got, err)
	}
	if r.scanned[0] != cardText || r.redacted[0] == cardText {
		t.Fatalf("the scan reads the raw text, and Redact the masked text: %q %q", r.scanned, r.redacted)
	}
	if r.sent[0] != "pay [card ••••1111] with [secret]" {
		t.Fatalf("%q", r.sent[0])
	}
	if len(r.notes) != 1 || len(r.notes[0].Findings) != 2 || r.notes[0].Original != cardText ||
		r.notes[0].TargetTitle != "Pricing team" || r.notes[0].HeldID == "" {
		t.Fatalf("%+v", r.notes)
	}
	h, ok, _ := r.d.Held.Claim(ctx, r.notes[0].HeldID)
	if !ok || h.Kind != "original" || h.Text != cardText || h.Target != "telegram:-100" {
		t.Fatalf("%+v %v", h, ok)
	}
	if len(r.logs) != 0 {
		t.Fatal("no guard call with no outside source")
	}
}

func TestDeliverGuardHold(t *testing.T) {
	r := outbound(t, false)
	g := &guardRec{reason: "the run read pastebin.com, and the text holds a secret. The purpose names neither."}
	r.d.Guard = g
	o := RunOutcome{Text: "report " + cardText, Send: true, Status: "ok", Sources: []string{"terminal:curl", "web_fetch"},
		Hosts: []string{"pastebin.com"}, ToolCounts: map[string]int{"web_fetch": 1}, SessionID: "s1"}
	got, err := r.d.Deliver(ctx, groupJob, o)
	if err != nil || got != DeliveryHeld || len(r.sent) != 0 {
		t.Fatal(got, err, r.sent)
	}
	want := security.GuardInput{SessionID: "s1", JobName: "competitor-prices", JobPurpose: "Check the prices.",
		Sources: o.Sources, Hosts: o.Hosts, ToolCounts: o.ToolCounts, Findings: map[string]int{"card": 1},
		Action: `deliver the final text to the Telegram group "Pricing team"`}
	if len(g.got) != 1 || !reflect.DeepEqual(g.got[0], want) {
		t.Fatalf("%+v", g.got)
	}
	card := HeldCardInfo{JobName: "competitor-prices", TargetTitle: "Pricing team", TargetKind: "group",
		Reason: g.reason, Sources: o.Sources}
	if len(r.holds) != 1 || !reflect.DeepEqual(r.holds[0], card) || r.logs[0] != security.DecisionBlocked+": "+g.reason {
		t.Fatalf("%+v %v", r.holds, r.logs)
	}
	rows, _ := r.d.Held.ClaimExpired(ctx, time.Now().Add(time.Hour))
	if len(rows) != 1 || rows[0].Kind != "hold" || rows[0].Text != o.Text || rows[0].Reason != g.reason {
		t.Fatalf("%+v", rows)
	}
	g.allow = true
	if got, _ := r.d.Deliver(ctx, groupJob, o); got != DeliverySent || r.logs[1] != security.DecisionAllowed+": "+g.reason {
		t.Fatal(got, r.logs)
	}
}

func TestDeliverGuardSkips(t *testing.T) {
	r := outbound(t, false)
	g := &guardRec{reason: "no"}
	r.d.Guard = g
	src := []string{"web_fetch"}
	for _, o := range []RunOutcome{
		{Text: "report", Send: true, Status: "ok"},
		{Text: "⚠ Cron job x failed: provider down", Send: true, Status: "error", Sources: src},
		{Text: "report", Send: true, Status: "ok", Sources: src, Pause: true},
	} {
		if got, _ := r.d.Deliver(ctx, groupJob, o); got != DeliverySent {
			t.Fatal(got)
		}
	}
	if len(g.got) != 0 {
		t.Fatalf("the guard ran for %+v", g.got)
	}
	partial := RunOutcome{Text: "⚠ Cron job x failed (timed out after 1s)" + partialAnswerMark + "\n\nhalf", Send: true,
		Status: "error", Sources: src}
	if got, _ := r.d.Deliver(ctx, groupJob, partial); got != DeliveryHeld || len(g.got) != 1 {
		t.Fatal(got, len(g.got))
	}
}

func TestDeliverHoldWithoutOwnerChat(t *testing.T) {
	r := outbound(t, false)
	r.d.Guard = &guardRec{reason: "no"}
	r.holdErr = errors.New("no owner chat")
	o := RunOutcome{Text: "report", Send: true, Status: "ok", Sources: []string{"web_fetch"}}
	got, err := r.d.Deliver(ctx, groupJob, o)
	if got != DeliveryFailed || err == nil || !strings.Contains(err.Error(), "model_guard held the delivery") || len(r.sent) != 0 {
		t.Fatal(got, err)
	}
	if rows, _ := r.d.Held.ClaimExpired(ctx, time.Now().Add(time.Hour)); len(rows) != 0 {
		t.Fatalf("a failed hold leaves no row: %+v", rows)
	}
}
