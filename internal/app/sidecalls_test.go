package app

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// newVisionApp returns an App whose primary entry has vision: true. The test closes it at the end.
func newVisionApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: " + fakeProviderURL(t) +
		"\n    api_key: k\n    model: m\n    vision: true\npricing:\n  sources: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// sidePNG writes a small PNG file and returns its path.
func sidePNG(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "side.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return p
}

// auxRows returns the number of aux sessions whose parent is parentID, and the contents of
// their rows in order. It closes the rows of the session query before it reads the messages,
// because state.db has one connection.
func auxRows(t *testing.T, a *App, parentID string) (n int, rows []sessions.StoredMessage) {
	t.Helper()
	q, err := a.DB.QueryContext(ctx, `SELECT id FROM sessions WHERE kind = ? AND parent_id = ? ORDER BY started_at`,
		sessions.KindAux, parentID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for q.Next() {
		var id string
		if err := q.Scan(&id); err != nil {
			q.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := q.Err(); err != nil {
		q.Close()
		t.Fatal(err)
	}
	q.Close()
	for _, id := range ids {
		msgs, err := a.Store.Messages(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, msgs...)
	}
	return len(ids), rows
}

func TestSideCallsRecordInOneAuxSession(t *testing.T) {
	a := newVisionApp(t)
	s, err := a.Store.Create(ctx, sessions.Session{Key: "cli:t", Source: "cli", Kind: "chat", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// view_image: the tool in the registry has the recorder.
	res := a.Registry.Dispatch(ctx, "view_image", json.RawMessage(fmt.Sprintf(`{"path":%q}`, sidePNG(t))),
		a.NewEnv(s, "cli"), map[string]bool{"view_image": true})
	if res.IsError {
		t.Fatal(res.Text)
	}
	// The compaction summary and the smart judge get the same recorder.
	if a.Agent.Compactor.Record == nil {
		t.Fatal("the compactor has no recorder")
	}
	a.Agent.Compactor.Record(ctx, s.ID, "compaction", a.Prov, provider.Result{Usage: provider.Usage{InputTokens: 5000, OutputTokens: 400}})
	j, ok := a.Gate.Judge.(security.ProviderJudge)
	if !ok || j.Record == nil {
		t.Fatalf("the judge has no recorder: %#v", a.Gate.Judge)
	}
	j.Record(ctx, s.ID, "approval", a.Prov, provider.Result{Usage: provider.Usage{InputTokens: 900, OutputTokens: 12}})

	n, rows := auxRows(t, a, s.ID)
	if n != 1 || len(rows) != 3 {
		t.Fatalf("want 1 aux session with 3 rows, got %d sessions and %d rows", n, len(rows))
	}
	for i, want := range []string{"vision", "compaction", "approval"} {
		r := rows[i]
		if r.Msg.Role != provider.RoleAssistant || r.Msg.Content != want || r.Meta.Model == "" {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	if rows[1].Meta.Usage.InputTokens != 5000 || rows[2].Meta.Usage.OutputTokens != 12 {
		t.Fatalf("%+v %+v", rows[1].Meta.Usage, rows[2].Meta.Usage)
	}
}

func TestStickerDescriberRecordsWithNoSession(t *testing.T) {
	a := newVisionApp(t)
	desc := a.stickerDescriber()
	if desc == nil {
		t.Fatal("a vision provider exists, so the describer must exist")
	}
	if _, err := desc(ctx, sidePNG(t), "Describe this sticker."); err != nil {
		t.Fatal(err)
	}
	n, rows := auxRows(t, a, "")
	if n != 1 || len(rows) != 1 || rows[0].Msg.Content != "sticker" {
		t.Fatalf("%d %+v", n, rows)
	}
	// With no vision provider, the describer is nil, so the inbox gives the fallback note.
	if newApp(t).stickerDescriber() != nil {
		t.Fatal("no vision provider: the describer must be nil")
	}
}
