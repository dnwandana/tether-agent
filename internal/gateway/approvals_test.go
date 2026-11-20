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
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// approvalsGate returns a gate with a new state.db that has one Always row for each id.
// The first id is the newest row: row i has the created_at value 1000*(len(ids)-i).
func approvalsGate(t *testing.T, ids ...string) *security.Gate {
	t.Helper()
	d := openTestDB(t)
	for i, id := range ids {
		if _, err := d.ExecContext(context.Background(),
			"INSERT INTO approval_grants(id, pattern_id, content_sha256, created_at) VALUES (?, ?, 'x', ?)", id, id, 1000*int64(len(ids)-i)); err != nil {
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

// wantListCode returns the first 8 hex characters of the SHA-256 hash of the ids, joined
// with "\n".
func wantListCode(ids ...string) string { return wantCode(strings.Join(ids, "\n")) }

// wantRow is one row of an expected list. desc is the escaped description, and at is the
// created_at value in milliseconds.
type wantRow struct {
	desc, id string
	at       int64
}

// wantList returns the list text of the rows, numbered from 1.
func wantList(now time.Time, rows ...wantRow) string {
	lines := []string{"🛡 **Always answers**"}
	for i, r := range rows {
		lines = append(lines, wantLine(i+1, r.desc, r.id, r.at, now))
	}
	return strings.Join(lines, "\n")
}

func TestApprovalsListTelegram(t *testing.T) {
	now := time.Now()
	setNow(t, now)
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete", "old_rule_x", "protected_path_write_x")))
	c, sent := interactiveCtx(t)
	run(t, c, "/approvals")
	r := lastSent(t, sent)
	want := wantList(now,
		wantRow{"recursive delete", "recursive_delete", 3000},
		wantRow{`old\_rule\_x`, "old_rule_x", 2000})
	wantB := [][]Button{
		{
			{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode("recursive_delete")},
			{Text: "Remove 2", Data: "/approvals remove 2 " + wantCode("old_rule_x")},
		},
		{{Text: "🗑 Remove all", Data: "/approvals remove all " + wantListCode("recursive_delete", "old_rule_x")}},
	}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}

	// A list of 1 row has no "Remove all" row.
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete")))
	run(t, c, "/approvals")
	r = lastSent(t, sent)
	want = wantList(now, wantRow{"recursive delete", "recursive_delete", 1000})
	wantB = [][]Button{{{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode("recursive_delete")}}}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
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

	// A list of 2 rows adds the "remove all" line with the real list code.
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, "recursive_delete", "git_force_push")))
	c, out = cmdCtx(t, "cli:t")
	run(t, c, "/approvals")
	want = wantList(now,
		wantRow{"recursive delete", "recursive_delete", 2000},
		wantRow{"git force push (rewrites remote history)", "git_force_push", 1000}) +
		"\n\nSend /approvals remove <number> <code> to remove an answer.\n" +
		"Send /approvals remove all " + wantListCode("recursive_delete", "git_force_push") +
		" to remove all answers.\n"
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
		if r := lastSent(t, sent); r.Text != empty || r.Buttons != nil || !r.Edit {
			t.Fatalf("%q %+v edit=%v", r.Text, r.Buttons, r.Edit)
		}
	}
}

