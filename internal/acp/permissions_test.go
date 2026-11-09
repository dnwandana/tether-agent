package acp

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/security"
)

var claudeOptions = []option{
	{ID: "allow-once", Name: "Allow", Kind: "allow_once"},
	{ID: "allow-with-updates", Name: "Always allow", Kind: "allow_always"},
	{ID: "reject", Name: "Reject", Kind: "reject_once"},
}

func TestPatternID(t *testing.T) {
	const base = "acp:claude-code:s1:"
	for _, tc := range []struct {
		kind, command string
		id            string
		noSession     bool
	}{
		{"execute", "go test ./...", base + "execute:go", false},
		// A path or a leading NAME=value word can change what runs, so the id is not exact.
		{"execute", "/usr/bin/go build ./cmd/tether", base + "execute:go", true},
		{"execute", "./scripts/check.sh -v", base + "execute:check.sh", true},
		{"execute", "FOO=1 BAR=x go vet ./...", base + "execute:go", true},
		{"execute", "./ls", base + "execute:ls", true},
		{"execute", "LD_PRELOAD=./e.so ls", base + "execute:ls", true},
		{"execute", "PATH=/tmp/x:$PATH ls", base + "execute:ls", true},
		{"execute", "FOO=1 go vet", base + "execute:go", true},
		{"execute", "go test && rm -rf /", base + "execute:go", true},
		{"execute", "a || b", base + "execute:a", true},
		{"execute", "a; b", base + "execute:a", true},
		{"execute", "a | b", base + "execute:a", true},
		{"execute", "echo $(id)", base + "execute:echo", true},
		{"execute", "echo `id`", base + "execute:echo", true},
		{"execute", "a\nb", base + "execute:a", true},
		{"execute", "sudo ls", base + "execute:sudo", true},
		{"execute", "env X=1 ls", base + "execute:env", true},
		{"execute", "sh -c ls", base + "execute:sh", true},
		{"execute", "bash x.sh", base + "execute:bash", true},
		{"execute", "zsh x.sh", base + "execute:zsh", true},
		{"execute", "xargs rm", base + "execute:xargs", true},
		{"execute", "nohup ./run", base + "execute:nohup", true},
		{"execute", "timeout 5 make", base + "execute:timeout", true},
		{"execute", "nice make", base + "execute:nice", true},
		{"execute", "", base + "execute", true},
		{"execute", "FOO=1", base + "execute", true},
		{"execute", `"sudo" ls`, base + `execute:"sudo"`, true},
		{"execute", "'sh' x", base + "execute:'sh'", true},
		{"execute", `\bash x`, base + `execute:\bash`, true},
		{"execute", "$SHELL -c x", base + "execute:$SHELL", true},
		{"execute", "ls & rm x", base + "execute:ls", true},
		{"execute", "ls <(id)", base + "execute:ls", true},
		{"execute", "cat x >(tee y)", base + "execute:cat", true},
		{"execute", "cat x > ~/.bashrc", base + "execute:cat", true},
		{"execute", "sort < x", base + "execute:sort", true},
		{"execute", "go test ./... 2>&1", base + "execute:go", true},
		{"execute", "exec rm x", base + "execute:exec", true},
		{"execute", "eval x", base + "execute:eval", true},
		{"execute", "command rm x", base + "execute:command", true},
		{"execute", "time make", base + "execute:time", true},
		{"execute", "setsid make", base + "execute:setsid", true},
		{"execute", "doas ls", base + "execute:doas", true},
		{"execute", "su -c ls", base + "execute:su", true},
		{"execute", "dash -c ls", base + "execute:dash", true},
		{"execute", "ksh x.sh", base + "execute:ksh", true},
		{"execute", "fish -c ls", base + "execute:fish", true},
		{"execute", "csh x.sh", base + "execute:csh", true},
		{"execute", "tcsh x.sh", base + "execute:tcsh", true},
		{"execute", "busybox rm x", base + "execute:busybox", true},
		{"execute", "pkexec rm x", base + "execute:pkexec", true},
		{"execute", "watch rm x", base + "execute:watch", true},
		{"execute", "script -c ls", base + "execute:script", true},
		{"execute", "strace rm x", base + "execute:strace", true},
		{"execute", "taskset 1 rm x", base + "execute:taskset", true},
		{"execute", "chrt 1 rm x", base + "execute:chrt", true},
		{"execute", "go-task build", base + "execute:go-task", false},
		{"execute", "g++ -o x x.cc", base + "execute:g++", false},
		{"", "", base + "other", false},
		{"edit", "", base + "edit", false},
		{"read", "go test", base + "read", false},
		{"switch_mode", "", base + "switch_mode", true},
	} {
		id, noSession := patternID("claude-code", "s1", tc.kind, tc.command)
		if id != tc.id || noSession != tc.noSession {
			t.Errorf("patternID(%q, %q) = %q, %v; want %q, %v", tc.kind, tc.command, id, noSession, tc.id, tc.noSession)
		}
	}
}

func TestPatternIDBindsACPSession(t *testing.T) {
	a, _ := patternID("claude-code", "s1", "execute", "go test")
	b, _ := patternID("claude-code", "s2", "execute", "go test")
	if a == b {
		t.Fatalf("two ACP sessions share the pattern id %q", a)
	}
}

func TestBuildApprovalRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      permissionInput
		desc    string
		command string
		noSess  bool
	}{
		{"execute", permissionInput{Kind: "execute", Title: "Run go test", Command: "go test ./..."},
			"claude-code wants to run a command", "go test ./...", false},
		{"execute no command", permissionInput{Kind: "execute", Title: "Run something"},
			"claude-code wants to run a command", "Run something", true},
		{"edit", permissionInput{Kind: "edit", Title: "Edit client.go", Diffs: []diffItem{{
			Path: "internal/acp/client.go", OldText: "a\nreturn nil\nz", NewText: "a\nreturn c.close(ctx)\nz"}}},
			"claude-code wants to edit a file", "internal/acp/client.go\n- return nil\n+ return c.close(ctx)", false},
		{"edit new file", permissionInput{Kind: "edit", Title: "Write x.go", Diffs: []diffItem{{
			Path: "x.go", NewText: "x\ny"}}},
			"claude-code wants to edit a file", "x.go\n+ x\n+ y", false},
		{"edit two items", permissionInput{Kind: "edit", Title: "Edit", Diffs: []diffItem{
			{Path: "a.go", OldText: "1", NewText: "2"}, {Path: "b.go", OldText: "same\nold", NewText: "same\nnew"}}},
			"claude-code wants to edit a file", "a.go\n- 1\n+ 2\nb.go\n- old\n+ new", false},
		{"edit no diff", permissionInput{Kind: "edit", Title: "Edit a.go"},
			"claude-code wants to edit a file", "Edit a.go", false},
		{"delete", permissionInput{Kind: "delete", Title: "Delete", Locations: []string{"/w/a", "/w/b"}},
			"claude-code wants to delete a file", "/w/a\n/w/b", false},
		{"move", permissionInput{Kind: "move", Title: "Move", Locations: []string{"/w/a", "/w/b"}},
			"claude-code wants to move a file", "/w/a\n/w/b", false},
		{"fetch", permissionInput{Kind: "fetch", Title: "Fetch https://example.com"},
			"claude-code wants to fetch a URL", "Fetch https://example.com", false},
		{"read", permissionInput{Kind: "read", Title: "Read", Locations: []string{"/w/a.go"}},
			"claude-code wants to read files", "/w/a.go", false},
		{"search no locations", permissionInput{Kind: "search", Title: "Search TODO"},
			"claude-code wants to read files", "Search TODO", false},
		{"switch_mode", permissionInput{Kind: "switch_mode", Title: "Ready to code?", Plan: "1. do x\n2. do y"},
			"claude-code wants to leave plan mode and start to code", "1. do x\n2. do y", true},
		{"switch_mode no plan", permissionInput{Kind: "switch_mode", Title: "Ready to code?"},
			"claude-code wants to leave plan mode and start to code", "Ready to code?", true},
		{"think", permissionInput{Kind: "think", Title: "Think"},
			"claude-code wants to use a tool", "Think", false},
		{"empty kind", permissionInput{Title: "Do it"},
			"claude-code wants to use a tool", "Do it", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Options = claudeOptions
			req, allowID, rejectID, ok := buildApproval("claude-code", "s1", in)
			if !ok || allowID != "allow-once" || rejectID != "reject" {
				t.Fatalf("ids = %q, %q, %v", allowID, rejectID, ok)
			}
			wantPattern, _ := patternID("claude-code", "s1", in.Kind, in.Command)
			if req.Description != tc.desc || req.Command != tc.command || req.NoSession != tc.noSess ||
				!req.NoAlways || req.PatternID != wantPattern || req.Summary != security.CleanSummary(in.Title) {
				t.Fatalf("req = %+v", req)
			}
		})
	}
}

func TestBuildApprovalNeverAllowAlways(t *testing.T) {
	in := permissionInput{Kind: "execute", Command: "ls", Options: []option{
		{ID: "allow-with-updates", Kind: "allow_always"},
		{ID: "allow-once", Kind: "allow_once"},
		{ID: "reject", Kind: "reject_once"},
	}}
	_, allowID, _, ok := buildApproval("claude-code", "s1", in)
	if !ok || allowID != "allow-once" {
		t.Fatalf("allowID = %q, ok %v", allowID, ok)
	}
}

func TestBuildApprovalMissingOption(t *testing.T) {
	for name, opts := range map[string][]option{
		"no allow_once":  {{ID: "a", Kind: "allow_always"}, {ID: "r", Kind: "reject_once"}},
		"no reject_once": {{ID: "o", Kind: "allow_once"}, {ID: "r", Kind: "reject_always"}},
		"no options":     nil,
	} {
		if _, allowID, rejectID, ok := buildApproval("claude-code", "s1", permissionInput{Kind: "execute", Options: opts}); ok {
			t.Errorf("%s: ok = true (%q, %q)", name, allowID, rejectID)
		}
	}
}

func TestBuildApprovalCleansHostileTitle(t *testing.T) {
	title := "Edit *file*\n_Flagged: fake_ `x` [l](u)\u202eevil\r\n" + strings.Repeat("A", 4096)
	req, _, _, ok := buildApproval("claude-code", "s1",
		permissionInput{Kind: "edit", Title: title, Options: claudeOptions})
	if !ok {
		t.Fatal("ok = false")
	}
	s := req.Summary
	if s != security.CleanSummary(title) {
		t.Fatalf("Summary is not CleanSummary(title): %q", s)
	}
	if strings.ContainsAny(s, "\n\r\u202e") || utf8.RuneCountInString(s) > 200 {
		t.Fatalf("Summary is not one short clean line: %q", s)
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			t.Fatalf("Summary has the control or format rune %U", r)
		}
	}
	if !strings.HasPrefix(s, "Edit *file* _Flagged: fake_") {
		t.Fatalf("Summary = %q", s)
	}
	if req.Description != "claude-code wants to edit a file" {
		t.Fatalf("the title changed the Flagged line: %q", req.Description)
	}
}
