package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// openTestDB opens a new state.db in a temporary folder. The test closes it at the end.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestCreateAppendReadBack(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: "cli:user", Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.ID) != 19 || sess.StartedAt.IsZero() || sess.LastActive.IsZero() {
		t.Fatalf("%+v", sess)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file", Args: `{"path":"a"}`}}},
		MessageMeta{Usage: provider.Usage{InputTokens: 12, OutputTokens: 3, CacheReadTokens: 4, FirstTokenMS: 200, TotalMS: 900}, Model: "glm-4.7", Provider: "primary", CostUSD: 0.001, FinishReason: "tool_calls"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "read_file", Content: "data"}, MessageMeta{Observed: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Msg.Content != "hi" || got[1].Msg.ToolCalls[0].Args != `{"path":"a"}` ||
		got[1].Meta.Usage.CacheReadTokens != 4 || got[1].Meta.CostUSD != 0.001 {
		t.Fatalf("%+v", got)
	}
	m := got[1].Meta
	if m.Model != "glm-4.7" || m.Provider != "primary" || m.FinishReason != "tool_calls" ||
		m.Usage.FirstTokenMS != 200 || m.Usage.TotalMS != 900 || m.Usage.InputTokens != 12 || m.Usage.OutputTokens != 3 {
		t.Fatalf("%+v", m)
	}
	if got[0].Msg.ToolCalls != nil || got[0].At.IsZero() || !(got[0].ID < got[1].ID) {
		t.Fatalf("%+v", got[0])
	}
	if got[2].Msg.ToolCallID != "c1" || got[2].Msg.ToolName != "read_file" || !got[2].Meta.Observed {
		t.Fatalf("%+v", got[2])
	}
	back, err := s.Get(ctx, sess.ID)
	if err != nil || back.Key != "cli:user" || back.Source != "cli" || back.Kind != "chat" || back.LastActive.Before(sess.LastActive) {
		t.Fatal(back, err)
	}
}

func TestGetUnknownSession(t *testing.T) {
	s := New(openTestDB(t))
	_, err := s.Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "no session nope") {
		t.Fatal(err)
	}
}

func TestCreateDefaultsKindAndUniqueIDs(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	a, _ := s.Create(ctx, Session{})
	b, _ := s.Create(ctx, Session{})
	if a.Kind != "chat" || a.ID == b.ID {
		t.Fatal(a, b)
	}
	if !(a.ID < b.ID) && a.ID[:13] != b.ID[:13] {
		t.Fatal("ids must sort by time", a.ID, b.ID)
	}
}

func TestActiveKeyMapping(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if a, err := s.Active(ctx, "telegram:dm:1"); a != nil || err != nil {
		t.Fatal("none yet", a, err)
	}
	a, err := s.ActiveOrCreate(ctx, "telegram:dm:1", Session{Source: "telegram", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.ActiveOrCreate(ctx, "telegram:dm:1", Session{Source: "telegram", Kind: "chat"})
	if a.ID != b.ID || a.Key != "telegram:dm:1" {
		t.Fatal("same key → same session")
	}
	c, _ := s.Create(ctx, Session{Key: "telegram:dm:1", Kind: "chat"})
	if err := s.SetActive(ctx, "telegram:dm:1", c.ID); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.Active(ctx, "telegram:dm:1"); x == nil || x.ID != c.ID {
		t.Fatal("SetActive")
	}
}

func TestActiveOrCreateConcurrent(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := s.ActiveOrCreate(ctx, "cli:race", Session{Source: "cli"})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = sess.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("two sessions for one key", ids)
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE session_key='cli:race'").Scan(&n); err != nil || n != 1 {
		t.Fatal("orphan sessions", n, err)
	}
}

func TestAppendImageStoresPathOnly(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	sess, _ := s.Create(ctx, Session{})
	img := provider.Image{Path: "/tmp/cat.png", MIME: "image/png", Data: []byte("PNGDATA")}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "look", Images: []provider.Image{img}}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Messages(ctx, sess.ID)
	if len(got) != 1 || !strings.Contains(got[0].Msg.Content, "/tmp/cat.png") || strings.Contains(got[0].Msg.Content, "PNGDATA") || got[0].Msg.Images != nil {
		t.Fatalf("%+v", got)
	}
}

func TestAppendSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := New(d)
	sess, _ := s.Create(ctx, Session{Key: "cli:user"})
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "keep me"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	// No Close on the first handle: a kill -9 does not close it either.
	d2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	defer d.Close()
	got, err := New(d2).Messages(ctx, sess.ID)
	if err != nil || len(got) != 1 || got[0].Msg.Content != "keep me" {
		t.Fatal(got, err)
	}
}