func TestApprovalsRemove(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	setNow(t, now)
	const empty = "No Always answers. The gate asks for each dangerous command."
	const stale = "The list changed. Send /approvals to see it again."
	rd := wantRow{"recursive delete", "recursive_delete", 3000}
	gfp := wantRow{"git force push (rewrites remote history)", "git_force_push", 2000}
	old := wantRow{`old\_rule\_x`, "old_rule_x", 1000}
	gate := approvalsGate(t, "recursive_delete", "git_force_push", "old_rule_x")
	registerTestCommand(t, ApprovalsCommand(gate))
	c, sent := interactiveCtx(t)

	// A remove edits the message to the result line and the list that is left, with new
	// numbers and new codes.
	run(t, c, "/approvals remove 2 "+wantCode(gfp.id))
	r := lastSent(t, sent)
	want := "✅ Removed the Always answer for git force push (rewrites remote history). The gate asks again.\n\n" +
		wantList(now, rd, old)
	wantB := [][]Button{
		{
			{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode(rd.id)},
			{Text: "Remove 2", Data: "/approvals remove 2 " + wantCode(old.id)},
		},
		{{Text: "🗑 Remove all", Data: "/approvals remove all " + wantListCode(rd.id, old.id)}},
	}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}

	// A stale action removes nothing and shows the current list. The first line is the
	// gone line when no row has the code, else the stale line.
	for _, tc := range []struct{ args, first string }{
		{"remove 1 " + wantCode(gfp.id), "ℹ️ This answer is already removed. This is the list now."},
		{"remove 2 " + wantCode(gfp.id), "ℹ️ This answer is already removed. This is the list now."},
		{"remove 1 " + wantCode(old.id), "⚠️ The list changed. Nothing was removed. This is the list now."}, // old is row 2 now
		{"remove 3 " + wantCode(old.id), "⚠️ The list changed. Nothing was removed. This is the list now."}, // out of range
		{"remove 0 " + wantCode(rd.id), "⚠️ The list changed. Nothing was removed. This is the list now."},
		{"remove x " + wantCode(rd.id), "⚠️ The list changed. Nothing was removed. This is the list now."}, // not a number
		// A code that is not 8 hex characters is a typo, not a removed row.
		{"remove 1 zzzz", "⚠️ The list changed. Nothing was removed. This is the list now."},
		{"remove 1 " + strings.ToUpper(wantCode(gfp.id)), "⚠️ The list changed. Nothing was removed. This is the list now."},
		{"remove 1 " + wantCode(gfp.id) + "0", "⚠️ The list changed. Nothing was removed. This is the list now."},
	} {
		run(t, c, "/approvals "+tc.args)
		r := lastSent(t, sent)
		if want := tc.first + "\n\n" + wantList(now, rd, old); r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
			t.Fatalf("%s: got:\n%s\n%+v edit=%v", tc.args, r.Text, r.Buttons, r.Edit)
		}
	}

	// A bad word form gives the short stale text with no list.
	for _, args := range []string{"remove 1", "remove", "remove all", "remove all x yes"} {
		run(t, c, "/approvals "+args)
		if r := lastSent(t, sent); r.Text != stale || r.Buttons != nil {
			t.Fatalf("%s: %q %+v", args, r.Text, r.Buttons)
		}
	}
	rows, err := gate.ListAlways(ctx)
	if err != nil || len(rows) != 2 || rows[0].PatternID != rd.id || rows[1].PatternID != old.id {
		t.Fatal(rows, err)
	}

	// A list of 1 row has no "Remove all" row.
	run(t, c, "/approvals remove 1 "+wantCode(rd.id))
	r = lastSent(t, sent)
	want = "✅ Removed the Always answer for recursive delete. The gate asks again.\n\n" + wantList(now, old)
	wantB = [][]Button{{{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode(old.id)}}}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}

	// The last remove gives the result line and the empty text, with no buttons.
	run(t, c, "/approvals remove 1 "+wantCode(old.id))
	r = lastSent(t, sent)
	if want := "✅ Removed the Always answer for old\\_rule\\_x. The gate asks again.\n\n" + empty; r.Text != want || r.Buttons != nil || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}
	if rows, err := gate.ListAlways(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}

	// A nil gate has no rows, so the answer is already removed.
	registerTestCommand(t, ApprovalsCommand(nil))
	run(t, c, "/approvals remove 1 "+wantCode(rd.id))
	r = lastSent(t, sent)
	if want := "ℹ️ This answer is already removed. This is the list now.\n\n" + empty; r.Text != want || r.Buttons != nil || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}
}

func TestApprovalsRemoveTwice(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	setNow(t, now)
	gate := approvalsGate(t, "recursive_delete", "git_force_push")
	registerTestCommand(t, ApprovalsCommand(gate))
	c, sent := interactiveCtx(t)
	left := wantList(now, wantRow{"git force push (rewrites remote history)", "git_force_push", 1000})
	wantB := [][]Button{{{Text: "Remove 1", Data: "/approvals remove 1 " + wantCode("git_force_push")}}}

	// The first tap removes the row.
	run(t, c, "/approvals remove 1 "+wantCode("recursive_delete"))
	r := lastSent(t, sent)
	if want := "✅ Removed the Always answer for recursive delete. The gate asks again.\n\n" + left; r.Text != want ||
		!reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}

	// The second tap of the same action gives the gone line, not the stale line.
	run(t, c, "/approvals remove 1 "+wantCode("recursive_delete"))
	r = lastSent(t, sent)
	if want := "ℹ️ This answer is already removed. This is the list now.\n\n" + left; r.Text != want ||
		!reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}
	rows, err := gate.ListAlways(ctx)
	if err != nil || len(rows) != 1 || rows[0].PatternID != "git_force_push" {
		t.Fatal(rows, err)
	}
}

