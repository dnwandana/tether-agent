package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/obs"
)

// LogLineView is one log line for the page.
type LogLineView struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs"`
}

// LogView is the body of GET /api/logs.
type LogView struct {
	Lines []LogLineView `json:"lines"`
}

// LogEvent is the data of the log event.
type LogEvent struct {
	Lines []LogLineView `json:"lines"`
}

// LogsAPI serves the log ring and sends the log event for new lines.
type LogsAPI struct {
	ring   *obs.Ring
	hub    *Hub
	ch     chan obs.LogLine
	cancel func()
	done   chan struct{}
	once   sync.Once
}

func init() { registerAPI(LogView{}, LogLineView{}, LogEvent{}) }

const (
	logsDefaultLimit = 500
	logsMaxLimit     = 2000
	logsBatchMax     = 100
)

// NewLogsAPI subscribes to ring and starts the goroutine that sends the log events.
func NewLogsAPI(ring *obs.Ring, hub *Hub) *LogsAPI {
	a := &LogsAPI{ring: ring, hub: hub, ch: make(chan obs.LogLine, 256), done: make(chan struct{})}
	// The subscriber drops a line when the channel is full, so a log call never waits.
	a.cancel = ring.Subscribe(func(l obs.LogLine) {
		select {
		case a.ch <- l:
		default:
		}
	})
	go a.run()
	return a
}

// run collects the waiting lines and publishes them as one log event.
func (a *LogsAPI) run() {
	for {
		var first obs.LogLine
		select {
		case <-a.done:
			return
		case first = <-a.ch:
		}
		batch := []obs.LogLine{first}
	collect:
		for len(batch) < logsBatchMax {
			select {
			case l := <-a.ch:
				batch = append(batch, l)
			default:
				break collect
			}
		}
		if a.hub.Streams() > 0 {
			a.hub.Publish(Event{Name: "log", Data: LogEvent{Lines: logsViews(batch)}})
		}
	}
}

// Register adds the route to mux.
func (a *LogsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", a.get)
}

// Close stops the subscription and the goroutine. A second call does nothing.
func (a *LogsAPI) Close() {
	a.once.Do(func() {
		a.cancel()
		close(a.done)
	})
}

func (a *LogsAPI) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := logsDefaultLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > logsMaxLimit {
			logsErr(w, http.StatusBadRequest, "limit must be a number from 1 to 2000.")
			return
		}
		limit = n
	}
	logsJSON(w, http.StatusOK, LogView{Lines: logsViews(a.ring.Lines(q.Get("level"), q.Get("q"), limit))})
}

// logsViews converts ring lines to page lines. The result is never nil.
func logsViews(lines []obs.LogLine) []LogLineView {
	out := make([]LogLineView, 0, len(lines))
	for _, l := range lines {
		out = append(out, LogLineView{Time: l.Time, Level: l.Level, Msg: l.Msg, Attrs: l.Attrs})
	}
	return out
}

func logsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func logsErr(w http.ResponseWriter, status int, msg string) {
	logsJSON(w, status, map[string]string{"error": msg})
}
