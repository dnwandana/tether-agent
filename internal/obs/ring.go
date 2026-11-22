package obs

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogLine is one redacted log record that the ring keeps.
type LogLine struct {
	Time              time.Time
	Level, Msg, Attrs string
}

// ringEntry is a line with the slog level of the line.
type ringEntry struct {
	line LogLine
	lvl  slog.Level
}

// Ring keeps the last n log lines in memory. It is safe for concurrent use.
type Ring struct {
	mu     sync.Mutex
	buf    []ringEntry
	next   int
	full   bool
	subs   map[int]func(LogLine)
	nextID int
}

// NewRing returns a ring for the last n lines. An n less than 1 keeps 1 line.
func NewRing(n int) *Ring {
	return &Ring{buf: make([]ringEntry, max(n, 1)), subs: map[int]func(LogLine){}}
}

// Lines returns the lines that match, oldest first, in a new slice. An empty
// level keeps each line. Another level keeps the lines at that level or above.
// An empty text keeps each line. Another text must be in the message or in the
// attributes, with no case difference. A limit of 1 or more keeps the last
// matches.
func (r *Ring) Lines(level, text string, limit int) []LogLine {
	floor := slog.Level(-1 << 30)
	if level != "" {
		floor = ParseLevel(level)
	}
	text = strings.ToLower(text)
	r.mu.Lock()
	n := r.next
	start := 0
	if r.full {
		n, start = len(r.buf), r.next
	}
	var out []LogLine
	for i := 0; i < n; i++ {
		e := r.buf[(start+i)%len(r.buf)]
		if e.lvl < floor {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.line.Msg), text) &&
			!strings.Contains(strings.ToLower(e.line.Attrs), text) {
			continue
		}
		out = append(out, e.line)
	}
	r.mu.Unlock()
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	if out == nil {
		out = []LogLine{}
	}
	return out
}

// Subscribe calls f for each new line, after the ring releases its lock. f
// must not block and must not log. The returned cancel stops the calls.
func (r *Ring) Subscribe(f func(LogLine)) (cancel func()) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.subs[id] = f
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

func (r *Ring) add(l LogLine, lvl slog.Level) {
	r.mu.Lock()
	r.buf[r.next] = ringEntry{line: l, lvl: lvl}
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
	fs := make([]func(LogLine), 0, len(r.subs))
	for _, f := range r.subs {
		fs = append(fs, f)
	}
	r.mu.Unlock()
	for _, f := range fs {
		f(l)
	}
}

// ringHandler is the slog.Handler that formats a record as a LogLine and adds it to the ring.
type ringHandler struct {
	ring   *Ring
	level  slog.Leveler
	attrs  string // text of the attributes of With
	prefix string // group prefix, with a final dot
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *ringHandler) Handle(_ context.Context, rec slog.Record) error {
	var b strings.Builder
	b.WriteString(h.attrs)
	rec.Attrs(func(a slog.Attr) bool {
		ringAppend(&b, h.prefix, a)
		return true
	})
	t := rec.Time
	if t.IsZero() {
		t = time.Now()
	}
	h.ring.add(LogLine{Time: t, Level: rec.Level.String(), Msg: rec.Message, Attrs: b.String()}, rec.Level)
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		ringAppend(&b, h.prefix, a)
	}
	c := *h
	c.attrs = b.String()
	return &c
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

// ringAppend adds the attribute a as key=value text to b. A group adds its own prefix.
func ringAppend(b *strings.Builder, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range v.Group() {
			ringAppend(b, p, g)
		}
		return
	}
	if a.Equal(slog.Attr{}) {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(prefix)
	b.WriteString(a.Key)
	b.WriteByte('=')
	s := v.String()
	if s == "" || strings.ContainsAny(s, " \"=") {
		s = strconv.Quote(s)
	}
	b.WriteString(s)
}

// fanout sends each record to two handlers.
type fanout struct{ a, b slog.Handler }

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return f.a.Enabled(ctx, l) || f.b.Enabled(ctx, l)
}

func (f fanout) Handle(ctx context.Context, rec slog.Record) error {
	var err error
	if f.a.Enabled(ctx, rec.Level) {
		err = f.a.Handle(ctx, rec.Clone())
	}
	if f.b.Enabled(ctx, rec.Level) {
		if e := f.b.Handle(ctx, rec.Clone()); err == nil {
			err = e
		}
	}
	return err
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanout{a: f.a.WithAttrs(attrs), b: f.b.WithAttrs(attrs)}
}

func (f fanout) WithGroup(name string) slog.Handler {
	return fanout{a: f.a.WithGroup(name), b: f.b.WithGroup(name)}
}