func TestApprovalsOwnerOnlyAndWhileBusy(t *testing.T) {
	cmd := ApprovalsCommand(nil)
	if cmd.Name != "approvals" || cmd.MemberOK || cmd.MemberOwnSession || !cmd.WhileBusy ||
		cmd.Help != "See and remove the Always answers, or read the decision log: /approvals [log [all] | remove <number> <code> | remove all <list code>]" {
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
	// Count only the Remove N buttons. Each action fits the callback data.
	n := 0
	for _, row := range r.Buttons {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "Remove ") && b.Text != "🗑 Remove all" {
				n++
			}
			if len(b.Data) > ActionMaxBytes {
				t.Fatal(len(b.Data), b.Data)
			}
		}
	}
	if lines := strings.Split(r.Text, "\n"); lines[len(lines)-1] != "and 1 more" {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
	// 20 Remove buttons fill 5 rows. The "Remove all" row is the sixth row, alone.
	all := []Button{{Text: "🗑 Remove all", Data: "/approvals remove all " + wantListCode(ids...)}}
	if n != 20 || len(r.Buttons) != 6 || !reflect.DeepEqual(r.Buttons[5], all) || !r.Edit ||
		strings.Count(r.Text, "\n") != 21 {
		t.Fatalf("%d Remove buttons, %+v edit=%v, text:\n%s", n, r.Buttons, r.Edit, r.Text)
	}
	// The action of the longest rule ID fits the callback data.
	longest := ""
	for _, p := range append(slices.Clone(security.DangerousPatterns), security.TetherDangerousPatterns...) {
		if len(p.ID) > len(longest) {
			longest = p.ID
		}
	}
	action := "/approvals remove 20 " + wantCode(longest)
	if len(action) > ActionMaxBytes {
		t.Fatal(len(action), action)
	}
}

