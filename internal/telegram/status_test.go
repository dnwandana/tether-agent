package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// apiCall is one request that fakeBot received.
type apiCall struct {
	Method string
	Body   map[string]any
}

// fakeBot is a fake Bot API that records each call. By default it accepts
// every call. Set reply to return an error for some calls.
type fakeBot struct {
	mu     sync.Mutex
	calls  []apiCall
	nextID int
	reply  func(method string, body map[string]any) *APIError
	srv    *httptest.Server
	files  map[string]fakeFile // by file_id
}

// fakeFile is a file that getFile and the file download serve.
type fakeFile struct {
	path string
	data []byte
}

// addFile makes the fake serve a file with the given id, path, and data.
func (b *fakeBot) addFile(id, path string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files = map[string]fakeFile{}
	}
	b.files[id] = fakeFile{path: path, data: data}
}

func newFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	b := &fakeBot{nextID: 100}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBot) serve(w http.ResponseWriter, r *http.Request) {
	if p, ok := strings.CutPrefix(r.URL.Path, "/file/botT/"); ok {
		b.mu.Lock()
		b.calls = append(b.calls, apiCall{Method: "download", Body: map[string]any{"path": p}})
		var data []byte
		found := false
		for _, f := range b.files {
			if f.path == p {
				data, found = f.data, true
			}
		}
		b.mu.Unlock()
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	method := parts[len(parts)-1]
	body := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err == nil {
			for k, v := range r.MultipartForm.Value {
				body[k] = v[0]
			}
			for k, fhs := range r.MultipartForm.File {
				body["_file_field"] = k
				body["_file_name"] = fhs[0].Filename
			}
		}
	} else {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	b.mu.Lock()
	b.calls = append(b.calls, apiCall{Method: method, Body: body})
	var apiErr *APIError
	if b.reply != nil {
		apiErr = b.reply(method, body)
	}
	var result any = true
	if apiErr == nil {
		switch method {
		case "sendMessage", "sendPhoto", "sendDocument", "sendAudio", "sendVoice", "sendVideo":
			b.nextID++
			result = map[string]any{"message_id": b.nextID, "chat": map[string]any{"id": body["chat_id"]}}
		case "editMessageText":
			result = map[string]any{"message_id": body["message_id"], "chat": map[string]any{"id": body["chat_id"]}}
		case "getFile":
			id, _ := body["file_id"].(string)
			f, ok := b.files[id]
			if !ok {
				apiErr = &APIError{Code: 400, Description: "Bad Request: invalid file_id"}
			}
			result = map[string]any{"file_id": id, "file_path": f.path, "file_size": len(f.data)}
		}
	}
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if apiErr != nil {
		env := map[string]any{"ok": false, "error_code": apiErr.Code, "description": apiErr.Description}
		if apiErr.RetryAfter > 0 {
			env["parameters"] = map[string]any{"retry_after": apiErr.RetryAfter}
		}
		_ = json.NewEncoder(w).Encode(env)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// client returns a client for the fake. Rate-limit waits take no time.
func (b *fakeBot) client() *Client {
	c := &Client{Token: "T", BaseURL: b.srv.URL, HC: b.srv.Client()}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func (b *fakeBot) setReply(f func(method string, body map[string]any) *APIError) {
	b.mu.Lock()
	b.reply = f
	b.mu.Unlock()
}

func (b *fakeBot) all() []apiCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]apiCall(nil), b.calls...)
}

func (b *fakeBot) count(method string) int {
	n := 0
	for _, c := range b.all() {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (b *fakeBot) methods() []string {
	var out []string
	for _, c := range b.all() {
		out = append(out, c.Method)
	}
	return out
}

// textsOf returns the "text" of each call of the method, in order.
func (b *fakeBot) textsOf(method string) []string {
	var out []string
	for _, c := range b.all() {
		if c.Method == method {
			s, _ := c.Body["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

// lastText returns the text of the last call that carries text.
func (b *fakeBot) lastText() string {
	calls := b.all()
	for i := len(calls) - 1; i >= 0; i-- {
		if s, ok := calls[i].Body["text"].(string); ok {
			return s
		}
	}
	return ""
}

// lastCall returns the last call of the method.
func (b *fakeBot) lastCall(method string) apiCall {
	calls := b.all()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == method {
			return calls[i]
		}
	}
	return apiCall{}
}

// fakeClock is a clock that moves only when the test moves it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func statusWithFakeAPI(t *testing.T, mode string) (*Status, *fakeBot, *fakeClock) {
	t.Helper()
	api := newFakeBot(t)
	clk := newFakeClock()
	s := &Status{C: api.client(), ChatID: 42, Mode: mode, Interval: 1500 * time.Millisecond, Now: clk.Now}
	t.Cleanup(func() { s.Close(ctx, false) })
	return s, api, clk
}

func TestCollapsesRepeats(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	for i := 0; i < 3; i++ {
		s.Add(ctx, "terminal", "python scrape.py", "{}")
	}
	s.Close(ctx, true)
	if !strings.Contains(api.lastText(), `terminal: "python scrape.py" (×3)`) {
		t.Fatal(api.lastText())
	}
	if strings.Count(api.lastText(), "\n") != 0 {
		t.Fatal("repeats must stay on one line:", api.lastText())
	}
}

func TestEditRateLimit1500ms(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	for i := 0; i < 10; i++ {
		s.Add(ctx, "read_file", fmt.Sprintf("f%d.txt", i), "{}")
		clk.Advance(90 * time.Millisecond)
	}
	if api.count("sendMessage") != 1 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	s.Close(ctx, true)
	if api.count("sendMessage") != 1 || api.count("editMessageText") != 1 {
		t.Fatal(api.methods())
	}
	if strings.Count(api.lastText(), "\n") != 9 {
		t.Fatal("final edit must hold all 10 lines:", api.lastText())
	}
}

func TestEditAfterInterval(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	clk.Advance(1600 * time.Millisecond)
	s.Add(ctx, "read_file", "b", "{}")
	if api.count("editMessageText") != 1 {
		t.Fatal(api.methods())
	}
	if c := api.lastCall("editMessageText"); c.Body["parse_mode"] != nil {
		t.Fatal("status lines are plain text:", c.Body)
	}
}

func TestDeferredFlushWithoutNewLine(t *testing.T) {
	api := newFakeBot(t)
	s := &Status{C: api.client(), ChatID: 1, Mode: "all", Interval: 30 * time.Millisecond}
	defer s.Close(ctx, false)
	s.Add(ctx, "read_file", "a", "{}")
	s.Add(ctx, "read_file", "b", "{}")
	deadline := time.Now().Add(2 * time.Second)
	for api.count("editMessageText") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if api.count("editMessageText") != 1 {
		t.Fatal("pending line was not flushed by the timer:", api.methods())
	}
}

func TestRolloverNear4032(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "verbose")
	long := `{"code":"` + strings.Repeat("x", 900) + `"}`
	for i := 0; i < 6; i++ {
		s.Add(ctx, "execute_code", "", strings.Replace(long, "x", fmt.Sprint(i), 1))
		clk.Advance(2 * time.Second)
	}
	s.Close(ctx, true)
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); UTF16Len(txt) > 4032 {
			t.Fatal("bubble too long:", UTF16Len(txt))
		}
	}
}

func TestResetStartsNewBubble(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	s.Reset()
	s.Add(ctx, "web_fetch", "b", "{}")
	if api.count("sendMessage") != 2 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	if !strings.HasPrefix(api.lastText(), "📄 web_fetch") {
		t.Fatal(api.lastText())
	}
}

func TestResetFlushesPendingLines(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	s.Add(ctx, "read_file", "b", "{}") // throttled
	s.Reset()
	if api.count("editMessageText") != 1 || !strings.Contains(api.lastText(), `"b"`) {
		t.Fatal("the closed bubble must show its last line:", api.methods())
	}
}

func TestNewModeOnlyOnToolChange(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "new")
	s.Add(ctx, "read_file", "a", "{}")
	s.Add(ctx, "read_file", "b", "{}")
	s.Add(ctx, "web_fetch", "c", "{}")
	s.Close(ctx, true)
	if n := strings.Count(api.lastText(), "\n") + 1; n != 2 {
		t.Fatal(api.lastText())
	}
}

func TestOffModeDoesNothing(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "off")
	s.Add(ctx, "read_file", "a", "{}")
	s.Close(ctx, true)
	if len(api.all()) != 0 {
		t.Fatal(api.methods())
	}
}

