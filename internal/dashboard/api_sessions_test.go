package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// sessRedact masks the one test secret.
func sessRedact(s string) string { return strings.ReplaceAll(s, "sk-live-123", "***") }

// sessionsTestMux returns a mux with the sessions routes, the store, and the DB.
func sessionsTestMux(t *testing.T) (*http.ServeMux, *sessions.Store, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := sessions.New(d)
	api := &SessionsAPI{
		D: Deps{DB: d, Sessions: st, Redact: sessRedact},
		ChatTitle: func(chatID, threadID string) (string, string, bool) {
			if chatID == "42" && threadID == "7" {
				return "Ops", "Alerts", true
			}
			return "", "", false
		},
	}
	mux := http.NewServeMux()
	api.Register(mux)
	return mux, st, d
}

// sessGet runs a GET on h, decodes a 200 body into v, and returns the status.
func sessGet(t *testing.T, h http.Handler, path string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK && v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body)
		}
	}
	return rec.Code
}

// A session with 10,000 messages loads in pages. No reply has more than 200 rows.
func TestMessagesPagedLimit(t *testing.T) {
	mux, st, d := sessionsTestMux(t)
	ctx := context.Background()
	sess, err := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Title: "long"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 10000)
		INSERT INTO messages (session_id, role, content, timestamp)
		SELECT ?, CASE i % 2 WHEN 1 THEN 'user' ELSE 'assistant' END, 'message ' || i, 1763600000000 + i FROM n`, sess.ID); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + sess.ID + "/messages"
	var firstPage MessagePage
	first := &firstPage.Rows
	if code := sessGet(t, mux, base, &firstPage); code != 200 || len(*first) != 50 ||
		(*first)[0].Text != "message 9951" || (*first)[49].Text != "message 10000" ||
		firstPage.FirstID != (*first)[0].ID || firstPage.LastID != (*first)[49].ID || !firstPage.HasOlder || firstPage.HasNewer {
		t.Fatalf("%d %d %+v", code, len(*first), firstPage.HasOlder)
	}
	var big MessagePage
	if code := sessGet(t, mux, base+"?limit=100000", &big); code != 200 || len(big.Rows) != 200 || big.Rows[199].Text != "message 10000" {
		t.Fatalf("%d %d", code, len(big.Rows))
	}
	// Walk back to the first message with "before".
	seen := map[int64]bool{}
	before, pages := int64(0), 0
	for {
		path := base + "?limit=200"
		if before > 0 {
			path += "&before=" + strconv.FormatInt(before, 10)
		}
		var mp MessagePage
		if code := sessGet(t, mux, path, &mp); code != 200 {
			t.Fatal(path, code)
		}
		page := mp.Rows
		if len(page) == 0 {
			break
		}
		if len(page) > 200 {
			t.Fatalf("a page has %d rows", len(page))
		}
		for i, m := range page {
			if seen[m.ID] || (i > 0 && page[i-1].ID >= m.ID) {
				t.Fatalf("message %d is twice or out of order", m.ID)
			}
			seen[m.ID] = true
		}
		before, pages = mp.FirstID, pages+1
		if !mp.HasOlder {
			break
		}
	}
	if len(seen) != 10000 || pages != 50 {
		t.Fatalf("%d messages in %d pages", len(seen), pages)
	}
	var after MessagePage
	if code := sessGet(t, mux, base+"?after="+strconv.FormatInt((*first)[47].ID, 10), &after); code != 200 ||
		len(after.Rows) != 2 || after.Rows[1].Text != "message 10000" || after.HasNewer || !after.HasOlder {
		t.Fatalf("%d %+v", code, after)
	}
	// An empty page tells if messages exist on the other side of its cursor.
	var none MessagePage
	if code := sessGet(t, mux, base+"?after="+strconv.FormatInt((*first)[49].ID, 10), &none); code != 200 ||
		len(none.Rows) != 0 || none.FirstID != 0 || none.LastID != 0 || none.HasNewer || !none.HasOlder {
		t.Fatalf("%d %+v", code, none)
	}
	if code := sessGet(t, mux, base+"?before=10&after=2", nil); code != http.StatusBadRequest {
		t.Fatal(code)
	}
}

func TestSessionsListAPI(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	dash, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Title: "Deploy sk-live-123"})
	tg, _ := st.Create(ctx, sessions.Session{Key: "telegram:42:7", Source: "telegram", ChatID: "42", ThreadID: "7", Title: "ops"})
	tg2, _ := st.Create(ctx, sessions.Session{Key: "telegram:43", Source: "telegram", ChatID: "43"})
	cli, _ := st.Create(ctx, sessions.Session{Key: "cli:user", Source: "cli", Title: "notes"})
	child, _ := st.Create(ctx, sessions.Session{Source: ChannelName, Kind: sessions.KindSubagent, ParentID: dash.ID})
	if _, err := st.Append(ctx, dash.ID, provider.Message{Role: provider.RoleAssistant, Content: "ok"},
		sessions.MessageMeta{Model: "glm-4.7", CostUSD: 0.25}); err != nil {
		t.Fatal(err)
	}
	var page SessionPage
	if code := sessGet(t, mux, "/api/sessions?limit=10", &page); code != 200 || page.Total != 4 || len(page.Rows) != 4 {
		t.Fatalf("%d %+v", code, page)
	}
	rows := map[string]SessionRow{}
	for _, r := range page.Rows {
		rows[r.ID] = r
	}
	for id, want := range map[string]struct {
		label    string
		readOnly bool
	}{
		dash.ID: {"Dashboard", false},
		tg.ID:   {"Telegram › Ops › Alerts", true},
		tg2.ID:  {"Telegram › 43", true},
		cli.ID:  {"CLI", true},
	} {
		if r := rows[id]; r.SourceLabel != want.label || r.ReadOnly != want.readOnly {
			t.Errorf("%s: %q %v, want %q %v", id, r.SourceLabel, r.ReadOnly, want.label, want.readOnly)
		}
	}
	d := rows[dash.ID]
	if d.Title != "Deploy ***" || d.Messages != 1 || d.CostUSD != 0.25 || d.Source != "dashboard" || d.Kind != "chat" ||
		len(d.Children) != 1 || d.Children[0].ID != child.ID || !d.Children[0].ReadOnly || d.Children[0].Kind != sessions.KindSubagent {
		t.Fatalf("%+v", d)
	}
	var tgPage SessionPage
	if code := sessGet(t, mux, "/api/sessions?source=telegram", &tgPage); code != 200 || tgPage.Total != 2 {
		t.Fatalf("%d %+v", code, tgPage)
	}
	var archived SessionPage
	if code := sessGet(t, mux, "/api/sessions?archived=1", &archived); code != 200 || archived.Total != 0 || len(archived.Rows) != 0 {
		t.Fatalf("%d %+v", code, archived)
	}
	var one SessionRow
	if code := sessGet(t, mux, "/api/sessions/"+dash.ID, &one); code != 200 || one.ID != dash.ID || len(one.Children) != 1 {
		t.Fatalf("%d %+v", code, one)
	}
	if code := sessGet(t, mux, "/api/sessions/nope", nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	if code := sessGet(t, mux, "/api/sessions?limit=x", nil); code != http.StatusBadRequest {
		t.Fatal(code)
	}
}

func TestTranscriptViews(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	add := func(m provider.Message, meta sessions.MessageMeta) int64 {
		t.Helper()
		id, err := st.Append(ctx, sess.ID, m, meta)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add(provider.Message{Role: provider.RoleUser, Content: "deploy with sk-live-123"}, sessions.MessageMeta{})
	call := add(provider.Message{Role: provider.RoleAssistant, Reasoning: &provider.Reasoning{Text: "think about it"},
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: `{"command":"echo sk-live-123"}`}}},
		sessions.MessageMeta{Usage: provider.Usage{InputTokens: 120, OutputTokens: 30, CacheReadTokens: 100, FirstTokenMS: 400, TotalMS: 1500},
			Model: "glm-4.7", Provider: "primary", CostUSD: 0.002})
	toolRow := add(provider.Message{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "terminal", Content: strings.Repeat("x", 20000)},
		sessions.MessageMeta{Observed: true})
	add(provider.Message{Role: provider.RoleAssistant, Content: "done"}, sessions.MessageMeta{Model: "glm-4.7", Provider: "primary"})

	var page MessagePage
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages", &page); code != 200 || len(page.Rows) != 3 {
		t.Fatalf("%d %+v", code, page)
	}
	views := page.Rows
	if views[0].Role != "user" || views[0].Text != "deploy with ***" {
		t.Fatalf("%+v", views[0])
	}
	v := views[1]
	if v.ID != call || v.Reasoning != "think about it" || v.ReasoningTokens != 3 || v.Model != "glm-4.7" || v.Provider != "primary" ||
		v.In != 120 || v.Out != 30 || v.Cached != 100 || v.FirstMS != 400 || v.LatencyMS != 1500 || v.CostUSD != 0.002 ||
		v.Unpriced || len(v.Tools) != 1 {
		t.Fatalf("%+v", v)
	}
	tv := v.Tools[0]
	if tv.Call != "c1" || tv.Name != "terminal" || tv.Emoji != tools.Emoji("terminal") || tv.Args != `{"command":"echo ***"}` ||
		tv.State != "done" || len(tv.Text) != 16384 || !tv.Cut {
		t.Fatalf("%+v", tv)
	}
	if views[2].Text != "done" || !views[2].Unpriced {
		t.Fatalf("%+v", views[2])
	}
	// "Show all" reads one saved row with the full text.
	var one MessageView
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages/"+strconv.FormatInt(toolRow, 10), &one); code != 200 ||
		one.Role != "tool" || len(one.Tools) != 1 || len(one.Tools[0].Text) != 20000 || one.Tools[0].Cut {
		t.Fatalf("%d %+v", code, one.Tools)
	}
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages/999999", nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	// A search hit opens the messages around it.
	var around MessagePage
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages?around="+strconv.FormatInt(call, 10), &around); code != 200 || len(around.Rows) != 3 {
		t.Fatalf("%d %+v", code, around)
	}
	// "Show all" on the call reads the full text of its result.
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages/"+strconv.FormatInt(call, 10), &one); code != 200 ||
		one.ID != call || len(one.Tools) != 1 || len(one.Tools[0].Text) != 20000 || one.Tools[0].Cut {
		t.Fatalf("%d %+v", code, one)
	}
}

func TestTranscriptCompactionBlock(t *testing.T) {
	mux, st, d := sessionsTestMux(t)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	var ids []int64
	for _, m := range []provider.Message{{Role: provider.RoleUser, Content: "one"}, {Role: provider.RoleAssistant, Content: "two"},
		{Role: provider.RoleUser, Content: "three"}} {
		id, err := st.Append(ctx, sess.ID, m, sessions.MessageMeta{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO compactions (session_id, from_message_id, upto_message_id, summary, created_at)
		VALUES (?, ?, ?, ?, ?)`, sess.ID, ids[0], ids[1], "summary sk-live-123", 1763600000000); err != nil {
		t.Fatal(err)
	}
	var page MessagePage
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages", &page); code != 200 || len(page.Rows) != 4 {
		t.Fatalf("%d %+v", code, page)
	}
	views := page.Rows
	if !views[0].Compacted || !views[1].Compacted || views[3].Compacted || views[3].Text != "three" {
		t.Fatalf("%+v", views)
	}
	if s := views[2]; s.ID >= 0 || s.Role != "summary" || s.Text != "summary ***" || !s.Compacted {
		t.Fatalf("%+v", s)
	}
}

