package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
		s.Add(ctx, Source{}, "terminal", "python scrape.py", "{}")
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
		s.Add(ctx, Source{}, "read_file", fmt.Sprintf("f%d.txt", i), "{}")
		clk.Advance(90 * time.Millisecond)
	}
	if api.count("sendMessage") != 1 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	s.Close(ctx, true)
	if api.count("sendMessage") != 1 || api.count("editMessageText") != 1 {
		t.Fatal(api.methods())
	}
	if got := api.lastText(); got != `📖 read_file: "f9.txt" (×10)` {
		t.Fatal("final edit must hold the newest call and the count:", got)
	}
}

func TestGroupsSameTool(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	for _, p := range []string{"a", "b", "c"} {
		s.Add(ctx, Source{}, "terminal", p, "{}")
	}
	s.Close(ctx, true)
	if got := api.lastText(); got != `💻 terminal: "c" (×3)` {
		t.Fatal(got)
	}
}

func TestGroupsKeepPlace(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "patch", "p", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	s.Close(ctx, true)
	if got := api.lastText(); got != "💻 terminal: \"b\" (×2)\n🔧 patch: \"p\"" {
		t.Fatal(got)
	}
}

func TestGroupsTwoDigitCount(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	for i := 0; i < 12; i++ {
		s.Add(ctx, Source{}, "terminal", fmt.Sprint(i), "{}")
	}
	s.Close(ctx, true)
	if got := api.lastText(); !strings.HasSuffix(got, " (×12)") || strings.Contains(got, "\n") {
		t.Fatal(got)
	}
}

func TestGroupsResetStartsNew(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Reset()
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	if got := api.lastText(); got != `💻 terminal: "b"` {
		t.Fatal(got)
	}
}

// fillBubble adds the lines t0, t1, ... until the open bubble has 3 free
// units or less. t0 has a preview of previewMax "p" characters, so a new t0
// call with the same preview does not fit.
func fillBubble(t *testing.T, s *Status) {
	t.Helper()
	full := strings.Repeat("p", previewMax)
	target := statusTextLimit - 3
	total := 0
	for i := 0; ; i++ {
		tool := fmt.Sprintf("t%d", i)
		sep := 0
		if total > 0 {
			sep = 1
		}
		// base is the line length with no preview characters.
		base := UTF16Len(ToolLine("all", tool, "x", "{}")) - 1
		k := target - total - sep - base
		if k < 1 {
			break
		}
		if k > previewMax {
			// Keep room for one more line with a preview of 1 character.
			// Its base can be 1 unit longer (one more digit).
			k = min(previewMax, k-(base+3))
		}
		s.Add(ctx, Source{}, tool, full[:k], "{}")
		total += sep + base + k
	}
	if statusTextLimit-total > 3 {
		t.Fatalf("setup: the bubble has %d free units, want 3 or less", statusTextLimit-total)
	}
}

func TestGroupUpdateSplitsBubble(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	full := strings.Repeat("p", previewMax)
	fillBubble(t, s)
	s.Add(ctx, Source{}, "t0", full, "{}")
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	t0 := ToolLine("all", "t0", full, "{}")
	edits := api.textsOf("editMessageText")
	if len(edits) == 0 {
		t.Fatal("the first bubble must get its pending lines:", api.methods())
	}
	first := edits[len(edits)-1]
	if !strings.HasPrefix(first, t0+"\n") || strings.Contains(first, "(×") {
		t.Fatal("the first bubble must keep its old text:", first[:min(len(first), 80)])
	}
	if sends := api.textsOf("sendMessage"); sends[1] != t0 {
		t.Fatal("the second bubble must start a new line with no count:", sends[1])
	}
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); UTF16Len(txt) > statusTextLimit {
			t.Fatal("bubble too long:", UTF16Len(txt))
		}
	}
}

func TestGroupsConcurrent(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.Add(ctx, Source{}, fmt.Sprintf("t%d", g), fmt.Sprint(i), "{}")
			}
		}()
	}
	wg.Wait()
	s.Close(ctx, true)
	lines := strings.Split(api.lastText(), "\n")
	if len(lines) != 6 {
		t.Fatal(api.lastText())
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, " (×20)") {
			t.Fatal(api.lastText())
		}
	}
}

