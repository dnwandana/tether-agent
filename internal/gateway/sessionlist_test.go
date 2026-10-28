package gateway

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// listNow is the fixed clock of the session list tests.
var listNow = time.Date(2026, 10, 28, 14, 2, 11, 0, time.Local)

func TestParseListArgs(t *testing.T) {
	cases := []struct {
		cmd, args string
		want      listArgs
	}{
		{"sessions", "", listArgs{}},
		{"sessions", "2", listArgs{Page: 2}},
		{"sessions", "2 bali", listArgs{Page: 2, Search: "bali"}},
		{"sessions", "archived", listArgs{Archived: true}},
		{"sessions", "3 archived", listArgs{Page: 3, Archived: true}},
		{"sessions", "archived photos", listArgs{Search: "archived photos"}},
		{"sessions", "12345", listArgs{Search: "12345"}},
		{"sessions", "  Bali   trip ", listArgs{Search: "Bali trip"}},
		{"sessions", "001a17f9c2e4b8d3a1c", listArgs{Search: "001a17f9c2e4b8d3a1c"}},
		{"resume", "001a17f9c2e4b8d3a1c", listArgs{ID: "001a17f9c2e4b8d3a1c"}},
		{"resume", "0012345678901234567", listArgs{ID: "0012345678901234567"}},
		{"resume", "abc", listArgs{Search: "abc"}},
		{"resume", "archived", listArgs{Search: "archived"}},
		{"resume", "2", listArgs{Page: 2}},
		{"delete", "001a17e01b7f4c0e9d2", listArgs{ID: "001a17e01b7f4c0e9d2"}},
		{"delete", "001a17e01b7f4c0e9dz", listArgs{Search: "001a17e01b7f4c0e9dz"}},
	}
	for _, c := range cases {
		if got := parseListArgs(c.cmd, c.args); got != c.want {
			t.Errorf("parseListArgs(%q, %q) = %+v, want %+v", c.cmd, c.args, got, c.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	now := listNow
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "now"},
		{now.Add(time.Minute), "now"},
		{now.Add(-5 * time.Minute), "5 min ago"},
		{time.Date(2026, 10, 28, 8, 0, 0, 0, time.Local), "today"},
		{time.Date(2026, 10, 27, 21, 40, 0, 0, time.Local), "yesterday"},
		{time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local), "2 days ago"},
		{time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local), "6 days ago"},
		{time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local), "Oct 14"},
		{time.Date(2025, 10, 14, 9, 0, 0, 0, time.Local), "Oct 14, 2025"},
		{time.Date(2026, 10, 28, 8, 0, 0, 0, time.Local).UTC(), "today"},
	}
	for _, c := range cases {
		if got := formatAge(c.at, now); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestSessionTitle(t *testing.T) {
	if got := sessionTitle(""); got != "New session" {
		t.Fatal(got)
	}
	if got := sessionTitle("a_b"); got != `a\_b` {
		t.Fatal(got)
	}
}

// listFixture is a command context for telegram:dm:1 with n older sessions and a current
// session with no title. The older sessions are "Session 01" (the oldest) to "Session {n}".
type listFixture struct {
	c       *CommandCtx
	older   []*sessions.Session // the oldest first
	current *sessions.Session
	sent    []Reply
}

func newListFixture(t *testing.T, n int, interactive bool) *listFixture {
	t.Helper()
	ctx := context.Background()
	c, _ := cmdCtxDB(t, "telegram:dm:1")
	f := &listFixture{c: c}
	if interactive {
		c.Send = func(r Reply) { f.sent = append(f.sent, r) }
	}
	base := listNow.Add(-72 * time.Hour)
	for i := 0; i < n; i++ {
		s, err := c.Deps.Store.Create(ctx, sessions.Session{Key: c.Key, Title: fmt.Sprintf("Session %02d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		setLastActive(t, c, s, base.Add(time.Duration(i)*time.Minute))
		f.older = append(f.older, s)
	}
	cur, err := c.Deps.Store.Create(ctx, sessions.Session{Key: c.Key})
	if err != nil {
		t.Fatal(err)
	}
	setLastActive(t, c, cur, listNow)
	if err := c.Deps.Store.SetActive(ctx, c.Key, cur.ID); err != nil {
		t.Fatal(err)
	}
	c.Session, f.current = cur, cur
	return f
}

// setLastActive sets last_active of s in the database and in s.
func setLastActive(t *testing.T, c *CommandCtx, s *sessions.Session, at time.Time) {
	t.Helper()
	if _, err := c.Deps.Agent.DB.ExecContext(context.Background(),
		`UPDATE sessions SET last_active = ? WHERE id = ?`, at.UnixMilli(), s.ID); err != nil {
		t.Fatal(err)
	}
	s.LastActive = at.UTC()
}

// list runs listReply in the fixture.
func (f *listFixture) list(t *testing.T, mode listMode, a listArgs) Reply {
	t.Helper()
	r, err := f.c.listReply(context.Background(), mode, a, listNow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// labels returns the labels of the buttons, row by row.
func labels(r Reply) [][]string {
	var out [][]string
	for _, row := range r.Buttons {
		var l []string
		for _, b := range row {
			l = append(l, b.Text)
		}
		out = append(out, l)
	}
	return out
}

func TestListReplySessionsPage1(t *testing.T) {
	f := newListFixture(t, 50, true)
	r := f.list(t, listSessions, listArgs{})
	wantStart := "📋 **Your sessions** · 1–8 of 50\n**▸ New session** · now _(current)_\n\n1. **Session 50** · 3 days ago · 0 msgs\n"
	if !strings.HasPrefix(r.Text, wantStart) {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "\n8. **Session 43** · 3 days ago · 0 msgs\n\nTap a number to resume.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if strings.Contains(r.Text, f.current.ID) || strings.Contains(r.Text, f.older[49].ID) {
		t.Fatal("a Telegram list must not show a session ID:\n", r.Text)
	}
	want := [][]string{{"1", "2", "3", "4"}, {"5", "6", "7", "8"}, {"·", "1 / 7", "Next ▶"}}
	if got := labels(r); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if d := r.Buttons[0][0].Data; d != "/resume "+f.older[49].ID {
		t.Fatal(d)
	}
	if d := r.Buttons[2][2].Data; d != "/sessions 2" {
		t.Fatal(d)
	}
	if r.Buttons[2][0].Data != "·" || r.Buttons[2][1].Data != "·" || r.Edit {
		t.Fatalf("%+v", r)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		s, _ := f.c.Deps.Store.Create(ctx, sessions.Session{Key: f.c.Key, Title: "old"})
		if err := f.c.Deps.Store.Archive(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	r = f.list(t, listSessions, listArgs{})
	if !strings.HasSuffix(r.Text, "Tap a number to resume.\n\n_3 archived. Send /sessions archived to see them._") {
		t.Fatalf("got:\n%s", r.Text)
	}
	// A search does not show the archived line.
	r = f.list(t, listSessions, listArgs{Search: "session 4"})
	if strings.Contains(r.Text, "archived") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestListReplyPage2AndSearch(t *testing.T) {
	f := newListFixture(t, 50, true)
	r := f.list(t, listSessions, listArgs{Page: 2})
	if !strings.HasPrefix(r.Text, "📋 **Your sessions** · 9–16 of 50\n") || !strings.Contains(r.Text, "\n9. **Session 42**") || !r.Edit {
		t.Fatalf("got:\n%s", r.Text)
	}
	if got := labels(r); !reflect.DeepEqual(got[2], []string{"◀ Prev", "2 / 7", "Next ▶"}) {
		t.Fatal(got)
	}
	if r.Buttons[2][0].Data != "/sessions 1" || r.Buttons[2][2].Data != "/sessions 3" {
		t.Fatalf("%+v", r.Buttons[2])
	}

	r = f.list(t, listResume, listArgs{Search: "session 1"})
	want := "↩️ **Sessions with \"session 1\"** · 10 found\n\n1. **Session 19** · 3 days ago · 0 msgs\n"
	if !strings.HasPrefix(r.Text, want) || strings.Contains(r.Text, "(current)") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if r.Buttons[2][2].Data != "/resume 2 session 1" {
		t.Fatalf("%+v", r.Buttons[2])
	}
}

func TestListReplyLastPageAndShortList(t *testing.T) {
	f := newListFixture(t, 50, true)
	r := f.list(t, listSessions, listArgs{Page: 99})
	if !strings.HasPrefix(r.Text, "📋 **Your sessions** · 49–50 of 50\n") || !r.Edit {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "\n49. **Session 02**") || !strings.Contains(r.Text, "\n50. **Session 01**") {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := [][]string{{"49", "50"}, {"◀ Prev", "7 / 7", "·"}}
	if got := labels(r); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if r.Buttons[1][0].Data != "/sessions 6" || r.Buttons[1][2].Data != "·" {
		t.Fatalf("%+v", r.Buttons[1])
	}

	f = newListFixture(t, 5, true)
	r = f.list(t, listSessions, listArgs{})
	if got := labels(r); !reflect.DeepEqual(got, [][]string{{"1", "2", "3", "4"}, {"5"}}) {
		t.Fatal(got)
	}
	if !strings.HasPrefix(r.Text, "📋 **Your sessions** · 1–5 of 5\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestListReplyDeleteModeNumbersCurrent(t *testing.T) {
	f := newListFixture(t, 50, true)
	r := f.list(t, listDelete, listArgs{})
	want := "🗑 **Delete a session** · 1–8 of 51\n\n1. **New session** · now · 0 msgs _(current)_\n2. **Session 50**"
	if !strings.HasPrefix(r.Text, want) {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.HasSuffix(r.Text, "\n\nTap a number. I ask again before I delete.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if r.Buttons[0][0].Data != "/delete "+f.current.ID || r.Buttons[0][1].Data != "/delete "+f.older[49].ID {
		t.Fatalf("%+v", r.Buttons[0])
	}
	if r.Buttons[2][2].Data != "/delete 2" {
		t.Fatalf("%+v", r.Buttons[2])
	}
}

func TestListReplyResumeHeader(t *testing.T) {
	f := newListFixture(t, 3, true)
	r := f.list(t, listResume, listArgs{})
	if !strings.HasPrefix(r.Text, "↩️ **Resume a session** · 1–3 of 3\n**▸ New session** · now _(current)_\n\n1. **Session 03**") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.HasSuffix(r.Text, "\n\nTap a number to resume.") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestListReplyArchivedAndEmpty(t *testing.T) {
	ctx := context.Background()
	f := newListFixture(t, 12, true)
	for _, s := range f.older[:3] {
		if err := f.c.Deps.Store.Archive(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	r := f.list(t, listArchived, listArgs{Archived: true})
	want := "🗄 **Archived sessions** · 1–3 of 3\n\n1. **Session 03** · 3 days ago · 0 msgs\n2. **Session 02**"
	if !strings.HasPrefix(r.Text, want) {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.HasSuffix(r.Text, "\n3. **Session 01** · 3 days ago · 0 msgs\n\nTap a number to resume. This also takes it out of the archive.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if r.Buttons[0][0].Data != "/resume "+f.older[2].ID || len(r.Buttons) != 1 {
		t.Fatalf("%+v", r.Buttons)
	}
	for _, s := range f.older[3:] {
		if err := f.c.Deps.Store.Archive(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	r = f.list(t, listArchived, listArgs{Archived: true})
	if r.Buttons[2][2].Data != "/sessions 2 archived" {
		t.Fatalf("%+v", r.Buttons[2])
	}

	c, _ := cmdCtxDB(t, "telegram:dm:1")
	c.Send = func(Reply) {}
	r, err := c.listReply(ctx, listSessions, listArgs{}, listNow)
	if err != nil || r.Text != "📭 **No sessions yet**\nSend a message to start one." || r.Buttons != nil {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = c.listReply(ctx, listArchived, listArgs{Archived: true}, listNow)
	if r.Text != "🗄 **No archived sessions**" {
		t.Fatal(r.Text)
	}
	r, _ = f.c.listReply(ctx, listSessions, listArgs{Search: "bali"}, listNow)
	if r.Text != "📋 **No sessions with \"bali\"**\nSend /sessions to see all sessions." || r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
}

func TestListReplyOnlyCurrent(t *testing.T) {
	f := newListFixture(t, 0, true)
	r := f.list(t, listSessions, listArgs{})
	if r.Text != "📋 **Your sessions**\n**▸ New session** · now _(current)_" || r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
}

func TestListReplyTitleIsEscaped(t *testing.T) {
	f := newListFixture(t, 1, true)
	if err := f.c.Deps.Store.Rename(context.Background(), f.older[0].ID, "my_project *draft* [x]"); err != nil {
		t.Fatal(err)
	}
	r := f.list(t, listSessions, listArgs{})
	if !strings.Contains(r.Text, `1. **my\_project \*draft\* \[x\]** · `) {
		t.Fatalf("got:\n%s", r.Text)
	}
	r = f.list(t, listSessions, listArgs{Search: "my_project"})
	if !strings.HasPrefix(r.Text, `📋 **Sessions with "my\_project"** · 1 found`) {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestListReplyCLIHints(t *testing.T) {
	f := newListFixture(t, 10, false)
	r := f.list(t, listSessions, listArgs{})
	if r.Buttons != nil {
		t.Fatalf("%+v", r.Buttons)
	}
	if !strings.Contains(r.Text, "\n1. **Session 10** · 3 days ago · 0 msgs · "+f.older[9].ID+"\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !strings.HasSuffix(r.Text, "\n\nSend /resume <id> to resume.\nSend /sessions 2 for the next page.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	r = f.list(t, listDelete, listArgs{Page: 2})
	if !strings.HasSuffix(r.Text, "\n\nSend /delete <id> to delete.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	r = f.list(t, listSessions, listArgs{Search: "session"})
	if !strings.HasSuffix(r.Text, "\nSend /sessions 2 session for the next page.") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestListReplyMessageCount(t *testing.T) {
	f := newListFixture(t, 2, true)
	seedTurns(t, f.c.Deps.Store, f.older[1].ID, 1)
	setLastActive(t, f.c, f.older[1], listNow.Add(-time.Hour))
	if _, err := f.c.Deps.Store.Append(context.Background(), f.older[0].ID,
		provider.Message{Role: provider.RoleUser, Content: "x"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	setLastActive(t, f.c, f.older[0], listNow.Add(-2*time.Hour))
	r := f.list(t, listSessions, listArgs{})
	if !strings.Contains(r.Text, "\n1. **Session 02** · today · 2 msgs\n2. **Session 01** · today · 1 msg\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestButtonTitle(t *testing.T) {
	if got := buttonTitle(""); got != "New session" {
		t.Fatal(got)
	}
	if got := buttonTitle("a_b"); got != "a_b" {
		t.Fatal(got)
	}
	long := strings.Repeat("é", 40)
	if got := buttonTitle(long); got != strings.Repeat("é", 31)+"…" {
		t.Fatal(got)
	}
}

// A search shows the current session as a marked row when its title matches, because the
// top line shows only on the full list.
func TestListReplySearchFindsCurrent(t *testing.T) {
	f := newListFixture(t, 3, true)
	if err := f.c.Deps.Store.Rename(context.Background(), f.current.ID, "Plan the Bali trip"); err != nil {
		t.Fatal(err)
	}
	f.current.Title = "Plan the Bali trip"
	r := f.list(t, listResume, listArgs{Search: "bali"})
	want := "↩️ **Sessions with \"bali\"** · 1 found\n\n1. **Plan the Bali trip** · now · 0 msgs _(current)_\n"
	if !strings.HasPrefix(r.Text, want) {
		t.Fatalf("got:\n%s", r.Text)
	}
	if d := r.Buttons[0][0].Data; d != "/resume "+f.current.ID {
		t.Fatal(d)
	}
}