func TestSessionExportRoute(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	for _, c := range []string{"a", "b"} {
		if _, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: c}, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	if code := sessGet(t, mux, "/api/sessions/nope/export", nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/export", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="`+sess.ID+`.jsonl"` ||
		strings.Count(rec.Body.String(), "\n") != 2 {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	// The temporary file is gone after the reply.
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Fatal(left, err)
	}
}

func TestSearchRoute(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Title: "Deploy sk-live-123"})
	mid, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "deploy with sk-live-123"}, sessions.MessageMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var hits []SearchHitView
	if code := sessGet(t, mux, "/api/search?q=deploy", &hits); code != 200 || len(hits) != 1 ||
		hits[0].SessionID != sess.ID || hits[0].MessageID != mid || hits[0].Snippet != "[deploy] with ***" || hits[0].Title != "Deploy ***" {
		t.Fatalf("%d %+v", code, hits)
	}
	for _, q := range []string{"", "%22open"} {
		if code := sessGet(t, mux, "/api/search?q="+q, nil); code != http.StatusBadRequest {
			t.Errorf("q=%q: %d", q, code)
		}
	}
	// offset, source, kind, and archived reach the SQL of the search.
	other, err := st.Create(ctx, sessions.Session{Key: "telegram:dm:1", Source: "telegram", ChatID: "1", Kind: "chat", Title: "Phone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(ctx, other.ID, provider.Message{Role: provider.RoleUser, Content: "deploy from the phone"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	search := func(query string) []SearchHitView {
		t.Helper()
		var out []SearchHitView
		if code := sessGet(t, mux, "/api/search?q=deploy&"+query, &out); code != 200 {
			t.Fatalf("%s: %d", query, code)
		}
		return out
	}
	if h := search("source=telegram"); len(h) != 1 || h[0].SessionID != other.ID || h[0].Source != "telegram" || h[0].Kind != "chat" {
		t.Fatalf("%+v", h)
	}
	if h := search("limit=1&offset=1"); len(h) != 1 || h[0].SessionID != sess.ID {
		t.Fatalf("the second hit, newest first: %+v", h)
	}
	if h := search("kind=cron"); len(h) != 0 {
		t.Fatalf("%+v", h)
	}
	if h := search("archived=true"); len(h) != 0 {
		t.Fatalf("%+v", h)
	}
	if h := search("archived=false"); len(h) != 2 {
		t.Fatalf("%+v", h)
	}
	for _, q := range []string{"archived=maybe", "offset=-1", "offset=x"} {
		if code := sessGet(t, mux, "/api/search?q=deploy&"+q, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
}

// The list reads the title search from "search".
func TestSessionsListSearchParam(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	_, _ = st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Title: "Deploy plan"})
	_, _ = st.Create(ctx, sessions.Session{Key: "cli:user", Source: "cli", Title: "notes"})
	var page SessionPage
	if code := sessGet(t, mux, "/api/sessions?search=deploy", &page); code != 200 || page.Total != 1 {
		t.Fatalf("%d %+v", code, page)
	}
}

