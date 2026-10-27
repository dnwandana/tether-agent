package agent

import (
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// SteerMarker starts the steer text in a tool result (Hermes).
const SteerMarker = "\n\nUser guidance: "

// SteerQueue holds the steer texts that the user sends during a run. It is safe for
// concurrent use. A nil *SteerQueue accepts nothing and holds nothing.
type SteerQueue struct {
	mu    sync.Mutex
	items []string
}

// NewSteerQueue returns an empty queue.
func NewSteerQueue() *SteerQueue { return &SteerQueue{} }

// Push adds text without the surrounding white space. It returns false and adds nothing
// when the text is blank, so the caller can queue the message for the next turn instead.
func (q *SteerQueue) Push(text string) bool {
	text = strings.TrimSpace(text)
	if q == nil || text == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, text)
	return true
}

// Pending returns the pending texts joined with "\n". The queue does not change.
func (q *SteerQueue) Pending() string {
	if q == nil {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return strings.Join(q.items, "\n")
}

// Drain returns the pending texts joined with "\n" and empties the queue.
func (q *SteerQueue) Drain() string {
	if q == nil {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	s := strings.Join(q.items, "\n")
	q.items = nil
	return s
}

// restash puts a drained text back at the front, so it stays before newer texts.
func (q *SteerQueue) restash(text string) {
	if q == nil || text == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append([]string{text}, q.items...)
}

// InjectSteer appends SteerMarker and text to the content of the last RoleTool message. It
// copies msgs and does not change the input. ok is false when msgs has no tool message.
func InjectSteer(msgs []provider.Message, text string) (out []provider.Message, ok bool) {
	i := lastToolIndex(msgs)
	if i < 0 {
		return msgs, false
	}
	out = append([]provider.Message(nil), msgs...)
	out[i].Content += SteerMarker + text
	return out, true
}

// lastToolIndex returns the index of the last RoleTool message, or -1.
func lastToolIndex(msgs []provider.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleTool {
			return i
		}
	}
	return -1
}
