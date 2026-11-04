package telegram

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// paceClass tells the pace which limits apply to a call and if the call
// waits for its slot or holds.
type paceClass int

const (
	// paceDefault lets the Client select the class from the method. The
	// pace treats it as paceWait.
	paceDefault paceClass = iota
	// paceWait uses the slot, the full group window, and the flood end. The
	// call blocks until it is free.
	paceWait
	// paceReply uses the slot and the flood end, but not the window. The
	// call blocks until it is free.
	paceReply
	// paceQueue uses the slot, the full group window, and the flood end.
	// The call holds when it is not free.
	paceQueue
	// paceInterim uses the slot, a smaller part of the group window, and the
	// flood end. The call holds when it is not free.
	paceInterim
)

const (
	// paceSlot is the minimum time between two counted calls in one chat.
	// Telegram allows about one message each second in a chat.
	paceSlot = time.Second
	// paceWindow is the length of the sliding window of a group chat.
	paceWindow = 60 * time.Second
	// paceGroupLimit is the number of counted calls in one window of a
	// group. Telegram allows about 20 messages each minute in a group, and
	// the pace keeps a small margin.
	paceGroupLimit = 18
	// paceInterimLimit is the number of window places that interim calls
	// can use. The other places stay free for the final answer.
	paceInterimLimit = 15
	// floodPad is added to each flood wait. Telegram rounds retry_after to
	// whole seconds, so a request at the exact end can get a 429 again.
	floodPad = time.Second
	// paceIdle is the age after which a call or a flood end no longer
	// keeps the state of a chat.
	paceIdle = 2 * time.Minute
	// paceSweepEvery is the minimum time between two clean-ups of the idle
	// chats. The clean-up runs lazily, in a call to the pace.
	paceSweepEvery = time.Minute
)

// ChatPace keeps the send budget of each chat: the 1 s slot, the 60 s
// sliding window of a group, and the flood end after a 429. All clients of
// one bot share one ChatPace.
//
// mu is a leaf lock: no method calls out while it holds mu.
type ChatPace struct {
	// Now gives the current time. Nil means time.Now.
	Now func() time.Time

	mu        sync.Mutex
	chats     map[int64]*chatState
	lastSweep time.Time
}

// chatState is the pace state of one chat.
type chatState struct {
	// calls holds the times of the counted calls and the reservations,
	// oldest first.
	calls []time.Time
	// floodEnd is the time before which no call goes to the chat.
	floodEnd time.Time
}

// NewChatPace returns an empty pace.
func NewChatPace() *ChatPace {
	return &ChatPace{chats: make(map[int64]*chatState)}
}

func (p *ChatPace) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// windowLimit returns the number of window places that the class can use in
// the chat. Zero means that the window does not apply.
func windowLimit(chatID int64, class paceClass) int {
	switch {
	case chatID >= 0, class == paceReply:
		return 0
	case class == paceInterim:
		return paceInterimLimit
	default:
		return paceGroupLimit
	}
}

