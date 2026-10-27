package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// stub is a fake provider that calls onCall and answers with "stub".
type stub struct {
	name   string
	onCall func()
}

func (s *stub) Name() string  { return s.name }
func (s *stub) Model() string { return "stub-model" }
func (s *stub) Stream(context.Context, Request) (<-chan Event, error) {
	if s.onCall != nil {
		s.onCall()
	}
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "stub"}
	ch <- Event{Type: EventDone, FinishReason: "stop", Usage: &Usage{}}
	close(ch)
	return ch, nil
}

// stubProvider returns a fake secondary provider that calls onCall.
func stubProvider(onCall func()) Provider { return &stub{name: "secondary", onCall: onCall} }

func TestFallbackOn503ThenSucceeds(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	good := fixtureServer(t, "testdata/openai_text_stream.sse")
	var logBuf bytes.Buffer
	f := NewFallback(NewOpenAI("primary", bad.URL, "k", "m", bad.Client()), NewOpenAI("fallback", good.URL, "k", "m", good.Client()), slog.New(slog.NewJSONHandler(&logBuf, nil)))
	ch, err := f.Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := Collect(ch, nil); r.Text == "" {
		t.Fatal("no text")
	}
	if !strings.Contains(logBuf.String(), "primary") || !strings.Contains(logBuf.String(), "fallback") {
		t.Fatal(logBuf.String())
	}
	if f.Name() != "primary" || f.Model() != "m" {
		t.Fatal(f.Name(), f.Model())
	}
}

func TestNoFallbackOn400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	called := false
	_, err := NewFallback(NewOpenAI("p", srv.URL, "k", "m", srv.Client()), stubProvider(func() { called = true }), slog.Default()).Stream(ctx, Request{})
	if err == nil || called || !strings.Contains(err.Error(), "400") {
		t.Fatal(err, called)
	}
}

// The stream drops after partial text. The fallback does not run, and the
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
	called := false
	sec := stubProvider(func() { called = true })
	ch, _ := NewFallback(NewOpenAI("p", srv.URL, "k", "m", srv.Client()), sec, slog.Default()).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if err == nil || r.Text != "half" || called {
		t.Fatal(r, err, called)
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
	ch, err := NewFallback(NewOpenAI("p", srv.URL, "k", "m", srv.Client()), stubProvider(nil), slog.Default()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err == nil || len(r.ToolCalls) != 0 {
		t.Fatal(r, err)
	}
}

func TestFallbackOnConnectError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	var n atomic.Int32
	ch, err := NewFallback(NewOpenAI("p", url, "k", "m", nil), stubProvider(func() { n.Add(1) }), slog.Default()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := Collect(ch, nil); r.Text != "stub" || n.Load() != 1 {
		t.Fatal(r, n.Load())
	}
}

func TestNoFallbackOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	_, err := NewFallback(NewOpenAI("p", srv.URL, "k", "m", srv.Client()), stubProvider(func() { called = true }), slog.Default()).Stream(c, Request{})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal(err, called)
	}
}

// recorder is a fake provider that stores the model of each request.
type recorder struct{ models []string }

func (r *recorder) Name() string  { return "rec" }
func (r *recorder) Model() string { return "rec-model" }
func (r *recorder) Stream(_ context.Context, req Request) (<-chan Event, error) {
	r.models = append(r.models, req.Model)
	ch := make(chan Event, 1)
	ch <- Event{Type: EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func TestFallbackDropsModelOverride(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	rec := &recorder{}
	ch, err := NewFallback(NewOpenAI("p", bad.URL, "k", "m", bad.Client()), rec, nil).Stream(ctx, Request{Model: "p-only-model"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(ch, nil)
	if len(rec.models) != 1 || rec.models[0] != "" {
		t.Fatal(rec.models)
	}
}

func TestFactoryRejectsUnknownFormat(t *testing.T) {
	if _, err := New("x", config.Provider{Format: "grpc"}, nil); err == nil || !strings.Contains(err.Error(), "openai | anthropic") {
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
}

func TestFromConfigWrapsPrimaryWithFallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer bad.Close()
	good := fixtureServer(t, "testdata/openai_text_stream.sse")
	cfg := config.Default()
	cfg.Providers["primary"] = config.Provider{Format: "openai", BaseURL: bad.URL, Model: "m1"}
	cfg.Providers["fallback"] = config.Provider{Format: "openai", BaseURL: good.URL, Model: "m2"}
	cfg.Providers["cheap"] = config.Provider{Format: "anthropic", BaseURL: good.URL, Model: "m3"}
	def, all, err := FromConfig(&cfg, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all["cheap"].Model() != "m3" || def.Name() != "primary" {
		t.Fatal(all, def)
	}
	ch, err := def.Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := Collect(ch, nil); err != nil || r.Text == "" {
		t.Fatal(r, err)
	}
	// The entry "primary" in the map is the same wrapped provider, so a
	// session that pins "primary" keeps the fallback.
	if all["primary"] != def {
		t.Fatal("all[primary] is not the default provider")
	}
}

func TestFromConfigWithoutFallback(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "m1"}
	def, all, err := FromConfig(&cfg, nil, nil)
	if err != nil || len(all) != 1 || def != all["primary"] {
		t.Fatal(def, all, err)
	}
	cfg.Providers = map[string]config.Provider{}
	if _, _, err := FromConfig(&cfg, nil, nil); err == nil {
		t.Fatal("want an error without a primary provider")
	}
}
