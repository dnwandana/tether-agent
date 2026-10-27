package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
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
	fails   int // the number of HTTP 502 replies before the first ok reply
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
			_, _ = w.Write([]byte("bad gateway"))
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

func commandNames(cmds []BotCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Command
	}
	return out
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

func TestCommandsListMatchesSpec(t *testing.T) {
	want := []string{"start", "new", "reset", "sessions", "resume", "rename", "archive", "delete", "fork", "model", "prompt", "compact", "usage", "stop", "steer", "sethome", "reload_mcp", "approve", "deny"}
	if !reflect.DeepEqual(commandNames(BotCommands()), want) {
		t.Fatal(commandNames(BotCommands()))
	}
	for _, c := range BotCommands() {
		if c.Description == "" || len(c.Description) > 256 {
			t.Fatal("bad description", c)
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
	if err := (&Poller{C: client(srv)}).RegisterCommands(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(BotCommands()) {
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