// freeAt returns the first time at which a call of the class can go to the
// chat.
func freeAt(st *chatState, chatID int64, class paceClass, now time.Time) time.Time {
	at := now
	if st == nil {
		return at
	}
	if n := len(st.calls); n > 0 {
		at = later(at, st.calls[n-1].Add(paceSlot))
	}
	at = later(at, st.floodEnd)
	// A call at time t counts the calls in (t-60s, t], so the oldest of the
	// last limit calls must be 60 s old.
	if limit := windowLimit(chatID, class); limit > 0 && len(st.calls) >= limit {
		at = later(at, st.calls[len(st.calls)-limit].Add(paceWindow))
	}
	return at
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Next returns the wait until a call of the class can go to the chat. Zero
// means that the call is free now. Next records nothing.
func (p *ChatPace) Next(chatID int64, class paceClass) time.Duration {
	if chatID == 0 {
		return 0
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	return freeAt(p.chats[chatID], chatID, class, now).Sub(now)
}

// TryTake records a call of the class when the call is free now and returns
// zero. Else it records nothing and returns the wait.
func (p *ChatPace) TryTake(chatID int64, class paceClass) time.Duration {
	wait, _ := p.TryReserve(chatID, class)
	return wait
}

// TryReserve is TryTake with a release function like the one of Reserve.
// The release function removes the record, for example when the request
// does not go out. When the call is not free, it records nothing, and the
// release function does nothing. Only the first call of release has an
// effect.
func (p *ChatPace) TryReserve(chatID int64, class paceClass) (wait time.Duration, release func()) {
	if chatID == 0 {
		return 0, func() {}
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)
	st := p.state(chatID, now)
	if at := freeAt(st, chatID, class, now); at.After(now) {
		return at.Sub(now), func() {}
	}
	st.calls = append(st.calls, now)
	return 0, p.releaseFunc(chatID, now)
}

// Reserve records a call of the class at the first free time and returns
// the wait until that time. The release function removes the record, for
// example when the caller stops before the call goes out. Only the first
// call of release has an effect.
func (p *ChatPace) Reserve(chatID int64, class paceClass) (wait time.Duration, release func()) {
	if chatID == 0 {
		return 0, func() {}
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)
	st := p.state(chatID, now)
	at := freeAt(st, chatID, class, now)
	// at is never before the last entry, so calls stays in time order.
	st.calls = append(st.calls, at)
	return at.Sub(now), p.releaseFunc(chatID, at)
}

// releaseFunc returns a function that removes the record at from the calls
// of the chat. Only the first call of the function has an effect.
func (p *ChatPace) releaseFunc(chatID int64, at time.Time) func() {
	done := false
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if done {
			return
		}
		done = true
		// A sweep can replace the state, so look up the chat again.
		st := p.chats[chatID]
		if st == nil {
			return
		}
		for i, t := range st.calls {
			if t.Equal(at) {
				st.calls = append(st.calls[:i], st.calls[i+1:]...)
				return
			}
		}
	}
}

// NoteFlood records a 429 for the chat. The flood end is now plus
// retryAfter (at least 1 s) plus floodPad. A later end that the chat
// already has stays.
func (p *ChatPace) NoteFlood(chatID int64, retryAfter time.Duration) {
	if chatID == 0 {
		return
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)
	st := p.state(chatID, now)
	end := now.Add(max(retryAfter, time.Second) + floodPad)
	if end.After(st.floodEnd) {
		st.floodEnd = end
	}
}

// FloodEnd returns the flood end of the chat, or the zero time when the
// chat has none. A nil pace has no flood end.
func (p *ChatPace) FloodEnd(chatID int64) time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.chats[chatID]; st != nil {
		return st.floodEnd
	}
	return time.Time{}
}

// state returns the state of the chat and makes it when it is missing. It
// removes the calls that are too old to count. The caller holds mu.
func (p *ChatPace) state(chatID int64, now time.Time) *chatState {
	st := p.chats[chatID]
	if st == nil {
		st = &chatState{}
		p.chats[chatID] = st
	}
	st.calls = dropOld(st.calls, now.Add(-paceIdle))
	return st
}

// sweep removes the idle chats, at most one time in each paceSweepEvery.
// The caller holds mu.
func (p *ChatPace) sweep(now time.Time) {
	if now.Sub(p.lastSweep) < paceSweepEvery {
		return
	}
	p.lastSweep = now
	cutoff := now.Add(-paceIdle)
	for id, st := range p.chats {
		st.calls = dropOld(st.calls, cutoff)
		if len(st.calls) == 0 && !st.floodEnd.After(cutoff) {
			delete(p.chats, id)
		}
	}
}

// dropOld removes the entries at or before cutoff from the start of calls.
func dropOld(calls []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(calls) && !calls[i].After(cutoff) {
		i++
	}
	return calls[i:]
}

// paceHold tells that the chat budget holds a call. The call did not go
// out. Wait is the time until the call is free.
type paceHold struct{ Wait time.Duration }

func (e *paceHold) Error() string {
	return fmt.Sprintf("telegram pace: the chat budget holds the call for %s", e.Wait)
}

// isPaceHold reports whether err is a pace hold, and the wait.
func isPaceHold(err error) (time.Duration, bool) {
	if ph, ok := errors.AsType[*paceHold](err); ok {
		return ph.Wait, true
	}
	return 0, false
}
