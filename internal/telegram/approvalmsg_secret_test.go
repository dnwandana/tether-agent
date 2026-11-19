package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

const tgHash = "3f2a0000000000000000000000000000000000000000000000000000000091c0"

func tgPinRequest() security.ApprovalRequest {
	return security.ApprovalRequest{Command: "twine upload dist/*", PinName: "PYPI_TOKEN",
		PinPath: "/home/user/.local/bin/twine", PinHash: tgHash, NoAlways: true, NoSession: true}
}

func TestBuildPinPrompt(t *testing.T) {
	mdv2, plain := buildApprovalPrompt(tgPinRequest(), 10*time.Minute)
	wantMD := "🔑 *Give PYPI\\_TOKEN to this program?*\n`/home/user/.local/bin/twine`\nsha256 3f2a…91c0\n" +
		"Command:\n```\ntwine upload dist/*\n```\n_No answer in 10 min \\= deny\\._"
	wantPlain := "🔑 Give PYPI_TOKEN to this program?\n/home/user/.local/bin/twine\nsha256 3f2a…91c0\n" +
		"Command: twine upload dist/*\nNo answer in 10 min = deny."
	if mdv2 != wantMD || plain != wantPlain {
		t.Fatalf("\n got %q\nwant %q\n got %q\nwant %q", mdv2, wantMD, plain, wantPlain)
	}
	if err := checkMDV2(mdv2); err != nil {
		t.Fatal(err)
	}
	req := tgPinRequest()
	req.ChildName = "2/5"
	if mdv2, plain := buildApprovalPrompt(req, 0); !strings.HasPrefix(mdv2, "🔑 *Give PYPI\\_TOKEN to this program?* \\(from subagent 2/5\\)\n") ||
		!strings.HasPrefix(plain, "🔑 Give PYPI_TOKEN to this program? (from subagent 2/5)\n") {
		t.Fatalf("%q\n%q", mdv2, plain)
	}
}

func TestPinKeyboardAndResult(t *testing.T) {
	kb := approvalKeyboard("abc", tgPinRequest())
	if len(kb.InlineKeyboard) != 2 || len(kb.InlineKeyboard[0]) != 1 || kb.InlineKeyboard[0][0].Text != "❌ Deny" ||
		len(kb.InlineKeyboard[1]) != 1 || kb.InlineKeyboard[1][0].Text != "✅ Allow this program" ||
		kb.InlineKeyboard[1][0].CallbackData != callbackData(approvalPrefix, "abc", "once") {
		t.Fatalf("%+v", kb)
	}
	for _, c := range []struct {
		scope security.Scope
		plain string
	}{
		{security.ScopeOnce, "✅ Program allowed"},
		{security.ScopeDeny, "❌ Program denied"},
		{security.ScopeTimeout, "⌛ Program denied · no answer in 10 min"},
	} {
		if got := resultHeader(true, c.scope, 10*time.Minute).plain(); got != c.plain {
			t.Errorf("%v: got %q, want %q", c.scope, got, c.plain)
		}
	}
	if got := resultHeader(false, security.ScopeOnce, 0).plain(); got != "✅ Approval allowed · once" {
		t.Fatal(got)
	}
	_, short, plain := buildApprovalResult(resultHeader(true, security.ScopeOnce, 0), tgPinRequest())
	if short != "✅ *Program allowed*\nPYPI\\_TOKEN: `/home/user/.local/bin/twine`" ||
		plain != "✅ Program allowed\nPYPI_TOKEN: /home/user/.local/bin/twine" {
		t.Fatalf("%q\n%q", short, plain)
	}
}

func TestBuildPromptSecretLines(t *testing.T) {
	for _, c := range []struct {
		name        string
		req         security.ApprovalRequest
		mdv2, plain string
	}{
		{"gets", security.ApprovalRequest{Command: "twine upload dist/*", Description: "d",
			Secrets: []string{"GH_TOKEN", "PYPI_TOKEN"}},
			"🛡 *Approval needed*\n_Flagged: d_\n🔑 Gets: GH\\_TOKEN, PYPI\\_TOKEN\n```\ntwine upload dist/*\n```",
			"🛡 Approval needed\nFlagged: d\n🔑 Gets: GH_TOKEN, PYPI_TOKEN\ntwine upload dist/*"},
		{"handle", security.ApprovalRequest{Command: "x", Description: "a printed credential after outside content",
			HandleNames: []string{"TMP_SECRET_1"}, OutsideSources: []string{"web_fetch"}, NoAlways: true, NoSession: true},
			"🛡 *Approval needed*\n_Flagged: a printed credential after outside content_\n" +
				"🔑 Uses a printed credential: TMP\\_SECRET\\_1\nThis turn read: web\\_fetch\n```\nx\n```",
			"🛡 Approval needed\nFlagged: a printed credential after outside content\n" +
				"🔑 Uses a printed credential: TMP_SECRET_1\nThis turn read: web_fetch\nx"},
		{"staged", security.ApprovalRequest{Command: "git add -A", Description: "d",
			StagedCredentials: []string{".env", "deploy/server.pem"}},
			"🛡 *Approval needed*\n_Flagged: d_\nThis stages credential files:\n• \\.env\n• deploy/server\\.pem\n```\ngit add -A\n```",
			"🛡 Approval needed\nFlagged: d\nThis stages credential files:\n• .env\n• deploy/server.pem\ngit add -A"},
		{"unchecked", security.ApprovalRequest{Command: "git add -A", Description: "d", StageUnchecked: true},
			"🛡 *Approval needed*\n_Flagged: d_\nTether Agent could not check which files this command stages\\.\n```\ngit add -A\n```",
			"🛡 Approval needed\nFlagged: d\nTether Agent could not check which files this command stages.\ngit add -A"},
	} {
		mdv2, plain := buildApprovalPrompt(c.req, 0)
		if mdv2 != c.mdv2 || plain != c.plain {
			t.Errorf("%s:\n got %q\nwant %q\n got %q\nwant %q", c.name, mdv2, c.mdv2, plain, c.plain)
		}
		if err := checkMDV2(mdv2); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