func TestAutoTitleFirst60(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: "cli:user", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	long := "Please   scrape the allsaints sale page\nand list every jacket under one hundred pounds with sizes"
	if err := s.SetTitleIfEmpty(ctx, sess.ID, long); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, sess.ID)
	want := "Please scrape the allsaints sale page and list every jacket"
	if got.Title != want {
		t.Fatalf("%q, want %q", got.Title, want)
	}
	if err := s.SetTitleIfEmpty(ctx, sess.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, sess.ID); got.Title != want {
		t.Fatalf("second call changed the title: %q", got.Title)
	}
	// A short text stays whole, and a word longer than 60 runes is cut at 60.
	short, _ := s.Create(ctx, Session{Key: "cli:user"})
	_ = s.SetTitleIfEmpty(ctx, short.ID, "  hi there ")
	if got, _ := s.Get(ctx, short.ID); got.Title != "hi there" {
		t.Fatalf("%q", got.Title)
	}
	word, _ := s.Create(ctx, Session{Key: "cli:user"})
	_ = s.SetTitleIfEmpty(ctx, word.ID, strings.Repeat("é", 70))
	if got, _ := s.Get(ctx, word.ID); got.Title != strings.Repeat("é", 60) {
		t.Fatalf("%q", got.Title)
	}
}

