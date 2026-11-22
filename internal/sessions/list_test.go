package sessions

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// touchAt sets last_active of the session to ms, so the test controls the list order.
func touchAt(t *testing.T, s *Store, id string, ms int64) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET last_active = ? WHERE id = ?`, ms, id); err != nil {
		t.Fatal(err)
	}
}

// seedRows inserts n messages into the session in one statement. Row i has the content
// "message i". Odd rows are user rows, and even rows are assistant rows.
func seedRows(t *testing.T, s *Store, sessionID string, n int) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO messages (session_id, role, content, timestamp)
		SELECT ?, CASE i % 2 WHEN 1 THEN 'user' ELSE 'assistant' END, 'message ' || i, 1763600000000 + i FROM n`,
		n, sessionID); err != nil {
		t.Fatal(err)
	}
}

func summaryIDs(l []SessionSummary) []string {
	var out []string
	for _, s := range l {
		out = append(out, s.ID)
	}
	return out
}

func messageIDs(l []StoredMessage) []int64 {
	var out []int64
	for _, m := range l {
		out = append(out, m.ID)
	}
	return out
}

func TestListAllFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	mk := func(in Session, ms int64) *Session {
		t.Helper()
		got, err := s.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		touchAt(t, s, got.ID, ms)
		return got
	}
	dash := mk(Session{Key: "dashboard:owner", Source: "dashboard", ChatID: "owner", Title: "Deploy plan"}, 1000)
	tg := mk(Session{Key: "telegram:42", Source: "telegram", ChatID: "42", Title: "deploy 100% done"}, 2000)
	cli := mk(Session{Key: "cli:user", Source: "cli", Title: "notes"}, 3000)
	job := mk(Session{Key: "cron:7", Source: "cron", Kind: "cron", Title: "nightly"}, 4000)
	arch := mk(Session{Key: "dashboard:owner", Source: "dashboard", Title: "old deploy", Archived: true}, 5000)
	child := mk(Session{Source: "dashboard", Kind: KindSubagent, ParentID: dash.ID, Title: "child"}, 6000)
	mk(Session{Source: "dashboard", Kind: KindAux, ParentID: dash.ID, Title: "side calls"}, 7000)
	orphan := mk(Session{Source: "cli", Kind: KindReview, Title: "lost review"}, 8000)
	fork := mk(Session{Key: "dashboard:owner", Source: "dashboard", ParentID: tg.ID, Title: "deploy (fork)"}, 9000)
	seedRows(t, s, dash.ID, 3)

	for _, c := range []struct {
		name  string
		q     ListAllQuery
		want  []string
		total int
	}{
		{"all", ListAllQuery{}, []string{fork.ID, orphan.ID, job.ID, cli.ID, tg.ID, dash.ID}, 6},
		{"source", ListAllQuery{Source: "dashboard"}, []string{fork.ID, dash.ID}, 2},
		{"kind", ListAllQuery{Kind: "cron"}, []string{job.ID}, 1},
		{"child kind", ListAllQuery{Kind: KindSubagent}, []string{child.ID}, 1},
		{"aux never", ListAllQuery{Kind: KindAux}, nil, 0},
		{"archived", ListAllQuery{Archived: true}, []string{arch.ID}, 1},
		{"search", ListAllQuery{Search: "DEPLOY"}, []string{fork.ID, tg.ID, dash.ID}, 3},
		{"literal percent", ListAllQuery{Search: "100%"}, []string{tg.ID}, 1},
		{"page 1", ListAllQuery{Limit: 2}, []string{fork.ID, orphan.ID}, 6},
		{"page 3", ListAllQuery{Limit: 2, Offset: 4}, []string{tg.ID, dash.ID}, 6},
		{"past the end", ListAllQuery{Limit: 2, Offset: 6}, nil, 6},
	} {
		got, total, err := s.ListAll(ctx, c.q)
		if err != nil || total != c.total || !slices.Equal(summaryIDs(got), c.want) {
			t.Errorf("%s: %v total %d err %v, want %v total %d", c.name, summaryIDs(got), total, err, c.want, c.total)
		}
	}
	got, _, err := s.ListAll(ctx, ListAllQuery{Source: "dashboard"})
	if err != nil || len(got) != 2 || got[1].MessageCount != 3 || got[0].MessageCount != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestChildrenOfASession(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	parent, _ := s.Create(ctx, Session{Key: "dashboard:owner", Source: "dashboard", Title: "p"})
	sub, _ := s.Create(ctx, Session{Source: "dashboard", Kind: KindSubagent, ParentID: parent.ID})
	time.Sleep(2 * time.Millisecond)
	rev, _ := s.Create(ctx, Session{Source: "dashboard", Kind: KindReview, ParentID: parent.ID})
	time.Sleep(2 * time.Millisecond)
	judge, _ := s.Create(ctx, Session{Source: "dashboard", Kind: KindGoalJudge, ParentID: parent.ID})
	time.Sleep(2 * time.Millisecond)
	if _, err := s.Create(ctx, Session{Source: "dashboard", Kind: KindAux, ParentID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Session{Key: "dashboard:owner", Source: "dashboard", ParentID: parent.ID, Title: "fork"}); err != nil {
		t.Fatal(err)
	}
	seedRows(t, s, rev.ID, 2)
	got, err := s.Children(ctx, parent.ID)
	if err != nil || !slices.Equal(summaryIDs(got), []string{sub.ID, rev.ID, judge.ID}) || got[1].MessageCount != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if none, err := s.Children(ctx, sub.ID); err != nil || len(none) != 0 {
		t.Fatal(none, err)
	}
}

func TestMessagesPage(t *testing.T) {
	ctx := context.Background()
	st, sess := newStoreSession(t)
	seedRows(t, st, sess.ID, 250)
	all, err := st.Messages(ctx, sess.ID)
	if err != nil || len(all) != 250 {
		t.Fatal(len(all), err)
	}
	ids := messageIDs(all)
	for _, c := range []struct {
		name          string
		before, after int64
		limit         int
		want          []int64
	}{
		{"newest, default limit", 0, 0, 0, ids[200:]},
		{"limit above the maximum", 0, 0, 1000, ids[50:]},
		{"before", ids[200], 0, 30, ids[170:200]},
		{"after", 0, ids[9], 5, ids[10:15]},
		{"before near the start", ids[2], 0, 50, ids[:2]},
		{"after the last row", 0, ids[249], 50, nil},
	} {
		got, err := st.MessagesPage(ctx, sess.ID, c.before, c.after, c.limit)
		if err != nil || !slices.Equal(messageIDs(got), c.want) {
			t.Errorf("%s: %v %v, want %v", c.name, messageIDs(got), err, c.want)
		}
	}
	page, _ := st.MessagesPage(ctx, sess.ID, 0, 0, 0)
	if page[0].Msg.Content != "message 201" || page[0].Msg.Role != provider.RoleUser {
		t.Fatalf("%+v", page[0])
	}
	if _, err := st.MessagesPage(ctx, sess.ID, ids[100], ids[10], 5); err == nil ||
		err.Error() != "sessions: messages page: use before or after, not both" {
		t.Fatal(err)
	}
	if got, err := st.MessagesPage(ctx, "nope", 0, 0, 10); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

func TestCostSum(t *testing.T) {
	ctx := context.Background()
	st, sess := newStoreSession(t)
	add := func(role provider.Role, meta MessageMeta) {
		t.Helper()
		if _, err := st.Append(ctx, sess.ID, provider.Message{Role: role, Content: "x"}, meta); err != nil {
			t.Fatal(err)
		}
	}
	add(provider.RoleUser, MessageMeta{})
	add(provider.RoleAssistant, MessageMeta{Model: "glm-4.7", CostUSD: 0.25})
	add(provider.RoleAssistant, MessageMeta{Model: "glm-4.7", CostUSD: 0.5})
	if usd, unpriced, err := st.CostSum(ctx, sess.ID); err != nil || usd != 0.75 || unpriced {
		t.Fatal(usd, unpriced, err)
	}
	add(provider.RoleAssistant, MessageMeta{Model: "glm-4.7"})
	if usd, unpriced, err := st.CostSum(ctx, sess.ID); err != nil || usd != 0.75 || !unpriced {
		t.Fatal(usd, unpriced, err)
	}
	// The copies of a fork are older than the fork, so they do not count for the fork.
	time.Sleep(2 * time.Millisecond)
	f, err := st.Fork(ctx, sess.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if usd, unpriced, err := st.CostSum(ctx, f.ID); err != nil || usd != 0 || unpriced {
		t.Fatal(usd, unpriced, err)
	}
	if usd, unpriced, err := st.CostSum(ctx, "nope"); err != nil || usd != 0 || unpriced {
		t.Fatal(usd, unpriced, err)
	}
}

// Each read closes its rows before it returns. A read that keeps its rows open blocks the
// single connection, and the context deadline then fails the other reads.
func TestListQueriesReleaseTheConnection(t *testing.T) {
	st, sess := newStoreSession(t)
	seedRows(t, st, sess.ID, 40)
	if _, err := st.Create(context.Background(), Session{Kind: KindSubagent, ParentID: sess.ID}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 20 {
		wg.Add(5)
		go func() { defer wg.Done(); _, _, err := st.ListAll(ctx, ListAllQuery{Limit: 5}); errs <- err }()
		go func() { defer wg.Done(); _, err := st.Children(ctx, sess.ID); errs <- err }()
		go func() { defer wg.Done(); _, err := st.MessagesPage(ctx, sess.ID, 0, 0, 10); errs <- err }()
		go func() { defer wg.Done(); _, _, err := st.CostSum(ctx, sess.ID); errs <- err }()
		go func() { defer wg.Done(); _, err := st.Get(ctx, sess.ID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
