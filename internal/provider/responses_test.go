package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// rsBody sends req through the Responses adapter of e to a fake server and
// returns the raw request body and the request headers.
func rsBody(t *testing.T, e endpoint, req Request) ([]byte, http.Header) {
	t.Helper()
	var raw []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		hdr = r.Header.Clone()
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	t.Cleanup(srv.Close)
	e.baseURL, e.hc = srv.URL, srv.Client()
	if ch, err := newResponses(e).Stream(ctx, req); err == nil {
		_, _ = Collect(ch, nil)
	}
	return raw, hdr
}

func TestResponsesRequestShape(t *testing.T) {
	e := endpoint{name: "openai", format: "responses", apiKey: "k", model: "gpt-x"}
	req := Request{System: "sys", MaxTokens: 1, Tools: []ToolSchema{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: RoleUser, Content: "q", Images: []Image{{MIME: "image/png", Data: []byte{1}}}},
			{Role: RoleSystem, Content: "note"},
			{Role: RoleAssistant, Content: "a", ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: `{"x":1}`}}},
			{Role: RoleTool, ToolCallID: "c1", Content: "out"},
		}}
	raw, hdr := rsBody(t, e, req)
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	if b["instructions"] != "sys" || b["store"] != false || b["stream"] != true || b["max_output_tokens"] != float64(16) {
		t.Fatalf("%s", raw)
	}
	if fmt.Sprint(b["include"]) != "[reasoning.encrypted_content]" || hdr.Get("Authorization") != "Bearer k" {
		t.Fatalf("%v %v", b["include"], hdr)
	}
	if _, ok := b["previous_response_id"]; ok {
		t.Fatal("the request must use no server-side state")
	}
	tools, _ := json.Marshal(b["tools"])
	eq(t, string(tools), `[{"description":"d","name":"t","parameters":{"type":"object"},"type":"function"}]`)
	want := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"},{"type":"input_image","image_url":"data:image/png;base64,AQ=="}]},` +
		`{"type":"message","role":"system","content":[{"type":"input_text","text":"note"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},` +
		`{"type":"function_call","call_id":"c1","name":"t","arguments":"{\"x\":1}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"out"}]`
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(raw, &body)
	if string(body.Input) != want {
		t.Fatalf("input\n%s", body.Input)
	}
	again, _ := rsBody(t, e, req)
	if !bytes.Equal(raw, again) {
		t.Fatal("the same history gave other bytes")
	}
	if !strings.HasPrefix(string(raw), `{"model":"gpt-x","instructions":"sys","input":[`) {
		t.Fatalf("field order: %s", raw)
	}
}

