package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// responses is the adapter for the Responses format of OpenAI and xAI. It
// uses no server-side state: each request sends the full history.
type responses struct {
	endpoint
}

// NewResponses returns a provider that sends POST {baseURL}/responses with
// streaming. The provider has no quirks. A nil hc uses http.DefaultClient.
func NewResponses(name, baseURL, apiKey, model string, hc *http.Client) Provider {
	return newResponses(endpoint{name: name, format: "responses", baseURL: baseURL, apiKey: apiKey, model: model, hc: hc})
}

// newResponses returns the Responses adapter for e.
func newResponses(e endpoint) Provider {
	return &responses{endpoint: e}
}

func (p *responses) Name() string  { return p.name }
func (p *responses) Model() string { return p.model }

// minOutputTokens is the lowest max_output_tokens. OpenAI rejects a lower
// value.
const minOutputTokens = 16

// The request body types of the Responses format. The field order is fixed,
// so the same history gives the same bytes.
type (
	rsRequest struct {
		Model           string       `json:"model"`
		Instructions    string       `json:"instructions,omitempty"`
		Input           []any        `json:"input"`
		Tools           []rsTool     `json:"tools,omitempty"`
		Stream          bool         `json:"stream"`
		Store           bool         `json:"store"`
		Include         []string     `json:"include"`
		MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
		PromptCacheKey  string       `json:"prompt_cache_key,omitempty"`
		Reasoning       *rsReasoning `json:"reasoning,omitempty"`
	}
	rsReasoning struct {
		Effort string `json:"effort"`
	}
	rsTool struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	// rsItem is one input item. One type holds the message, the function
	// call, and the function call output, in the field order of each.
	rsItem struct {
		Type      string   `json:"type"`
		Role      string   `json:"role,omitempty"`
		Content   []rsPart `json:"content,omitempty"`
		CallID    string   `json:"call_id,omitempty"`
		Name      string   `json:"name,omitempty"`
		Arguments string   `json:"arguments,omitempty"`
		Output    *string  `json:"output,omitempty"`
	}
	rsPart struct {
		Type     string  `json:"type"`
		Text     *string `json:"text,omitempty"`
		ImageURL string  `json:"image_url,omitempty"`
	}
)

// Stream sends req and returns the event stream.
func (p *responses) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	h := http.Header{}
	if p.apiKey != "" {
		h.Set("Authorization", "Bearer "+p.apiKey)
	}
	if req.CacheKey != "" && p.has("conv_id_header") {
		h.Set("x-grok-conv-id", req.CacheKey)
	}
	resp, err := postJSON(ctx, p.hc, p.name, joinURL(p.baseURL, "/responses"), h, body, p.apiKey)
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

// buildRequest maps req to the Responses request body.
func (p *responses) buildRequest(req Request) (*rsRequest, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	out := &rsRequest{
		Model:        model,
		Instructions: req.System,
		Input:        []any{},
		Stream:       true,
		Store:        false,
		// include goes on each request, also to a model with no reasoning.
		// OpenAI calls the field legacy, but it accepts it. If a model with
		// no reasoning rejects it, send it only to a model that can reason.
		Include: []string{"reasoning.encrypted_content"},
	}
	if req.MaxTokens > 0 {
		out.MaxOutputTokens = max(req.MaxTokens, minOutputTokens)
	}
	if req.CacheKey != "" && p.has("prompt_cache_key") {
		out.PromptCacheKey = req.CacheKey
	}
	for _, m := range req.Messages {
		items, err := p.items(m, model)
		if err != nil {
			return nil, err
		}
		out.Input = append(out.Input, items...)
	}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = emptySchema
		}
		out.Tools = append(out.Tools, rsTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	style := p.effortStyle
	if style == "" {
		style = "responses"
	}
	if f := mapEffort(style, req.Effort); f.ResponsesEffort != "" {
		out.Reasoning = &rsReasoning{Effort: f.ResponsesEffort}
	}
	return out, nil
}

