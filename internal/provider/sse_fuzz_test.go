package provider

import (
	"errors"
	"strings"
	"testing"
)

// FuzzReadSSE feeds random streams to ReadSSE. It must not panic, it must not
// call fn more often than the stream has lines, and it must return the error
// of fn unchanged.
func FuzzReadSSE(f *testing.F) {
	f.Add("data: x\n\n")
	f.Add("event: message_start\ndata: {\"a\":1}\n\n")
	f.Add(": keep-alive\r\ndata: a\r\ndata: b\r\n\r\n")
	f.Add("data")
	f.Add("data:\n\ndata: tail")
	f.Fuzz(func(t *testing.T, in string) {
		calls := 0
		err := ReadSSE(strings.NewReader(in), func(_, _ string) error {
			calls++
			return nil
		})
		if lines := strings.Count(in, "\n") + 1; calls > lines {
			t.Fatalf("%d events from %d lines", calls, lines)
		}
		// strings.Reader gives no read error, so only a long line can fail.
		if err != nil && !strings.Contains(err.Error(), "longer than") {
			t.Fatalf("unexpected error after %d events: %v", calls, err)
		}

		stop := errors.New("stop")
		err = ReadSSE(strings.NewReader(in), func(_, _ string) error { return stop })
		if calls > 0 && !errors.Is(err, stop) {
			t.Fatalf("the error of fn is lost: %v", err)
		}
	})
}
