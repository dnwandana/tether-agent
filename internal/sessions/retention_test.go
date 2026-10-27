package sessions

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// retNow is the fixed clock of the retention tests.
var retNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// agedSession creates a session of kind with one message, one compaction, and one key
// mapping, and sets its last_active to age before retNow.
func agedSession(t *testing.T, d *db.DB, s *Store, kind, source string, age time.Duration) *Session {
	t.Helper()
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: kind + ":" + source, Source: source, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "retentionword " + kind}, MessageMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO compactions (session_id, upto_message_id, summary) VALUES (?, ?, 's')`, sess.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(ctx, "map:"+sess.ID, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE sessions SET last_active = ? WHERE id = ?`, retNow.Add(-age).UnixMilli(), sess.ID); err != nil {
		t.Fatal(err)
	}
	sess.LastActive = retNow.Add(-age)
	return sess
}

// rowCount returns the result of a count query.
func rowCount(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// related returns the number of rows in other tables that refer to the session.
func related(t *testing.T, d *db.DB, id string) int {
	t.Helper()
	return rowCount(t, d, `SELECT count(*) FROM messages WHERE session_id = ?`, id) +
		rowCount(t, d, `SELECT count(*) FROM compactions WHERE session_id = ?`, id) +
		rowCount(t, d, `SELECT count(*) FROM active_session WHERE session_id = ?`, id) +
		rowCount(t, d, `SELECT count(*) FROM delegation_batches WHERE parent_session_id = ?`, id)
}

func addBatch(t *testing.T, d *db.DB, id, parentID, status string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `INSERT INTO delegation_batches (id, parent_session_id, status)
		VALUES (?, ?, ?)`, id, parentID, status); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDeletesOldAutomaticSessionsOnly(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	day := 24 * time.Hour
	oldCron := agedSession(t, d, s, "cron", "cron", 31*day)
	oldSub := agedSession(t, d, s, "subagent", "telegram", 40*day)
	oldReview := agedSession(t, d, s, "review", "cli", 400*day)
	newCron := agedSession(t, d, s, "cron", "cron", 29*day)
	oldCLI := agedSession(t, d, s, "chat", "cli", 400*day)
	oldTG := agedSession(t, d, s, "chat", "telegram", 400*day)
	addBatch(t, d, "b-done", oldCron.ID, "done")

	got, err := s.PruneAutomatic(ctx, retNow.Add(-30*day))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{oldCron.ID, oldSub.ID, oldReview.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("pruned %v, want %v", got, want)
	}
	for _, sess := range []*Session{oldCron, oldSub, oldReview} {
		if _, err := s.Get(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s session must be deleted: %v", sess.Kind, err)
		}
		if n := related(t, d, sess.ID); n != 0 {
			t.Fatalf("%s session left %d related rows", sess.Kind, n)
		}
	}
	for _, sess := range []*Session{newCron, oldCLI, oldTG} {
		if _, err := s.Get(ctx, sess.ID); err != nil {
			t.Fatalf("%s/%s session must stay: %v", sess.Kind, sess.Source, err)
		}
		if n := rowCount(t, d, `SELECT count(*) FROM messages WHERE session_id = ?`, sess.ID); n != 1 {
			t.Fatalf("%s/%s session lost its messages", sess.Kind, sess.Source)
		}
	}
	// The FTS index keeps only the rows of the 3 sessions that stay.
	if n := rowCount(t, d, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'retentionword'`); n != 3 {
		t.Fatalf("FTS rows = %d, want 3", n)
	}
}

func TestPruneBoundaryAndZeroCutoff(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	exact := agedSession(t, d, s, "cron", "cron", 30*24*time.Hour)
	got, err := s.PruneAutomatic(ctx, retNow.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a session active at the cutoff must stay, pruned %v", got)
	}
	if _, err := s.Get(ctx, exact.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPruneSkipsSessionWithRunningBatch(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	busy := agedSession(t, d, s, "cron", "cron", 90*24*time.Hour)
	addBatch(t, d, "b-run", busy.ID, "running")
	got, err := s.PruneAutomatic(ctx, retNow.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pruned %v, want none", got)
	}
	if _, err := s.Get(ctx, busy.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPruneManyInBatches(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	n := pruneBatch*2 + 3
	for range n {
		agedSession(t, d, s, "subagent", "cli", 10*24*time.Hour)
	}
	got, err := s.PruneAutomatic(ctx, retNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("pruned %d, want %d", len(got), n)
	}
	if left := rowCount(t, d, `SELECT count(*) FROM sessions`); left != 0 {
		t.Fatalf("%d sessions left", left)
	}
	if left := rowCount(t, d, `SELECT count(*) FROM messages`); left != 0 {
		t.Fatalf("%d messages left", left)
	}
}

func TestPruneRollsBackOnFailure(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	old := agedSession(t, d, s, "review", "cli", 90*24*time.Hour)
	// A trigger makes the last statement of the transaction fail.
	if _, err := d.ExecContext(ctx, `CREATE TRIGGER fail_delete BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneAutomatic(ctx, retNow); err == nil {
		t.Fatal("want the injected failure")
	}
	if _, err := s.Get(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if n := related(t, d, old.ID); n != 3 {
		t.Fatalf("the failed transaction must keep all related rows, have %d", n)
	}
}

func TestPruneKeepsForksOfDeletedSessions(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	old := agedSession(t, d, s, "cron", "cron", 90*24*time.Hour)
	fork, err := s.Fork(ctx, old.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE sessions SET kind = 'chat' WHERE id = ?`, fork.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneAutomatic(ctx, retNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "" {
		t.Fatalf("the fork must lose its parent_id, have %q", got.ParentID)
	}
}

func TestDeleteOrphansChildSessions(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	ctx := context.Background()
	parent, _ := s.Create(ctx, Session{Key: "cli:user", Source: "cli"})
	fork, err := s.Fork(ctx, parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	child, _ := s.Create(ctx, Session{Source: "cli", Kind: "subagent", ParentID: parent.ID})
	other, _ := s.Create(ctx, Session{Source: "cli", Kind: "review", ParentID: "someone-else"})
	addBatch(t, d, "b-done", parent.ID, "done")
	addBatch(t, d, "b-run", parent.ID, "running")
	if err := s.Delete(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fork.ID, child.ID} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("a child session must stay: %v", err)
		}
		if got.ParentID != "" {
			t.Fatalf("the child must lose its parent_id, have %q", got.ParentID)
		}
	}
	if got, _ := s.Get(ctx, other.ID); got.ParentID != "someone-else" {
		t.Fatal("Delete must not change the children of other sessions")
	}
	// A finished batch goes with its parent. A running batch stays until it ends.
	if n := rowCount(t, d, `SELECT count(*) FROM delegation_batches WHERE id = 'b-done'`); n != 0 {
		t.Fatal("the finished batch of the session must be deleted")
	}
	if n := rowCount(t, d, `SELECT count(*) FROM delegation_batches WHERE id = 'b-run'`); n != 1 {
		t.Fatal("the running batch must stay")
	}
}
