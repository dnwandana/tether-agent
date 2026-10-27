package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// anthropicVersion is the value of the anthropic-version header.
const anthropicVersion = "2023-06-01"

// anthropicMaxTokens is max_tokens when Request.MaxTokens is not set.
const anthropicMaxTokens = 8192

// anthropicCacheBreakpoints is the number of last messages that get a
// cache_control marker. The system prompt and the tools are part of the
// cached prefix of each marker.
const anthropicCacheBreakpoints = 3

// anthropic is the adapter for the Anthropic messages format.
type anthropic struct {
	name, baseURL, apiKey, model string
	hc                           *http.Client
}

// NewAnthropic returns a provider that sends POST {baseURL}/v1/messages
// with streaming. When baseURL already ends with /v1, the adapter does not
// add it again. A nil hc uses http.DefaultClient.
func NewAnthropic(name, baseURL, apiKey, model string, hc *http.Client) Provider {
	return &anthropic{name: name, baseURL: baseURL, apiKey: apiKey, model: model, hc: hc}
}

func (p *anthropic) Name() string  { return p.name }
func (p *anthropic) Model() string { return p.model }

// The request body types of the Anthropic format.
type (
	anRequest struct {
		Model     string      `json:"model"`
		MaxTokens int         `json:"max_tokens"`
		System    string      `json:"system,omitempty"`
		Messages  []anMessage `json:"messages"`
		Tools     []anTool    `json:"tools,omitempty"`
		Stream    bool        `json:"stream"`
	}
	anMessage struct {
		Role    string    `json:"role"`
		Content []anBlock `json:"content"`
	}
	anBlock struct {
		Type         string          `json:"type"`
		Text         string          `json:"text,omitempty"`
		ID           string          `json:"id,omitempty"`
		Name         string          `json:"name,omitempty"`
		Input        json.RawMessage `json:"input,omitempty"`
		ToolUseID    string          `json:"tool_use_id,omitempty"`
		Content      string          `json:"content,omitempty"`
		Source       *anSource       `json:"source,omitempty"`
		CacheControl *anCache        `json:"cache_control,omitempty"`
	}
	anSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	anCache struct {
		Type string `json:"type"`
	}
	anTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
)

// The stream event types of the Anthropic format.
type (
	anEvent struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message *struct {
			Usage *anUsage `json:"usage"`
		} `json:"message"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *anUsage        `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	anUsage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
)

// Stream sends req and returns the event stream.
func (p *anthropic) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	h := http.Header{}
	h.Set("anthropic-version", anthropicVersion)
	if p.apiKey != "" {
		h.Set("x-api-key", p.apiKey)
	}
	base := strings.TrimRight(p.baseURL, "/")
	url := base + "/v1/messages"
	if strings.HasSuffix(base, "/v1") {
		url = base + "/messages"
	}
	resp, err := postJSON(ctx, p.hc, p.name, url, h, body, p.apiKey)
	if err != nil {
		return nil, err
	}
	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		e := &emitter{ctx: ctx, ch: ch}
		if err := p.parse(e, resp, start); err != nil {
			e.fail(err)
		}
	}()
	return ch, nil
}

// buildRequest maps req to the Anthropic request body.
func (p *anthropic) buildRequest(req Request) (*anRequest, error) {
	out := &anRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
		Stream:    true,
	}
	if out.Model == "" {
		out.Model = p.model
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = anthropicMaxTokens
	}
	// add appends blocks as a message of role. It joins them to the last
	// message when that message has the same role, because the format
	// needs the roles to alternate.
	add := func(role string, blocks ...anBlock) {
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Content = append(out.Messages[n-1].Content, blocks...)
			return
		}
		out.Messages = append(out.Messages, anMessage{Role: role, Content: blocks})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			// The format has no system role in the history. A system note
			// goes after the system prompt.
			if out.System != "" {
				out.System += "\n\n"
			}
			out.System += m.Content
		case RoleTool:
			add("user", anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		case RoleAssistant:
			var blocks []anBlock
			if m.Content != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: toolInput(tc.Args)})
			}
			if len(blocks) == 0 {
				// The format does not accept an empty assistant message (Hermes).
				blocks = append(blocks, anBlock{Type: "text", Text: "(empty)"})
			}
			add("assistant", blocks...)
		default:
			var blocks []anBlock
			if m.Content != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				data, mime, err := imageData(img)
				if err != nil {
					return nil, fmt.Errorf("provider %s: image %q: %w", p.name, img.Path, err)
				}
				blocks = append(blocks, anBlock{Type: "image", Source: &anSource{Type: "base64", MediaType: mime, Data: b64(data)}})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anBlock{Type: "text", Text: "(empty)"})
			}
			add("user", blocks...)
		}
	}
	// Mark the last messages for the prompt cache (Hermes system_and_3).
	for i := max(len(out.Messages)-anthropicCacheBreakpoints, 0); i < len(out.Messages); i++ {
		c := out.Messages[i].Content
		c[len(c)-1].CacheControl = &anCache{Type: "ephemeral"}
	}
	for _, t := range req.Tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = emptySchema
		}
		out.Tools = append(out.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out, nil
}

