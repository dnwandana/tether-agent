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
	items []steerItem
	// last is the sequence number of the last push. The numbers start at 1 and never reset.
	last uint64
}

// steerItem is one steer text and its sequence number.
type steerItem struct {
	text string
	seq  uint64
}

// NewSteerQueue returns an empty queue.
func NewSteerQueue() *SteerQueue { return &SteerQueue{} }

// Push adds text without the surrounding white space. It returns false and adds nothing
// when the text is blank, so the caller can queue the message for the next turn instead.
func (q *SteerQueue) Push(text string) bool {
	_, ok := q.PushSeq(text)
	return ok
}

// PushSeq adds text like Push and returns the sequence number of the text. A blank text
// gets no number, so the result is 0 and false.
func (q *SteerQueue) PushSeq(text string) (seq uint64, ok bool) {
	text = strings.TrimSpace(text)
	if q == nil || text == "" {
		return 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.last++
	q.items = append(q.items, steerItem{text: text, seq: q.last})
	return q.last, true
}

// Pending returns the pending texts joined with "\n". The queue does not change.
func (q *SteerQueue) Pending() string {
	if q == nil {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return joinSteer(q.items)
}

// Drain returns the pending texts joined with "\n" and empties the queue.
func (q *SteerQueue) Drain() string {
	text, _ := q.DrainSeq()
	return text
}

// DrainSeq returns the pending texts joined with "\n" and the highest sequence number of
// those texts, and empties the queue. An empty queue returns "" and 0.
func (q *SteerQueue) DrainSeq() (text string, seq uint64) {
	if q == nil {
		return "", 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		seq = max(seq, it.seq)
	}
	text = joinSteer(q.items)
	q.items = nil
	return text, seq
}

// restash puts a drained text back at the front, so it stays before newer texts. The text
// keeps its sequence number, so a later drain reports that number again.
func (q *SteerQueue) restash(text string, seq uint64) {
	if q == nil || text == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append([]steerItem{{text: text, seq: seq}}, q.items...)
}

// joinSteer returns the texts of items joined with "\n".
func joinSteer(items []steerItem) string {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.text
	}
	return strings.Join(texts, "\n")
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