func TestApprovalsRemoveAllTelegram(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	setNow(t, now)
	const (
		again   = "The gate asks again for each dangerous command."
		empty   = "No Always answers. The gate asks for each dangerous command."
		changed = "⚠️ The list changed. Nothing was removed. This is the list now."
		allGone = "ℹ️ All answers are already removed."
		short   = "The list changed. Send /approvals to see it again."
	)
	rd := wantRow{desc: "recursive delete", id: "recursive_delete", at: 2000}
	gf := wantRow{desc: "git force push (rewrites remote history)", id: "git_force_push", at: 1000}
	ox := wantRow{desc: `old\_rule\_x`, id: "old_rule_x", at: 3000}
	// listButtons returns the buttons of a list reply with 2 to 4 rows.
	listButtons := func(rows ...wantRow) [][]Button {
		var remove []Button
		var ids []string
		for i, r := range rows {
			remove = append(remove, Button{Text: fmt.Sprintf("Remove %d", i+1),
				Data: fmt.Sprintf("/approvals remove %d %s", i+1, wantCode(r.id))})
			ids = append(ids, r.id)
		}
		return [][]Button{remove, {{Text: "🗑 Remove all", Data: "/approvals remove all " + wantListCode(ids...)}}}
	}
	count := func(gate *security.Gate, want int) {
		t.Helper()
		if rows, err := gate.ListAlways(ctx); err != nil || len(rows) != want {
			t.Fatalf("%d rows, want %d: %v", len(rows), want, err)
		}
	}
	gate := approvalsGate(t, "recursive_delete", "git_force_push")
	registerTestCommand(t, ApprovalsCommand(gate))
	c, sent := interactiveCtx(t)
	// do runs /approvals with args and checks the text, the buttons, and Edit of the reply.
	do := func(args, wantText string, wantB [][]Button) {
		t.Helper()
		run(t, c, "/approvals "+args)
		r := lastSent(t, sent)
		if r.Text != wantText || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
			t.Fatalf("%s:\ngot:\n%s\n%+v edit=%v", args, r.Text, r.Buttons, r.Edit)
		}
	}
	code := wantListCode("recursive_delete", "git_force_push")

	// The longest button action fits the callback data.
	if a := "/approvals remove all " + code + " " + confirmWord; len(a) != 38 || len(a) > ActionMaxBytes {
		t.Fatal(len(a), a)
	}

	// A bad form gets the short stale text and removes nothing.
	for _, args := range []string{
		"remove all",
		"remove all " + code + " yes",
		"remove all " + code + " confirm extra",
	} {
		run(t, c, "/approvals "+args)
		if r := lastSent(t, sent); r.Text != short || r.Buttons != nil {
			t.Fatalf("%s: %q %+v", args, r.Text, r.Buttons)
		}
	}
	count(gate, 2)

	// The question form asks first. Cancel is alone on row 1.
	do("remove all "+code, "🗑 **Remove all 2 Always answers?**\n"+again, [][]Button{
		{{Text: "Cancel", Data: "/approvals"}},
		{{Text: "🗑 Remove all", Data: "/approvals remove all " + code + " confirm"}},
	})
	count(gate, 2)

	// Cancel sends /approvals. The reply shows the list again.
	do("", wantList(now, rd, gf), listButtons(rd, gf))

	// A stale list code removes nothing and shows the current list, in both forms.
	do("remove all 00000000", changed+"\n\n"+wantList(now, rd, gf), listButtons(rd, gf))
	do("remove all 00000000 confirm", changed+"\n\n"+wantList(now, rd, gf), listButtons(rd, gf))
	count(gate, 2)

	// A list code from before a new row removes nothing.
	if _, err := gate.DB.ExecContext(ctx,
		"INSERT INTO approval_grants(id, pattern_id, content_sha256, created_at) VALUES (?, ?, 'x', ?)", "old_rule_x", "old_rule_x", int64(3000)); err != nil {
		t.Fatal(err)
	}
	do("remove all "+code+" confirm", changed+"\n\n"+wantList(now, ox, rd, gf), listButtons(ox, rd, gf))
	count(gate, 3)

	// The confirm form removes all rows.
	code3 := wantListCode("old_rule_x", "recursive_delete", "git_force_push")
	do("remove all "+code3+" confirm", "✅ Removed 3 Always answers. The gate asks again.\n\n"+empty, nil)
	count(gate, 0)

	// A repeated confirm, and both forms on an empty list, give the all-gone line.
	for _, args := range []string{
		"remove all " + code3 + " confirm",
		"remove all " + code3,
		"remove all " + wantListCode() + " confirm",
	} {
		do(args, allGone+"\n\n"+empty, nil)
	}

	// One row: the question and the result use the singular.
	one := approvalsGate(t, "recursive_delete")
	registerTestCommand(t, ApprovalsCommand(one))
	code1 := wantListCode("recursive_delete")
	do("remove all "+code1, "🗑 **Remove 1 Always answer?**\n"+again, [][]Button{
		{{Text: "Cancel", Data: "/approvals"}},
		{{Text: "🗑 Remove all", Data: "/approvals remove all " + code1 + " confirm"}},
	})
	do("remove all "+code1+" confirm", "✅ Removed 1 Always answer. The gate asks again.\n\n"+empty, nil)
	count(one, 0)

	// A nil gate has no rows.
	registerTestCommand(t, ApprovalsCommand(nil))
	do("remove all "+code1+" confirm", allGone+"\n\n"+empty, nil)
}

