package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// openAI is the adapter for the OpenAI chat.completions format.
type openAI struct {
	endpoint
}

// NewOpenAI returns a provider that sends POST {baseURL}/chat/completions
// with streaming. The provider has no quirks. A nil hc uses
// http.DefaultClient.
func NewOpenAI(name, baseURL, apiKey, model string, hc *http.Client) Provider {
	return newOpenAI(endpoint{name: name, format: "openai", baseURL: baseURL, apiKey: apiKey, model: model, hc: hc})
}

// newOpenAI returns the chat.completions adapter for e.
func newOpenAI(e endpoint) Provider {
	return &openAI{endpoint: e}
}

func (p *openAI) Name() string  { return p.name }
func (p *openAI) Model() string { return p.model }

// The request body types of the OpenAI format.
type (
	oaRequest struct {
		Model         string      `json:"model"`
		Messages      []oaMessage `json:"messages"`
		Tools         []oaTool    `json:"tools,omitempty"`
		Stream        bool        `json:"stream"`
		StreamOptions oaStreamOpt `json:"stream_options"`
		MaxTokens     int         `json:"max_tokens,omitempty"`
		// The effort fields. The empty effort sends none of them.
		ReasoningEffort string       `json:"reasoning_effort,omitempty"`
		Reasoning       *oaReasoning `json:"reasoning,omitempty"`
		Thinking        *oaThinking  `json:"thinking,omitempty"`
		EnableThinking  *bool        `json:"enable_thinking,omitempty"`
	}
	// oaReasoning is the OpenRouter reasoning object.
	oaReasoning struct {
		Effort string `json:"effort"`
	}
	// oaThinking is the thinking object of Z.AI, DeepSeek, and Kimi.
	oaThinking struct {
		Type          string `json:"type"`
		ClearThinking *bool  `json:"clear_thinking,omitempty"`
	}
	oaStreamOpt struct {
		IncludeUsage bool `json:"include_usage"`
	}
	oaMessage struct {
		Role       string       `json:"role"`
		Content    any          `json:"content"`
		ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
		ToolCallID string       `json:"tool_call_id,omitempty"`
		// ReasoningContent goes only to an entry with the quirk
		// reasoning_content.
		ReasoningContent *string `json:"reasoning_content,omitempty"`
	}
	oaPart struct {
		Type     string      `json:"type"`
		Text     string      `json:"text,omitempty"`
		ImageURL *oaImageURL `json:"image_url,omitempty"`
	}
	oaImageURL struct {
		URL string `json:"url"`
	}
	oaToolCall struct {
		ID       string     `json:"id"`
		Type     string     `json:"type"`
		Function oaFunction `json:"function"`
	}
	oaFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	oaTool struct {
		Type     string        `json:"type"`
		Function oaToolFuncDef `json:"function"`
	}
	oaToolFuncDef struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	}
)

