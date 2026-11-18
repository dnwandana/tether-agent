package security

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeBin returns a new folder with an executable file for each name. A test uses it as the
// PATH of a request.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// grantRow is one row of approval_grants.
type grantRow struct {
	id, patternID, content, workdir, via, programs, preview string
	createdAt                                               int64
}

// readGrants returns the rows of approval_grants in id order.
func readGrants(t *testing.T, g *Gate) []grantRow {
	t.Helper()
	rows, err := g.DB.QueryContext(ctx, `SELECT id, pattern_id, content_sha256, workdir, via, programs,
		preview, created_at FROM approval_grants ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []grantRow
	for rows.Next() {
		var r grantRow
		if err := rows.Scan(&r.id, &r.patternID, &r.content, &r.workdir, &r.via, &r.programs, &r.preview, &r.createdAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// insertGrant adds one approval_grants row and returns its id. content is the content hash.
func insertGrant(t *testing.T, g *Gate, patternID, content, workdir, via, programs string, at int64) string {
	t.Helper()
	id := grantID(patternID, content, workdir, via)
	if _, err := g.DB.ExecContext(ctx, `INSERT INTO approval_grants(id, pattern_id, content_sha256, workdir,
		via, programs, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, patternID, content, workdir, via, programs, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAlwaysBindsContentWorkdirAndPrograms(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd, other := fakeBin(t, "rm"), t.TempDir(), t.TempDir()
	p := &countingPrompter{answer: ScopeAlways}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s1", Command: "  rm -rf build/\n", Workdir: wd, Path: bin}, p); err != nil ||
		p.n != 1 || p.last.NoAlways {
		t.Fatal(err, p.n, p.last.NoAlways)
	}
	content := fmt.Sprintf("%x", sha256.Sum256([]byte("rm -rf build/")))
	if commandHash("  rm -rf build/\n") != content {
		t.Fatal("the hash input is the command with only the outer white space trimmed")
	}
	wantID := fmt.Sprintf("%x", sha256.Sum256([]byte("recursive_delete\x00"+content+"\x00"+wd+"\x00")))
	if grantID("recursive_delete", content, wd, "") != wantID {
		t.Fatal("the id is the SHA-256 of the four values joined with \\0")
	}
	rows := readGrants(t, g)
	if len(rows) != 1 || rows[0].createdAt == 0 {
		t.Fatalf("%+v", rows)
	}
	rows[0].createdAt = 0
	want := grantRow{id: wantID, patternID: "recursive_delete", content: content, workdir: wd,
		programs: `["` + RealPath(filepath.Join(bin, "rm")) + `"]`, preview: "rm -rf build/"}
	if rows[0] != want {
		t.Fatalf("got %+v, want %+v", rows[0], want)
	}

	// Another gate on the same state.db has no session grant. It reads only the row.
	g2 := &Gate{Mode: "manual", CronMode: "deny", Timeout: 5 * time.Second, DB: g.DB}
	q := &countingPrompter{answer: ScopeDeny}
	if err := g2.Check(ctx, ApprovalRequest{SessionID: "s2", Command: "rm -rf build/", Workdir: wd, Path: bin}, q); err != nil || q.n != 0 {
		t.Fatal("the same bytes in the same workdir asked again", err, q.n)
	}
	for i, req := range []ApprovalRequest{
		{SessionID: "s2", Command: "rm -rf dist/", Workdir: wd, Path: bin},     // other bytes
		{SessionID: "s2", Command: "rm -rf build/", Workdir: other, Path: bin}, // other workdir
	} {
		if err := g2.Check(ctx, req, q); err == nil || q.n != i+1 {
			t.Fatal("the row covered another request", req, err, q.n)
		}
	}
	// A changed program path asks.
	real := filepath.Join(t.TempDir(), "rm-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(bin, "rm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(bin, "rm")); err != nil {
		t.Fatal(err)
	}
	if err := g2.Check(ctx, ApprovalRequest{SessionID: "s2", Command: "rm -rf build/", Workdir: wd, Path: bin}, q); err == nil || q.n != 3 {
		t.Fatal("a changed program path did not ask", err, q.n)
	}
	// A new Always tap updates the row in place.
	if err := g2.Check(ctx, ApprovalRequest{SessionID: "s3", Command: "rm -rf build/", Workdir: wd, Path: bin},
		&countingPrompter{answer: ScopeAlways}); err != nil {
		t.Fatal(err)
	}
	if rows := readGrants(t, g); len(rows) != 1 || rows[0].programs != `["`+RealPath(real)+`"]` {
		t.Fatalf("%+v", rows)
	}
}

func TestAlwaysNotOffered(t *testing.T) {
	g := newGate(t, "manual", "deny")
	bin, wd := fakeBin(t, "rm"), t.TempDir()
	for name, req := range map[string]ApprovalRequest{
		"no workdir":        {SessionID: "s", Command: "rm -rf build/", Path: bin},
		"program not found": {SessionID: "s", Command: "rm -rf build/", Workdir: wd, Path: t.TempDir()},
		"could not read":    {SessionID: "s", Command: "rm -rf build/; echo \"x", Workdir: wd, Path: bin},
		"member":            {SessionID: "s", Command: "rm -rf build/", Workdir: wd, Path: bin, Member: true},
	} {
		p := &countingPrompter{answer: ScopeAlways}
		if err := g.Check(ctx, req, p); err != nil || p.n != 1 || !p.last.NoAlways {
			t.Fatal(name, err, p.n, p.last.NoAlways)
		}
		g.ResetSession("s")
	}
	if rows := readGrants(t, g); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestGrantPreview(t *testing.T) {
	g := &Gate{}
	if got := g.grantPreview("rm -rf build/ &&\necho done"); got != "rm -rf build/ && echo done" {
		t.Fatalf("%q", got)
	}
	if got := g.grantPreview("rm -rf " + strings.Repeat("a", 100)); utf8.RuneCountInString(got) != 60 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
	if got := g.grantPreview("rm -rf x\u202e"); got != "rm -rf x<U+202E>" {
		t.Fatalf("%q", got)
	}
}

func TestRemovedAlwaysCount(t *testing.T) {
	g := newGate(t, "manual", "deny")
	if n, err := g.RemovedAlwaysCount(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := g.DB.KVSet(ctx, "approvals_always_removed", "3"); err != nil {
		t.Fatal(err)
	}
	if n, err := g.RemovedAlwaysCount(ctx); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if err := g.ClearRemovedAlways(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := g.DB.KVGet(ctx, "approvals_always_removed"); err != nil || ok {
		t.Fatal("the key is still there", ok, err)
	}
	if n, err := g.RemovedAlwaysCount(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := g.DB.KVSet(ctx, "approvals_always_removed", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RemovedAlwaysCount(ctx); err == nil {
		t.Fatal("a value that is not a number must give an error")
	}
	// A gate with no state.db has no notice.
	if n, err := (&Gate{}).RemovedAlwaysCount(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := (&Gate{}).ClearRemovedAlways(ctx); err != nil {
		t.Fatal(err)
	}
}
