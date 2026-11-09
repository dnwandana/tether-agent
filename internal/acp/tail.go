package acp

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// stderrMax is the number of stderr bytes that a tailBuffer keeps.
	stderrMax = 64 << 10
	// lineMax is the length of the open part of a line that a tailBuffer keeps for onLine.
	// A line with no end cannot fill the memory.
	lineMax = 4 << 10
)

// tailBuffer keeps the last stderrMax bytes that an adapter writes to stderr, and it gives
// each line to a callback. The adapter gets the write end of an os.Pipe. A writer that is not
// a file makes cmd.Wait copy the output, and then cmd.Wait blocks while a grandchild keeps
// stderr open.
type tailBuffer struct {
	w *os.File

	mu       sync.Mutex
	buf      []byte
	wOnce    sync.Once
	finished chan struct{}
}

// newTailBuffer makes the pipe and starts the reader goroutine. The goroutine calls onLine
// once for each complete line, with no "\n", and once at EOF for a last open part that is
// not empty.
func newTailBuffer(onLine func(line string)) (*tailBuffer, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	t := &tailBuffer{w: w, finished: make(chan struct{})}
	go func() {
		defer close(t.finished)
		defer r.Close()
		var line []byte
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			chunk := b[:n]
			t.mu.Lock()
			t.buf = append(t.buf, chunk...)
			if len(t.buf) > stderrMax {
				t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrMax:]...)
			}
			t.mu.Unlock()
			for len(chunk) > 0 {
				i := bytes.IndexByte(chunk, '\n')
				part := chunk
				if i >= 0 {
					part = chunk[:i]
				}
				if room := lineMax - len(line); room > 0 {
					line = append(line, part[:min(room, len(part))]...)
				}
				if i < 0 {
					break
				}
				if onLine != nil {
					onLine(string(line))
				}
				line = line[:0]
				chunk = chunk[i+1:]
			}
			if err != nil {
				if len(line) > 0 && onLine != nil {
					onLine(string(line))
				}
				return
			}
		}
	}()
	return t, nil
}

// closeWriter closes the parent copy of the write end. The child keeps its own copy.
func (t *tailBuffer) closeWriter() {
	t.wOnce.Do(func() { _ = t.w.Close() })
}

// String returns the bytes that the buffer keeps.
func (t *tailBuffer) String() string {
	// Give the reader a moment to take the last bytes of a dead process.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-t.finished:
	case <-timer.C:
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// lastLines returns the last n lines of the buffer, joined with "\n".
func (t *tailBuffer) lastLines(n int) string {
	if t == nil || n <= 0 {
		return ""
	}
	s := strings.TrimSuffix(t.String(), "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
