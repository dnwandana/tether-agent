package security

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSessionGrantKeyHasVia(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeSession}
	for _, tc := range []struct {
		cmd, key, via string
		n             int
	}{
		{"rm -rf build/", "recursive_delete", "", 1},
		{"timeout 60 sh -c 'rm -rf dist/'", "recursive_delete", "", 1}, // transparent wrappers: same key
		{"sudo rm -rf build/", "recursive_delete via sudo", "sudo", 2},
		{"sudo rm -rf dist/", "recursive_delete via sudo", "sudo", 2},
		{"ssh prod 'rm -rf build/'", "recursive_delete via ssh prod", "ssh prod", 3},
		{"docker exec web sh -c 'rm -rf /data'", "delete_in_root_path via docker exec web", "docker exec web", 4},
		// One Via value for the full command.
		{"sudo apt update && rm -rf build/", "recursive_delete via sudo", "sudo", 4},
	} {
		if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: tc.cmd}, p); err != nil || p.n != tc.n {
			t.Fatal(tc.cmd, err, p.n)
		}
		// Check the key and the wrapper text of each command that asked.
		if p.last.Command == tc.cmd && (p.last.GrantKey != tc.key || p.last.Via != tc.via) {
			t.Fatalf("%q: key %q via %q, want %q via %q", tc.cmd, p.last.GrantKey, p.last.Via, tc.key, tc.via)
		}
	}
}

func TestScriptSessionGrantIsBoundToBytes(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeSession}
	code := "print(1)"
	if err := g.CheckScript(ctx, scriptReq("s", code), p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
	if want := fmt.Sprintf("execute_code:%x", sha256.Sum256([]byte(code))); p.last.GrantKey != want {
		t.Fatal(p.last.GrantKey, want)
	}
	// The same bytes run again with no question.
	if err := g.CheckScript(ctx, scriptReq("s", code), p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
	// The hash trims nothing. Other bytes ask, also a harmless change.
	for i, other := range []string{"print(1)\n", "import shutil\nshutil.rmtree('/home/u/x')"} {
		if err := g.CheckScript(ctx, scriptReq("s", other), p); err != nil || p.n != i+2 {
			t.Fatal(other, err, p.n)
		}
	}
	// A script grant does not cover a terminal command.
	p.answer = ScopeOnce
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf build"}, p); err != nil || p.n != 4 ||
		p.last.PatternID != "recursive_delete" {
		t.Fatal(err, p.n, p.last.PatternID)
	}
}

func TestCredentialGivesOnlyOnce(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Redactor = NewRedactor(nil)
	bin, wd := fakeBin(t, "mysql"), t.TempDir()
	p := &countingPrompter{answer: ScopeSession}
	cred := `MYSQL_PASSWORD=hunter2hunter2 mysql -e "DROP TABLE users"`
	for want := 1; want <= 2; want++ {
		if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: cred, Workdir: wd, Path: bin}, p); err != nil || p.n != want {
			t.Fatal("a credential command must ask each time", err, p.n)
		}
		if !p.last.NoSession || !p.last.NoAlways {
			t.Fatalf("%+v", p.last)
		}
	}
	// A masked value that is only a shell expansion is not a credential.
	g.ResetSession("s")
	exp := `GH_TOKEN=$GH_TOKEN mysql -e "DROP TABLE users"`
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s2", Command: exp, Workdir: wd, Path: bin}, p); err != nil || p.last.NoSession {
		t.Fatalf("%v %+v", err, p.last)
	}
	// A nil redactor masks nothing.
	g.Redactor = nil
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s3", Command: cred, Workdir: wd, Path: bin}, p); err != nil || p.last.NoSession {
		t.Fatalf("%v %+v", err, p.last)
	}
}