func TestCloseShowsFinalCounts(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	if api.count("sendMessage") != 1 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	s.Close(ctx, true)
	if got := api.lastText(); got != `💻 terminal: "b" (×2)` {
		t.Fatal(got)
	}
}

func TestVerboseDoesNotGroup(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "verbose")
	s.Add(ctx, Source{}, "terminal", "a", `{"command":"a"}`)
	s.Add(ctx, Source{}, "terminal", "b", `{"command":"b"}`)
	s.Close(ctx, true)
	got := api.lastText()
	if !strings.Contains(got, `{"command":"a"}`) || !strings.Contains(got, `{"command":"b"}`) || strings.Contains(got, "(×") {
		t.Fatal(got)
	}
}

func TestNoEditDoesNotGroup(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	for _, p := range []string{"a", "b", "b", "c"} {
		s.Add(ctx, Source{}, "terminal", p, "{}")
		clk.Advance(2 * time.Second)
	}
	want := []string{`💻 terminal: "a"`, `💻 terminal: "b" (×2)`, `💻 terminal: "c"`}
	if got := api.textsOf("sendMessage"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEditAfterInterval(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	clk.Advance(1600 * time.Millisecond)
	s.Add(ctx, Source{}, "read_file", "b", "{}")
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
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Add(ctx, Source{}, "read_file", "b", "{}")
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
		s.Add(ctx, Source{}, "execute_code", "", strings.Replace(long, "x", fmt.Sprint(i), 1))
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
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Reset()
	s.Add(ctx, Source{}, "web_fetch", "b", "{}")
	if api.count("sendMessage") != 2 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	if !strings.HasPrefix(api.lastText(), "📄 web_fetch") {
		t.Fatal(api.lastText())
	}
}

func TestResetFlushesPendingLines(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Add(ctx, Source{}, "read_file", "b", "{}") // throttled
	s.Reset()
	if api.count("editMessageText") != 1 || !strings.Contains(api.lastText(), `"b"`) {
		t.Fatal("the closed bubble must show its last line:", api.methods())
	}
}

func TestNewModeOnlyOnToolChange(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "new")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	s.Add(ctx, Source{}, "web_fetch", "c", "{}")
	s.Close(ctx, true)
	if n := strings.Count(api.lastText(), "\n") + 1; n != 2 {
		t.Fatal(api.lastText())
	}
}

func TestOffModeDoesNothing(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "off")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Close(ctx, true)
	if len(api.all()) != 0 {
		t.Fatal(api.methods())
	}
}

func TestCleanupDeletesOnSuccess(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Reset()
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	s.Close(ctx, true)
	if api.count("deleteMessage") != 2 {
		t.Fatal(api.methods())
	}

	s2, api2, _ := statusWithFakeAPI(t, "all")
	s2.Cleanup = true
	s2.Add(ctx, Source{}, "read_file", "a", "{}")
	s2.Close(ctx, false)
	if api2.count("deleteMessage") != 0 {
		t.Fatal("failed run must keep the status:", api2.methods())
	}

	s3, api3, _ := statusWithFakeAPI(t, "all")
	s3.Add(ctx, Source{}, "read_file", "a", "{}")
	s3.Close(ctx, true)
	if api3.count("deleteMessage") != 0 {
		t.Fatal("default keeps the status:", api3.methods())
	}
}

