package telegram

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// screenshotCommand is the command of the user screenshot of an allowed approval.
const screenshotCommand = `R=~/.tether/cache/scratch/crypto-gainers/sol-batch-20261031
mkdir -p $R/gainers
python3 -c 'import sys;AB="123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
[print(len(a),a,[c for c in a if c not in AB]) for a in sys.argv[1:]]' \
HvitmAeh6TkToQonoiNwqWcrZsYdYiaSZHP7yMmspump \
5THtNEGTF1y67eRWvcgz3MomDZePCVx1xdskHHeCpump`

// punctScript returns a script of exactly n runes with many MarkdownV2 special
// characters, backslashes, and backticks.
func punctScript(n int) string {
	const line = "echo `id` \\ `pwd` \\ *_[]()~>#+-=|{}.! ok\n"
	s := strings.Repeat(line, n/len([]rune(line))+1)
	return string([]rune(s)[:n])
}

// visibleQuote returns the text that Telegram shows in the expandable quote of s:
// the quote lines without the quote marks, without the final "||", and without
// the escapes.
func visibleQuote(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "\n**>")
	if i < 0 {
		t.Fatalf("no expandable quote in %q", s)
	}
	lines := strings.Split(s[i+1:], "\n")
	for k, l := range lines {
		if k == 0 {
			l = strings.TrimPrefix(l, "**")
		}
		if !strings.HasPrefix(l, ">") {
			t.Fatalf("line %d is not a quote line: %q", k, l)
		}
		l = l[1:]
		if k == len(lines)-1 {
			if !strings.HasSuffix(l, "||") {
				t.Fatalf("the quote does not end with ||: %q", l)
			}
			l = l[:len(l)-2]
		}
		lines[k] = reUnescape.ReplaceAllString(l, "$1")
	}
	return strings.Join(lines, "\n")
}

func TestHeaderFor(t *testing.T) {
	for _, c := range []struct {
		scope                 security.Scope
		timeout               time.Duration
		mdv2, plain, markdown string
	}{
		{security.ScopeOnce, 0, "✅ *Approval allowed* · once", "✅ Approval allowed · once", "✅ **Approval allowed** · once"},
		{security.ScopeSession, 0, "✅ *Approval allowed* · this session", "✅ Approval allowed · this session", "✅ **Approval allowed** · this session"},
		{security.ScopeAlways, 0, "✅ *Approval allowed* · always", "✅ Approval allowed · always", "✅ **Approval allowed** · always"},
		{security.ScopeDeny, 10 * time.Minute, "❌ *Approval denied*", "❌ Approval denied", "❌ **Approval denied**"},
		{security.ScopeTimeout, 10 * time.Minute, "⌛ *Approval denied* · no answer in 10 min", "⌛ Approval denied · no answer in 10 min", "⌛ **Approval denied** · no answer in 10 min"},
		{security.ScopeTimeout, 45 * time.Second, "⌛ *Approval denied* · no answer in 45 s", "⌛ Approval denied · no answer in 45 s", "⌛ **Approval denied** · no answer in 45 s"},
		{security.ScopeTimeout, 0, "⌛ *Approval denied* · no answer", "⌛ Approval denied · no answer", "⌛ **Approval denied** · no answer"},
	} {
		h := headerFor(c.scope, c.timeout)
		if h.mdv2() != c.mdv2 || h.plain() != c.plain || h.markdown() != c.markdown {
			t.Errorf("headerFor(%v, %v) = %q, %q, %q", c.scope, c.timeout, h.mdv2(), h.plain(), h.markdown())
		}
	}
}

