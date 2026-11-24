package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseEvent is one event of a stream. A heartbeat has the Name ": heartbeat" and no Data.
type sseEvent struct {
	Name string
	Data []byte
}

// sseClient connects to h through a new test server, and returns the events of the stream.
// stop closes the stream and the server. A test cleanup also calls it.
func sseClient(t *testing.T, h *Hub, snap func() Snapshot) (events <-chan sseEvent, stop func()) {
	t.Helper()
	srv := httptest.NewServer(h.Handler(snap))
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	ch := make(chan sseEvent, 4096)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		name := ""
		for sc.Scan() {
			switch line := sc.Text(); {
			case line == ": heartbeat":
				ch <- sseEvent{Name: ": heartbeat"}
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ch <- sseEvent{Name: name, Data: []byte(strings.TrimPrefix(line, "data: "))}
			}
		}
		checkScan(t, sc)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			resp.Body.Close()
			srv.CloseClientConnections()
			srv.Close()
		})
	}
	t.Cleanup(stop)
	return ch, stop
}

// nextEvent returns the next event that is not a heartbeat. It fails the test after 2 s.
func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream ended")
			}
			if e.Name != ": heartbeat" {
				return e
			}
		case <-timeout:
			t.Fatal("no event in 2 s")
		}
	}
}

// expectQuiet fails the test when an event that is not a heartbeat comes within d.
func expectQuiet(t *testing.T, events <-chan sseEvent, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case e, ok := <-events:
			if ok && e.Name != ": heartbeat" {
				t.Fatalf("unexpected event %s %s", e.Name, e.Data)
			}
			if !ok {
				return
			}
		case <-timeout:
			return
		}
	}
}