func TestCleanupDeletesOnSuccess(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.Add(ctx, "read_file", "a", "{}")
	s.Reset()
	s.Add(ctx, "read_file", "b", "{}")
	s.Close(ctx, true)
	if api.count("deleteMessage") != 2 {
		t.Fatal(api.methods())
	}

	s2, api2, _ := statusWithFakeAPI(t, "all")
	s2.Cleanup = true
	s2.Add(ctx, "read_file", "a", "{}")
	s2.Close(ctx, false)
	if api2.count("deleteMessage") != 0 {
		t.Fatal("failed run must keep the status:", api2.methods())
	}

	s3, api3, _ := statusWithFakeAPI(t, "all")
	s3.Add(ctx, "read_file", "a", "{}")
	s3.Close(ctx, true)
	if api3.count("deleteMessage") != 0 {
		t.Fatal("default keeps the status:", api3.methods())
	}
}

func TestFloodControlBacksOff(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "b", "{}") // edit → 429
	if api.count("editMessageText") != 1 {
		t.Fatal("a long retry_after must not be retried inline:", api.methods())
	}
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "c", "{}") // inside the 10 s wait → no edit
	if api.count("editMessageText") != 1 {
		t.Fatal("edit during flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	s.Add(ctx, "read_file", "d", "{}")
	if api.count("editMessageText") != 2 || !strings.Contains(api.lastText(), `"d"`) {
		t.Fatal(api.methods())
	}

	// A permanent edit error switches to one message for each line.
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message to edit not found"}
		}
		return nil
	})
	clk.Advance(2 * time.Second)
	s.Add(ctx, "web_fetch", "e", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, "web_search", "f", "{}")
	sends := api.textsOf("sendMessage")
	if len(sends) != 3 || !strings.Contains(sends[1], "web_fetch") || !strings.HasPrefix(sends[2], "🔍 web_search") {
		t.Fatal(sends)
	}
}