func TestQuotedExpansionIsNotCredential(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Redactor = NewRedactor(nil)
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{`gh api --token "$GH_TOKEN" /user`, false},
		{`gh api --token $GH_TOKEN /user`, false},
		{`mysql --password="${DB_PASS}"`, false},
		{`tool --token=$T`, false},
		{`curl https://u:"$P"@h/x`, false},
		{`curl -H 'Authorization: Basic $B' https://h`, false},
		{`echo "pass --token to the tool"`, false},
		{`echo 'use --password now'`, false},
		{`sh -c 'mysql --password x'`, true},
		{`ssh h "tool --token abc"`, true},
		{`gh api --token abc123tok /user`, true},
		{`mysql --password "two words"`, true},
	} {
		if got := g.hasCredential(tc.cmd); got != tc.want {
			t.Errorf("%q: hasCredential = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestRuntimeTextGetsNoAlways(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd := fakeBin(t, "rm"), t.TempDir()
	p := &countingPrompter{answer: ScopeAlways}
	req := ApprovalRequest{SessionID: "s", Command: `rm -rf "$BUILD_DIR"`, Workdir: wd, Path: bin}
	if err := g.Check(ctx, req, p); err != nil || p.n != 1 || !p.last.NoAlways || p.last.NoSession {
		t.Fatalf("%v %d %+v", err, p.n, p.last)
	}
	// The Always tap acts as a Session tap.
	if err := g.Check(ctx, req, p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
	if rows := readGrants(t, g); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestBrokenProgramsTurnsOffAllGrants(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd := fakeBin(t, "rm", "chmod"), t.TempDir()
	g.approveSession("s", "world_other_writable_permissions")
	insertGrant(t, g, "recursive_delete", commandHash("rm -rf build/"), wd, "", "not json", 1)
	p := &countingPrompter{answer: ScopeAlways}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf build/", Workdir: wd, Path: bin}, p); err != nil || p.n != 1 {
		t.Fatal(err, p.n)
	}
	if !p.last.NoSession || !p.last.NoAlways {
		t.Fatalf("%+v", p.last)
	}
	if off, cause := g.GrantsOff(); !off || !strings.Contains(cause, "programs") {
		t.Fatal(off, cause)
	}
	// The session grant of another rule no longer applies.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "chmod 777 x", Workdir: wd, Path: bin}, p); err != nil ||
		p.n != 2 || !p.last.NoSession || !p.last.NoAlways {
		t.Fatalf("%v %d %+v", err, p.n, p.last)
	}
	if rows := readGrants(t, g); len(rows) != 1 {
		t.Fatalf("the Always tap saved a row: %+v", rows)
	}
}

func TestMissingTableTurnsOffGrants(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd := fakeBin(t, "rm"), t.TempDir()
	if _, err := g.DB.ExecContext(ctx, "DROP TABLE approval_grants"); err != nil {
		t.Fatal(err)
	}
	p := &countingPrompter{answer: ScopeOnce}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf build/", Workdir: wd, Path: bin}, p); err != nil {
		t.Fatal(err)
	}
	if off, cause := g.GrantsOff(); !off || !strings.Contains(cause, "no such table") {
		t.Fatal(off, cause)
	}
}

func TestListAlwaysTurnsOffGrantsOnBrokenRow(t *testing.T) {
	g := newGate(t, "manual", "deny")
	insertGrant(t, g, "recursive_delete", "a", "/w", "", "not json", 1)
	ok := insertGrant(t, g, "git_force_push", "b", "/w", "", "[]", 2)
	rows, err := g.ListAlways(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != ok {
		t.Fatal(rows, err)
	}
	if off, _ := g.GrantsOff(); !off {
		t.Fatal("a broken row did not set the latch")
	}
}

func TestStopAndBusyKeepGrantsOn(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd := fakeBin(t, "rm"), t.TempDir()
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if err := g.Check(stopped, ApprovalRequest{SessionID: "s", Command: "rm -rf build/", Workdir: wd, Path: bin},
		&countingPrompter{answer: ScopeOnce}); err == nil {
		t.Fatal("a stopped check ran the command")
	}
	if off, cause := g.GrantsOff(); off {
		t.Fatal("a /stop set the latch:", cause)
	}
	for _, err := range []error{
		context.Canceled,
		fmt.Errorf("query: %w", context.DeadlineExceeded),
		errors.New("database is locked (5) (SQLITE_BUSY)"),
	} {
		if grantStoreDamaged(err) {
			t.Fatal("not damage:", err)
		}
	}
	for _, err := range []error{
		errors.New("SQL logic error: no such table: approval_grants (1)"),
		errors.New("SQL logic error: no such column: via (1)"),
		errors.New(`sql: Scan error on column index 0, name "programs": converting NULL to string is unsupported`),
	} {
		if !grantStoreDamaged(err) {
			t.Fatal("damage:", err)
		}
	}
}

func TestRemoveAlwaysDeletesKeyFamily(t *testing.T) {
	g := newGate(t, "manual", "deny")
	keys := []string{
		"recursive_delete", "recursive_delete via sudo", "recursive_delete_long_flag",
		"mcp:gh.create_issue:0123456789abcdef", "mcp:gh.create_issue_comment:0123456789abcdef",
	}
	for _, k := range keys {
		g.approveSession("s", k)
	}
	id := insertGrant(t, g, "recursive_delete", commandHash("rm -rf build/"), "/w", "", "[]", 1)
	mcp := insertGrant(t, g, "mcp:gh.create_issue", "0123456789abcdef", "", "", "[]", 2)
	left := func(want ...string) {
		t.Helper()
		var got []string
		for _, k := range keys {
			if g.sessionApproved("s", k) {
				got = append(got, k)
			}
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if err := g.RemoveAlways(ctx, id); err != nil {
		t.Fatal(err)
	}
	left("recursive_delete_long_flag", "mcp:gh.create_issue:0123456789abcdef", "mcp:gh.create_issue_comment:0123456789abcdef")
	if err := g.RemoveAlways(ctx, mcp); err != nil {
		t.Fatal(err)
	}
	left("recursive_delete_long_flag", "mcp:gh.create_issue_comment:0123456789abcdef")
}
