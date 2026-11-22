package telegram

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

func cardRequest() security.ApprovalRequest {
	return security.ApprovalRequest{Tool: "mcp:gmail.send_email", Class: "send", Server: "gmail", ToolName: "send_email",
		Command: "to: vendor@acme.io", NoSession: true, NoAlways: true,
		Findings: []security.Finding{{Kind: "card", Label: "[card ••••1111]"}}}
}

func TestRedactedKeyboard(t *testing.T) {
	kb := approvalKeyboard("id1", cardRequest())
	var rows [][]string
	for _, row := range kb.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.Text+"="+b.CallbackData)
		}
		rows = append(rows, r)
	}
	want := [][]string{{"❌ Deny=ap:id1:deny"}, {"✅ Send redacted=ap:id1:r", "✅ Once=ap:id1:once"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("%v", rows)
	}
	if got := headerFor(security.ScopeRedacted, 0).plain(); got != "✅ Sent redacted" {
		t.Fatalf("%q", got)
	}
	if security.FitScope(security.ScopeRedacted, true, true) != security.ScopeRedacted {
		t.Fatal("FitScope must keep redacted")
	}
}

func TestRedactedPromptText(t *testing.T) {
	mdv2, plain := buildApprovalPrompt(cardRequest(), 0)
	if !strings.HasSuffix(plain, "\nto: vendor@acme.io\n⚠️ Found: card ••••1111") {
		t.Fatalf("%q", plain)
	}
	if !strings.HasSuffix(mdv2, "\n```\n⚠️ Found: card ••••1111") {
		t.Fatalf("%q", mdv2)
	}
	_, plain = buildApprovalPrompt(security.ApprovalRequest{Command: "rm -rf x", Description: "recursive delete"}, 0)
	if strings.Contains(plain, "Found:") {
		t.Fatal(plain)
	}
}

func TestRedactedTap(t *testing.T) {
	it, api := interactions(t)
	done := make(chan security.Scope, 1)
	go func() {
		s, _ := it.promptApproval(context.Background(), 5, "", "", cardRequest())
		done <- s
	}()
	waitSends(t, api, 1)
	_, data := keyboardOf(api, 0)
	it.HandleCallback(context.Background(), press(5, approvalData(t, data, "r")))
	if s := <-done; s != security.ScopeRedacted {
		t.Fatal(s)
	}
	if got := api.lastCall("editMessageText").Body["text"]; !strings.HasPrefix(got.(string), "✅ *Sent redacted*") {
		t.Fatalf("%q", got)
	}
}

func TestRedactedTapNeedsFindings(t *testing.T) {
	it, api := interactions(t)
	done := make(chan security.Scope, 1)
	req := cardRequest()
	req.Findings = nil
	go func() {
		s, _ := it.promptApproval(context.Background(), 5, "", "", req)
		done <- s
	}()
	waitSends(t, api, 1)
	_, data := keyboardOf(api, 0)
	id := strings.Split(data[0], ":")[1]
	it.HandleCallback(context.Background(), press(5, "ap:"+id+":r"))
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != invalidCallback {
		t.Fatalf("%q", got)
	}
	it.HandleCallback(context.Background(), press(5, "ap:"+id+":deny"))
	if s := <-done; s != security.ScopeDeny {
		t.Fatal(s)
	}
}