// items returns the input items of the message m for model.
func (p *responses) items(m Message, model string) ([]any, error) {
	switch m.Role {
	case RoleUser, RoleSystem:
		text := m.Content
		parts := []rsPart{{Type: "input_text", Text: &text}}
		if m.Role == RoleUser {
			for _, img := range m.Images {
				data, mime, err := imageData(img)
				if err != nil {
					return nil, fmt.Errorf("provider %s: image %q: %w", p.name, img.Path, err)
				}
				parts = append(parts, rsPart{Type: "input_image", ImageURL: "data:" + mime + ";base64," + b64(data)})
			}
		}
		return []any{rsItem{Type: "message", Role: string(m.Role), Content: parts}}, nil
	case RoleTool:
		output := m.Content
		return []any{rsItem{Type: "function_call_output", CallID: m.ToolCallID, Output: &output}}, nil
	case RoleAssistant:
		if stored := p.replayItems(m, model); len(stored) > 0 {
			return stored, nil
		}
		var out []any
		if m.Content != "" {
			text := m.Content
			out = append(out, rsItem{Type: "message", Role: string(RoleAssistant), Content: []rsPart{{Type: "output_text", Text: &text}}})
		}
		for _, tc := range m.ToolCalls {
			args := tc.Args
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			out = append(out, rsItem{Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: args})
		}
		return out, nil
	}
	return nil, nil
}

// replayItems returns the stored output items of the assistant message m
// when this endpoint issued them for model. OpenAI and xAI want the items
// back untouched and in order. A last reasoning item with no item after it
// is dropped, because the API rejects it with missing_following_item.
func (p *responses) replayItems(m Message, model string) []any {
	r := m.Reasoning
	if r == nil || len(r.Output) == 0 || r.Issuer != p.issuer(model) {
		return nil
	}
	items := r.Output
	for len(items) > 0 && itemType(items[len(items)-1]) == "reasoning" {
		items = items[:len(items)-1]
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, it)
	}
	return out
}

// itemType returns the "type" field of the raw item, or "" for bad JSON.
func itemType(raw json.RawMessage) string {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Type
}