func TestBuildApprovalPrompt(t *testing.T) {
	const timeoutLine = "\n_No answer in 10 min \\= deny\\._"
	for _, c := range []struct {
		name        string
		req         security.ApprovalRequest
		timeout     time.Duration
		mdv2, plain string
	}{
		{"summary", security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", Summary: "Delete the build folder."}, 10 * time.Minute,
			"🛡 *Approval needed*\nDelete the build folder\\.\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```" + timeoutLine,
			"🛡 Approval needed\nDelete the build folder.\nFlagged: recursive delete\nrm -rf ./build\nNo answer in 10 min = deny."},
		{"no summary", security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"}, 10 * time.Minute,
			"🛡 *Approval needed*\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```" + timeoutLine,
			"🛡 Approval needed\nFlagged: recursive delete\nrm -rf ./build\nNo answer in 10 min = deny."},
		{"no description", security.ApprovalRequest{Command: "rm -rf ./build"}, 10 * time.Minute,
			"🛡 *Approval needed*\n_Flagged: dangerous command_\n```\nrm -rf ./build\n```" + timeoutLine,
			"🛡 Approval needed\nFlagged: dangerous command\nrm -rf ./build\nNo answer in 10 min = deny."},
		{"subagent", security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", ChildName: "2/5"}, 10 * time.Minute,
			"🛡 *Approval needed*\n_Flagged: recursive delete_ \\(from subagent 2/5\\)\n```\nrm -rf ./build\n```" + timeoutLine,
			"🛡 Approval needed\nFlagged: recursive delete (from subagent 2/5)\nrm -rf ./build\nNo answer in 10 min = deny."},
		{"no timeout", security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"}, 0,
			"🛡 *Approval needed*\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```",
			"🛡 Approval needed\nFlagged: recursive delete\nrm -rf ./build"},
		{"pre escapes", security.ApprovalRequest{Command: "echo `date` \\ *x*", Description: "d"}, 0,
			"🛡 *Approval needed*\n_Flagged: d_\n```\necho \\`date\\` \\\\ *x*\n```",
			"🛡 Approval needed\nFlagged: d\necho `date` \\ *x*"},
	} {
		mdv2, plain := buildApprovalPrompt(c.req, c.timeout)
		if mdv2 != c.mdv2 || plain != c.plain {
			t.Errorf("%s:\n got %q\nwant %q\n got %q\nwant %q", c.name, mdv2, c.mdv2, plain, c.plain)
		}
		if err := checkMDV2(mdv2); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestBuildApprovalResult(t *testing.T) {
	h := headerFor(security.ScopeSession, 0)
	for _, c := range []struct {
		name               string
		req                security.ApprovalRequest
		full, short, plain string
	}{
		{"summary", security.ApprovalRequest{Command: "rm -rf ./build\nls", Description: "recursive delete", Summary: "Delete the build folder."},
			"✅ *Approval allowed* · this session\nDelete the build folder\\.\n**>rm \\-rf \\./build\n>ls||",
			"✅ *Approval allowed* · this session\nDelete the build folder\\.",
			"✅ Approval allowed · this session\nDelete the build folder."},
		{"fallback, one line", security.ApprovalRequest{Command: "rm -rf /", Description: "recursive delete of root filesystem"},
			"✅ *Approval allowed* · this session\nrecursive delete of root filesystem: `rm -rf /`\n**>rm \\-rf /||",
			"✅ *Approval allowed* · this session\nrecursive delete of root filesystem: `rm -rf /`",
			"✅ Approval allowed · this session\nrecursive delete of root filesystem: rm -rf /"},
		{"fallback, matched line in the middle", security.ApprovalRequest{Command: "cd /x\nrm -rf /\necho done", Description: "recursive delete of root filesystem"},
			"✅ *Approval allowed* · this session\nrecursive delete of root filesystem: `rm -rf /` \\(\\+2 lines\\)\n**>cd /x\n>rm \\-rf /\n>echo done||",
			"✅ *Approval allowed* · this session\nrecursive delete of root filesystem: `rm -rf /` \\(\\+2 lines\\)",
			"✅ Approval allowed · this session\nrecursive delete of root filesystem: rm -rf / (+2 lines)"},
		{"fallback, code escapes", security.ApprovalRequest{Command: "echo `id` \\", Description: "d_x"},
			"✅ *Approval allowed* · this session\nd\\_x: `echo \\`id\\` \\\\`\n**>echo \\`id\\` \\\\||",
			"✅ *Approval allowed* · this session\nd\\_x: `echo \\`id\\` \\\\`",
			"✅ Approval allowed · this session\nd_x: echo `id` \\"},
		{"empty line", security.ApprovalRequest{Command: "a\n\nb", Summary: "S"},
			"✅ *Approval allowed* · this session\nS\n**>a\n>\n>b||",
			"✅ *Approval allowed* · this session\nS",
			"✅ Approval allowed · this session\nS"},
	} {
		full, short, plain := buildApprovalResult(h, c.req)
		if full != c.full || short != c.short || plain != c.plain {
			t.Errorf("%s:\n got %q\nwant %q\n got %q\nwant %q\n got %q\nwant %q", c.name, full, c.full, short, c.short, plain, c.plain)
		}
		for _, s := range []string{full, short} {
			if err := checkMDV2(s); err != nil {
				t.Errorf("%s: %v: %q", c.name, err, s)
			}
		}
	}
}

func TestApprovalQuoteShowsCommand(t *testing.T) {
	for _, cmd := range []string{
		screenshotCommand,
		"ls **/*.go",
		"ls *.go *.md",
		"_build_",
		"a || b",
		`grep 'a\|b'`,
		"echo `date`",
		"[a](http://x)",
		"make lint ||",
		"**>x\nls",
		"a\n\nb",
		punctScript(1500),
	} {
		for _, req := range []security.ApprovalRequest{
			{Command: cmd, Description: "d", Summary: "Do the thing."},
			{Command: cmd, Description: "d"},
		} {
			full, _, _ := buildApprovalResult(headerFor(security.ScopeOnce, 0), req)
			if got := visibleQuote(t, full); got != cmd {
				t.Errorf("the quote shows %q, want %q", got, cmd)
			}
			if err := checkMDV2(full); err != nil {
				t.Errorf("%q: %v", cmd, err)
			}
		}
	}
}

func TestApprovalResultBudget(t *testing.T) {
	script := punctScript(3800)
	lines := strings.Split(script, "\n")
	full, short, plain := buildApprovalResult(headerFor(security.ScopeTimeout, 10*time.Minute), security.ApprovalRequest{Command: script, Description: "d", Summary: "Run the script."})
	if n := UTF16Len(full); n > maxMessageLen {
		t.Fatalf("got %d units", n)
	}
	if !strings.HasSuffix(full, "||") {
		t.Fatalf("the quote must end with ||: %q", tail(full, 40))
	}
	if err := checkMDV2(full); err != nil {
		t.Fatal(err)
	}
	shown := strings.Split(visibleQuote(t, full), "\n")
	k := len(shown) - 1
	if k < 1 || shown[k] != fmt.Sprintf("… (+%d lines)", len(lines)-k) {
		t.Fatalf("the last quote line is %q", shown[k])
	}
	if strings.Join(shown[:k], "\n") != strings.Join(lines[:k], "\n") {
		t.Fatal("the quote must show the first lines of the script")
	}
	if !strings.HasPrefix(full, short+"\n**>") || plain != "⌛ Approval denied · no answer in 10 min\nRun the script." {
		t.Fatalf("%q", plain)
	}
	// One long line with no line break is cut at a rune, with "…".
	one := strings.ReplaceAll(script, "\n", " ")
	full, _, _ = buildApprovalResult(headerFor(security.ScopeOnce, 0), security.ApprovalRequest{Command: one, Description: "d"})
	if n := UTF16Len(full); n > maxMessageLen || !strings.HasSuffix(full, "||") || checkMDV2(full) != nil {
		t.Fatalf("got %d units: %q", n, tail(full, 40))
	}
	got := visibleQuote(t, full)
	if !strings.HasSuffix(got, "…") || !strings.HasPrefix(one, strings.TrimSuffix(got, "…")) {
		t.Fatalf("%q", tail(got, 40))
	}
}

func TestApprovalPromptBudget(t *testing.T) {
	script := punctScript(3800)
	mdv2, plain := buildApprovalPrompt(security.ApprovalRequest{Command: script, Description: "d", Summary: "Run the script."}, 10*time.Minute)
	if n := UTF16Len(mdv2); n > maxMessageLen {
		t.Fatalf("got %d units", n)
	}
	if !strings.HasSuffix(mdv2, "\n```\n_No answer in 10 min \\= deny\\._") {
		t.Fatalf("the timeout line must stay: %q", tail(mdv2, 60))
	}
	if !strings.Contains(mdv2, "\n… (+") {
		t.Fatal("the command must be cut at a line, with a line count")
	}
	if err := checkMDV2(mdv2); err != nil {
		t.Fatal(err)
	}
	if n := UTF16Len(plain); n > maxMessageLen || !strings.HasSuffix(plain, "\nNo answer in 10 min = deny.") {
		t.Fatalf("got %d units: %q", n, tail(plain, 60))
	}
}

func TestApprovalSummaryStaysLiteral(t *testing.T) {
	for _, sum := range []string{
		"*Bold* _it_ `code` [a](b) ||x|| ~s~",
		"> starts like a quote",
		"# starts like a heading",
		"**>starts like an expandable quote",
	} {
		req := security.ApprovalRequest{Command: "rm -rf ./build", Description: "d", Summary: sum}
		prompt, _ := buildApprovalPrompt(req, 0)
		full, short, _ := buildApprovalResult(headerFor(security.ScopeOnce, 0), req)
		for _, s := range []string{prompt, full, short} {
			line := strings.Split(s, "\n")[1]
			if line != escapeMDV2(sum) || reUnescape.ReplaceAllString(line, "$1") != sum {
				t.Errorf("line 2 is %q, want the literal summary %q", line, sum)
			}
			if err := checkMDV2(s); err != nil {
				t.Errorf("%q: %v", sum, err)
			}
		}
		if strings.Count(full, "\n**>") != 1 || visibleQuote(t, full) != "rm -rf ./build" {
			t.Errorf("%q: the summary changed the quote: %q", sum, full)
		}
	}
}

// A bidi or zero-width rune in the command must not change how the command looks, so the
// approval messages show each one as a visible mark.
func TestApprovalShowsHiddenRunesInCommand(t *testing.T) {
	req := security.ApprovalRequest{Command: "rm -rf /tmp/\u202ex\u200b", Description: "delete in root path"}
	mdv2, plain := buildApprovalPrompt(req, 0)
	full, short, rplain := buildApprovalResult(headerFor(security.ScopeOnce, 0), req)
	for name, s := range map[string]string{"mdv2": mdv2, "plain": plain, "full": full, "short": short, "result plain": rplain} {
		if strings.ContainsAny(s, "\u202e\u200b") {
			t.Errorf("%s has a hidden rune: %q", name, s)
		}
	}
	if !strings.Contains(mdv2, "rm -rf /tmp/<U+202E>x<U+200B>") || !strings.Contains(full, `<U\+202E\>x<U\+200B\>`) {
		t.Fatalf("no visible marks:\n%s\n%s", mdv2, full)
	}
}