// A transcript tool line has a redacted preview, and one route gives the full result.
func TestTranscriptToolPreviewAndFullResult(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	if _, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: `{"command":"echo sk-live-123"}`}}}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "terminal",
		Content: "out sk-live-123 " + strings.Repeat("x", 20000)}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	var page MessagePage
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/messages", &page); code != 200 || len(page.Rows) != 1 || len(page.Rows[0].Tools) != 1 {
		t.Fatalf("%d %+v", code, page)
	}
	views := page.Rows
	if p := views[0].Tools[0].Preview; !strings.Contains(p, "echo ***") || strings.Contains(p, "sk-live") {
		t.Fatalf("preview %q", p)
	}
	var tv ToolView
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/tools/c1", &tv); code != 200 ||
		tv.Call != "c1" || tv.Name != "terminal" || tv.Cut || len(tv.Text) < 20000 || strings.Contains(tv.Text, "sk-live") ||
		!strings.Contains(tv.Preview, "echo ***") {
		t.Fatalf("%d %q %+v", code, tv.Text[:min(40, len(tv.Text))], tv)
	}
	if code := sessGet(t, mux, "/api/sessions/"+sess.ID+"/tools/nope", nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	if code := sessGet(t, mux, "/api/sessions/other/tools/c1", nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
}

// toolTurnSession saves one user message, then calls tool calls, each in its own assistant
// message when parallel is false, or all in one assistant message when parallel is true. Each
// call has its result row. A final answer ends the turn.
func toolTurnSession(t *testing.T, st *sessions.Store, calls int, parallel bool) string {
	t.Helper()
	ctx := context.Background()
	sess, err := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	if err != nil {
		t.Fatal(err)
	}
	add := func(m provider.Message) {
		t.Helper()
		if _, err := st.Append(ctx, sess.ID, m, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(i int) provider.ToolCall {
		return provider.ToolCall{ID: "c" + strconv.Itoa(i), Name: "terminal", Args: `{"command":"echo ` + strconv.Itoa(i) + `"}`}
	}
	result := func(i int) provider.Message {
		return provider.Message{Role: provider.RoleTool, ToolCallID: "c" + strconv.Itoa(i), ToolName: "terminal", Content: "out " + strconv.Itoa(i)}
	}
	add(provider.Message{Role: provider.RoleUser, Content: "run the checks"})
	if parallel {
		var all []provider.ToolCall
		for i := range calls {
			all = append(all, call(i))
		}
		add(provider.Message{Role: provider.RoleAssistant, ToolCalls: all})
		for i := range calls {
			add(result(i))
		}
	} else {
		for i := range calls {
			add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call(i)}})
			add(result(i))
		}
	}
	add(provider.Message{Role: provider.RoleAssistant, Content: "final answer"})
	return sess.ID
}

