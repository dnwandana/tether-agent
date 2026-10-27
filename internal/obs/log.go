// Package obs holds the logs and the usage reports of Tether Agent.
package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/security"
)

// RotatingWriter is an io.WriteCloser that writes to a file and rotates the
// file by size. Before a write makes the file larger than the limit, it
// renames the file to <path>.1, shifts the older files up by one, and opens
// a new file. It keeps maxFiles files in total, the current file included.
// It is safe for concurrent use.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64
}

// NewRotatingWriter opens path for append and returns a RotatingWriter. It
// makes the parent folder if necessary. A maxSizeMB of 0 or less turns
// rotation off. A maxFiles of 1 or less keeps no old files.
func NewRotatingWriter(path string, maxSizeMB, maxFiles int) (*RotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("log: cannot make the folder for %s: %w", path, err)
	}
	w := &RotatingWriter{path: path, maxBytes: int64(maxSizeMB) << 20, maxFiles: max(maxFiles, 1)}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("log: cannot open %s: %w", w.path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("log: cannot read the size of %s: %w", w.path, err)
	}
	w.f, w.size = f, fi.Size()
	return nil
}

// Write writes p to the current file. It rotates first if p would make the
// file larger than the limit. A p that is larger than the limit goes into a
// new file of its own.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the current file, shifts <path>.N-1 to <path>.N, renames
// the current file to <path>.1, and opens a new file. It removes the old
// files beyond maxFiles-1. If a rename fails, rotate still opens the file,
// so that the log does not stop. rotate returns an error only when no file
// is open after it.
func (w *RotatingWriter) rotate() error {
	w.f.Close()
	w.f = nil
	keep := w.maxFiles - 1
	// Remove the files that a smaller maxFiles left behind.
	for n := max(keep, 1); ; n++ {
		if os.Remove(w.name(n)) != nil {
			break
		}
	}
	if keep == 0 {
		os.Remove(w.path)
	} else {
		for n := keep - 1; n >= 1; n-- {
			os.Rename(w.name(n), w.name(n+1))
		}
		os.Rename(w.path, w.name(1))
	}
	return w.open()
}

func (w *RotatingWriter) name(n int) string {
	return w.path + "." + strconv.Itoa(n)
}

// Close closes the current file. A Write after Close returns os.ErrClosed.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ParseLevel returns the slog level for a level name: debug, info, warn (or
// warning), or error. An unknown name gives info.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger returns a logger that writes JSON lines to w at the given level.
// It redacts the message and every string in the attributes, the nested
// groups included, through r.
func NewLogger(w io.Writer, level string, r *security.Redactor) *slog.Logger {
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: ParseLevel(level)})
	return slog.New(&redactHandler{inner: inner, r: r})
}

// redactHandler redacts each record before the inner handler writes it.
type redactHandler struct {
	inner slog.Handler
	r     *security.Redactor
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = h.attr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(red), r: h.r}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), r: h.r}
}

// attr returns a with each string in its value redacted.
func (h *redactHandler) attr(a slog.Attr) slog.Attr {
	a.Value = h.value(a.Value.Resolve())
	return a
}

func (h *redactHandler) value(v slog.Value) slog.Value {
	switch v.Kind() {
	case slog.KindString:
		return slog.StringValue(h.r.Redact(v.String()))
	case slog.KindGroup:
		g := v.Group()
		red := make([]slog.Attr, len(g))
		for i, a := range g {
			red[i] = h.attr(a)
		}
		return slog.GroupValue(red...)
	case slog.KindAny:
		return h.anyValue(v.Any())
	default:
		return v
	}
}

// anyValue redacts a value of kind Any. An error or a fmt.Stringer becomes
// its redacted text. Another value becomes its JSON text, redacted. If the
// redacted text is still valid JSON, the log keeps it as JSON.
func (h *redactHandler) anyValue(x any) slog.Value {
	switch t := x.(type) {
	case nil:
		return slog.AnyValue(nil)
	case error:
		return slog.StringValue(h.r.Redact(t.Error()))
	case fmt.Stringer:
		return slog.StringValue(h.r.Redact(t.String()))
	case []byte:
		return slog.StringValue(h.r.Redact(string(t)))
	}
	b, err := json.Marshal(x)
	if err != nil {
		return slog.StringValue(h.r.Redact(fmt.Sprintf("%+v", x)))
	}
	red := h.r.Redact(string(b))
	if red == string(b) {
		return slog.AnyValue(json.RawMessage(b))
	}
	if json.Valid([]byte(red)) {
		return slog.AnyValue(json.RawMessage(red))
	}
	return slog.StringValue(red)
}