func TestListByKeyHidesArchived(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	a, _ := s.Create(ctx, Session{Key: "k1"})
	b, _ := s.Create(ctx, Session{Key: "k1"})
	_, _ = s.Create(ctx, Session{Key: "k2"})
	if _, err := s.Append(ctx, a.ID, provider.Message{Role: provider.RoleUser, Content: "x"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	list, total, err := s.ListPage(ctx, "k1", ListQuery{Limit: 10})
	if err != nil || total != 2 || len(list) != 2 || list[0].ID != a.ID || list[0].MessageCount != 1 || list[1].ID != b.ID {
		t.Fatalf("%+v %v", list, err)
	}
	if err := s.Archive(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := s.ListPage(ctx, "k1", ListQuery{Limit: 10}); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("%+v", list)
	}
	if got, _ := s.Get(ctx, a.ID); !got.Archived {
		t.Fatal("archived flag")
	}
	if err := s.Archive(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// seedListSessions creates one session for each title under key, plus one session for
// another key. The first title gets the oldest last_active. It returns the sessions in
// the order of the titles.
func seedListSessions(t *testing.T, s *Store, key string, titles []string) []*Session {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	out := make([]*Session, 0, len(titles))
	for i, title := range titles {
		sess, err := s.Create(ctx, Session{Key: key, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		at := base + int64(i)*60_000
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_active = ? WHERE id = ?`, at, sess.ID); err != nil {
			t.Fatal(err)
		}
		sess.LastActive = time.UnixMilli(at).UTC()
		out = append(out, sess)
	}
	if _, err := s.Create(ctx, Session{Key: "telegram:dm:2", Title: titles[0]}); err != nil {
		t.Fatal(err)
	}
	return out
}

func numberedTitles(n int) []string {
	titles := make([]string, n)
	for i := range titles {
		titles[i] = fmt.Sprintf("session %02d", i)
	}
	return titles
}

func TestListPageCountsAndOrders(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	seeded := seedListSessions(t, s, "telegram:dm:1", numberedTitles(50))
	if _, err := s.Append(ctx, seeded[41].ID, provider.Message{Role: provider.RoleUser, Content: "x"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	// Append moves last_active of seeded[41] to now, so it becomes the newest.
	order := []*Session{seeded[41]}
	for i := len(seeded) - 1; i >= 0; i-- {
		if i != 41 {
			order = append(order, seeded[i])
		}
	}
	page, total, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Offset: 8, Limit: 8})
	if err != nil || total != 50 || len(page) != 8 {
		t.Fatalf("len %d, total %d, err %v", len(page), total, err)
	}
	for i, got := range page {
		if want := order[8+i]; got.ID != want.ID || got.Title != want.Title {
			t.Fatalf("row %d: got %q, want %q", 8+i, got.Title, want.Title)
		}
	}
	first, _, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8})
	if err != nil || len(first) != 8 || first[0].ID != seeded[41].ID || first[0].MessageCount != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	last, total, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Offset: 48, Limit: 8})
	if err != nil || total != 50 || len(last) != 2 {
		t.Fatalf("len %d, total %d, err %v", len(last), total, err)
	}
	none, total, err := s.ListPage(ctx, "telegram:dm:9", ListQuery{Limit: 8})
	if err != nil || total != 0 || len(none) != 0 {
		t.Fatalf("%+v %d %v", none, total, err)
	}
}

func TestListPageExcludeAndArchived(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	seeded := seedListSessions(t, s, "telegram:dm:1", numberedTitles(50))
	current := seeded[49]
	page, total, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8, Exclude: current.ID})
	if err != nil || total != 49 || len(page) != 8 || page[0].ID == current.ID {
		t.Fatalf("len %d, total %d, err %v", len(page), total, err)
	}
	archived := map[string]bool{}
	for _, sess := range seeded[10:13] {
		if err := s.Archive(ctx, sess.ID); err != nil {
			t.Fatal(err)
		}
		archived[sess.ID] = true
	}
	page, total, err = s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8, Archived: true})
	if err != nil || total != 3 || len(page) != 3 {
		t.Fatalf("len %d, total %d, err %v", len(page), total, err)
	}
	for _, got := range page {
		if !archived[got.ID] || !got.Archived {
			t.Fatalf("%+v is not archived", got)
		}
	}
	page, total, err = s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 100})
	if err != nil || total != 47 || len(page) != 47 {
		t.Fatalf("len %d, total %d, err %v", len(page), total, err)
	}
	for _, got := range page {
		if archived[got.ID] {
			t.Fatalf("archived session %s in the list", got.ID)
		}
	}
	if n, err := s.CountArchived(ctx, "telegram:dm:1"); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := s.CountArchived(ctx, "telegram:dm:2"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestListPageSearchEscapesWildcards(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	seedListSessions(t, s, "telegram:dm:1", []string{"Plan the Bali trip", "Bali plan B", "100% done", "a_b", "axb", `back\slash`})
	cases := []struct {
		search string
		want   []string
	}{
		{"bali", []string{"Bali plan B", "Plan the Bali trip"}},
		{"BALI", []string{"Bali plan B", "Plan the Bali trip"}},
		{"100%", []string{"100% done"}},
		{"a_b", []string{"a_b"}},
		{`k\s`, []string{`back\slash`}},
		{"zzz", nil},
	}
	for _, c := range cases {
		page, total, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8, Search: c.search})
		if err != nil || total != len(c.want) || len(page) != len(c.want) {
			t.Fatalf("search %q: len %d, total %d, err %v", c.search, len(page), total, err)
		}
		for i, got := range page {
			if got.Title != c.want[i] {
				t.Fatalf("search %q: row %d is %q, want %q", c.search, i, got.Title, c.want[i])
			}
		}
	}
}

func TestUnarchive(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	seeded := seedListSessions(t, s, "telegram:dm:1", []string{"one", "two"})
	if err := s.Archive(ctx, seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	if page, _, _ := s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8}); len(page) != 1 {
		t.Fatalf("%+v", page)
	}
	if err := s.Unarchive(ctx, seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	page, total, err := s.ListPage(ctx, "telegram:dm:1", ListQuery{Limit: 8})
	if err != nil || total != 2 || len(page) != 2 || page[1].ID != seeded[0].ID || page[1].Archived {
		t.Fatalf("%+v %d %v", page, total, err)
	}
	if n, _ := s.CountArchived(ctx, "telegram:dm:1"); n != 0 {
		t.Fatal(n)
	}
	if err := s.Unarchive(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRenameAndOverrides(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	a, _ := s.Create(ctx, Session{Key: "k1"})
	if err := s.Rename(ctx, a.ID, "infra"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel(ctx, a.ID, "cheap:glm-4.7-flash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrompt(ctx, a.ID, "Answer in Indonesian."); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, a.ID)
	if got.Title != "infra" || got.ModelOverride != "cheap:glm-4.7-flash" || got.PromptOverride != "Answer in Indonesian." {
		t.Fatalf("%+v", got)
	}
	for _, err := range []error{s.Rename(ctx, "nope", "x"), s.SetModel(ctx, "nope", "x"), s.SetPrompt(ctx, "nope", "x")} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
}

func TestDeleteRemovesRelatedRows(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	a, _ := s.Create(ctx, Session{Key: "k1"})
	keep, _ := s.Create(ctx, Session{Key: "k1"})
	id, _ := s.Append(ctx, a.ID, provider.Message{Role: provider.RoleUser, Content: "zebrafish secret"}, MessageMeta{})
	_, _ = s.Append(ctx, keep.ID, provider.Message{Role: provider.RoleUser, Content: "zebrafish kept"}, MessageMeta{})
	if _, err := d.ExecContext(ctx, `INSERT INTO compactions (session_id, upto_message_id, summary) VALUES (?, ?, 's')`, a.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(ctx, "k1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		var n int
		if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT count(*) FROM messages WHERE session_id = ?`, a.ID) != 0 ||
		count(`SELECT count(*) FROM compactions WHERE session_id = ?`, a.ID) != 0 ||
		count(`SELECT count(*) FROM active_session WHERE session_id = ?`, a.ID) != 0 {
		t.Fatal("related rows left")
	}
	if count(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'zebrafish'`) != 1 {
		t.Fatal("the FTS index must keep only the other session")
	}
	if err := s.Delete(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCountMessages(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: "telegram:dm:1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountMessages(ctx, sess.ID); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := s.CountMessages(ctx, "nope"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