func TestApprovalsRemoveAllCLI(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	setNow(t, now)
	const question = "Remove all 2 Always answers? The gate asks again for each dangerous command."
	rd := wantRow{desc: "recursive delete", id: "recursive_delete", at: 2000}
	gf := wantRow{desc: "git force push (rewrites remote history)", id: "git_force_push", at: 1000}
	gate := approvalsGate(t, "recursive_delete", "git_force_push")
	registerTestCommand(t, ApprovalsCommand(gate))
	code := wantListCode("recursive_delete", "git_force_push")
	tail := "\n\nSend /approvals remove <number> <code> to remove an answer.\n" +
		"Send /approvals remove all " + code + " to remove all answers.\n"
	count := func(want int) {
		t.Helper()
		if rows, err := gate.ListAlways(ctx); err != nil || len(rows) != want {
			t.Fatalf("%d rows, want %d: %v", len(rows), want, err)
		}
	}

	// No Send and no Confirm: the question and the line with the confirm command.
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/approvals remove all "+code)
	want := "🗑 **Remove all 2 Always answers?**\nThe gate asks again for each dangerous command.\n" +
		"To remove all, send: /approvals remove all " + code + " confirm\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	count(2)

	// Confirm says no: the list with the first line "Not removed.".
	var prompts []string
	answer := false
	c, out = cmdCtx(t, "cli:t")
	c.Confirm = func(p string) bool { prompts = append(prompts, p); return answer }
	run(t, c, "/approvals remove all "+code)
	if want := "Not removed.\n\n" + wantList(now, rd, gf) + tail; out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	if !reflect.DeepEqual(prompts, []string{question}) {
		t.Fatalf("%q", prompts)
	}
	count(2)

	// Confirm says yes: the confirm path removes all rows.
	answer = true
	out.Reset()
	run(t, c, "/approvals remove all "+code)
	if want := "✅ Removed 2 Always answers. The gate asks again.\n\n" +
		"No Always answers. The gate asks for each dangerous command.\n"; out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	if !reflect.DeepEqual(prompts, []string{question, question}) {
		t.Fatalf("%q", prompts)
	}
	count(0)
}

// The second line of a row holds the preview and the workdir. A group shows no second line.
// TestApprovalsMCPRowHostileNames checks that a server name and a tool name from an MCP
// server show hidden characters and stay on one line.
func TestApprovalsMCPRowHostileNames(t *testing.T) {
	now := time.Now()
	setNow(t, now)
	at := time.UnixMilli(1000)
	rows := []security.AlwaysRow{{ID: "m1", PatternID: "mcp:x.y", Server: "srv\x1b[2J", ToolName: "post\u202e\n2. fake", CreatedAt: at}}
	c, _ := interactiveCtx(t)
	want := fmt.Sprintf("1. MCP write · srv<U+001B>\\[2J · post<U+202E> 2. fake · %s · `%s`", formatDate(at, now), wantCode("m1"))
	if r := approvalsReply(c, "", rows); !strings.Contains(r.Text, want) {
		t.Fatalf("got:\n%s\nwant line:\n%s", r.Text, want)
	}
}

func TestApprovalsPreviewLine(t *testing.T) {
	now := time.Now()
	setNow(t, now)
	t.Setenv("HOME", "/home/u")
	at := time.UnixMilli(1000)
	long := "/srv/" + strings.Repeat("a", 40) + "/deep/proj"
	rows := []security.AlwaysRow{
		{ID: "id1", PatternID: "recursive_delete", Preview: "rm -rf build/", Workdir: "/home/u/proj", CreatedAt: at},
		{ID: "id2", PatternID: "recursive_delete", Preview: "rm -rf x", CreatedAt: at},
		{ID: "id3", PatternID: "recursive_delete", Preview: "echo `id`\nrm -rf y", Workdir: "/srv/my_app", CreatedAt: at},
		{ID: "id4", PatternID: "recursive_delete", Preview: "rm -rf z\x1b[2J", Workdir: long, CreatedAt: at},
		{ID: "id5", PatternID: "git_force_push", Workdir: "/home/u/proj", CreatedAt: at},
		{ID: "id6", PatternID: "recursive_delete", Preview: "rm -rf w", Workdir: "/home/u", CreatedAt: at},
		{ID: "id7", PatternID: "recursive_delete", Preview: "rm -rf v", Workdir: "/srv/a`b", CreatedAt: at},
		{ID: "m1", PatternID: "mcp:a.b.c", Server: "a_b", ToolName: "c", CreatedAt: at},
	}
	const rd, gfp = "recursive delete", "git force push (rewrites remote history)"
	row := func(n int, desc, id string) string {
		return fmt.Sprintf("%d. %s · %s · `%s`", n, desc, formatDate(at, now), wantCode(id))
	}
	private := strings.Join([]string{
		"🛡 **Always answers**",
		row(1, rd, "id1"), "  `rm -rf build/` in `~/proj`",
		row(2, rd, "id2"), "  `rm -rf x`",
		row(3, rd, "id3"), "  echo \\`id\\` rm -rf y in `/srv/my_app`",
		row(4, rd, "id4"), "  `rm -rf z<U+001B>[2J` in `…" + strings.Repeat("a", 29) + "/deep/proj`",
		row(5, gfp, "id5"),
		row(6, rd, "id6"), "  `rm -rf w` in `~`",
		row(7, rd, "id7"), "  `rm -rf v` in /srv/a\\`b",
		row(8, `MCP write · a\_b · c`, "m1"),
	}, "\n")
	c, _ := interactiveCtx(t)
	if r := approvalsReply(c, "", rows); r.Text != private {
		t.Fatalf("private chat:\n%s", r.Text)
	}
	// A group shows no preview and no workdir.
	c.Key = "telegram:group:-1001:5"
	group := strings.Join([]string{
		"🛡 **Always answers**",
		row(1, rd, "id1"), row(2, rd, "id2"), row(3, rd, "id3"), row(4, rd, "id4"),
		row(5, gfp, "id5"), row(6, rd, "id6"), row(7, rd, "id7"), row(8, `MCP write · a\_b · c`, "m1"),
	}, "\n")
	if r := approvalsReply(c, "", rows); r.Text != group {
		t.Fatalf("group:\n%s", r.Text)
	}
	// The CLI shows the second lines, then its typed-command lines.
	cli, _ := cmdCtx(t, "cli:t")
	if r := approvalsReply(cli, "", rows); !strings.HasPrefix(r.Text, private+"\n\nSend /approvals remove <number> <code>") {
		t.Fatalf("cli:\n%s", r.Text)
	}
}