// checkToolTurn checks that the views show each call once with its result, show no result
// alone, and end with the final answer.
func checkToolTurn(t *testing.T, views []MessageView, calls int) {
	t.Helper()
	seen := map[string]int{}
	ids := map[int64]bool{}
	for _, v := range views {
		if ids[v.ID] {
			t.Fatalf("message %d shows twice", v.ID)
		}
		ids[v.ID] = true
		if v.Role == "tool" {
			t.Fatalf("a tool result shows alone: %+v", v.Tools)
		}
		for _, tv := range v.Tools {
			seen[tv.Call]++
			if want := "out " + strings.TrimPrefix(tv.Call, "c"); tv.Text != want {
				t.Fatalf("call %s has the text %q, want %q", tv.Call, tv.Text, want)
			}
		}
	}
	for i := range calls {
		if n := seen["c"+strconv.Itoa(i)]; n != 1 {
			t.Fatalf("call c%d shows %d times", i, n)
		}
	}
	if len(views) == 0 || views[0].Text != "run the checks" || views[len(views)-1].Text != "final answer" {
		t.Fatalf("the first or the last message is missing: %d views", len(views))
	}
}

// Paging with the raw cursors reaches every row of a turn with many tool calls. No page puts
// a call and its result on two pages.
func TestMessagesPageToolTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		parallel bool
		limit    int
	}{
		{"one call for each message", false, 50},
		{"one call for each message, small pages", false, 7},
		{"parallel calls", true, 50},
		{"parallel calls, small pages", true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, st, _ := sessionsTestMux(t)
			const calls = 30
			id := toolTurnSession(t, st, calls, tc.parallel)
			base := "/api/sessions/" + id + "/messages?limit=" + strconv.Itoa(tc.limit)
			get := func(q string) MessagePage {
				t.Helper()
				var p MessagePage
				if code := sessGet(t, mux, base+q, &p); code != 200 {
					t.Fatal(q, code)
				}
				for _, v := range p.Rows {
					if v.Role == "tool" {
						t.Fatalf("%s: a tool result shows alone", q)
					}
				}
				return p
			}
			// The chat loads the newer pages after the user message, like loadNewer.
			first := get("&before=" + strconv.Itoa(1<<30))
			for first.HasOlder {
				first = get("&before=" + strconv.FormatInt(first.FirstID, 10))
			}
			userID := first.Rows[0].ID
			views := []MessageView{first.Rows[0]}
			cursor, pages := userID, 0
			for {
				p := get("&after=" + strconv.FormatInt(cursor, 10))
				if p.FirstID <= cursor && len(p.Rows) > 0 {
					t.Fatalf("a newer page starts at %d, after %d", p.FirstID, cursor)
				}
				views = append(views, p.Rows...)
				cursor, pages = p.LastID, pages+1
				if !p.HasNewer {
					break
				}
				if pages > 100 {
					t.Fatal("the pages do not end")
				}
			}
			checkToolTurn(t, views, calls)
			// The transcript loads the older pages from the newest page.
			p := get("")
			views = p.Rows
			for p.HasOlder {
				p = get("&before=" + strconv.FormatInt(p.FirstID, 10))
				views = append(slices.Clone(p.Rows), views...)
			}
			checkToolTurn(t, views, calls)
		})
	}
}

