package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// fakeUpdates is a fake getUpdates peer. It returns the queued updates with
// an id at or above the requested offset, and records each offset.
type fakeUpdates struct {
	mu      sync.Mutex
	ups     []Update
	offsets []int64
	fails   int // the number of API error 502 replies before the first ok reply
}

func (f *fakeUpdates) firstOffset() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.offsets) == 0 {
		return -1
	}
	return f.offsets[0]
}

func updatesServer(t *testing.T, ups []Update) (*httptest.Server, *fakeUpdates) {
	t.Helper()
	f := &fakeUpdates{ups: ups}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Offset int64 `json:"offset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.offsets = append(f.offsets, body.Offset)
		if f.fails > 0 {
			f.fails--
			f.mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":502,"description":"Bad Gateway"}`))
			return
		}
		var out []Update
		for _, u := range f.ups {
			if u.UpdateID >= body.Offset {
				out = append(out, u)
			}
		}
		f.mu.Unlock()
		if len(out) == 0 {
			// A real long poll waits. Wait a little, so the loop does not spin.
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": out})
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func client(srv *httptest.Server) *Client {
	return &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOffsetPersistedBeforeHandle(t *testing.T) {
	srv, _ := updatesServer(t, []Update{{UpdateID: 100}, {UpdateID: 101}})
	d := openTestDB(t)
	p := &Poller{C: client(srv), DB: d, TimeoutS: 1}
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	var n atomic.Int32
	var mu sync.Mutex
	var seenOffsets []string
	err := p.Run(c, func(hc context.Context, u Update) {
		// The offset of this update is already stored when handle starts.
		mu.Lock()
		v, _, _ := d.KVGet(context.Background(), offsetKey)
		seenOffsets = append(seenOffsets, v)
		mu.Unlock()
		if n.Add(1) == 2 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("want the context error")
	}
	v, _, _ := d.KVGet(ctx, offsetKey)
	if v != "102" {
		t.Fatal(v)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range seenOffsets {
		if s != "101" && s != "102" {
			t.Fatal("offset not stored before handle:", seenOffsets)
		}
	}
}

func TestRestartDoesNotReplay(t *testing.T) {
	srv, _ := updatesServer(t, []Update{{UpdateID: 100}, {UpdateID: 101}})
	d := openTestDB(t)
	c, cancel := context.WithCancel(ctx)
	var n atomic.Int32
	_ = (&Poller{C: client(srv), DB: d, TimeoutS: 1}).Run(c, func(context.Context, Update) {
		if n.Add(1) == 2 {
			cancel()
		}
	})

	srv2, f2 := updatesServer(t, []Update{{UpdateID: 100}, {UpdateID: 101}})
	c2, cancel2 := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel2()
	var replayed atomic.Int32
	_ = (&Poller{C: client(srv2), DB: d, TimeoutS: 1}).Run(c2, func(context.Context, Update) { replayed.Add(1) })
	if got := f2.firstOffset(); got != 102 {
		t.Fatal("first offset after restart:", got)
	}
	if replayed.Load() != 0 {
		t.Fatal("replayed", replayed.Load())
	}
}

func TestBackoffOnError(t *testing.T) {
	srv, f := updatesServer(t, []Update{{UpdateID: 7}})
	f.fails = 2
	var slept []time.Duration
	p := &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1}
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = p.Run(c, func(context.Context, Update) { close(done) })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handle was not called")
	}
	cancel()
	if !reflect.DeepEqual(slept, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatal(slept)
	}
}

func TestBackoffCapsAt30s(t *testing.T) {
	var got []time.Duration
	d := time.Duration(0)
	for i := 0; i < 8; i++ {
		d = nextBackoff(d)
		got = append(got, d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatal(got)
		}
	}
}

func TestBotCommandsMenu(t *testing.T) {
	want := []BotCommand{
		{Command: "new", Description: "Start a new session"},
		{Command: "sessions", Description: "See and resume sessions"},
		{Command: "stop", Description: "Stop the current task"},
		{Command: "steer", Description: "Send a note to the current task"},
		{Command: "now", Description: "Do my waiting message now"},
		{Command: "goal", Description: "Work toward a goal"},
		{Command: "todo", Description: "Show the task list"},
		{Command: "model", Description: "Change the model"},
		{Command: "effort", Description: "Change the reasoning effort"},
		{Command: "usage", Description: "See tokens and cost"},
		{Command: "help", Description: "See all commands"},
		{Command: "rename", Description: "Change the session title"},
		{Command: "fork", Description: "Copy this session"},
		{Command: "compact", Description: "Make the context shorter"},
		{Command: "prompt", Description: "Change the prompt"},
		{Command: "archive", Description: "Hide this session"},
		{Command: "delete", Description: "Delete a session"},
		{Command: "approve", Description: "Allow the waiting command"},
		{Command: "deny", Description: "Deny the waiting command"},
		{Command: "sethome", Description: "Send cron results here"},
		{Command: "reload_mcp", Description: "Reload the MCP servers"},
		{Command: "restart", Description: "Restart the gateway"},
	}
	if got := BotCommands(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	for _, c := range BotCommands(nil) {
		for _, r := range c.Description {
			if r > 0x2000 {
				t.Errorf("the description of %q has the icon %q", c.Command, r)
			}
		}
		switch c.Command {
		case "start", "reset", "resume":
			t.Errorf("%q must not be in the menu", c.Command)
		}
	}
}

func TestCanonicalCommand(t *testing.T) {
	cases := map[string]string{
		"/reload_mcp":            "/reload-mcp",
		"/reload_mcp@tether_bot": "/reload-mcp@tether_bot",
		"/reload_mcp now":        "/reload-mcp now",
		"/reload_mcpx":           "/reload_mcpx",
		"/new":                   "/new",
		"hello /reload_mcp":      "hello /reload_mcp",
	}
	for in, want := range cases {
		if got := CanonicalCommand(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestRegisterCommands(t *testing.T) {
	var got []any
	srv := tgServer(t, func(method string, body map[string]any) any {
		if method == "setMyCommands" {
			got = body["commands"].([]any)
		}
		return true
	})
	if err := (&Poller{C: client(srv)}).RegisterCommands(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(BotCommands(nil)) {
		t.Fatal(got)
	}
}

func TestEditedMessageReachesHandler(t *testing.T) {
	// Hermes handles an edited message like a new message (python-telegram-bot
	// effective_message), so handle gets the edit in Message.
	edit := &Message{MessageID: 4, Chat: Chat{ID: 6, Type: "private"}, Text: "fixed text"}
	srv, _ := updatesServer(t, []Update{{UpdateID: 9, EditedMessage: edit}})
	p := &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1}
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	got := make(chan Update, 1)
	go func() { _ = p.Run(c, func(_ context.Context, u Update) { got <- u }) }()
	select {
	case u := <-got:
		if u.Message == nil || u.Message.Text != "fixed text" || u.Message.MessageID != 4 {
			t.Fatalf("%+v", u)
		}
		if u.EditedMessage == nil {
			t.Fatal("the handler cannot see that the message is an edit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handle was not called")
	}
}

// chatMsg returns an update with a text message in chat.
func chatMsg(id, chat int64) Update {
	return Update{UpdateID: id, Message: &Message{MessageID: int(id), Chat: Chat{ID: chat, Type: "private"}, Text: strconv.FormatInt(id, 10)}}
}

// runPoller runs p with handle until the test ends.
func runPoller(t *testing.T, ups []Update, handle func(context.Context, Update)) {
	t.Helper()
	srv, _ := updatesServer(t, ups)
	p := &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1}
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(c, handle) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestUpdatesOfOneChatRunInOrder(t *testing.T) {
	// The gateway, the debouncer, and the steer queue need the messages of a chat in order.
	var ups []Update
	for i := int64(1); i <= 6; i++ {
		ups = append(ups, chatMsg(2*i-1, 6), chatMsg(2*i, 7))
	}
	var mu sync.Mutex
	var order []int64
	var inChat6, overlap atomic.Int32
	all := make(chan struct{})
	var n atomic.Int32
	runPoller(t, ups, func(_ context.Context, u Update) {
		if u.Message.Chat.ID == 6 {
			if inChat6.Add(1) > 1 {
				overlap.Add(1)
			}
			// The first updates take longest, so a parallel run changes the order.
			time.Sleep(time.Duration(12-u.UpdateID) * 3 * time.Millisecond)
			mu.Lock()
			order = append(order, u.UpdateID)
			mu.Unlock()
			inChat6.Add(-1)
		}
		if n.Add(1) == int32(len(ups)) {
			close(all)
		}
	})
	select {
	case <-all:
	case <-time.After(5 * time.Second):
		t.Fatal("not all updates were handled")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int64{1, 3, 5, 7, 9, 11}) || overlap.Load() != 0 {
		t.Fatal(order, overlap.Load())
	}
}

func TestOtherChatsAndCallbacksDoNotWait(t *testing.T) {
	// A slow update of chat 6 does not block chat 7 or a button press in chat 6.
	cb := Update{UpdateID: 3, CallbackQuery: &CallbackQuery{ID: "q", Message: &Message{Chat: Chat{ID: 6}}}}
	other, pressed := make(chan struct{}), make(chan struct{})
	released := make(chan bool, 1)
	runPoller(t, []Update{chatMsg(1, 6), chatMsg(2, 7), cb}, func(_ context.Context, u Update) {
		switch {
		case u.CallbackQuery != nil:
			close(pressed)
		case u.Message.Chat.ID == 7:
			close(other)
		default:
			ok := true
			for _, ch := range []chan struct{}{other, pressed} {
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					ok = false
				}
			}
			released <- ok
		}
	})
	select {
	case ok := <-released:
		if !ok {
			t.Fatal("chat 7 or the callback waited for chat 6")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handle was not called")
	}
}

func TestPollerRunWaitsForItsHandlers(t *testing.T) {
	// A gateway stop waits for the receive loop, so Run must not leave a handler behind.
	srv, _ := updatesServer(t, []Update{chatMsg(1, 6), {UpdateID: 2}})
	p := &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1}
	c, cancel := context.WithCancel(ctx)
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	var ended atomic.Int32
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = p.Run(c, func(context.Context, Update) {
			started.Done()
			<-release
			ended.Add(1)
		})
	}()
	started.Wait()
	cancel()
	select {
	case <-runDone:
		t.Fatal("Run returned before its handlers ended")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if ended.Load() != 2 {
		t.Fatal(ended.Load())
	}
}

func TestPollerHandleCtxOutlivesTheRunContext(t *testing.T) {
	srv, _ := updatesServer(t, []Update{chatMsg(1, 6), {UpdateID: 2}})
	type key struct{}
	hctx := context.WithValue(ctx, key{}, "turns")
	p := &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1, HandleCtx: hctx}
	c, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var bad []string
	var n sync.WaitGroup
	n.Add(2)
	go func() {
		n.Wait()
		cancel()
	}()
	_ = p.Run(c, func(got context.Context, u Update) {
		defer n.Done()
		mu.Lock()
		defer mu.Unlock()
		if got.Value(key{}) != "turns" {
			bad = append(bad, "update "+strconv.FormatInt(u.UpdateID, 10)+" did not get HandleCtx")
		}
	})
	if got := hctx.Err(); got != nil || len(bad) != 0 {
		t.Fatal(got, bad)
	}
}

func TestBotMenuHasRestart(t *testing.T) {
	for _, c := range BotCommands(nil) {
		if c.Command == "restart" {
			if c.Description != "Restart the gateway" {
				t.Fatal(c)
			}
			return
		}
	}
	t.Fatal("no restart command in the menu")
}

// The inflight count covers each update from the push until its handler returns. An update
// that waits behind another update of the same chat counts too, so the stop drain waits for it.
func TestChatQueuesCountWaitingUpdates(t *testing.T) {
	var n atomic.Int64
	var wg sync.WaitGroup
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	c := &chatQueues{
		handle:   func(context.Context, Update) { started <- struct{}{}; <-release },
		log:      slog.New(slog.DiscardHandler),
		wg:       &wg,
		inflight: &n,
	}
	c.push(context.Background(), 1, Update{UpdateID: 1})
	c.push(context.Background(), 1, Update{UpdateID: 2})
	<-started
	if got := n.Load(); got != 2 {
		t.Fatalf("inflight %d while one update runs and one waits, want 2", got)
	}
	close(release)
	wg.Wait()
	if got := n.Load(); got != 0 {
		t.Fatalf("inflight %d after the handlers returned, want 0", got)
	}
}

// pollReq is one getUpdates request that scriptServer received.
type pollReq struct {
	Offset int64
	Limit  int // 0 when the request has no limit
}

// emptyBatch is a getUpdates reply with no update.
const emptyBatch = `{"ok":true,"result":[]}`

// scriptServer is a fake getUpdates peer that sends raw reply bodies. reply gets the number
// of the request, from 0, and the request. It returns the body of the reply. The second
// result of scriptServer returns the requests until now.
func scriptServer(t *testing.T, reply func(i int, r pollReq) string) (*httptest.Server, func() []pollReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []pollReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Offset int64 `json:"offset"`
			Limit  int   `json:"limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		req := pollReq{Offset: body.Offset, Limit: body.Limit}
		mu.Lock()
		i := len(reqs)
		reqs = append(reqs, req)
		mu.Unlock()
		out := reply(i, req)
		if out == emptyBatch {
			// A real long poll waits. Wait a little, so the loop does not spin.
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []pollReq {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(reqs)
	}
}

// scriptPoller is a poller for srv. It writes its log to the buffer, and it records each
// backoff wait in slept. A wait takes no time.
func scriptPoller(t *testing.T, srv *httptest.Server) (p *Poller, logb *bytes.Buffer, slept *[]time.Duration) {
	t.Helper()
	logb, slept = &bytes.Buffer{}, &[]time.Duration{}
	p = &Poller{C: client(srv), DB: openTestDB(t), TimeoutS: 1,
		Log: slog.New(slog.NewJSONHandler(logb, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	p.sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return p, logb, slept
}

// runScript runs p until done returns true or 3 seconds pass. Then it stops p and waits for
// Run to return, so the test can read the log and the waits of p.
func runScript(t *testing.T, p *Poller, handle func(context.Context, Update), done func() bool) {
	t.Helper()
	c, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = p.Run(c, handle) }()
	for end := time.Now().Add(3 * time.Second); !done() && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-stopped
}

// limits returns the limit of each request.
func limits(reqs []pollReq) []int {
	out := make([]int, len(reqs))
	for i, r := range reqs {
		out[i] = r.Limit
	}
	return out
}

func TestUnreadableReplyStartsSingleMode(t *testing.T) {
	srv, reqs := scriptServer(t, func(i int, _ pollReq) string {
		if i == 0 {
			return "not json"
		}
		return emptyBatch
	})
	p, logb, slept := scriptPoller(t, srv)
	runScript(t, p, func(context.Context, Update) {}, func() bool { return len(reqs()) >= 2 })
	got := reqs()
	if len(got) < 2 || got[0].Limit != 0 || got[1].Limit != 1 {
		t.Fatalf("limits %v, want 0 and then 1", limits(got))
	}
	if len(*slept) != 0 {
		t.Fatalf("the poller waited %v before the single mode", *slept)
	}
	want := `"level":"WARN","msg":"telegram: cannot read the update batch, read one update at a time"}`
	if strings.Count(logb.String(), want) != 1 {
		t.Fatalf("want one line %s in\n%s", want, logb)
	}
}

func TestEmptyReplyEndsSingleMode(t *testing.T) {
	srv, reqs := scriptServer(t, func(i int, _ pollReq) string {
		if i == 0 {
			return "not json"
		}
		return emptyBatch
	})
	p, _, _ := scriptPoller(t, srv)
	runScript(t, p, func(context.Context, Update) {}, func() bool { return len(reqs()) >= 4 })
	if got := limits(reqs()); len(got) < 4 || !reflect.DeepEqual(got[:4], []int{0, 1, 0, 0}) {
		t.Fatalf("limits %v, want 0, 1, 0, 0", got)
	}
}

func TestSingleModeSkipsTheUpdateThatDoesNotDecode(t *testing.T) {
	ups := []string{
		`{"update_id":1,"message":{"message_id":1,"chat":{"id":6,"type":"private"},"text":"SECRET-1"}}`,
		`{"update_id":2,"message":5}`,
		`{"update_id":3,"message":{"message_id":3,"chat":{"id":6,"type":"private"},"text":"SECRET-3"}}`,
	}
	srv, reqs := scriptServer(t, func(_ int, r pollReq) string {
		var out []string
		for i, u := range ups {
			if int64(i+1) >= r.Offset && (r.Limit == 0 || len(out) < r.Limit) {
				out = append(out, u)
			}
		}
		return `{"ok":true,"result":[` + strings.Join(out, ",") + `]}`
	})
	p, logb, slept := scriptPoller(t, srv)
	var mu sync.Mutex
	var handled []int64
	handle := func(_ context.Context, u Update) {
		mu.Lock()
		defer mu.Unlock()
		handled = append(handled, u.UpdateID)
	}
	// Request 5 is the first request after the single mode: 0 is the batch, 1 to 3 are the
	// three updates, and 4 is the empty reply.
	runScript(t, p, handle, func() bool { return len(reqs()) >= 6 })
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(handled, []int64{1, 3}) {
		t.Fatalf("handled %v, want 1 and 3 in order", handled)
	}
	want := []pollReq{{0, 0}, {0, 1}, {2, 1}, {3, 1}, {4, 1}, {4, 0}}
	if got := reqs(); len(got) < 6 || !reflect.DeepEqual(got[:6], want) {
		t.Fatalf("requests %v, want %v", got, want)
	}
	if v, _, _ := p.DB.KVGet(ctx, offsetKey); v != "4" {
		t.Fatalf("stored offset %q, want 4", v)
	}
	skip := `"level":"WARN","msg":"telegram: update cannot be read, skip it","update":2}`
	if out := logb.String(); strings.Count(out, skip) != 1 || strings.Count(out, "skip it") != 1 ||
		strings.Count(out, "cannot read the update batch") != 1 {
		t.Fatalf("want one line %s and one batch line in\n%s", skip, out)
	}
	if strings.Contains(logb.String(), "SECRET") {
		t.Fatalf("the log holds the text of a message:\n%s", logb)
	}
	if len(*slept) != 0 {
		t.Fatalf("the poller waited %v", *slept)
	}
}

// failFirst fails the first n requests with a network error. It sends the other requests.
type failFirst struct {
	n    atomic.Int32
	next http.RoundTripper
}

func (f *failFirst) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.n.Add(-1) >= 0 {
		return nil, errors.New("connection refused")
	}
	return f.next.RoundTrip(r)
}

func TestNetworkErrorDoesNotStartSingleMode(t *testing.T) {
	srv, reqs := scriptServer(t, func(int, pollReq) string { return emptyBatch })
	p, logb, slept := scriptPoller(t, srv)
	ft := &failFirst{next: srv.Client().Transport}
	ft.n.Store(1)
	p.C.HC = &http.Client{Transport: ft}
	runScript(t, p, func(context.Context, Update) {}, func() bool { return len(reqs()) >= 2 })
	// The server did not get the failed request.
	if got := limits(reqs()); len(got) < 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("limits %v, want no limit", got)
	}
	if !reflect.DeepEqual(*slept, []time.Duration{time.Second}) {
		t.Fatalf("waits %v, want 1 s", *slept)
	}
	if out := logb.String(); strings.Contains(out, "cannot read the update batch") ||
		!strings.Contains(out, `"msg":"telegram getUpdates failed, wait and retry"`) {
		t.Fatal(out)
	}
}

func TestSingleModeWaitsWhenTheUpdateHasNoID(t *testing.T) {
	srv, reqs := scriptServer(t, func(int, pollReq) string { return "not json" })
	p, logb, _ := scriptPoller(t, srv)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	var slept []time.Duration
	// The third wait stops the poller.
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		if len(slept) == 3 {
			cancel()
			return c.Err()
		}
		return nil
	}
	stopped := make(chan error, 1)
	go func() { stopped <- p.Run(c, func(context.Context, Update) {}) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the poller did not wait three times")
	}
	if !reflect.DeepEqual(slept, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("waits %v", slept)
	}
	// Request 0 starts the single mode with no wait. Each later request has one wait after it.
	if got := limits(reqs()); !reflect.DeepEqual(got, []int{0, 1, 1, 1}) {
		t.Fatalf("limits %v, want 0, 1, 1, 1", got)
	}
	line := `"level":"WARN","msg":"telegram: the update at the offset cannot be read","offset":0}`
	if strings.Count(logb.String(), line) != 3 {
		t.Fatalf("want three lines %s in\n%s", line, logb)
	}
}

func TestSingleModeEndsAfter100Updates(t *testing.T) {
	// The queue never gets empty: each request gets the update at its offset.
	srv, reqs := scriptServer(t, func(i int, r pollReq) string {
		if i == 0 {
			return "not json"
		}
		id := strconv.FormatInt(max(r.Offset, 1), 10)
		return `{"ok":true,"result":[{"update_id":` + id + `}]}`
	})
	p, _, _ := scriptPoller(t, srv)
	runScript(t, p, func(context.Context, Update) {}, func() bool { return len(reqs()) >= 102 })
	got := limits(reqs())
	if len(got) < 102 {
		t.Fatalf("%d requests, want 102", len(got))
	}
	for i := 1; i <= 100; i++ {
		if got[i] != 1 {
			t.Fatalf("request %d has the limit %d, want 1", i, got[i])
		}
	}
	if got[101] != 0 {
		t.Fatalf("request 101 has the limit %d, want no limit", got[101])
	}
}
