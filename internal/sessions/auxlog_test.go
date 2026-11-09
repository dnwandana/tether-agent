package sessions_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// auxProv is a provider that the tests never stream.
type auxProv struct{}

func (auxProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, errors.New("auxProv: no stream")
}
func (auxProv) Name() string  { return "primary" }
func (auxProv) Model() string { return "glm-5.3" }

// auxOut is a billed result. Its text is not stored.
var auxOut = provider.Result{Text: "a summary", FinishReason: "stop",
	Usage: provider.Usage{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 20}}

func pricedMeta(p provider.Provider, out provider.Result) sessions.MessageMeta {
	return sessions.MessageMeta{Usage: out.Usage, Model: p.Model(), Provider: p.Name(),
		FinishReason: out.FinishReason, CostUSD: 0.003}
}

// openAux opens a new state.db in a temporary folder and returns it with a store over it.
func openAux(t *testing.T) (*db.DB, *sessions.Store) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, sessions.New(d)
}

// auxIDs returns the IDs of the aux sessions whose parent_id is parentID, oldest first.
func auxIDs(t *testing.T, d *db.DB, parentID string) []string {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT id FROM sessions WHERE kind = 'aux' AND parent_id = ? ORDER BY started_at, id`, parentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// mustCreate creates a session or stops the test.
func mustCreate(t *testing.T, store *sessions.Store, in sessions.Session) *sessions.Session {
	t.Helper()
	s, err := store.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mustMessages returns the messages of a session or stops the test.
func mustMessages(t *testing.T, store *sessions.Store, id string) []sessions.StoredMessage {
	t.Helper()
	ms, err := store.Messages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestAuxLogRecordCreatesOnceAndReuses(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "telegram:42:7", Source: "telegram",
		ChatID: "42", ThreadID: "7", UserID: "42"})
	l := &sessions.AuxLog{Store: store, Meta: pricedMeta}
	for _, task := range []string{"compaction", "vision"} {
		if err := l.Record(ctx, parent.ID, task, auxProv{}, auxOut); err != nil {
			t.Fatalf("Record %s: %v", task, err)
		}
	}
	// A new AuxLog has an empty cache. It must find the session in the table.
	if err := (&sessions.AuxLog{Store: store, Meta: pricedMeta}).Record(ctx, parent.ID, "approval", auxProv{}, auxOut); err != nil {
		t.Fatalf("Record approval: %v", err)
	}
	ids := auxIDs(t, d, parent.ID)
	if len(ids) != 1 {
		t.Fatalf("aux sessions = %v, want 1", ids)
	}
	aux, err := store.Get(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if aux.Kind != sessions.KindAux || aux.Key != "" || aux.ParentID != parent.ID ||
		aux.Title != "side calls" || aux.Source != "telegram" || aux.ChatID != "42" ||
		aux.ThreadID != "7" || aux.UserID != "42" {
		t.Errorf("aux session = %+v", aux)
	}
	ms := mustMessages(t, store, aux.ID)
	if len(ms) != 3 {
		t.Fatalf("messages = %d, want 3", len(ms))
	}
	for i, task := range []string{"compaction", "vision", "approval"} {
		m := ms[i]
		if m.Msg.Role != provider.RoleAssistant || m.Msg.Content != task {
			t.Errorf("message %d = %s %q, want assistant %q", i, m.Msg.Role, m.Msg.Content, task)
		}
		if m.Meta.Model != "glm-5.3" || m.Meta.Provider != "primary" ||
			m.Meta.Usage.InputTokens != 100 || m.Meta.Usage.CacheReadTokens != 900 ||
			m.Meta.Usage.OutputTokens != 20 || m.Meta.CostUSD != 0.003 {
			t.Errorf("message %d meta = %+v", i, m.Meta)
		}
	}
}

// Two view_image calls in one tool round run at the same time. They must share one aux
// session. The test uses 8 calls to make a race without the lock more likely.
func TestAuxLogConcurrentRecordsShareOneSession(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "cli:user", Source: "cli"})
	l := &sessions.AuxLog{Store: store}
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			errs <- l.Record(ctx, parent.ID, "vision", auxProv{}, auxOut)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := auxIDs(t, d, parent.ID)
	if len(ids) != 1 {
		t.Fatalf("aux sessions = %v, want 1", ids)
	}
	if ms := mustMessages(t, store, ids[0]); len(ms) != 8 {
		t.Errorf("messages = %d, want 8", len(ms))
	}
}

// /delete clears parent_id of the aux child of a chat. A call with no session must not take
// that old child, which keeps the chat data of the deleted chat. A new AuxLog has an empty
// ID cache, as after a restart.
func TestAuxLogNoSessionSkipsTheAuxChildOfADeletedChat(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	chat := mustCreate(t, store, sessions.Session{Key: "telegram:42:7", Source: "telegram", ChatID: "42"})
	if err := (&sessions.AuxLog{Store: store}).Record(ctx, chat.ID, "compaction", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	old := auxIDs(t, d, chat.ID)
	if len(old) != 1 {
		t.Fatalf("aux sessions of the chat = %v, want 1", old)
	}
	if err := store.Delete(ctx, chat.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&sessions.AuxLog{Store: store}).Record(ctx, "", "sticker", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	if ms := mustMessages(t, store, old[0]); len(ms) != 1 {
		t.Errorf("messages in the old aux child = %d, want 1 (the sticker went there)", len(ms))
	}
	for _, id := range auxIDs(t, d, "") {
		if id == old[0] {
			continue
		}
		aux, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if aux.ChatID != "" || aux.Source != "telegram" {
			t.Errorf("aux session with no parent = %+v, want no chat ID and the source telegram", aux)
		}
		return
	}
	t.Fatal("no new aux session with no parent")
}

// /delete or retention removes the parent during the call. The row goes to the aux
// session with no parent, and the ranges still count it. A call with no session (the
// sticker description) goes to the same session.
func TestAuxLogDeletedParentAndNoSessionShareTheNoParentSession(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "cli:user", Source: "cli"})
	if err := store.Delete(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	l := &sessions.AuxLog{Store: store, Meta: pricedMeta}
	if err := l.Record(ctx, parent.ID, "vision", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	if ids := auxIDs(t, d, parent.ID); len(ids) != 0 {
		t.Errorf("aux sessions of the deleted parent = %v, want none", ids)
	}
	rows, _, err := obs.UsageReport(ctx, d, time.Now().Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "glm-5.3" || rows[0].Calls != 1 {
		t.Errorf("usage rows = %+v, want one glm-5.3 row with 1 call", rows)
	}
	if err := l.Record(ctx, "", "sticker", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	if err := (&sessions.AuxLog{Store: store}).Record(ctx, "", "sticker", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	ids := auxIDs(t, d, "")
	if len(ids) != 1 {
		t.Fatalf("aux sessions with no parent = %v, want 1", ids)
	}
	aux, err := store.Get(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if aux.Kind != sessions.KindAux || aux.Key != "" || aux.ParentID != "" ||
		aux.Title != "side calls" || aux.Source != "telegram" {
		t.Errorf("aux session = %+v", aux)
	}
	ms := mustMessages(t, store, aux.ID)
	want := []string{"vision", "sticker", "sticker"}
	if len(ms) != len(want) {
		t.Fatalf("messages = %d, want %d", len(ms), len(want))
	}
	for i, w := range want {
		if ms[i].Msg.Content != w {
			t.Errorf("message %d = %q, want %q", i, ms[i].Msg.Content, w)
		}
	}
}

// Retention deletes an old aux session. The next call creates a new one.
func TestAuxLogRecreatesPrunedSession(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "cli:user", Source: "cli"})
	l := &sessions.AuxLog{Store: store}
	if err := l.Record(ctx, parent.ID, "compaction", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	old := auxIDs(t, d, parent.ID)[0]
	if err := store.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, parent.ID, "compaction", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	ids := auxIDs(t, d, parent.ID)
	if len(ids) != 1 || ids[0] == old {
		t.Fatalf("aux sessions = %v, want 1 new session (old %s)", ids, old)
	}
	if ms := mustMessages(t, store, ids[0]); len(ms) != 1 {
		t.Errorf("messages of the new session = %d, want 1", len(ms))
	}
	if ms := mustMessages(t, store, old); len(ms) != 0 {
		t.Errorf("messages of the old session = %d, want 0", len(ms))
	}
}

func TestAuxLogNilMetaUsesResultStamp(t *testing.T) {
	ctx := context.Background()
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "cli:user", Source: "cli"})
	l := &sessions.AuxLog{Store: store}
	if err := l.Record(ctx, parent.ID, "vision", auxProv{},
		provider.Result{Provider: "vision", Model: "qwen-vl", Usage: provider.Usage{OutputTokens: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, parent.ID, "approval", auxProv{},
		provider.Result{FinishReason: "stop", Usage: provider.Usage{OutputTokens: 4}}); err != nil {
		t.Fatal(err)
	}
	ms := mustMessages(t, store, auxIDs(t, d, parent.ID)[0])
	if len(ms) != 2 {
		t.Fatalf("messages = %d, want 2", len(ms))
	}
	if m := ms[0].Meta; m.Provider != "vision" || m.Model != "qwen-vl" || m.Usage.OutputTokens != 3 || m.CostUSD != 0 {
		t.Errorf("vision meta = %+v", m)
	}
	if m := ms[1].Meta; m.Provider != "primary" || m.Model != "glm-5.3" || m.FinishReason != "stop" || m.Usage.OutputTokens != 4 {
		t.Errorf("approval meta = %+v", m)
	}
	if m := sessions.ResultMeta(auxProv{}, provider.Result{Provider: "vision", Model: "qwen-vl"}); m.Provider != "vision" || m.Model != "qwen-vl" {
		t.Errorf("ResultMeta = %+v, want the stamp of the result", m)
	}
}

// The provider already billed the call, so a cancel must not stop the record.
func TestAuxLogRecordsAfterCancel(t *testing.T) {
	d, store := openAux(t)
	parent := mustCreate(t, store, sessions.Session{Key: "cli:user", Source: "cli"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &sessions.AuxLog{Store: store, Meta: pricedMeta}
	if err := l.Record(ctx, parent.ID, "compaction", auxProv{}, auxOut); err != nil {
		t.Fatal(err)
	}
	ids := auxIDs(t, d, parent.ID)
	if len(ids) != 1 {
		t.Fatalf("aux sessions = %v, want 1", ids)
	}
	if ms := mustMessages(t, store, ids[0]); len(ms) != 1 {
		t.Errorf("messages = %d, want 1", len(ms))
	}
}