// The stream chunk types of the OpenAI format.
type (
	oaChunk struct {
		Choices []oaChoice      `json:"choices"`
		Usage   *oaUsage        `json:"usage"`
		Error   json.RawMessage `json:"error"`
	}
	oaChoice struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []oaDeltaToolC `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	oaDeltaToolC struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	oaUsage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
)

// emptySchema is the parameters schema for a tool that has none.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// The OpenRouter app headers. OpenRouter shows the app name in its
// rankings and its activity page.
const (
	openRouterReferer = "https://github.com/dnwandana/tether-agent"
	openRouterTitle   = "Tether Agent"
)

// errStreamDone stops ReadSSE at the [DONE] marker.
var errStreamDone = errors.New("stream done")

// Stream sends req and returns the event stream.
func (p *openAI) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	h := http.Header{}
	if p.apiKey != "" {
		h.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.has("openrouter_headers") {
		h.Set("HTTP-Referer", openRouterReferer)
		h.Set("X-Title", openRouterTitle)
	}
	resp, err := postJSON(ctx, p.hc, p.name, joinURL(p.baseURL, "/chat/completions"), h, body, p.apiKey)
	if err != nil {
		return nil, err
	}
	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		e := &emitter{ctx: ctx, ch: ch, provider: p.name, model: body.Model}
		if err := p.parse(e, resp, start, body.Model); err != nil {
			e.fail(err)
		}
	}()
	return ch, nil
}

// buildRequest maps req to the OpenAI request body.
func (p *openAI) buildRequest(req Request) (*oaRequest, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	out := &oaRequest{
		Model:         model,
		Stream:        true,
		StreamOptions: oaStreamOpt{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, oaMessage{Role: string(RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: string(m.Role), Content: m.Content}
		switch m.Role {
		case RoleAssistant:
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				om.ToolCalls = append(om.ToolCalls, oaToolCall{ID: tc.ID, Type: "function", Function: oaFunction{Name: tc.Name, Arguments: args}})
			}
			if len(om.ToolCalls) > 0 && m.Content == "" {
				om.Content = nil
			}
			if p.has("reasoning_content") {
				om.ReasoningContent = p.replayText(m, model)
			}
		case RoleTool:
			om.ToolCallID = m.ToolCallID
		case RoleUser:
			if len(m.Images) > 0 {
				parts := []oaPart{{Type: "text", Text: m.Content}}
				for _, img := range m.Images {
					data, mime, err := imageData(img)
					if err != nil {
						return nil, fmt.Errorf("provider %s: image %q: %w", p.name, img.Path, err)
					}
					parts = append(parts, oaPart{Type: "image_url", ImageURL: &oaImageURL{URL: "data:" + mime + ";base64," + b64(data)}})
				}
				om.Content = parts
			}
		}
		out.Messages = append(out.Messages, om)
	}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptySchema
		}
		out.Tools = append(out.Tools, oaTool{Type: "function", Function: oaToolFuncDef{Name: t.Name, Description: t.Description, Parameters: params}})
	}
	p.setEffort(out, req.Effort)
	return out, nil
}

// setEffort puts the effort fields of level on out. The quirk keep_thinking
// asks Z.AI to keep the thinking of earlier turns. It shares one thinking
// object with a disabled thinking, because the API accepts only one.
func (p *openAI) setEffort(out *oaRequest, level string) {
	f := mapEffort(p.effortStyle, level)
	out.ReasoningEffort = f.ReasoningEffort
	if f.ResponsesEffort != "" {
		out.Reasoning = &oaReasoning{Effort: f.ResponsesEffort}
	}
	out.EnableThinking = f.EnableThinking
	switch {
	case f.ThinkingDisabled:
		out.Thinking = &oaThinking{Type: "disabled"}
	case p.has("keep_thinking"):
		keep := false
		out.Thinking = &oaThinking{Type: "enabled", ClearThinking: &keep}
	}
}

// reasoningPad is the reasoning_content of an assistant message with no
// stored reasoning from this issuer. DeepSeek and Kimi need the field on
// each assistant message, and DeepSeek rejects an empty string (Hermes).
const reasoningPad = " "

// replayText returns the reasoning_content of the assistant message m: the
// stored text when this endpoint issued it for model, else the pad.
func (p *openAI) replayText(m Message, model string) *string {
	if r := m.Reasoning; r != nil && r.Text != "" && r.Issuer == p.issuer(model) {
		return &r.Text
	}
	pad := reasoningPad
	return &pad
}

// oaToolAcc collects the deltas of one tool call.
type oaToolAcc struct {
	id, name string
	args     strings.Builder
}

// parse reads the SSE body, sends the text deltas at once, and sends the
// tool calls and the done event at the end of the stream. With the quirk
// reasoning_content, the done event carries the reasoning text of model.
func (p *openAI) parse(e *emitter, resp *http.Response, start time.Time, model string) error {
	var (
		thinking   strings.Builder
		acc        = map[int]*oaToolAcc{}
		lastID     = map[int]string{} // raw index -> last id seen at it
		activeSlot = map[int]int{}    // raw index -> slot in acc
		finish     string
		usage      Usage
		firstMS    int64
		ended      bool
	)
	markFirst := func() {
		if firstMS == 0 {
			firstMS = max(time.Since(start).Milliseconds(), 1)
		}
	}
	err := ReadSSE(resp.Body, func(_, data string) error {
		data = strings.TrimSpace(data)
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			return errStreamDone
		}
		var c oaChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return fmt.Errorf("provider %s: bad stream chunk: %w", p.name, err)
		}
		if len(c.Error) > 0 && string(c.Error) != "null" {
			return fmt.Errorf("provider %s: stream error: %s", p.name, shortBody(redact(errorText(c.Error), p.apiKey)))
		}
		if c.Usage != nil {
			usage.InputTokens = c.Usage.PromptTokens
			usage.OutputTokens = c.Usage.CompletionTokens
			usage.CacheReadTokens = 0
			if d := c.Usage.PromptTokensDetails; d != nil {
				usage.CacheReadTokens = d.CachedTokens
				// prompt_tokens includes the cache reads. Usage keeps them apart.
				usage.InputTokens = max(c.Usage.PromptTokens-d.CachedTokens, 0)
			}
		}
		if len(c.Choices) == 0 {
			return nil
		}
		ch := c.Choices[0]
		if ch.Delta.ReasoningContent != "" && p.has("reasoning_content") {
			// The reasoning text is not an answer, so it never goes into
			// an EventText.
			markFirst()
			thinking.WriteString(ch.Delta.ReasoningContent)
		}
		if ch.Delta.Content != "" {
			markFirst()
			if !e.send(Event{Type: EventText, Delta: ch.Delta.Content}) {
				return e.ctx.Err()
			}
		}
		for _, d := range ch.Delta.ToolCalls {
			markFirst()
			raw := 0
			if d.Index != nil {
				raw = *d.Index
			}
			if _, ok := activeSlot[raw]; !ok {
				activeSlot[raw] = raw
			}
			// Ollama-style endpoints reuse one index for parallel calls and
			// tell them apart only by id (Hermes).
			if d.ID != "" {
				if prev, ok := lastID[raw]; ok && prev != d.ID {
					next := -1
					for k := range acc {
						next = max(next, k)
					}
					activeSlot[raw] = next + 1
				}
				lastID[raw] = d.ID
			}
			slot := activeSlot[raw]
			a := acc[slot]
			if a == nil {
				a = &oaToolAcc{}
				acc[slot] = a
			}
			if a.id == "" && d.ID != "" {
				a.id = d.ID
			}
			if d.Function.Name != "" {
				// Some providers send the full name again in a later delta.
				// Keep the name; do not join the two.
				a.name = d.Function.Name
			}
			a.args.WriteString(d.Function.Arguments)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finish = *ch.FinishReason
		}
		return nil
	})
	switch {
	case errors.Is(err, errStreamDone):
		ended = true
	case err != nil:
		return err
	case finish != "":
		// Some providers end the stream without [DONE].
		ended = true
	}
	if !ended {
		return errEndOfStream(p.name)
	}

	slots := make([]int, 0, len(acc))
	for k := range acc {
		slots = append(slots, k)
	}
	sort.Ints(slots)
	for i, k := range slots {
		a := acc[k]
		id := a.id
		if id == "" {
			id = "call_" + strconv.Itoa(i)
		}
		tc := &ToolCall{ID: id, Name: a.name, Args: a.args.String()}
		if !e.send(Event{Type: EventToolCall, ToolCall: tc}) {
			return e.ctx.Err()
		}
	}
	if len(slots) > 0 && (finish == "" || finish == "stop") {
		finish = "tool_calls"
	}
	if finish == "" {
		finish = "stop"
	}
	usage.FirstTokenMS = firstMS
	usage.TotalMS = time.Since(start).Milliseconds()
	done := Event{Type: EventDone, FinishReason: finish, Usage: &usage}
	if thinking.Len() > 0 {
		done.Reasoning = &Reasoning{Issuer: p.issuer(model), Text: thinking.String()}
	}
	if !e.send(done) {
		return e.ctx.Err()
	}
	return nil
}

// errorText returns the message of a JSON error value. The value can be a
// string or an object with a "message" field.
func errorText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Message != "" {
		return o.Message
	}
	return string(raw)
}
