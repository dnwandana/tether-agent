package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// testDeps returns the deps of a channel on a random loopback port.
func testDeps(t *testing.T) Deps {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := config.Default().Dashboard
	cfg.Enabled, cfg.Listen = true, "127.0.0.1:0"
	return Deps{Cfg: cfg, DB: d, Sessions: sessions.New(d),
		Redact: func(s string) string { return strings.ReplaceAll(s, "hunter22secret", "«PW»") },
		Status: func(context.Context) Status { return Status{Model: "m", Mode: "guarded"} }}
}

// runChannel runs c until the test ends and returns the base URL, the two cancel funcs, and the
// result of Run.
func runChannel(t *testing.T, c *Channel) (base string, stopRecv, stopWork context.CancelFunc, runErr chan error) {
	t.Helper()
	work, stopWork := context.WithCancel(context.Background())
	recv, stopRecv := context.WithCancel(work)
	c.SetTurnContext(work)
	runErr = make(chan error, 1)
	go func() { runErr <- c.Run(recv, nil) }()
	select {
	case <-c.ready:
	case err := <-runErr:
		t.Fatal("Run ended:", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no listener")
	}
	t.Cleanup(func() {
		stopWork()
		_ = c.Shutdown(context.Background())
	})
	return "http://" + c.addr.String(), stopRecv, stopWork, runErr
}

// getWithSession sends a GET with the two session values. cookie and header can be "".
func getWithSession(t *testing.T, url, cookie, header string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest("GET", url, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	if header != "" {
		r.Header.Set(HeaderName, header)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// channelStream opens /api/events with the session values and returns the events.
func channelStream(t *testing.T, base, cookie, header string) <-chan sseEvent {
	t.Helper()
	resp := getWithSession(t, base+"/api/events", cookie, header)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	t.Cleanup(func() { resp.Body.Close() })
	ch := make(chan sseEvent, 1024)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		name := ""
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ch <- sseEvent{Name: name, Data: []byte(strings.TrimPrefix(line, "data: "))}
			}
		}
		checkScan(t, sc)
	}()
	return ch
}

func TestChannelNamesAndInbound(t *testing.T) {
	if _, err := NewChannel(Deps{}); err == nil {
		t.Fatal("a channel needs a database")
	}
	c, err := NewChannel(testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "dashboard" || ChannelName != "dashboard" || SessionKey != "dashboard:owner" || OwnerChatID != "owner" {
		t.Fatal(c.Name())
	}
	var _ gateway.InflightChannel = c
	in := c.Inbound("hello")
	want := gateway.Inbound{Key: "dashboard:owner", Source: "dashboard", ChatID: "owner", Text: "hello", IsText: true,
		Target: gateway.Target{Source: "dashboard", ChatID: "owner"}, Owner: true, SenderID: "owner"}
	if in.Key != want.Key || in.Source != want.Source || in.ChatID != want.ChatID || in.Text != want.Text || !in.IsText ||
		in.Target != want.Target || !in.Owner || in.Shared || in.SenderID != "owner" || in.UserID != "" || in.ThreadID != "" {
		t.Fatalf("%+v", in)
	}
	if c.Cards() == nil || c.Hub() == nil || c.SignIn() == nil || c.Gateway() != nil || c.Stopping() {
		t.Fatal("accessors")
	}
}

func TestChannelInflight(t *testing.T) {
	c, _ := NewChannel(testDeps(t))
	d1, d2 := c.BeginInflight(), c.BeginInflight()
	if c.Inflight() != 2 {
		t.Fatal(c.Inflight())
	}
	d1()
	d1()
	if c.Inflight() != 1 {
		t.Fatal("done must count once:", c.Inflight())
	}
	d2()
	if c.Inflight() != 0 {
		t.Fatal(c.Inflight())
	}
}

func TestChannelServesAndStops(t *testing.T) {
	c, err := NewChannel(testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	base, stopRecv, stopWork, runErr := runChannel(t, c)
	resp := getWithSession(t, base+"/", "", "")
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if resp = getWithSession(t, base+"/api/events", "", ""); resp.StatusCode != 401 {
		t.Fatal("the API needs a session:", resp.StatusCode)
	}
	resp.Body.Close()
	_, cookie, header, _ := c.store.CreateSession(context.Background(), "Phone", "ua", "127.0.0.1", time.Hour, time.Now())
	for _, p := range []string{"/api/nope", "/auth/nope"} {
		resp = getWithSession(t, base+p, cookie, header)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 || string(b) != `{"error":"Not found."}`+"\n" {
			t.Fatalf("%s: %d %q", p, resp.StatusCode, b)
		}
	}
	ev := channelStream(t, base, cookie, header)
	hello := nextEvent(t, ev)
	var s Snapshot
	if hello.Name != "hello" || json.Unmarshal(hello.Data, &s) != nil || s.BuildID != BuildID() || s.Status.Model != "m" ||
		s.Turn != nil || s.SessionID != "" || s.LastMessageID != 0 {
		t.Fatalf("%s %s", hello.Name, hello.Data)
	}
	c.NewRenderer("r1").Ack("hi")
	if e := nextEvent(t, ev); e.Name != "ack" || !strings.Contains(string(e.Data), `"request_id":"r1"`) {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if err := c.Send(context.Background(), gateway.Target{Source: "dashboard", ChatID: "owner"},
		gateway.Outbound{Text: "🔄 Back. pw hunter22secret", Files: []string{"/data/media/a.png"}}); err != nil {
		t.Fatal(err)
	}
	var n Notice
	if e := nextEvent(t, ev); e.Name != "notice" || json.Unmarshal(e.Data, &n) != nil || n.Text != "🔄 Back. pw «PW»\n📎 a.png" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if ns := c.Hub().Notices(); len(ns) != 1 || ns[0].Text != n.Text {
		t.Fatalf("%+v", ns)
	}
	if err := c.Send(context.Background(), gateway.Target{Source: "telegram", ChatID: "5"}, gateway.Outbound{Text: "x"}); err == nil {
		t.Fatal("a target of another channel must fail")
	}
	// The receive context ends first. The stream stays open until the work context ends.
	stopRecv()
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}
	if !c.Stopping() {
		t.Fatal("Stopping must be true after Run returns")
	}
	if e := nextEvent(t, ev); e.Name != "restart" || string(e.Data) != `{"reason":"stopping"}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	c.NewRenderer("r2").Ack("still open")
	if e := nextEvent(t, ev); e.Name != "ack" {
		t.Fatalf("the stream must outlive the receive context: %s", e.Name)
	}
	stopWork()
	deadline := time.After(2 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-ev:
		case <-deadline:
			t.Fatal("the stream must end with the work context")
		}
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Fatal("the server must stop")
	}
}

func TestChannelRunFailsOnPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d := testDeps(t)
	d.Cfg.Listen = ln.Addr().String()
	c, _ := NewChannel(d)
	c.SetTurnContext(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a port in use must give an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return at once on a port in use")
	}
	// The channel still takes a message with no renderer.
	if err := c.Send(context.Background(), gateway.Target{Source: "dashboard", ChatID: "owner"}, gateway.Outbound{Text: "saved"}); err != nil {
		t.Fatal(err)
	}
	if c.Shutdown(context.Background()) != nil {
		t.Fatal("Shutdown with no server must return nil")
	}
}

func TestChannelStatusLoop(t *testing.T) {
	old := statusEvery
	statusEvery = 20 * time.Millisecond
	t.Cleanup(func() { statusEvery = old })
	d := testDeps(t)
	var model atomic.Value
	model.Store("m")
	d.Status = func(context.Context) Status { return Status{Model: model.Load().(string)} }
	c, _ := NewChannel(d)
	base, _, _, _ := runChannel(t, c)
	_, cookie, header, _ := c.store.CreateSession(context.Background(), "Phone", "ua", "127.0.0.1", time.Hour, time.Now())
	ev := channelStream(t, base, cookie, header)
	nextEvent(t, ev)
	model.Store("m2")
	for {
		e := nextEvent(t, ev)
		if e.Name == "status" && strings.Contains(string(e.Data), `"model":"m2"`) {
			break
		}
	}
}

func TestDepsTypesAreRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, v := range apiTypes {
		names[reflectTypeName(v)] = true
	}
	for _, want := range []string{"DoctorCheck", "UpdateInfo", "PairingRow", "SetupStep"} {
		if !names[want] {
			t.Errorf("%s is not registered", want)
		}
	}
	if names["PairingFuncs"] || names["Deps"] {
		t.Error("a type with funcs is not a page type")
	}
}

func reflectTypeName(v any) string { return reflect.TypeOf(v).Name() }

// The session event goes out when the view of the active session changes: after the gateway
// sets the session of a turn, after a turn, and after a command. The same view sends nothing.
func TestChannelSessionEvent(t *testing.T) {
	d := testDeps(t)
	var mu sync.Mutex
	view := SessionView{ID: "s1", Title: "First"}
	d.Session = func(context.Context) SessionView {
		mu.Lock()
		defer mu.Unlock()
		return view
	}
	c, err := NewChannel(d)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := sseClient(t, c.hub, func() Snapshot { return Snapshot{} })
	if e := nextEvent(t, events); e.Name != "hello" {
		t.Fatal(e.Name)
	}
	waitStreams(t, c.hub, 1)

	c.NewRenderer("r1").(gateway.SessionRenderer).SetSession("s1")
	e := nextEvent(t, events)
	var got SessionView
	if e.Name != "session" || json.Unmarshal(e.Data, &got) != nil || got.ID != "s1" || got.Title != "First" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	c.refreshSession(context.Background())
	expectQuiet(t, events, 200*time.Millisecond)

	mu.Lock()
	view = SessionView{ID: "s2", Title: "Second"}
	mu.Unlock()
	c.refreshSession(context.Background())
	e = nextEvent(t, events)
	if e.Name != "session" || json.Unmarshal(e.Data, &got) != nil || got.ID != "s2" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
}

// Two quick refresh requests send the newest view last.
func TestChannelRefreshNewestLast(t *testing.T) {
	d := testDeps(t)
	var mu sync.Mutex
	view := SessionView{ID: "s1", Title: "Old"}
	first := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	d.Session = func(context.Context) SessionView {
		mu.Lock()
		calls++
		n, v := calls, view
		mu.Unlock()
		if n == 1 {
			close(first)
			<-release
		}
		return v
	}
	c, err := NewChannel(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	events, _ := sseClient(t, c.hub, func() Snapshot { return Snapshot{} })
	if e := nextEvent(t, events); e.Name != "hello" {
		t.Fatal(e.Name)
	}
	waitStreams(t, c.hub, 1)
	c.refreshSession(context.Background())
	<-first
	mu.Lock()
	view = SessionView{ID: "s1", Title: "New"}
	mu.Unlock()
	c.refreshSession(context.Background())
	c.refreshSession(context.Background())
	close(release)
	var got SessionView
	e := nextEvent(t, events)
	if json.Unmarshal(e.Data, &got) != nil || got.Title != "Old" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	e = nextEvent(t, events)
	if json.Unmarshal(e.Data, &got) != nil || got.Title != "New" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	expectQuiet(t, events, 200*time.Millisecond)
}

// An event that comes while the stream reads the database goes out after hello. Before, the
// stream read the database before the hub added it, so such an event went to no stream.
func TestChannelEventsKeepsEventsOfTheReads(t *testing.T) {
	d := testDeps(t)
	var c *Channel
	var once sync.Once
	streams := -1
	d.Session = func(context.Context) SessionView {
		once.Do(func() {
			streams = c.hub.Streams()
			c.hub.Publish(Event{Name: "turn.done", Data: map[string]int64{"message_id": 7}})
		})
		return SessionView{ID: "s1", Title: "First"}
	}
	c, err := NewChannel(d)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _, _ := runChannel(t, c)
	_, cookie, header, _ := c.store.CreateSession(context.Background(), "Phone", "ua", "127.0.0.1", time.Hour, time.Now())
	ev := channelStream(t, base, cookie, header)
	var s Snapshot
	if e := nextEvent(t, ev); e.Name != "hello" || json.Unmarshal(e.Data, &s) != nil || s.Session.Title != "First" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if e := nextEvent(t, ev); e.Name != "turn.done" || string(e.Data) != `{"message_id":7}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if streams != 1 {
		t.Fatalf("the hub must have the stream before the reads: %d streams", streams)
	}
}

// Two quick queue changes send the newest queue last. Before, each change sent the queue from
// its own goroutine, so an older queue could go out last.
func TestChannelQueueNewestLast(t *testing.T) {
	c, err := NewChannel(testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	var mu sync.Mutex
	queue := []gateway.QueueItem{{Kind: "text", Preview: "old"}}
	first := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	c.queueOf = func(string) []gateway.QueueItem {
		mu.Lock()
		calls++
		n, q := calls, queue
		mu.Unlock()
		if n == 1 {
			close(first)
			<-release
		}
		return q
	}
	events, _ := sseClient(t, c.hub, func() Snapshot { return Snapshot{} })
	if e := nextEvent(t, events); e.Name != "hello" {
		t.Fatal(e.Name)
	}
	waitStreams(t, c.hub, 1)
	c.queueChanged("telegram:5")
	c.queueChanged(SessionKey)
	<-first
	mu.Lock()
	queue = []gateway.QueueItem{{Kind: "text", Preview: "old"}, {Kind: "goal", Preview: "new"}}
	mu.Unlock()
	c.queueChanged(SessionKey)
	c.queueChanged(SessionKey)
	close(release)
	if e := nextEvent(t, events); e.Name != "queue" || string(e.Data) != `{"items":[{"kind":"text","preview":"old"}],"max":20}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if e := nextEvent(t, events); e.Name != "queue" ||
		string(e.Data) != `{"items":[{"kind":"text","preview":"old"},{"kind":"goal","preview":"new"}],"max":20}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	expectQuiet(t, events, 200*time.Millisecond)
	c.turn.mu.Lock()
	q := c.turn.queue
	c.turn.mu.Unlock()
	if len(q) != 2 || q[1].Preview != "new" {
		t.Fatalf("%+v", q)
	}
}

// The queue event and the turn state show each preview through the redactor.
func TestChannelQueueRedacts(t *testing.T) {
	h := newCmdHarness(t, &cmdProv{hold: make(chan struct{})})
	h.ch.d.Redact = func(s string) string { return strings.ReplaceAll(s, "hunter22secret", "«PW»") }
	ev := cmdEvents(t, h.ch.Hub())
	ctx := context.Background()
	h.gw.Submit(ctx, h.ch.Inbound("hi"), func() gateway.Renderer { return h.ch.NewRenderer("r1") }, nil)
	if err := h.gw.Enqueue(ctx, SessionKey, "pw hunter22secret"); err != nil {
		t.Fatal(err)
	}
	got := cmdEvent(t, ev, "queue", `"kind":"system"`)
	if got != `{"items":[{"kind":"system","preview":"pw «PW»"}],"max":20}` {
		t.Fatal(got)
	}
	cmdWait(t, func() bool {
		h.ch.turn.mu.Lock()
		defer h.ch.turn.mu.Unlock()
		return len(h.ch.turn.queue) == 1 && h.ch.turn.queue[0].Preview == "pw «PW»"
	})
}
