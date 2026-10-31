package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// FuzzResponsesParse checks that no Responses stream makes the parser panic.
func FuzzResponsesParse(f *testing.F) {
	for _, p := range []string{"testdata/responses_text_stream.sse", "testdata/responses_tool_stream.sse", "testdata/responses_reasoning_tool_stream.sse"} {
		b, _ := os.ReadFile(p)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := &responses{endpoint: endpoint{name: "f", format: "responses", model: "m"}}
		e := &emitter{ctx: context.Background(), ch: make(chan Event, 1024), provider: "f", model: "m"}
		resp := &http.Response{Body: io.NopCloser(bytes.NewReader(data))}
		_ = p.parse(e, resp, time.Now(), "m") // must not panic
	})
}
