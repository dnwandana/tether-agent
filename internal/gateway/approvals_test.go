package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// approvalsGate returns a gate with a new state.db that has one Always row for each id.
// Row i has the created_at value 1000*(i+1).
func approvalsGate(t *testing.T, ids ...string) *security.Gate {
	t.Helper()
	d := openTestDB(t)
	for i, id := range ids {
		if _, err := d.ExecContext(context.Background(),
			"INSERT INTO approvals_always(pattern_id, created_at) VALUES (?, ?)", id, 1000*int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	return &security.Gate{DB: d}
}

// wantCode returns the first 8 hex characters of the SHA-256 hash of id.
func wantCode(id string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(id)))[:8] }

// wantLine returns the list line of row n.
func wantLine(n int, desc, id string, at int64, now time.Time) string {
	return fmt.Sprintf("%d. %s · %s · `%s`", n, desc, formatDate(time.UnixMilli(at), now), wantCode(id))
}

func TestApprovalsListTelegram(t *testing.T) {
	now := time.Now()
	setNow(t, now)
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete", "old_rule_x", "protected_path_write_x")))
	c, sent := interactiveCtx(t)
	run(t, c, "/approvals")
	r := lastSent(t, sent)
	want := "🛡 **Always answers**\n" +
		wantLine(1, "recursive delete", "recursive_delete", 1000, now) + "\n" +
		wantLine(2, `old\_rule\_x`, "old_rule_x", 2000, now)
	wantB := [][]Button{{
		{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode("recursive_delete")},
		{Text: "Remove 2", Data: "/approvals remove 2 " + wantCode("old_rule_x")},
	}}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) {
		t.Fatalf("got:\n%s\n%+v", r.Text, r.Buttons)
	}
}

func TestApprovalsListCLI(t *testing.T) {
	now := time.Now()
	setNow(t, now)
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete")))
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/approvals")
	want := "🛡 **Always answers**\n" + wantLine(1, "recursive delete", "recursive_delete", 1000, now) +
		"\n\nSend /approvals remove <number> <code> to remove an answer.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestApprovalsEmpty(t *testing.T) {
	const empty = "No Always answers. The gate asks for each dangerous command."
	for _, gate := range []*security.Gate{approvalsGate(t), approvalsGate(t, "protected_path_write_x"), nil} {
		registerTestCommand(t, ApprovalsCommand(gate))
		c, sent := interactiveCtx(t)
		run(t, c, "/approvals")
		if r := lastSent(t, sent); r.Text != empty || r.Buttons != nil {
			t.Fatalf("%q %+v", r.Text, r.Buttons)
		}
	}
}

func TestApprovalsRemove(t *testing.T) {
	ctx := context.Background()
	gate := approvalsGate(t, "recursive_delete", "git_force_push")
	registerTestCommand(t, ApprovalsCommand(gate))
	c, sent := interactiveCtx(t)
	run(t, c, "/approvals remove 2 "+wantCode("git_force_push"))
	if r := lastSent(t, sent); r.Text != "✅ Removed the Always answer for git force push (rewrites remote history). The gate asks again." {
		t.Fatal(r.Text)
	}
	// A stale or bad action removes nothing.
	for _, args := range []string{
		"remove 1 " + wantCode("git_force_push"), // row 1 is now recursive_delete
		"remove 2 " + wantCode("git_force_push"), // out of range
		"remove 0 " + wantCode("recursive_delete"),
		"remove x " + wantCode("recursive_delete"), // not a number
		"remove 1",
		"remove",
	} {
		run(t, c, "/approvals "+args)
		if r := lastSent(t, sent); r.Text != "The list changed. Send /approvals to see it again." {
			t.Fatalf("%s: %q", args, r.Text)
		}
	}
	rows, err := gate.ListAlways(ctx)
	if err != nil || len(rows) != 1 || rows[0].PatternID != "recursive_delete" {
		t.Fatal(rows, err)
	}
	// A nil gate gives the stale reply too.
	registerTestCommand(t, ApprovalsCommand(nil))
	run(t, c, "/approvals remove 1 "+wantCode("recursive_delete"))
	if r := lastSent(t, sent); r.Text != "The list changed. Send /approvals to see it again." {
		t.Fatal(r.Text)
	}
}

func TestApprovalsOwnerOnlyAndWhileBusy(t *testing.T) {
	cmd := ApprovalsCommand(nil)
	if cmd.Name != "approvals" || cmd.MemberOK || cmd.MemberOwnSession || !cmd.WhileBusy ||
		cmd.Help != "See and remove the Always answers: /approvals [remove <number> <code>]" {
		t.Fatalf("%+v", cmd)
	}
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete")))
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	r := &recRenderer{}
	_ = g.Handle(context.Background(), member("k", "/approvals"), func() Renderer { return r })
	if len(r.acks) != 1 || r.acks[0] != "⛔ Only the owner can use /approvals." {
		t.Fatalf("%q", r.acks)
	}
}

func TestApprovalsButtonLimits(t *testing.T) {
	var ids []string
	for _, p := range security.DangerousPatterns[:21] {
		ids = append(ids, p.ID)
	}
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, ids...)))
	c, sent := interactiveCtx(t)
	run(t, c, "/approvals")
	r := lastSent(t, sent)
	n := 0
	for _, row := range r.Buttons {
		n += len(row)
	}
	if n != 20 || strings.Count(r.Text, "\n") != 21 {
		t.Fatalf("%d buttons, text:\n%s", n, r.Text)
	}
	// The action of the longest rule ID fits the callback data.
	longest := ""
	for _, p := range append(slices.Clone(security.DangerousPatterns), security.TetherDangerousPatterns...) {
		if len(p.ID) > len(longest) {
			longest = p.ID
		}
	}
	action := "/approvals remove 20 " + wantCode(longest)
	if len(action) > 30 || len(action) > modelActionMaxBytes {
		t.Fatal(len(action), action)
	}
}