func TestTransientEditErrorKeepsEditing(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	broken := s.C
	s.C = &Client{Token: "T", BaseURL: "http://127.0.0.1:1", HC: &http.Client{Transport: failTransport{}}}
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "b", "{}")
	s.C = broken
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "c", "{}")
	if api.count("editMessageText") != 1 || api.count("sendMessage") != 1 {
		t.Fatal(api.methods())
	}
}

func TestNotModifiedIsSuccess(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, "read_file", "a", "{}")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "b", "{}")
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	s.Add(ctx, "read_file", "c", "{}")
	if api.count("editMessageText") != 2 || api.count("sendMessage") != 1 {
		t.Fatal(api.methods())
	}
}

func TestPreviewCut40(t *testing.T) {
	line := ToolLine("all", "terminal", strings.Repeat("é", 100), "{}")
	start := strings.Index(line, `"`)
	preview := strings.Trim(line[start:], `"`)
	if utf8.RuneCountInString(preview) != 40 || !strings.HasSuffix(preview, "...") {
		t.Fatal(line)
	}
	if got := ToolLine("all", "terminal", "ls -la", "{}"); got != `💻 terminal: "ls -la"` {
		t.Fatal(got)
	}
	if got := ToolLine("all", "unknown_tool", "", "{}"); got != "🔧 unknown_tool..." {
		t.Fatal(got)
	}
	if got := ToolLine("all", "terminal", "a\nb", "{}"); strings.Contains(got, "\n") {
		t.Fatal("preview must be one line:", got)
	}
}

func TestToolLineVerbose(t *testing.T) {
	got := ToolLine("verbose", "read_file", "a.txt", `{"path":"a.txt","limit":5}`)
	if got != "📖 read_file(path, limit)\n{\"path\":\"a.txt\",\"limit\":5}" {
		t.Fatal(got)
	}
	// Empty arguments fall back to the short form, like Hermes.
	if got := ToolLine("verbose", "read_file", "a.txt", "{}"); got != `📖 read_file: "a.txt"` {
		t.Fatal(got)
	}
}

func TestChildLineShownAsIs(t *testing.T) {
	if got := ToolLine("all", `🔀 [2/3] browser_navigate "https://x.org"`, "", "{}"); got != `🔀 [2/3] browser_navigate "https://x.org"` {
		t.Fatal(got)
	}
	if got := ToolLine("all", "🔀 [1/2] read_file", "a.txt", "{}"); got != `🔀 [1/2] read_file "a.txt"` {
		t.Fatal(got)
	}
}

func TestStatusThreadParam(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.ThreadID = "17585"
	s.Add(ctx, "read_file", "a", "{}")
	if c := api.lastCall("sendMessage"); c.Body["message_thread_id"] != float64(17585) {
		t.Fatal(c.Body)
	}
}
