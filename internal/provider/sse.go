package provider

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxSSELine is the longest SSE line that ReadSSE accepts.
const maxSSELine = 1 << 20

// ReadSSE reads a server-sent event stream from r and calls fn for each
// event. It joins the data lines of one event with "\n". It ignores comment
// lines. An error from fn stops the read and ReadSSE returns that error.
// The end of r dispatches an event that has data but no blank line after it.
func ReadSSE(r io.Reader, fn func(event, data string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		event   string
		data    strings.Builder
		hasData bool
	)
	dispatch := func() error {
		if !hasData {
			event = ""
			return nil
		}
		err := fn(event, data.String())
		event, hasData = "", false
		data.Reset()
		return err
	}
	for {
		line, err := readLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return dispatch()
			}
			return err
		}
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// A comment line, for example a keep-alive.
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				if hasData {
					data.WriteByte('\n')
				}
				data.WriteString(value)
				hasData = true
			}
		}
	}
}

// readLine reads one line without its "\n" or "\r\n" end. It returns
// io.EOF only when no bytes are left.
func readLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return string(buf), nil
			}
			return "", err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxSSELine {
			return "", fmt.Errorf("sse: a line is longer than %d bytes", maxSSELine)
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}

// Collect reads the events from ch until ch closes and assembles the result.
// It calls onDelta for each text delta when onDelta is not nil. When an
// EventError arrives, Collect keeps the partial result and returns the error.
// Collect sets FirstTokenMS and TotalMS when the adapter does not set them.
// Collect takes the stamp from the first event that has one, of any type,
// because a stream can stop before its EventDone.
func Collect(ch <-chan Event, onDelta func(string)) (Result, error) {
	var (
		res     Result
		text    strings.Builder
		errOut  error
		first   int64
		started = time.Now()
	)
	mark := func() {
		if first == 0 {
			first = max(time.Since(started).Milliseconds(), 1)
		}
	}
	for ev := range ch {
		if res.Provider == "" && ev.Provider != "" {
			res.Provider, res.Model = ev.Provider, ev.Model
		}
		switch ev.Type {
		case EventText:
			mark()
			text.WriteString(ev.Delta)
			if onDelta != nil && ev.Delta != "" {
				onDelta(ev.Delta)
			}
		case EventToolCall:
			mark()
			if ev.ToolCall != nil {
				res.ToolCalls = append(res.ToolCalls, *ev.ToolCall)
			}
		case EventDone:
			if ev.FinishReason != "" {
				res.FinishReason = ev.FinishReason
			}
			if ev.Usage != nil {
				res.Usage = *ev.Usage
			}
		case EventError:
			if errOut == nil {
				errOut = ev.Err
				if errOut == nil {
					errOut = errors.New("provider: the stream failed with no error detail")
				}
			}
		}
	}
	res.Text = text.String()
	if res.Usage.FirstTokenMS == 0 {
		res.Usage.FirstTokenMS = first
	}
	if res.Usage.TotalMS == 0 {
		res.Usage.TotalMS = time.Since(started).Milliseconds()
	}
	return res, errOut
}
