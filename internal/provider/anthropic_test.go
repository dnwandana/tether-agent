package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicCapture starts a fake server that stores the request body in
// *body and answers with the tool fixture.
func anthropicCapture(t *testing.T, body *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(body)
		serveFile(w, "testdata/anthropic_tool_stream.sse")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnthropicRequestShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Error(r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		serveFile(w, "testdata/anthropic_tool_stream.sse")
	}))
	defer srv.Close()
	p := NewAnthropic("a", srv.URL, "k", "claude-x", srv.Client())
	ch, err := p.Stream(ctx, Request{System: "S", Messages: []Message{{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Args: `{"path":"a"}`}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "ok"}}, Tools: []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(ch, nil)
	if body["system"] != "S" {
		t.Fatal(body["system"])
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["input_schema"] == nil {
		t.Fatal(tools)
	}
	msgs := body["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blk := last["content"].([]any)[0].(map[string]any)
	if last["role"] != "user" || blk["type"] != "tool_result" || blk["tool_use_id"] != "t1" {
		t.Fatal(last)
	}
	if body["stream"] != true || body["max_tokens"] != float64(8192) || body["model"] != "claude-x" {
		t.Fatal(body)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	tu := asst[0].(map[string]any)
	if len(asst) != 1 || tu["type"] != "tool_use" || tu["id"] != "t1" || tu["input"].(map[string]any)["path"] != "a" {
		t.Fatal(asst)
	}
}

func TestAnthropicStreamToolUseAndUsage(t *testing.T) {
	p := NewAnthropic("a", fixtureServer(t, "testdata/anthropic_tool_stream.sse").URL, "k", "m", http.DefaultClient)
	ch, _ := p.Stream(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	r, err := Collect(ch, nil)
	if err != nil || r.Text != "Reading." || len(r.ToolCalls) != 1 || r.ToolCalls[0].Args != `{"path":"a.txt"}` {
		t.Fatal(r, err)
	}
	if r.FinishReason != "tool_calls" || r.Usage.OutputTokens != 21 || r.Usage.CacheReadTokens != 100 {
		t.Fatal(r)
	}
	if r.ToolCalls[0].ID != "toolu_1" || r.ToolCalls[0].Name != "read_file" || r.Usage.InputTokens != 50 {
		t.Fatal(r)
	}
}

func TestAnthropicConsecutiveToolResultsMergeIntoOneUserMessage(t *testing.T) {
	var body map[string]any
	srv := anthropicCapture(t, &body)
	ch, err := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "Two reads.", ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Args: `{}`}, {ID: "t2", Name: "read_file", Args: ``}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "one"},
		{Role: RoleTool, ToolCallID: "t2", Content: "two"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(ch, nil)
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatal(msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 || asst[0].(map[string]any)["type"] != "text" || asst[1].(map[string]any)["type"] != "tool_use" {
		t.Fatal(asst)
	}
	if in, ok := asst[2].(map[string]any)["input"].(map[string]any); !ok || len(in) != 0 {
		t.Fatal("empty args must become {}", asst[2])
	}
	last := msgs[2].(map[string]any)
	blocks := last["content"].([]any)
	if last["role"] != "user" || len(blocks) != 2 || blocks[1].(map[string]any)["tool_use_id"] != "t2" {
		t.Fatal(last)
	}
}

func TestAnthropicImageBlock(t *testing.T) {
	var body map[string]any
	srv := anthropicCapture(t, &body)
	img := Image{MIME: "image/png", Data: []byte("PNGDATA")}
	ch, err := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "what?", Images: []Image{img}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(ch, nil)
	blocks := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	ib := blocks[1].(map[string]any)
	src := ib["source"].(map[string]any)
	if ib["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "UE5HREFUQQ==" {
		t.Fatal(blocks)
	}
}

func TestAnthropicCacheBreakpointsOnLastMessages(t *testing.T) {
	var body map[string]any
	srv := anthropicCapture(t, &body)
	var msgs []Message
	for i := range 5 {
		msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprint("u", i)}, Message{Role: RoleAssistant, Content: fmt.Sprint("a", i)})
	}
	ch, _ := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{System: "S", Messages: msgs})
	_, _ = Collect(ch, nil)
	n := 0
	for i, m := range body["messages"].([]any) {
		blocks := m.(map[string]any)["content"].([]any)
		if _, ok := blocks[len(blocks)-1].(map[string]any)["cache_control"]; ok {
			n++
			if i < 7 {
				t.Fatal("breakpoint on an early message", i)
			}
		}
	}
	if n != 3 {
		t.Fatal(n)
	}
}

func TestAnthropicSystemNoteGoesToSystem(t *testing.T) {
	var body map[string]any
	srv := anthropicCapture(t, &body)
	ch, _ := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{System: "S", Messages: []Message{
		{Role: RoleUser, Content: "hi"}, {Role: RoleSystem, Content: "Stop now."}}})
	_, _ = Collect(ch, nil)
	if body["system"] != "S\n\nStop now." || len(body["messages"].([]any)) != 1 {
		t.Fatal(body)
	}
}

func TestAnthropic429IsBeforeFirstByte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	defer srv.Close()
	_, err := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
}

func TestAnthropicErrorEventIsNotDropped(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"par\"}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, stream) }))
	defer srv.Close()
	ch, err := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err == nil || !strings.Contains(err.Error(), "Overloaded") || r.Text != "par" {
		t.Fatal(r, err)
	}
}

func TestAnthropicEOFBeforeMessageStopIsError(t *testing.T) {
	stream := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"x\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, stream) }))
	defer srv.Close()
	ch, _ := NewAnthropic("a", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	r, err := Collect(ch, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(r.ToolCalls) != 0 {
		t.Fatal(r, err)
	}
}

func TestAnthropicBaseURLWithV1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Error(r.URL.Path)
		}
		serveFile(w, "testdata/anthropic_tool_stream.sse")
	}))
	defer srv.Close()
	ch, err := NewAnthropic("a", srv.URL+"/v1/", "k", "m", srv.Client()).Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(ch, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicStampsEveryEvent(t *testing.T) {
	srv := fixtureServer(t, "testdata/anthropic_tool_stream.sse")
	ch, err := NewAnthropic("an", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	checkStamp(t, ch, "an", "m")
}
