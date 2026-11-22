package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// The times of the streams. The tests change them.
var (
	heartbeatEvery    = 15 * time.Second
	frameWriteTimeout = 10 * time.Second
)

// noticeMax is the size of the notice buffer.
const noticeMax = 20

// textStopping is the 503 text of a stream after Close.
const textStopping = "The gateway is stopping."

// Hub sends the events of the dashboard to the open pages.
type Hub struct {
	maxQueue int
	mu       sync.Mutex
	streams  map[*stream]struct{}
	closed   bool
	done     chan struct{}

	noticeMu sync.Mutex
	notices  []Notice
}

// stream is the queue of one open page. Hub.mu guards its fields.
type stream struct {
	items []item
	bytes int
	wake  chan struct{} // capacity 1
	gone  chan struct{} // closed when the hub removes the stream
	logs  bool          // the page asked for log events with ?follow=logs
}

// item is one queued frame. A delta keeps its text, so the next delta can join it.
type item struct {
	name  string
	data  []byte
	delta string
}

// NewHub returns a hub. Each stream queue holds at most maxQueueBytes bytes.
func NewHub(maxQueueBytes int) *Hub {
	return &Hub{
		maxQueue: maxQueueBytes,
		streams:  map[*stream]struct{}{},
		done:     make(chan struct{}),
	}
}

// Publish queues e for each open stream. It never waits for a page.
func (h *Hub) Publish(e Event) { h.publishWith(e, nil) }

// publishWith runs apply and queues e under the hub lock, so a snapshot sees both or neither.
func (h *Hub) publishWith(e Event, apply func()) {
	it := item{name: e.Name}
	text, isDelta := "", false
	if e.Name == "delta" {
		text, isDelta = e.Data.(string)
	}
	if isDelta {
		it.delta = text
	} else {
		b, err := json.Marshal(e.Data)
		if err != nil {
			b = []byte("null")
		}
		it.data = b
	}
	size := len(it.data) + len(it.delta) + len(it.name)

	h.mu.Lock()
	defer h.mu.Unlock()
	if apply != nil {
		apply()
	}
	for s := range h.streams {
		if e.Name == "log" && !s.logs {
			continue
		}
		if last := len(s.items) - 1; isDelta && last >= 0 && s.items[last].name == "delta" && s.items[last].data == nil {
			if s.bytes+len(text) > h.maxQueue {
				h.removeLocked(s)
				continue
			}
			s.items[last].delta += text
			s.bytes += len(text)
		} else {
			if s.bytes+size > h.maxQueue {
				h.removeLocked(s)
				continue
			}
			s.items = append(s.items, it)
			s.bytes += size
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// removeLocked drops a stream and ends its handler. The caller holds h.mu.
func (h *Hub) removeLocked(s *stream) {
	if _, ok := h.streams[s]; !ok {
		return
	}
	delete(h.streams, s)
	close(s.gone)
}

// Streams returns the number of open streams.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams)
}

// Handler serves one SSE stream. The first frame is hello with the result of snap. The hub calls
// snap under its lock, so snap must read memory only and must not call the hub.
func (h *Hub) Handler(snap func() Snapshot) http.HandlerFunc { return h.handler(snap, nil) }

// handler is Handler with fill. fill adds the slow parts, for example the database reads, to the
// snapshot. The hub calls fill after it adds the stream and without its lock. Thus each event
// that comes during fill waits in the stream queue and goes out after hello, and no event falls
// in a gap. fill can be nil.
func (h *Hub) handler(snap func() Snapshot, fill func(r *http.Request, sn *Snapshot)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := &stream{wake: make(chan struct{}, 1), gone: make(chan struct{}),
			logs: r.URL.Query().Get("follow") == "logs"}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": textStopping})
			return
		}
		sn := snap()
		h.streams[s] = struct{}{}
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			h.removeLocked(s)
			h.mu.Unlock()
		}()
		if fill != nil {
			fill(r, &sn)
		}

		hdr := w.Header()
		hdr.Set("Content-Type", "text/event-stream")
		hdr.Set("Cache-Control", "no-cache, no-transform")
		hdr.Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		write := func(b []byte) bool {
			if err := rc.SetWriteDeadline(time.Now().Add(frameWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return false
			}
			if _, err := w.Write(b); err != nil {
				return false
			}
			if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return false
			}
			return true
		}

		hello, err := json.Marshal(sn)
		if err != nil {
			return
		}
		if !write(item{name: "hello", data: hello}.frame()) {
			return
		}
		tick := time.NewTicker(heartbeatEvery)
		defer tick.Stop()
		for {
			select {
			case <-s.wake:
				h.mu.Lock()
				items := s.items
				s.items, s.bytes = nil, 0
				h.mu.Unlock()
				var buf []byte
				for _, it := range items {
					buf = append(buf, it.frame()...)
				}
				if len(buf) > 0 && !write(buf) {
					return
				}
			case <-tick.C:
				if !write([]byte(": heartbeat\n\n")) {
					return
				}
			case <-s.gone:
				return
			case <-h.done:
				return
			case <-r.Context().Done():
				return
			}
		}
	}
}

// AddNotice keeps a notice that went to no live renderer. The buffer holds the last noticeMax.
// The caller publishes the notice event.
func (h *Hub) AddNotice(text string) {
	h.noticeMu.Lock()
	defer h.noticeMu.Unlock()
	h.notices = append(h.notices, Notice{Text: text, At: time.Now()})
	if n := len(h.notices); n > noticeMax {
		h.notices = append([]Notice(nil), h.notices[n-noticeMax:]...)
	}
}

// Notices returns a copy of the notice buffer, oldest first.
func (h *Hub) Notices() []Notice {
	h.noticeMu.Lock()
	defer h.noticeMu.Unlock()
	return append([]Notice(nil), h.notices...)
}

// Close ends every stream. After Close, Handler answers 503.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for s := range h.streams {
		h.removeLocked(s)
	}
}

// frame returns the SSE bytes of one item.
func (it item) frame() []byte {
	data := it.data
	if it.name == "delta" && data == nil {
		data, _ = json.Marshal(struct {
			Text string `json:"text"`
		}{it.delta})
	}
	b := make([]byte, 0, len(it.name)+len(data)+16)
	b = append(b, "event: "...)
	b = append(b, it.name...)
	b = append(b, "\ndata: "...)
	b = append(b, data...)
	return append(b, "\n\n"...)
}
