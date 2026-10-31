package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// stub is a fake provider that calls onCall and answers with "stub". It
// counts its calls and stamps its events with its name.
type stub struct {
	name   string
	onCall func()
	calls  atomic.Int32
}

func (s *stub) Name() string  { return s.name }
func (s *stub) Model() string { return "stub-model" }
func (s *stub) Stream(context.Context, Request) (<-chan Event, error) {
	s.calls.Add(1)
	if s.onCall != nil {
		s.onCall()
	}
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "stub", Provider: s.name, Model: s.Model()}
	ch <- Event{Type: EventDone, FinishReason: "stop", Usage: &Usage{}, Provider: s.name, Model: s.Model()}
	close(ch)
	return ch, nil
}

// recorder is a fake provider that adds the model of each request to
// models, and answers like stub.
type recorder struct {
	name   string
	models *[]string
}

func (r *recorder) Name() string  { return r.name }
func (r *recorder) Model() string { return r.name + "-model" }
func (r *recorder) Stream(c context.Context, req Request) (<-chan Event, error) {
	*r.models = append(*r.models, req.Model)
	return (&stub{name: r.name}).Stream(c, req)
}

func TestChainOn503ThenRealAdapterServes(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	good := fixtureServer(t, "testdata/openai_text_stream.sse")
	ch := chainOf(NewHealth(nil), NewOpenAI("primary", bad.URL, "k", "m", bad.Client()), NewOpenAI("fallback", good.URL, "k", "m2", good.Client()))
	r := mustCollect(t, ch, Request{})
	if r.Text == "" || r.Provider != "fallback" || r.Model != "m2" {
		t.Fatal(r)
	}
	if ch.Name() != "primary" || ch.Model() != "m" {
		t.Fatal(ch.Name(), ch.Model())
	}
}

func TestChainOnConnectError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	sec := &stub{name: "fallback"}
	if r := mustCollect(t, chainOf(NewHealth(nil), NewOpenAI("p", url, "k", "m", nil), sec), Request{}); r.Text != "stub" || sec.calls.Load() != 1 {
		t.Fatal(r, sec.calls.Load())
	}
}

// The stream drops after partial text. The next entry does not run, and the
// partial text stays.
func TestNoFallbackAfterFirstByteAndPartialKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		hj.Close() // drop mid-stream
	}))
	defer srv.Close()
	sec := &stub{name: "fallback"}
	ch, _ := chainOf(NewHealth(nil), NewOpenAI("p", srv.URL, "k", "m", srv.Client()), sec).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if err == nil || r.Text != "half" || sec.calls.Load() != 0 {
		t.Fatal(r, err, sec.calls.Load())
	}
}

// The stream drops in the middle of a tool call. The stream ends with an
// error and gives no tool call.
func TestDropInToolCallEndsWithErrorAndNoCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		hj.Close()
	}))
	defer srv.Close()
	ch, err := chainOf(NewHealth(nil), NewOpenAI("p", srv.URL, "k", "m", srv.Client()), &stub{name: "fallback"}).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err == nil || len(r.ToolCalls) != 0 {
		t.Fatal(r, err)
	}
}

func TestFactoryRejectsUnknownFormat(t *testing.T) {
	if _, err := New("x", config.Provider{Format: "grpc"}, nil); err == nil || !strings.Contains(err.Error(), "openai | anthropic | responses") {
		t.Fatal(err)
	}
}

func TestFactoryFormats(t *testing.T) {
	p, err := New("a", config.Provider{Format: "anthropic", Model: "claude-x"}, nil)
	if err != nil || p.Name() != "a" || p.Model() != "claude-x" {
		t.Fatal(p, err)
	}
	p, err = New("o", config.Provider{Format: "openai", Model: "glm"}, nil)
	if err != nil || p.Name() != "o" || p.Model() != "glm" {
		t.Fatal(p, err)
	}
	p, err = New("r", config.Provider{Format: "responses", Model: "grok"}, nil)
	if err != nil || p.Name() != "r" || p.Model() != "grok" {
		t.Fatal(p, err)
	}
}

func TestStatusOfPlainProvider(t *testing.T) {
	if _, ok := StatusOf(NewOpenAI("p", "http://127.0.0.1:1", "k", "m", nil)); ok {
		t.Fatal("a plain adapter must not report a fallback state")
	}
	if _, ok := StatusOf(chainOf(NewHealth(nil), &stub{name: "p"})); !ok {
		t.Fatal("a chain must report a fallback state")
	}
}