// toolInput returns args as a JSON object. Empty or bad args become {},
// because the format needs an object.
func toolInput(args string) json.RawMessage {
	s := strings.TrimSpace(args)
	if !strings.HasPrefix(s, "{") || !json.Valid([]byte(s)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// anToolAcc collects the parts of one tool_use block.
type anToolAcc struct {
	id, name string
	start    json.RawMessage
	args     strings.Builder
}

// parse reads the SSE body, sends the text deltas at once, and sends the
// tool calls and the done event at message_stop.
func (p *anthropic) parse(e *emitter, resp *http.Response, start time.Time) error {
	var (
		tools   = map[int]*anToolAcc{}
		stop    string
		usage   Usage
		firstMS int64
	)
	markFirst := func() {
		if firstMS == 0 {
			firstMS = max(time.Since(start).Milliseconds(), 1)
		}
	}
	setUsage := func(u *anUsage) {
		if u == nil {
			return
		}
		// Some compatible servers send the input counts in message_delta.
		if in := u.InputTokens + u.CacheCreationInputTokens; in > 0 {
			usage.InputTokens = in
		}
		if u.CacheReadInputTokens > 0 {
			usage.CacheReadTokens = u.CacheReadInputTokens
		}
		if u.OutputTokens > 0 {
			usage.OutputTokens = u.OutputTokens
		}
	}
	err := ReadSSE(resp.Body, func(event, data string) error {
		if strings.TrimSpace(data) == "" {
			return nil
		}
		var ev anEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("provider %s: bad stream event %q: %w", p.name, event, err)
		}
		if ev.Type == "" {
			ev.Type = event
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				setUsage(ev.Message.Usage)
			}
		case "content_block_start":
			if cb := ev.ContentBlock; cb != nil && cb.Type == "tool_use" {
				markFirst()
				tools[ev.Index] = &anToolAcc{id: cb.ID, name: cb.Name, start: cb.Input}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					markFirst()
					if !e.send(Event{Type: EventText, Delta: ev.Delta.Text}) {
						return e.ctx.Err()
					}
				}
			case "input_json_delta":
				if t := tools[ev.Index]; t != nil {
					t.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stop = ev.Delta.StopReason
			}
			setUsage(ev.Usage)
		case "message_stop":
			return errStreamDone
		case "error":
			return fmt.Errorf("provider %s: stream error: %s", p.name, shortBody(redact(errorText(ev.Error), p.apiKey)))
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStreamDone) {
		return err
	}
	if err == nil {
		return errEndOfStream(p.name)
	}

	idx := make([]int, 0, len(tools))
	for k := range tools {
		idx = append(idx, k)
	}
	sort.Ints(idx)
	for _, k := range idx {
		t := tools[k]
		args := t.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
			if s := strings.TrimSpace(string(t.start)); s != "" && s != "{}" && s != "null" {
				args = s
			}
		}
		if !e.send(Event{Type: EventToolCall, ToolCall: &ToolCall{ID: t.id, Name: t.name, Args: args}}) {
			return e.ctx.Err()
		}
	}
	finish := stop
	switch stop {
	case "tool_use":
		finish = "tool_calls"
	case "end_turn", "stop_sequence", "":
		finish = "stop"
	case "max_tokens":
		finish = "length"
	}
	if len(idx) > 0 && finish == "stop" {
		finish = "tool_calls"
	}
	usage.FirstTokenMS = firstMS
	usage.TotalMS = time.Since(start).Milliseconds()
	if !e.send(Event{Type: EventDone, FinishReason: finish, Usage: &usage}) {
		return e.ctx.Err()
	}
	return nil
}
