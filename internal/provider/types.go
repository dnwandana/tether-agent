// Package provider holds the model provider interface, the wire adapters
// for the OpenAI, Anthropic, and Responses formats, and the fallback chain.
package provider

import "encoding/json"

// Role is the author of a message.
type Role string

// The message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Image is an image that goes inline to a vision model. Data holds the
// bytes. Path is the source file, for display only.
type Image struct {
	Path string
	MIME string
	Data []byte
}

// ToolCall is one tool call from the model. Args is raw JSON.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// Message is one message of the history in the format-neutral shape.
// A RoleTool message carries ToolCallID and ToolName. A RoleAssistant
// message can carry ToolCalls.
type Message struct {
	Role       Role
	Content    string
	Images     []Image
	ToolCallID string
	ToolName   string
	ToolCalls  []ToolCall
	// Reasoning is the reasoning of the model for an assistant message, in
	// the shape of the adapter that produced it. It goes back only to the
	// issuer that produced it.
	Reasoning *Reasoning
}

// Reasoning is the reasoning of the model for one assistant message. The
// adapter compacts each raw item when it arrives, so the bytes in memory
// and the bytes after a reload are the same.
type Reasoning struct {
	// Issuer is "<format> <base_url> <model>" of the call that produced it.
	Issuer string `json:"issuer"`
	// Text is the reasoning_content of the Chat Completions format.
	Text string `json:"text,omitempty"`
	// Output holds all the output items of the Responses format, or the
	// thinking blocks of the Anthropic format, in order.
	Output []json.RawMessage `json:"output,omitempty"`
}

// ToolSchema describes one tool to the model. Parameters is a JSON schema.
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Usage holds the token counts and the timings of one model call.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
	FirstTokenMS    int64
	TotalMS         int64
}

// EventType is the kind of a stream event.
type EventType int

// The stream event kinds.
const (
	EventText EventType = iota
	EventToolCall
	EventDone
	EventError
)

// Event is one item of the internal event stream. EventText sets Delta.
// EventToolCall sets ToolCall. EventDone sets FinishReason and Usage, and
// it can set Reasoning. EventError sets Err. An event of each type can
// carry the stamp: Provider is the config entry name and Model is the model
// of the request that served the stream.
type Event struct {
	Type         EventType
	Delta        string
	ToolCall     *ToolCall
	FinishReason string
	Usage        *Usage
	Err          error
	Reasoning    *Reasoning
	Provider     string
	Model        string
}

// Request is one model call. An empty Model uses the model of the
// provider. A zero MaxTokens uses the default of the adapter.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolSchema
	Model     string
	MaxTokens int
	// CacheKey is the session id. The Responses adapter sends it as the
	// prompt cache key. The other adapters ignore it.
	CacheKey string
	// Effort is the reasoning effort level. The empty value sends no
	// effort field.
	Effort string
	// StartAt is the name of the chain entry that the call starts at. The
	// empty value starts at the first entry.
	StartAt string
	// NeedVision is true when the call has no use without its images, for
	// example view_image. A chain then skips each entry with no vision.
	NeedVision bool
}

// Result is the assembled result of one stream. Reasoning comes from the
// EventDone event. Provider and Model name the provider that served the
// stream. They are empty when no event carries a stamp.
type Result struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	Reasoning    *Reasoning
	Provider     string
	Model        string
}