// A page that starts with the results of a call that the client has already moves back to the
// call. This occurs when a page loads after the call is saved and before its results are.
func TestMessagesPageResultsAfterCall(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	sess, _ := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID})
	if _, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "go"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	call, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "c0", Name: "terminal", Args: `{}`}}}, sessions.MessageMeta{})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + sess.ID + "/messages"
	var p MessagePage
	if code := sessGet(t, mux, base, &p); code != 200 || p.LastID != call || p.Rows[1].Tools[0].Text != "" {
		t.Fatalf("%d %+v", code, p)
	}
	res, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleTool, ToolCallID: "c0", ToolName: "terminal", Content: "out 0"},
		sessions.MessageMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var next MessagePage
	if code := sessGet(t, mux, base+"?after="+strconv.FormatInt(call, 10), &next); code != 200 || len(next.Rows) != 1 ||
		next.Rows[0].ID != call || next.Rows[0].Tools[0].Text != "out 0" || next.FirstID != call || next.LastID != res {
		t.Fatalf("%d %+v", code, next)
	}
}

// Each message has its time, and a child row names its parent.
func TestTranscriptTimesAndParent(t *testing.T) {
	mux, st, _ := sessionsTestMux(t)
	ctx := context.Background()
	parent, err := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Kind: "chat", Title: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID,
		Kind: sessions.KindSubagent, ParentID: parent.ID, Title: "Child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(ctx, child.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	var page MessagePage
	if code := sessGet(t, mux, "/api/sessions/"+child.ID+"/messages", &page); code != 200 || len(page.Rows) != 1 ||
		page.Rows[0].Created.IsZero() || time.Since(page.Rows[0].Created) > time.Minute {
		t.Fatalf("%d %+v", code, page)
	}
	var row SessionRow
	if code := sessGet(t, mux, "/api/sessions/"+child.ID, &row); code != 200 || row.ParentID != parent.ID {
		t.Fatalf("%d %+v", code, row)
	}
	if code := sessGet(t, mux, "/api/sessions/"+parent.ID, &row); code != 200 || row.ParentID != "" ||
		len(row.Children) != 1 || row.Children[0].ParentID != parent.ID {
		t.Fatalf("%d %+v", code, row)
	}
}
