package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenAIRequestShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Error(r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		serveFile(w, "testdata/openai_text_stream.sse")
	}))
	defer srv.Close()
	p := NewOpenAI("primary", srv.URL+"/v1", "k", "glm-4.7", srv.Client())
	ch, err := p.Stream(ctx, Request{System: "S", Messages: []Message{{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Args: `{}`}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "ok"}}, Tools: []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err != nil || r.Text != "Hello, world." || r.FinishReason != "stop" || r.Usage.InputTokens != 120 || r.Usage.OutputTokens != 5 {
		t.Fatal(r, err)
	}
	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" {
		t.Fatal(msgs)
	}
	if body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatal(body)
	}
	if body["model"] != "glm-4.7" {
		t.Fatal(body["model"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatal("max_tokens is sent but Request.MaxTokens is 0")
	}
	asst := msgs[2].(map[string]any)
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if tc["id"] != "c1" || tc["type"] != "function" || fn["name"] != "read_file" || fn["arguments"] != "{}" {
		t.Fatal(asst)
	}
	if v, ok := asst["content"]; !ok || v != nil {
		t.Fatal("assistant content with tool calls must be null", asst)
	}
	tool := msgs[3].(map[string]any)
	if tool["tool_call_id"] != "c1" || tool["content"] != "ok" {
		t.Fatal(tool)
	}
	tools := body["tools"].([]any)
	tf := tools[0].(map[string]any)
	if tf["type"] != "function" || tf["function"].(map[string]any)["name"] != "read_file" || tf["function"].(map[string]any)["parameters"] == nil {
		t.Fatal(tools)
	}
}

func TestOpenAIAssemblesToolCallDeltasByIndex(t *testing.T) {
	p := NewOpenAI("p", fixtureServer(t, "testdata/openai_tool_stream.sse").URL, "k", "m", http.DefaultClient)
	ch, _ := p.Stream(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	r, err := Collect(ch, nil)
	if err != nil || len(r.ToolCalls) != 2 || r.ToolCalls[1].Args != `{"path":"b.txt"}` || r.FinishReason != "tool_calls" {
		t.Fatal(r, err)
	}
	if r.ToolCalls[0].ID != "call_a" || r.ToolCalls[0].Args != `{"path":"a.txt"}` || r.ToolCalls[1].ID != "call_b" {
		t.Fatal(r.ToolCalls)
	}
	if r.Usage.InputTokens == 0 || r.Usage.CacheReadTokens != 64 { // prompt_tokens_details.cached_tokens
		t.Fatal(r.Usage)
	}
	if r.Usage.InputTokens != 200-64 || r.Usage.OutputTokens != 30 {
		t.Fatal("InputTokens must not include the cache reads", r.Usage)
	}
}

func TestOpenAIImagePart(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		serveFile(w, "testdata/openai_text_stream.sse")
	}))
	defer srv.Close()
	img := Image{MIME: "image/png", Data: []byte("PNGDATA")}
	ch, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "what?", Images: []Image{img}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(ch, nil)
	parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatal(parts)
	}
	text := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "what?" {
		t.Fatal(text)
	}
	ip := parts[1].(map[string]any)
	if ip["type"] != "image_url" || ip["image_url"].(map[string]any)["url"] != "data:image/png;base64,UE5HREFUQQ==" {
		t.Fatal(ip)
	}
}

func TestOpenAI5xxIsBeforeFirstByte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
}

func TestOpenAI429AndConnectErrorAreBeforeFirstByte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	url := srv.URL
	srv.Close()
	_, err = NewOpenAI("p", url, "k", "m", http.DefaultClient).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
}