// The stream event types of the Responses format.
type (
	rsEvent struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		ItemID   string          `json:"item_id"`
		Item     json.RawMessage `json:"item"`
		Response *rsResponse     `json:"response"`
		// Message and Error are the fields of an "error" event.
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	rsResponse struct {
		Usage             *rsUsage `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error json.RawMessage `json:"error"`
	}
	rsUsage struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	rsOutputItem struct {
		Type      string  `json:"type"`
		ID        string  `json:"id"`
		CallID    string  `json:"call_id"`
		Name      string  `json:"name"`
		Arguments *string `json:"arguments"`
	}
)

// rsCall collects one function call of the stream.
type rsCall struct {
	callID, name string
	args         strings.Builder
}

// errResponseEnd stops ReadSSE at the finish event.
var errResponseEnd = errors.New("response done")

// parse reads the SSE body, sends the text deltas at once, and sends each
// tool call when its item is done. The event kind comes from the JSON type
// field, not from the SSE event line. The done event carries all the output
// items of model, compacted and in order, so that they can go back to the
// same issuer.
func (p *responses) parse(e *emitter, resp *http.Response, start time.Time, model string) error {
	var (
		calls   = map[string]*rsCall{} // item id -> call
		items   []json.RawMessage
		nCalls  int
		finish  string
		usage   Usage
		firstMS int64
	)
	markFirst := func() {
		if firstMS == 0 {
			firstMS = max(time.Since(start).Milliseconds(), 1)
		}
	}
	setUsage := func(r *rsResponse) {
		if r == nil || r.Usage == nil {
			return
		}
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens = r.Usage.InputTokens, r.Usage.OutputTokens, 0
		if d := r.Usage.InputTokensDetails; d != nil {
			// input_tokens includes the cache reads. Usage keeps them apart.
			usage.CacheReadTokens = d.CachedTokens
			usage.InputTokens = max(r.Usage.InputTokens-d.CachedTokens, 0)
		}
	}
	err := ReadSSE(resp.Body, func(_, data string) error {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			// xAI sends [DONE] after the finish event.
			return nil
		}
		var ev rsEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("provider %s: bad stream chunk: %w", p.name, err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta == "" {
				return nil
			}
			markFirst()
			if !e.send(Event{Type: EventText, Delta: ev.Delta}) {
				return e.ctx.Err()
			}
		case "response.output_item.added":
			var it rsOutputItem
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				markFirst()
				calls[it.ID] = &rsCall{callID: it.CallID, name: it.Name}
			}
		case "response.function_call_arguments.delta":
			markFirst()
			if c := calls[ev.ItemID]; c != nil {
				c.args.WriteString(ev.Delta)
			}
		case "response.output_item.done":
			var buf bytes.Buffer
			if err := json.Compact(&buf, ev.Item); err != nil {
				return fmt.Errorf("provider %s: bad output item: %w", p.name, err)
			}
			items = append(items, buf.Bytes())
			var it rsOutputItem
			if json.Unmarshal(ev.Item, &it) != nil || it.Type != "function_call" {
				return nil
			}
			markFirst()
			tc := p.doneCall(it, calls[it.ID], nCalls)
			nCalls++
			if !e.send(Event{Type: EventToolCall, ToolCall: tc}) {
				return e.ctx.Err()
			}
		case "response.completed":
			setUsage(ev.Response)
			finish = "stop"
			if nCalls > 0 {
				finish = "tool_calls"
			}
			return errResponseEnd
		case "response.incomplete":
			setUsage(ev.Response)
			finish = "length"
			if r := ev.Response; r != nil && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "content_filter" {
				finish = "content_filter"
			}
			return errResponseEnd
		case "response.failed":
			var raw json.RawMessage
			if ev.Response != nil {
				raw = ev.Response.Error
			}
			return p.streamError(raw, "the response failed")
		case "error":
			if ev.Message != "" {
				return p.streamError(nil, ev.Message)
			}
			return p.streamError(ev.Error, "error event")
		}
		return nil
	})
	switch {
	case errors.Is(err, errResponseEnd):
	case err != nil:
		return err
	default:
		return errEndOfStream(p.name)
	}
	usage.FirstTokenMS = firstMS
	usage.TotalMS = time.Since(start).Milliseconds()
	done := Event{Type: EventDone, FinishReason: finish, Usage: &usage}
	if len(items) > 0 {
		done.Reasoning = &Reasoning{Issuer: p.issuer(model), Output: items}
	}
	if !e.send(done) {
		return e.ctx.Err()
	}
	return nil
}

// doneCall returns the tool call of the done item it. The arguments of the
// done item win over the deltas in acc. n is the index of the call, for a
// call with no id.
func (p *responses) doneCall(it rsOutputItem, acc *rsCall, n int) *ToolCall {
	tc := &ToolCall{ID: it.CallID, Name: it.Name}
	if acc != nil {
		if tc.ID == "" {
			tc.ID = acc.callID
		}
		if tc.Name == "" {
			tc.Name = acc.name
		}
		tc.Args = acc.args.String()
	}
	if it.Arguments != nil && *it.Arguments != "" {
		tc.Args = *it.Arguments
	}
	if tc.ID == "" {
		tc.ID = it.ID
	}
	if tc.ID == "" {
		tc.ID = "call_" + strconv.Itoa(n)
	}
	return tc
}

// streamError returns the error of a failed stream. raw is the error value
// of the provider, and text is the message when raw is empty.
func (p *responses) streamError(raw json.RawMessage, text string) error {
	if len(raw) > 0 && string(raw) != "null" {
		text = errorText(raw)
	}
	return fmt.Errorf("provider %s: stream error: %s", p.name, shortBody(redact(text, p.apiKey)))
}