// The text shows the newest 20 rows and then the count of the other rows. "Remove all"
// covers all rows.
func TestApprovalsMoreRows(t *testing.T) {
	var ids []string
	for _, p := range security.DangerousPatterns[:23] {
		ids = append(ids, p.ID)
	}
	registerTestCommand(t, ApprovalsCommand(approvalsGate(t, ids...)))
	c, sent := interactiveCtx(t)
	run(t, c, "/approvals")
	r := lastSent(t, sent)
	lines := strings.Split(r.Text, "\n")
	if len(lines) != 22 || !strings.HasPrefix(lines[20], "20. ") || lines[21] != "and 3 more" {
		t.Fatalf("text:\n%s", r.Text)
	}
	all := r.Buttons[len(r.Buttons)-1]
	if len(r.Buttons) != 6 || len(all) != 1 || all[0].Data != "/approvals remove all "+wantListCode(ids...) {
		t.Fatalf("%+v", r.Buttons)
	}
	cli, out := cmdCtx(t, "cli:t")
	run(t, cli, "/approvals")
	if !strings.Contains(out.String(), "\nand 3 more\n\nSend /approvals remove <number> <code> to remove an answer.\n") {
		t.Fatalf("cli:\n%s", out)
	}
}

func TestApprovalsRemovedNoticeOnce(t *testing.T) {
	ctx := context.Background()
	gate := approvalsGate(t)
	if err := gate.DB.KVSet(ctx, "approvals_always_removed", "3"); err != nil {
		t.Fatal(err)
	}
	cmd := ApprovalsCommand(gate)
	var out []Reply
	c := &CommandCtx{Send: func(r Reply) { out = append(out, r) }}
	for range 2 {
		if err := cmd.Run(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	notice := "3 old Always answers were removed by the update. The gate asks again once for each command."
	if len(out) != 2 || !strings.HasPrefix(out[0].Text, notice+"\n\n") || strings.Contains(out[1].Text, notice) {
		t.Fatalf("%+v", out)
	}
	if _, ok, err := gate.DB.KVGet(ctx, "approvals_always_removed"); err != nil || ok {
		t.Fatal("the first reply must delete the key", ok, err)
	}
}

func TestApprovalsLatchLine(t *testing.T) {
	ctx := context.Background()
	gate := approvalsGate(t)
	if _, err := gate.DB.ExecContext(ctx, `INSERT INTO approval_grants(id, pattern_id, content_sha256, programs, created_at)
		VALUES ('broken_row', 'recursive_delete', 'x', 'not json', 1)`); err != nil {
		t.Fatal(err)
	}
	var out []Reply
	c := &CommandCtx{Send: func(r Reply) { out = append(out, r) }}
	if err := ApprovalsCommand(gate).Run(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(out[0].Text, "\n")
	if !strings.HasPrefix(first, "Saved answers are off, because the answer store is damaged: ") ||
		!strings.HasSuffix(first, ". Restart the gateway after you repair it.") || !strings.Contains(first, `broken\_row`) {
		t.Fatal(out[0].Text)
	}
	// A missing table gives the latch line, not an error.
	gate = approvalsGate(t)
	if _, err := gate.DB.ExecContext(ctx, "DROP TABLE approval_grants"); err != nil {
		t.Fatal(err)
	}
	out = nil
	if err := ApprovalsCommand(gate).Run(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.HasPrefix(out[0].Text, "Saved answers are off, because the answer store is damaged: ") {
		t.Fatalf("%+v", out)
	}
}

// logGate returns a gate with a new state.db that holds rows in approval_log.
func logGate(t *testing.T, rows ...security.LogRow) *security.Gate {
	t.Helper()
	d := openTestDB(t)
	for _, r := range rows {
		if _, err := d.ExecContext(context.Background(), `INSERT INTO approval_log(at, session_id, scope, kind, tool,
			server, tool_name, rule, class, decision, source, answer, content_sha256, preview)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.At.UnixMilli(), r.SessionID, r.Scope, r.Kind, r.Tool, r.Server, r.ToolName, r.Rule, r.Class, r.Decision, r.Source,
			r.Answer, r.ContentSHA256, r.Preview); err != nil {
			t.Fatal(err)
		}
	}
	return &security.Gate{DB: d}
}

// logRows returns the rows of the log view test. "telegram:1:0" is the scope of the test chat.
func logRows() []security.LogRow {
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.Local) }
	row := func(t time.Time, rule, decision, source, answer, preview string) security.LogRow {
		return security.LogRow{At: t, Scope: "telegram:1:0", Kind: "chat", Tool: "terminal", Rule: rule,
			Decision: decision, Source: source, Answer: answer, Preview: preview}
	}
	cron := row(at(17, 23, 5), "recursive_delete", security.DecisionBlocked, "cron-mode", "", "rm -rf build/")
	cron.Kind = "cron"
	mcp := row(at(18, 13, 12), "mcp:slack.chat_post_message", security.DecisionDenied, "timeout", "", "channel: C1")
	mcp.Tool, mcp.Class, mcp.Server, mcp.ToolName = "mcp:slack.chat_post_message", "send", "slack", "chat_post_message"
	other := row(at(18, 14, 10), "recursive_delete", security.DecisionAllowed, "grant", "", "rm -rf dist/")
	other.Scope = "telegram:9:0"
	noChat := row(at(18, 9, 0), "recursive_delete", security.DecisionBlocked, "cron-mode", "", "rm -rf x/")
	noChat.Scope, noChat.Kind = "", "cron"
	return []security.LogRow{
		row(at(18, 14, 2), "recursive_delete", security.DecisionAllowed, "grant", "", "rm -rf build/"),
		row(at(18, 13, 58), "git_force_push", security.DecisionAllowed, "owner", "session", "git push --force origin main"),
		row(at(18, 13, 40), "stop_restart_system_service", security.DecisionAllowed, "smart", "", "systemctl restart nginx"),
		mcp,
		row(at(18, 12, 50), "human_only_password_change", security.DecisionBlocked, "human-only", "", "passwd"),
		row(at(18, 12, 31), "recursive_delete_home_directory", security.DecisionBlocked, "hardline", "", "rm -rf ~"),
		cron, other, noChat,
	}
}

func TestApprovalsLogThisTopic(t *testing.T) {
	setNow(t, time.Date(2026, 11, 18, 14, 30, 0, 0, time.Local))
	registerTestCommand(t, ApprovalsCommand(logGate(t, logRows()...)))
	c, sent := interactiveCtx(t)
	c.Session = &sessions.Session{Source: "telegram", ChatID: "1"}
	run(t, c, "/approvals log")
	want := "📜 **Approval log** (this topic, last 7)\n" +
		"14:02 ✅ grant · recursive delete\n  `rm -rf build/`\n" +
		"13:58 ✅ owner · Session · git force push (rewrites remote history)\n  `git push --force origin main`\n" +
		"13:40 ✅ smart · stop/restart system service\n  `systemctl restart nginx`\n" +
		"13:12 ❌ timeout · MCP send (slack · chat\\_post\\_message)\n" +
		"12:50 ⛔ human-only · password change\n  `passwd`\n" +
		"12:31 ⛔ hardline · recursive delete of home directory\n  `rm -rf ~`\n" +
		"Nov 17 23:05 ⛔ cron-mode · cron · recursive delete\n  `rm -rf build/`"
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
}

func TestApprovalsLogAllAndGroup(t *testing.T) {
	setNow(t, time.Date(2026, 11, 18, 14, 30, 0, 0, time.Local))
	registerTestCommand(t, ApprovalsCommand(logGate(t, logRows()...)))
	c, out := cmdCtxDB(t, "cli:t")
	c.Session = &sessions.Session{Source: "cli", Key: "cli:t"}
	run(t, c, "/approvals log all")
	for _, want := range []string{
		"📜 **Approval log** (all chats, last 9)\n",
		"14:10 ✅ grant · recursive delete · telegram:9:0\n  `rm -rf dist/`\n",
		"14:02 ✅ grant · recursive delete · telegram:1:0\n",
		"09:00 ⛔ cron-mode · cron · recursive delete · no chat\n  `rm -rf x/`\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}

	g, sent := interactiveCtx(t)
	g.Key = "telegram:group:-100"
	g.Session = &sessions.Session{Source: "telegram", ChatID: "-100"}
	run(t, g, "/approvals log all")
	if r := lastSent(t, sent); r.Text != "Send /approvals log all in a private chat. A group can read this reply." {
		t.Fatalf("%q", r.Text)
	}
}

func TestApprovalsLogEmptyAndPreviews(t *testing.T) {
	setNow(t, time.Date(2026, 11, 18, 14, 30, 0, 0, time.Local))
	registerTestCommand(t, ApprovalsCommand(logGate(t)))
	c, sent := interactiveCtx(t)
	c.Session = &sessions.Session{Source: "telegram", ChatID: "1"}
	run(t, c, "/approvals log")
	if r := lastSent(t, sent); r.Text != "No approval decisions in this topic yet." {
		t.Fatalf("%q", r.Text)
	}

	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.Local)
	registerTestCommand(t, ApprovalsCommand(logGate(t,
		security.LogRow{At: at, Scope: "telegram:1:0", Kind: "chat", Tool: "terminal", Rule: "recursive_delete",
			Decision: security.DecisionAllowed, Source: "owner", Answer: "once", Preview: "rm -rf `id`/x"},
		security.LogRow{At: at.Add(time.Minute), Scope: "telegram:1:0", Kind: "chat", Tool: "terminal", Rule: "recursive_delete",
			Decision: security.DecisionAllowed, Source: "owner", Answer: "always", Preview: "rm -rf " + strings.Repeat("a", 80)})))
	run(t, c, "/approvals log")
	want := "📜 **Approval log** (this topic, last 2)\n" +
		"10:01 ✅ owner · Always · recursive delete\n  `rm -rf " + strings.Repeat("a", 52) + "…`\n" +
		"10:00 ✅ owner · Once · recursive delete\n  rm -rf \\`id\\`/x"
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
}

func TestApprovalsLogRedactedAnswer(t *testing.T) {
	setNow(t, time.Date(2026, 11, 18, 14, 30, 0, 0, time.Local))
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.Local)
	mcp := func(at time.Time, kind, decision, source, answer string) security.LogRow {
		return security.LogRow{At: at, Scope: "telegram:1:0", Kind: kind, Tool: "mcp:gmail.send_email",
			Rule: "mcp:gmail.send_email", Class: "send", Server: "gmail", ToolName: "send_email",
			Decision: decision, Source: source, Answer: answer, Preview: "to: x"}
	}
	registerTestCommand(t, ApprovalsCommand(logGate(t,
		mcp(at, "chat", security.DecisionAllowed, "owner", "redacted"),
		mcp(at.Add(time.Minute), "cron", security.DecisionBlocked, "model-guard", ""))))
	c, sent := interactiveCtx(t)
	c.Session = &sessions.Session{Source: "telegram", ChatID: "1"}
	run(t, c, "/approvals log")
	want := "📜 **Approval log** (this topic, last 2)\n" +
		"10:01 ⛔ model-guard · cron · MCP send (gmail · send\\_email)\n" +
		"10:00 ✅ owner · Send redacted · MCP send (gmail · send\\_email)"
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
}