func TestOpenAI400IsPlainErrorWithRedactedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"bad key sk-secret-123 and `+strings.Repeat("x", 2000)+`"}}`)
	}))
	defer srv.Close()
	_, err := NewOpenAI("p", srv.URL, "sk-secret-123", "m", srv.Client()).Stream(ctx, Request{})
	if err == nil || errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	s := err.Error()
	if !strings.Contains(s, "400") || strings.Contains(s, "sk-secret-123") || len(s) > 800 {
		t.Fatal(s)
	}
}

func TestOpenAIStreamErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n")
	}))
	defer srv.Close()
	ch, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err == nil || !strings.Contains(err.Error(), "overloaded") || r.Text != "a" {
		t.Fatal(r, err)
	}
}

func TestOpenAIEOFWithoutFinishIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
	}))
	defer srv.Close()
	ch, _ := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || r.Text != "a" {
		t.Fatal(r, err)
	}
}

// Hermes: Ollama-style endpoints reuse index 0 for each parallel call and
// tell the calls apart only by id. Some providers send no index, or send
// the full name again in a later delta.
func TestOpenAIToolCallQuirks(t *testing.T) {
	stream := "" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"x1","function":{"name":"read_file","arguments":"{\"a\":1}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"x2","function":{"name":"web_fetch","arguments":""}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"url\":"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"web_fetch","arguments":"\"u\"}"}}]}}],"usage":null}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, stream) }))
	defer srv.Close()
	ch, _ := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if err != nil || len(r.ToolCalls) != 2 || r.FinishReason != "tool_calls" {
		t.Fatal(r, err)
	}
	if r.ToolCalls[0].ID != "x1" || r.ToolCalls[0].Name != "read_file" {
		t.Fatal(r.ToolCalls)
	}
	// The third and fourth deltas have no index and no id, so they join the
	// call that is active at index 0, which is x2. The repeated name is not
	// joined to the first name.
	if r.ToolCalls[1].ID != "x2" || r.ToolCalls[1].Name != "web_fetch" || r.ToolCalls[1].Args != `{"url":"u"}` {
		t.Fatal(r.ToolCalls)
	}
}

func TestOpenAIMissingToolCallIDGetsOne(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, stream) }))
	defer srv.Close()
	ch, _ := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if err != nil || len(r.ToolCalls) != 1 || r.ToolCalls[0].ID == "" {
		t.Fatal(r, err)
	}
}

func TestOpenAICancelClosesChannel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	c, cancel := context.WithCancel(ctx)
	ch, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(c, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-ch; ev.Type != EventText {
		t.Fatal(ev)
	}
	cancel()
	done := make(chan error)
	go func() { _, err := Collect(ch, nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the channel did not close after cancel")
	}
}

// checkStamp reads all events from ch and checks the stamp of each event.
func checkStamp(t *testing.T, ch <-chan Event, name, model string) {
	t.Helper()
	n := 0
	for ev := range ch {
		n++
		if ev.Provider != name || ev.Model != model {
			t.Fatalf("event %d: provider %q, model %q, want %q, %q", n, ev.Provider, ev.Model, name, model)
		}
	}
	if n == 0 {
		t.Fatal("no events")
	}
}

func TestOpenAIStampsEveryEvent(t *testing.T) {
	srv := fixtureServer(t, "testdata/openai_text_stream.sse")
	p := NewOpenAI("primary", srv.URL, "k", "m", srv.Client())
	ch, err := p.Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	checkStamp(t, ch, "primary", "m")
	ch, err = p.Stream(ctx, Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	checkStamp(t, ch, "primary", "x")
}

func TestOpenAIStampsMidStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		hj.Close()
	}))
	defer srv.Close()
	ch, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err == nil || r.Text != "half" || r.Provider != "p" || r.Model != "m" {
		t.Fatal(r, err)
	}
}

func reasoningEndpoint(url string, hc *http.Client) endpoint {
	return endpoint{name: "deepseek", format: "openai", baseURL: url, apiKey: "k", model: "deepseek-chat",
		hc: hc, quirks: map[string]bool{"reasoning_content": true}}
}

func TestOpenAIKeepsReasoningContent(t *testing.T) {
	srv := fixtureServer(t, "testdata/openai_reasoning_stream.sse")
	ch, err := newOpenAI(reasoningEndpoint(srv.URL, srv.Client())).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err != nil || r.Reasoning == nil {
		t.Fatal(err, r.Reasoning)
	}
	if r.Reasoning.Text != "Check df." || r.Reasoning.Issuer != "openai "+srv.URL+" deepseek-chat" || r.Text != "" {
		t.Fatalf("%+v %q", r.Reasoning, r.Text)
	}
}

func TestOpenAIReplaysReasoningAndPads(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body, "testdata/openai_text_stream.sse")
	e := reasoningEndpoint(srv.URL, srv.Client())
	own := &Reasoning{Issuer: e.issuer(""), Text: "plan A"}
	other := &Reasoning{Issuer: "openai https://openrouter.ai/api/v1 x", Text: "plan B"}
	req := Request{Messages: []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, Content: "a1", Reasoning: own},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: "{}"}}, Reasoning: other},
		{Role: RoleTool, ToolCallID: "c1", Content: "out"},
		{Role: RoleAssistant, Content: "a3"}, // an old row: no reasoning
	}}
	if _, err := newOpenAI(e).Stream(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got []any
	for _, m := range body["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "assistant" {
			got = append(got, mm["reasoning_content"])
		}
	}
	if !reflect.DeepEqual(got, []any{"plan A", " ", " "}) {
		t.Fatalf("%v", got)
	}
}

func TestOpenAIWithoutQuirkSendsNoReasoning(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body, "testdata/openai_text_stream.sse")
	req := Request{Messages: []Message{{Role: RoleUser, Content: "q"}, {Role: RoleAssistant, Content: "a", Reasoning: &Reasoning{Text: "x"}}}}
	if _, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(body["messages"]), "reasoning_content") {
		t.Fatal(body["messages"])
	}
}

func chatBody(t *testing.T, e endpoint, req Request) (map[string]any, http.Header) {
	t.Helper()
	var body map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&body)
		serveFile(w, "testdata/openai_text_stream.sse")
	}))
	t.Cleanup(srv.Close)
	e.baseURL, e.hc = srv.URL, srv.Client()
	if _, err := newOpenAI(e).Stream(ctx, req); err != nil {
		t.Fatal(err)
	}
	return body, hdr
}

func TestChatEffortFields(t *testing.T) {
	cases := []struct {
		style, quirk, level string
		want                map[string]any // keys that must be present, with their JSON values
		absent              []string
	}{
		{"glm", "", "medium", map[string]any{"reasoning_effort": "high"}, []string{"thinking"}},
		{"glm", "", "none", map[string]any{"thinking": map[string]any{"type": "disabled"}}, []string{"reasoning_effort"}},
		{"glm", "keep_thinking", "high", map[string]any{"reasoning_effort": "high",
			"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}, nil},
		{"glm", "keep_thinking", "none", map[string]any{"thinking": map[string]any{"type": "disabled"}}, nil},
		{"glm", "keep_thinking", "", map[string]any{"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}, []string{"reasoning_effort"}},
		{"glm-think", "", "none", map[string]any{"reasoning_effort": "low"}, []string{"thinking"}},
		{"glm-think", "keep_thinking", "none", map[string]any{"reasoning_effort": "low",
			"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}, nil},
		{"glm-think", "keep_thinking", "xhigh", map[string]any{"reasoning_effort": "max",
			"thinking": map[string]any{"type": "enabled", "clear_thinking": false}}, nil},
		{"qwen", "", "none", map[string]any{"enable_thinking": false}, []string{"reasoning_effort"}},
		{"qwen", "", "high", map[string]any{"enable_thinking": true, "reasoning_effort": "xhigh"}, nil},
		{"openrouter", "", "low", map[string]any{"reasoning": map[string]any{"effort": "low"}}, []string{"reasoning_effort"}},
		{"ollama", "", "none", map[string]any{"reasoning_effort": "none"}, nil},
		{"chat", "", "", nil, []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking"}},
	}
	for _, c := range cases {
		e := endpoint{name: "p", format: "openai", model: "m", effortStyle: c.style, quirks: map[string]bool{c.quirk: c.quirk != ""}}
		body, _ := chatBody(t, e, Request{Effort: c.level})
		for k, v := range c.want {
			if !reflect.DeepEqual(body[k], v) {
				t.Errorf("%s %s %s: %s = %v", c.style, c.quirk, c.level, k, body[k])
			}
		}
		for _, k := range c.absent {
			if _, ok := body[k]; ok {
				t.Errorf("%s %s %s: %s is present", c.style, c.quirk, c.level, k)
			}
		}
	}
}

func TestOpenRouterHeaders(t *testing.T) {
	e := endpoint{name: "openrouter", format: "openai", model: "m", quirks: map[string]bool{"openrouter_headers": true}}
	_, hdr := chatBody(t, e, Request{})
	if hdr.Get("HTTP-Referer") != "https://github.com/dnwandana/tether-agent" || hdr.Get("X-Title") != "Tether Agent" {
		t.Fatal(hdr)
	}
	_, hdr = chatBody(t, endpoint{name: "p", format: "openai", model: "m"}, Request{})
	if hdr.Get("X-Title") != "" {
		t.Fatal("headers without the quirk")
	}
}