// waitStreams waits until h has n streams. It fails the test after 2 s.
func waitStreams(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.Streams() != n {
		if time.Now().After(deadline) {
			t.Fatalf("streams %d, want %d", h.Streams(), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func emptySnap() Snapshot { return Snapshot{BuildID: "b1"} }

func TestHubHelloFirstAndFanOut(t *testing.T) {
	h := NewHub(1 << 20)
	a, _ := sseClient(t, h, emptySnap)
	b, _ := sseClient(t, h, emptySnap)
	for _, ev := range []<-chan sseEvent{a, b} {
		hello := nextEvent(t, ev)
		var s Snapshot
		if hello.Name != "hello" || json.Unmarshal(hello.Data, &s) != nil || s.BuildID != "b1" || s.Turn != nil {
			t.Fatalf("%s %s", hello.Name, hello.Data)
		}
	}
	waitStreams(t, h, 2)
	h.Publish(Event{Name: "status", Data: Status{Model: "gpt-5", Mode: "guarded", Waiting: 1}})
	for _, ev := range []<-chan sseEvent{a, b} {
		e := nextEvent(t, ev)
		if e.Name != "status" || !strings.Contains(string(e.Data), `"model":"gpt-5"`) || !strings.Contains(string(e.Data), `"waiting":1`) {
			t.Fatalf("%s %s", e.Name, e.Data)
		}
	}
}

func TestHubHeaders(t *testing.T) {
	h := NewHub(1 << 20)
	srv := httptest.NewServer(h.Handler(emptySnap))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache, no-transform" ||
		resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("%v", resp.Header)
	}
}

func TestHubHeartbeat(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = old })
	h := NewHub(1 << 20)
	ev, _ := sseClient(t, h, emptySnap)
	nextEvent(t, ev)
	select {
	case e := <-ev:
		if e.Name != ": heartbeat" {
			t.Fatalf("%s", e.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("no heartbeat")
	}
}

// frozenWriter is a ResponseWriter of a phone that does not read. Each Write waits for release.
type frozenWriter struct {
	header  http.Header
	release chan struct{}
	mu      sync.Mutex
	buf     bytes.Buffer
}

func newFrozenWriter() *frozenWriter {
	return &frozenWriter{header: http.Header{}, release: make(chan struct{})}
}
func (f *frozenWriter) Header() http.Header { return f.header }
func (f *frozenWriter) WriteHeader(int)     {}
func (f *frozenWriter) Flush()              {}
func (f *frozenWriter) Write(p []byte) (int, error) {
	<-f.release
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *frozenWriter) text() string { f.mu.Lock(); defer f.mu.Unlock(); return f.buf.String() }

// runFrozen runs the handler of h for fw until the test ends.
func runFrozen(t *testing.T, h *Hub, fw *frozenWriter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Handler(emptySnap)(fw, httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-fw.release:
		default:
			close(fw.release)
		}
		<-done
	})
}

func TestHubMergesDeltas(t *testing.T) {
	h := NewHub(1 << 20)
	fw := newFrozenWriter()
	runFrozen(t, h, fw)
	waitStreams(t, h, 1) // the handler waits in the write of hello
	for _, s := range []string{"a", "b", "c"} {
		h.Publish(Event{Name: "delta", Data: s})
	}
	h.Publish(Event{Name: "boundary", Data: struct{}{}})
	h.Publish(Event{Name: "delta", Data: "d"})
	close(fw.release)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(fw.text(), `{"text":"d"}`) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := fw.text()
	want := "event: delta\ndata: {\"text\":\"abc\"}\n\nevent: boundary\ndata: {}\n\nevent: delta\ndata: {\"text\":\"d\"}\n\n"
	if !strings.HasPrefix(got, "event: hello\n") || !strings.HasSuffix(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestHubFullQueueClosesOnlyThatStream(t *testing.T) {
	h := NewHub(64 << 10)
	fw := newFrozenWriter()
	runFrozen(t, h, fw)
	live, _ := sseClient(t, h, emptySnap)
	nextEvent(t, live)
	waitStreams(t, h, 2)
	big := Status{Model: strings.Repeat("m", 200)}
	for i := range 400 {
		start := time.Now()
		h.Publish(Event{Name: "status", Data: big})
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Fatalf("publish %d waited %v for a page", i, d)
		}
		if e := nextEvent(t, live); e.Name != "status" {
			t.Fatalf("%d: %s", i, e.Name)
		}
	}
	if h.Streams() != 1 {
		t.Fatalf("the full stream must close, and the live stream must stay: %d", h.Streams())
	}
}

// TestHelloAfterTurnEnded: a phone sleeps through the end of a turn and wakes up. The new
// hello shows no running turn and the id of the final saved answer, and no old event comes again.
func TestHelloAfterTurnEnded(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 50 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = old })
	h := NewHub(1 << 20)
	var mu sync.Mutex
	state := Snapshot{Turn: &TurnView{Running: true, Text: "Hel"}, SessionID: "s1", LastMessageID: 41, BuildID: "b1"}
	snap := func() Snapshot { mu.Lock(); defer mu.Unlock(); return state }

	phone, sleep := sseClient(t, h, snap)
	var s Snapshot
	if e := nextEvent(t, phone); e.Name != "hello" || json.Unmarshal(e.Data, &s) != nil || s.Turn == nil || !s.Turn.Running || s.Turn.Text != "Hel" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	waitStreams(t, h, 1)
	h.Publish(Event{Name: "delta", Data: "lo"})
	if e := nextEvent(t, phone); e.Name != "delta" || string(e.Data) != `{"text":"lo"}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}

	sleep()
	waitStreams(t, h, 0)
	// The turn ends while the phone sleeps. The answer is saved as message 42.
	mu.Lock()
	state.Turn, state.LastMessageID = nil, 42
	mu.Unlock()
	h.Publish(Event{Name: "delta", Data: " world"})
	h.Publish(Event{Name: "turn.done", Data: TurnDone{MessageID: 42}})

	woke, _ := sseClient(t, h, snap)
	e := nextEvent(t, woke)
	s = Snapshot{}
	if e.Name != "hello" || json.Unmarshal(e.Data, &s) != nil {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if s.Turn != nil || s.LastMessageID != 42 || s.SessionID != "s1" {
		t.Fatalf("hello must show no running turn and the last saved message: %s", e.Data)
	}
	if !strings.Contains(string(e.Data), `"turn":null`) || !strings.Contains(string(e.Data), `"last_message_id":42`) {
		t.Fatalf("snake_case JSON: %s", e.Data)
	}
	// No replay: the page loads message 42 once, from the saved messages.
	expectQuiet(t, woke, 200*time.Millisecond)
}

func TestHubNoticesAndClose(t *testing.T) {
	h := NewHub(1 << 20)
	for i := range 25 {
		h.AddNotice("n" + string(rune('a'+i)))
	}
	n := h.Notices()
	if len(n) != 20 || n[0].Text != "nf" || n[19].Text != "ny" || n[0].At.IsZero() {
		t.Fatalf("%+v", n)
	}
	n[0].Text = "changed"
	if h.Notices()[0].Text != "nf" {
		t.Fatal("Notices must return a copy")
	}
	ev, _ := sseClient(t, h, emptySnap)
	nextEvent(t, ev)
	waitStreams(t, h, 1)
	h.Close()
	waitStreams(t, h, 0)
	for range ev {
	}
	rec := httptest.NewRecorder()
	h.Handler(emptySnap)(rec, httptest.NewRequest("GET", "/api/events", nil))
	if rec.Code != 503 || rec.Body.String() != `{"error":"The gateway is stopping."}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	h.Publish(Event{Name: "status", Data: Status{}}) // no panic after Close
}

func TestAPITypesAreRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, v := range apiTypes {
		names[reflect.TypeOf(v).Name()] = true
	}
	for _, want := range []string{"Snapshot", "TurnView", "ToolView", "PlanItem", "QueueItem", "Card", "Status", "Notice",
		"Button", "ReplyView", "CostView", "TurnDone", "AckView", "TaskView", "CardAnswer"} {
		if !names[want] {
			t.Errorf("%s is not registered", want)
		}
	}
	if names["Event"] {
		t.Error("Event is not a page type")
	}
}

// A log event goes only to a stream that asked for it with ?follow=logs.
func TestHubLogEventsOnlyWithFollow(t *testing.T) {
	h := NewHub(1 << 20)
	events, _ := sseClient(t, h, func() Snapshot { return Snapshot{} })
	if e := nextEvent(t, events); e.Name != "hello" {
		t.Fatal(e.Name)
	}
	waitStreams(t, h, 1)
	h.Publish(Event{Name: "log", Data: map[string]any{"lines": []string{"x"}}})
	h.Publish(Event{Name: "status", Data: Status{Model: "m"}})
	if e := nextEvent(t, events); e.Name != "status" {
		t.Fatalf("a stream without ?follow=logs got %s", e.Name)
	}
}

// checkScan reports a scanner error. It ignores the errors of a closed stream and the errors
// that come after the test ended. It does not call t.Fatal, so a goroutine can call it.
func checkScan(t *testing.T, sc *bufio.Scanner) {
	t.Helper()
	err := sc.Err()
	if err == nil || t.Context().Err() != nil || errors.Is(err, http.ErrBodyReadAfterClose) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) {
		return
	}
	t.Error("the event stream failed:", err)
}

// The ring keeps the last 30 answered cards, newest first, and Answered returns a copy.
func TestHubAnsweredRing(t *testing.T) {
	h := NewHub(1 << 20)
	if got := h.Answered(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	for i := range 32 {
		h.addAnswered(AnsweredCard{ID: strconv.Itoa(i)})
	}
	got := h.Answered()
	if len(got) != 30 || got[0].ID != "31" || got[29].ID != "2" {
		t.Fatalf("%d %+v", len(got), got)
	}
	got[0].ID = "x"
	if h.Answered()[0].ID != "31" {
		t.Fatal("Answered must return a copy")
	}
}
