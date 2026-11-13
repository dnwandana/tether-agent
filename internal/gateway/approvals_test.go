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
		wantRow{"recursive delete", "recursive_delete", 1000},
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
		wantRow{"recursive delete", "recursive_delete", 1000},
		wantRow{"git force push (rewrites remote history)", "git_force_push", 2000}) +
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
	rd := wantRow{"recursive delete", "recursive_delete", 1000}
	gfp := wantRow{"git force push (rewrites remote history)", "git_force_push", 2000}
	old := wantRow{`old\_rule\_x`, "old_rule_x", 3000}
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
	left := wantList(now, wantRow{"git force push (rewrites remote history)", "git_force_push", 2000})
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
		cmd.Help != "See and remove the Always answers: /approvals [remove <number> <code> | remove all <list code>]" {
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
			if len(b.Data) > modelActionMaxBytes {
				t.Fatal(len(b.Data), b.Data)
			}
		}
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
	if len(action) > modelActionMaxBytes {
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
	rd := wantRow{desc: "recursive delete", id: "recursive_delete", at: 1000}
	gf := wantRow{desc: "git force push (rewrites remote history)", id: "git_force_push", at: 2000}
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
	if a := "/approvals remove all " + code + " " + confirmWord; len(a) != 38 || len(a) > modelActionMaxBytes {
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
		"INSERT INTO approvals_always(pattern_id, created_at) VALUES (?, ?)", "old_rule_x", int64(3000)); err != nil {
		t.Fatal(err)
	}
	do("remove all "+code+" confirm", changed+"\n\n"+wantList(now, rd, gf, ox), listButtons(rd, gf, ox))
	count(gate, 3)

	// The confirm form removes all rows.
	code3 := wantListCode("recursive_delete", "git_force_push", "old_rule_x")
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
	rd := wantRow{desc: "recursive delete", id: "recursive_delete", at: 1000}
	gf := wantRow{desc: "git force push (rewrites remote history)", id: "git_force_push", at: 2000}
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