func TestFloodControlBacksOff(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}") // edit → 429
	if api.count("editMessageText") != 1 {
		t.Fatal("a long retry_after must not be retried inline:", api.methods())
	}
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "c", "{}") // inside the 10 s wait → no edit
	if api.count("editMessageText") != 1 {
		t.Fatal("edit during flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	s.Add(ctx, Source{}, "read_file", "d", "{}")
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
	s.Add(ctx, Source{}, "web_fetch", "e", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "web_search", "f", "{}")
	sends := api.textsOf("sendMessage")
	if len(sends) != 3 || !strings.Contains(sends[1], "web_fetch") || !strings.HasPrefix(sends[2], "🔍 web_search") {
		t.Fatal(sends)
	}
}

func TestTransientEditErrorKeepsEditing(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	broken := s.C
	s.C = &Client{Token: "T", BaseURL: "http://127.0.0.1:1", HC: &http.Client{Transport: failTransport{}}}
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	s.C = broken
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "c", "{}")
	if api.count("editMessageText") != 1 || api.count("sendMessage") != 1 {
		t.Fatal(api.methods())
	}
}

func TestNotModifiedIsSuccess(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "c", "{}")
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

func TestScreenshotGroups(t *testing.T) {
	// The order of the calls in a screenshot of a delegate_task run with three children.
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "delegate_task", "3 tasks", "")
	names := map[byte]string{'T': "terminal", 'F': "web_fetch", 'S': "web_search"}
	calls := "1T 2F 3S 2F 3S 3S 1T 1T 2F 2S 3F 3F 1T 1T 1T 2F 2F 3S 3F 1T 1T 2F 2F 3S 3F 1T 1T 1T 1T 3S 3F 2F 2F 1T 1T 3S 3F 1T"
	for _, c := range strings.Fields(calls) {
		s.Add(ctx, Source{Batch: 1, Child: c[:1] + "/3"}, names[c[1]], "p", "")
	}
	s.Close(ctx, true)
	want := strings.Join([]string{
		`🔀 delegate_task: "3 tasks"`,
		`🔀 [1/3] terminal "p" (×15)`,
		`🔀 [2/3] web_fetch "p" (×9)`,
		`🔀 [3/3] web_search "p" (×7)`,
		`🔀 [2/3] web_search "p"`,
		`🔀 [3/3] web_fetch "p" (×6)`,
	}, "\n")
	if got := api.lastText(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTwoBatchesSeparateLines(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "a", "")
	s.Add(ctx, Source{}, "delegate_task", "3 tasks", "")
	s.Add(ctx, Source{Batch: 2, Child: "1/3"}, "terminal", "b", "")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "c", "")
	s.Close(ctx, true)
	if got := api.lastText(); got != "🔀 [1/3] terminal \"c\" (×2)\n🔀 delegate_task: \"3 tasks\"\n🔀 [1/3] terminal \"b\"" {
		t.Fatal(got)
	}
}

func TestMainAndChildSameTool(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "m", "")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "x", "")
	s.Close(ctx, true)
	if got := api.lastText(); got != "💻 terminal: \"m\"\n🔀 [1/3] terminal \"x\"" {
		t.Fatal(got)
	}
}

func TestChildPreviewCut(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "read_file", strings.Repeat("a", 60), "")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "", "")
	s.Close(ctx, true)
	want := `🔀 [1/3] read_file "` + strings.Repeat("a", 37) + `..."` + "\n🔀 [1/3] terminal"
	if got := api.lastText(); got != want {
		t.Fatal(got)
	}
}

func TestNewModeChildKey(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "new")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "a", "")
	s.Add(ctx, Source{Batch: 1, Child: "1/3"}, "terminal", "b", "")
	s.Close(ctx, true)
	if got := api.lastText(); got != `🔀 [1/3] terminal "a"` {
		t.Fatal(got)
	}
}

func TestStatusThreadParam(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.ThreadID = "17585"
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	if c := api.lastCall("sendMessage"); c.Body["message_thread_id"] != float64(17585) {
		t.Fatal(c.Body)
	}
}

func TestNewModeResetShowsLine(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "new")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Reset()
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || sends[1] != `💻 terminal: "b"` {
		t.Fatal("the first call after Reset must start a new bubble:", sends)
	}
}

// failEdits makes each edit of the status fail for good from now on.
func failEdits(api *fakeBot) {
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
}