func TestResponsesReplayRule(t *testing.T) {
	e := endpoint{name: "primary", format: "responses", model: "grok-4.7"}
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { raw, _ = io.ReadAll(r.Body) }))
	t.Cleanup(srv.Close)
	e.baseURL, e.hc = srv.URL, srv.Client()
	item1 := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}`)
	item2 := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"c1","name":"t","arguments":"{}"}`)
	tail := json.RawMessage(`{"type":"reasoning","id":"rs_2","encrypted_content":"END"}`)
	own := &Reasoning{Issuer: e.issuer(""), Output: []json.RawMessage{item1, item2, tail}}
	other := &Reasoning{Issuer: "responses https://api.openai.com/v1 gpt-x", Output: []json.RawMessage{item1}}
	req := Request{Messages: []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: "{}"}}, Reasoning: own},
		{Role: RoleTool, ToolCallID: "c1", Content: "out"},
		{Role: RoleAssistant, Content: "b", Reasoning: other},
	}}
	if ch, err := newResponses(e).Stream(ctx, req); err == nil {
		_, _ = Collect(ch, nil)
	}
	s := string(raw)
	if !strings.Contains(s, `{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"},{"type":"function_call","id":"fc_1"`) {
		t.Fatalf("own items not sent in order: %s", s)
	}
	if strings.Contains(s, "END") {
		t.Fatal("a last reasoning item with nothing after it must be dropped")
	}
	if strings.Count(s, "ENC") != 1 || !strings.Contains(s, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"b"}]}`) {
		t.Fatalf("another issuer must be built from text: %s", s)
	}
}

func TestResponsesQuirksAndEffort(t *testing.T) {
	xai := endpoint{name: "xai", format: "responses", model: "grok", effortStyle: "xai",
		quirks: map[string]bool{"conv_id_header": true, "prompt_cache_key": true}}
	raw, hdr := rsBody(t, xai, Request{CacheKey: "sess-1", Effort: "max"})
	if hdr.Get("x-grok-conv-id") != "sess-1" || !strings.Contains(string(raw), `"prompt_cache_key":"sess-1"`) ||
		!strings.Contains(string(raw), `"reasoning":{"effort":"xhigh"}`) {
		t.Fatalf("%s %v", raw, hdr)
	}
	raw, hdr = rsBody(t, xai, Request{})
	if hdr.Get("x-grok-conv-id") != "" || strings.Contains(string(raw), "prompt_cache_key") || strings.Contains(string(raw), `"reasoning"`) {
		t.Fatalf("empty CacheKey and effort: %s", raw)
	}
	// Without the quirks, a CacheKey sends no header and no field.
	plain := endpoint{name: "openai", format: "responses", model: "gpt-x"}
	raw, hdr = rsBody(t, plain, Request{CacheKey: "sess-1", Effort: "minimal"})
	if hdr.Get("x-grok-conv-id") != "" || strings.Contains(string(raw), "prompt_cache_key") ||
		!strings.Contains(string(raw), `"reasoning":{"effort":"minimal"}`) {
		t.Fatalf("no quirks: %s %v", raw, hdr)
	}
	p, err := New("o", config.Provider{Format: "responses", BaseURL: "http://x", Model: "m"}, nil)
	if err != nil || p.Name() != "o" || p.Model() != "m" {
		t.Fatal(p, err)
	}
	if p := NewResponses("r", "http://x", "k", "m2", nil); p.Name() != "r" || p.Model() != "m2" {
		t.Fatal(p.Name(), p.Model())
	}
}

// rsStream streams the SSE fixture through the Responses adapter.
func rsStream(t *testing.T, fixture string) (Result, error) {
	t.Helper()
	srv := fixtureServer(t, fixture)
	return rsCollect(t, srv)
}

// rsCollect streams the answer of srv through the Responses adapter.
func rsCollect(t *testing.T, srv *httptest.Server) (Result, error) {
	t.Helper()
	ch, err := newResponses(endpoint{name: "xai", format: "responses", baseURL: srv.URL, model: "grok", hc: srv.Client()}).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	return Collect(ch, nil)
}

// inlineServer starts a fake server that answers each request with body as SSE.
func inlineServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResponsesStreamText(t *testing.T) {
	r, err := rsStream(t, "testdata/responses_text_stream.sse")
	if err != nil || r.Text != "Hello there" || r.FinishReason != "stop" || r.Provider != "xai" || r.Model != "grok" {
		t.Fatalf("%+v %v", r, err)
	}
	// input_tokens 100 with 40 cached; output_tokens 7.
	if r.Usage.InputTokens != 60 || r.Usage.CacheReadTokens != 40 || r.Usage.OutputTokens != 7 {
		t.Fatalf("%+v", r.Usage)
	}
}

func TestResponsesStreamTwoToolCallsByItemID(t *testing.T) {
	r, err := rsStream(t, "testdata/responses_tool_stream.sse")
	if err != nil || r.FinishReason != "tool_calls" || len(r.ToolCalls) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if r.ToolCalls[0] != (ToolCall{ID: "call_a", Name: "terminal", Args: `{"cmd":"df -h"}`}) ||
		r.ToolCalls[1] != (ToolCall{ID: "call_b", Name: "terminal", Args: `{"cmd":"du -sh /"}`}) {
		t.Fatalf("%+v", r.ToolCalls)
	}
}

func TestResponsesStreamKeepsOutputItems(t *testing.T) {
	r, err := rsStream(t, "testdata/responses_reasoning_tool_stream.sse")
	if err != nil || r.Reasoning == nil || len(r.Reasoning.Output) != 2 {
		t.Fatalf("%+v %v", r.Reasoning, err)
	}
	if !bytes.HasPrefix(r.Reasoning.Output[0], []byte(`{"type":"reasoning"`)) ||
		!bytes.HasPrefix(r.Reasoning.Output[1], []byte(`{"type":"function_call"`)) ||
		bytes.ContainsAny(r.Reasoning.Output[0], " \n") ||
		!strings.HasPrefix(r.Reasoning.Issuer, "responses http://") || !strings.HasSuffix(r.Reasoning.Issuer, " grok") {
		t.Fatalf("%s", r.Reasoning.Output[0])
	}
}

func TestResponsesStreamEnds(t *testing.T) {
	r, err := rsStream(t, "testdata/responses_incomplete_stream.sse") // reason max_output_tokens, with usage
	if err != nil || r.FinishReason != "length" || r.Usage.OutputTokens == 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := rsStream(t, "testdata/responses_failed_stream.sse"); err == nil || !strings.Contains(err.Error(), "rate limit reached") {
		t.Fatal(err)
	}
	if _, err := rsStream(t, "testdata/responses_eof_stream.sse"); err == nil {
		t.Fatal("EOF before response.completed must be an error")
	}
	r, err = rsCollect(t, inlineServer(t, `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":3,"output_tokens":1}}}`+"\n\n"))
	if err != nil || r.FinishReason != "content_filter" {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = rsCollect(t, inlineServer(t, `data: {"type":"error","code":"server_error","message":"the model is busy"}`+"\n\n"))
	if err == nil || !strings.Contains(err.Error(), "the model is busy") {
		t.Fatal(err)
	}
}
