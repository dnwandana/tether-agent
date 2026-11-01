package telegram

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/security"
)

// FuzzApprovalMessages checks the approval prompt and the result for any command and any
// summary: each text fits one message, the MarkdownV2 is valid, the quote ends with "||",
// and no hidden rune reaches the user.
func FuzzApprovalMessages(f *testing.F) {
	for _, seed := range []struct{ command, summary string }{
		{"rm -rf /tmp/x", "Delete the temp folder."},
		{"", ""},
		{"\nls", ""},
		{"ls **/*.go\na || b\ngrep 'a\\|b'\necho `date`\n[a](http://x)\nend||\n**>x", "*bold* _it_ [l](u)"},
		{"rm -rf /tmp/\u202ex\u200b\x1b[2K", "\u202eabc\x1b[31m"},
		{strings.Repeat("!.-_*[]()~`>#+=|{}\\", 250), strings.Repeat("😀", 300)},
		{strings.Repeat("a\n", 3000), ""},
	} {
		f.Add(seed.command, seed.summary)
	}
	headers := []approvalHeader{
		headerFor(security.ScopeOnce, 0),
		headerFor(security.ScopeDeny, 0),
		headerFor(security.ScopeTimeout, 10*time.Minute),
	}
	f.Fuzz(func(t *testing.T, command, summary string) {
		// The tools clean the summary before the gate sees it.
		req := security.ApprovalRequest{Command: command, Description: "delete in root path", Summary: security.CleanSummary(summary)}
		mdv2, plain := buildApprovalPrompt(req, 10*time.Minute)
		texts := map[string]string{"prompt": mdv2, "prompt plain": plain}
		if err := checkMDV2(mdv2); err != nil {
			t.Fatalf("prompt: %v\n%s", err, mdv2)
		}
		for _, h := range headers {
			full, short, rplain := buildApprovalResult(h, req)
			if !strings.HasSuffix(full, "||") {
				t.Fatalf("the quote does not end with ||: %q", full)
			}
			for name, s := range map[string]string{"full": full, "short": short} {
				if err := checkMDV2(s); err != nil {
					t.Fatalf("%s: %v\n%s", name, err, s)
				}
			}
			texts["full "+h.Title] = full
			texts["short "+h.Title] = short
			texts["result plain "+h.Title] = rplain
		}
		for name, s := range texts {
			if n := UTF16Len(s); n > maxMessageLen {
				t.Fatalf("%s has %d UTF-16 units", name, n)
			}
			for _, r := range s {
				if r != '\n' && r != '\t' && unicode.In(r, unicode.Cc, unicode.Cf) {
					t.Fatalf("%s has the hidden rune %U: %q", name, r, s)
				}
			}
		}
	})
}