func TestNoEditSendsCount(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "go test", "{}")
	failEdits(api)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "terminal", "go test", "{}")
	want := []string{`💻 terminal: "go test"`, `💻 terminal: "go test" (×2)`}
	if got := api.textsOf("sendMessage"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNoEditSendsGroupLine(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "patch", "p", "{}")
	if api.count("editMessageText") != 2 {
		t.Fatal(api.methods())
	}
	failEdits(api)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "terminal", "c", "{}")
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || sends[1] != `💻 terminal: "c" (×3)` {
		t.Fatalf("the new message must be the group line of the call: %q", sends)
	}
}

func TestNoEditSplitSendsCallLine(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	fillBubble(t, s)
	failEdits(api)
	full := strings.Repeat("p", previewMax)
	s.Add(ctx, Source{}, "t0", full, "{}")
	sends := api.textsOf("sendMessage")
	if t0 := ToolLine("all", "t0", full, "{}"); len(sends) != 2 || sends[1] != t0 {
		t.Fatalf("the line of the call must go out alone, once: %q", sends[1:])
	}
}

// flakyEdits passes each call to the fake. While on is set, each
// editMessageText call fails with a network error and does not reach the fake.
type flakyEdits struct {
	base   http.RoundTripper
	on     atomic.Bool
	failed atomic.Int32 // the edits that failed
}

func (f *flakyEdits) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.on.Load() && strings.HasSuffix(r.URL.Path, "/editMessageText") {
		f.failed.Add(1)
		return nil, errors.New("connection reset")
	}
	return f.base.RoundTrip(r)
}

// withFlakyEdits makes s send its calls through a flakyEdits transport.
func withFlakyEdits(s *Status, api *fakeBot) *flakyEdits {
	f := &flakyEdits{base: api.srv.Client().Transport}
	s.C.HC = &http.Client{Transport: f}
	return f
}

// floodEdits makes each edit of the status get a 429 with a wait of 10 s.
func floodEdits(api *fakeBot) {
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
}

// fireTimer runs the pending status timer now, and returns false when no
// timer is pending. The tests use a fake clock, so a real timer waits 1.5 s or
// more and does not fire during a test.
func fireTimer(s *Status) bool {
	s.mu.Lock()
	tm := s.timer
	s.mu.Unlock()
	if tm == nil || !tm.Stop() {
		return false
	}
	s.onTimer()
	return true
}

// lastEditOf returns the text of the last edit of the message.
func lastEditOf(api *fakeBot, msgID int) string {
	text := ""
	for _, c := range api.all() {
		if c.Method == "editMessageText" && c.Body["message_id"] == float64(msgID) {
			text, _ = c.Body["text"].(string)
		}
	}
	return text
}

// firstBubble is the message ID of the first message that the fake sends.
const firstBubble = 101

func TestResetTransientEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	f := withFlakyEdits(s, api)
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}") // throttled
	f.on.Store(true)
	s.Reset()
	f.on.Store(false)
	if f.failed.Load() != 1 {
		t.Fatal("Reset must try the edit once:", f.failed.Load())
	}
	s.Add(ctx, Source{}, "patch", "p", "{}")
	s.Add(ctx, Source{}, "patch", "q", "{}") // throttled
	if api.count("sendMessage") != 2 || api.count("editMessageText") != 0 {
		t.Fatal(api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer for the closed bubble")
	}
	if got := lastEditOf(api, firstBubble); got != `💻 terminal: "b" (×2)` {
		t.Fatal("the closed bubble must get its last lines:", got, api.methods())
	}
	if api.count("editMessageText") != 1 {
		t.Fatal("the open bubble must wait for the interval:", api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer for the open bubble")
	}
	if got := lastEditOf(api, firstBubble+1); got != `🔧 patch: "q" (×2)` {
		t.Fatal(got, api.methods())
	}
}

func TestResetFloodEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	floodEdits(api)
	s.Reset()
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || api.count("editMessageText") != 1 {
		t.Fatal("edit during flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	if got := lastEditOf(api, firstBubble); api.count("editMessageText") != 2 || got != `💻 terminal: "b" (×2)` {
		t.Fatal(got, api.methods())
	}
}

func TestSplitTransientEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	f := withFlakyEdits(s, api)
	fillBubble(t, s)
	s.mu.Lock()
	want := strings.Join(s.lines, "\n")
	s.mu.Unlock()
	f.on.Store(true)
	s.Add(ctx, Source{}, "t0", strings.Repeat("p", previewMax), "{}")
	f.on.Store(false)
	if f.failed.Load() != 1 || api.count("sendMessage") != 2 {
		t.Fatal(f.failed.Load(), api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer for the closed bubble")
	}
	if got := lastEditOf(api, firstBubble); got != want {
		t.Fatal("the closed bubble must get all its lines:", api.methods())
	}
}

func TestSplitFloodEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	fillBubble(t, s)
	s.mu.Lock()
	want := strings.Join(s.lines, "\n")
	s.mu.Unlock()
	floodEdits(api)
	s.Add(ctx, Source{}, "t0", strings.Repeat("p", previewMax), "{}")
	api.setReply(nil)
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || api.count("editMessageText") != 1 {
		t.Fatal("edit during flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	if got := lastEditOf(api, firstBubble); got != want {
		t.Fatal("the closed bubble must get all its lines:", api.methods())
	}
}

func TestCloseTransientEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	f := withFlakyEdits(s, api)
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	f.on.Store(true)
	s.Close(ctx, true)
	f.on.Store(false)
	s.Add(ctx, Source{}, "patch", "p", "{}") // after Close: no effect
	clk.Advance(2 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after Close")
	}
	if got := lastEditOf(api, firstBubble); got != `💻 terminal: "b" (×2)` {
		t.Fatal("the timer after Close must send the final counts:", got)
	}
	if api.count("sendMessage") != 1 || fireTimer(s) {
		t.Fatal(api.methods())
	}
}

func TestCloseFloodEditRetries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	floodEdits(api)
	s.Close(ctx, true)
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || api.count("editMessageText") != 1 {
		t.Fatal("edit during flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	if got := lastEditOf(api, firstBubble); got != `💻 terminal: "b" (×2)` {
		t.Fatal("the timer after Close must send the final counts:", got)
	}
}

func TestClosedEditStopsAfterThreeTries(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	f := withFlakyEdits(s, api)
	f.on.Store(true)
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	s.Reset()
	for i := 0; i < 5 && fireTimerAfter(s, clk); i++ {
	}
	if n := f.failed.Load(); n != 3 {
		t.Fatal("tries:", n)
	}
	if !strings.Contains(logs.String(), "drop") {
		t.Fatal("no log line for the dropped edit:", logs.String())
	}
}

func TestClosedEditPermanentErrorKeepsEditing(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	f := withFlakyEdits(s, api)
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	f.on.Store(true)
	s.Reset()
	f.on.Store(false)
	api.setReply(func(method string, body map[string]any) *APIError {
		if method == "editMessageText" && body["message_id"] == float64(firstBubble) {
			return &APIError{Code: 400, Description: "Bad Request: message to edit not found"}
		}
		return nil
	})
	s.Add(ctx, Source{}, "patch", "p", "{}")
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || fireTimer(s) {
		t.Fatal("a permanent error must drop the closed edit:", api.methods())
	}
	s.Add(ctx, Source{}, "patch", "q", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "patch", "r", "{}")
	if got := lastEditOf(api, firstBubble+1); api.count("sendMessage") != 2 || got != `🔧 patch: "r" (×3)` {
		t.Fatal("the open bubble must keep its edits:", got, api.methods())
	}
}

func TestCleanupDropsClosedEdits(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	f := withFlakyEdits(s, api)
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "b", "{}")
	f.on.Store(true)
	s.Close(ctx, true)
	if api.count("deleteMessage") != 1 || fireTimer(s) {
		t.Fatal("deleted messages need no edit:", api.methods())
	}
}

// fireTimerAfter moves the clock past the interval and runs the timer.
func fireTimerAfter(s *Status, clk *fakeClock) bool {
	clk.Advance(2 * time.Second)
	return fireTimer(s)
}
