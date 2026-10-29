// Package tools holds the tool interface, the registry with safe dispatch,
// the toolset policy, and the core tools.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// ErrToolError marks an error whose message is meant for the model. Dispatch
// returns the message of such an error without the "tool <name> failed" prefix.
var ErrToolError = errors.New("tool error")

// toolErr returns an error that wraps ErrToolError with a message for the model.
func toolErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrToolError, fmt.Sprintf(format, args...))
}

// Tool is one tool that the model can call.
type Tool interface {
	Name() string
	Toolset() string
	Schema() provider.ToolSchema
	ParallelSafe() bool
	Run(ctx context.Context, args json.RawMessage, env *Env) (string, error)
}

// Env is the per-call context of a tool.
type Env struct {
	SessionID  string
	SessionKey string
	Kind       string
	// RootKind is the kind of the top-level run of a child run, for example "cron" for
	// a child of a cron job. It is empty for a top-level run. Use RunRootKind to read it.
	RootKind string
	Channel  string
	WorkDir  string
	DataDir  string
	Log      *slog.Logger
	Ask      Asker
	Gate     Approver
	Progress func(line string)
	// Values holds per-session tool state, for example the read tracker.
	Values map[string]any
	// ScriptCall is true for a call from an execute_code script. The script reads a
	// successful result as data, so the result gets no note.
	ScriptCall bool
}

// RunRootKind returns the kind of the top-level run of env: RootKind, or Kind for a
// top-level run.
func (e *Env) RunRootKind() string {
	if e == nil {
		return ""
	}
	if e.RootKind != "" {
		return e.RootKind
	}
	return e.Kind
}

// Question is a question to the user.
type Question struct {
	Text    string
	Choices []string
	Multi   bool
	Timeout time.Duration
}

// Answer is the answer of the user to a Question.
type Answer struct {
	Text    string
	Choices []string
}

// Asker asks the user a question on the channel of the session.
type Asker interface {
	Ask(ctx context.Context, q Question) (Answer, error)
}

// Approver checks a shell command against the approval gate.
type Approver interface {
	CheckCommand(ctx context.Context, env *Env, command string) error
}

// Result is the result of one tool call for the model.
type Result struct {
	Text    string
	IsError bool
}

// Registry holds the tools by name. It is safe for concurrent use: /reload-mcp
// changes the MCP tools while turns read the registry.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds t. It panics on a duplicate name, because a duplicate at
// startup is a programming error. Use Replace for a name that can exist.
func (r *Registry) Register(t Tool) {
	name := t.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; ok {
		panic(fmt.Sprintf("tools: tool %q is registered two times", name))
	}
	r.tools[name] = t
}

// Unregister removes the tool with the given name. It reports whether the tool
// was in the registry.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tools[name]
	delete(r.tools, name)
	return ok
}

// Replace removes the tools named in remove and then adds the tools in add, in
// one step. A reader sees the old set or the new set, never a part of the
// change. Replace does not add a tool whose name is still in the registry. It
// returns the names of these tools, in the order of add.
func (r *Registry) Replace(remove []string, add []Tool) (skipped []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range remove {
		delete(r.tools, name)
	}
	for _, t := range add {
		name := t.Name()
		if _, ok := r.tools[name]; ok {
			skipped = append(skipped, name)
			continue
		}
		r.tools[name] = t
	}
	return skipped
}

// Get returns the tool with the given name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All returns all tools, sorted by name. The slice is a copy, so a later
// change of the registry does not change it.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Dispatch runs the tool name with args. A nil visible map makes no tool
// visible. Dispatch never panics. Each failure becomes an error Result with
// a text that names the cause. The text is always valid UTF-8.
func (r *Registry) Dispatch(ctx context.Context, name string, args json.RawMessage, env *Env, visible map[string]bool) (res Result) {
	// The lock covers only the lookup. A long tool call does not block a reload.
	t, ok := r.Get(name)
	if !ok {
		return Result{Text: fmt.Sprintf("unknown tool %q", name), IsError: true}
	}
	if !visible[name] {
		return Result{Text: fmt.Sprintf("tool %q is not available in this session", name), IsError: true}
	}
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return Result{Text: fmt.Sprintf("tool %s: invalid JSON arguments", name), IsError: true}
	}
	if env == nil {
		env = &Env{}
	}
	defer func() {
		if p := recover(); p != nil {
			log := env.Log
			if log == nil {
				log = slog.Default()
			}
			log.Error("tool panic", "tool", name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			res = Result{Text: fmt.Sprintf("tool %s failed: internal error", name), IsError: true}
		}
	}()
	// The check runs under the recover. A panic result replaces the whole
	// result, so it gets no note.
	note := argsNote(name, t, args, env)
	out, err := t.Run(ctx, args, env)
	if err != nil {
		return Result{Text: ctxmgr.SanitizeUTF8(withNote(errorText(name, err), note)), IsError: true}
	}
	if env.ScriptCall {
		note = ""
	}
	return Result{Text: ctxmgr.SanitizeUTF8(withNote(out, note))}
}

// argsNote checks the top-level keys of args against the schema of t. For
// unknown keys, it writes a Warn line with the key names and returns the note
// for the model. The Warn line never holds an argument value.
func argsNote(name string, t Tool, args json.RawMessage, env *Env) string {
	unknown, known, ok := unknownArgs(args, t.Schema().Parameters)
	if !ok || len(unknown) == 0 {
		return ""
	}
	log := env.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("tools: the call has unknown arguments", "tool", name, "keys", strings.Join(unknown, ","))
	return unknownArgsNote(unknown, known)
}

// withNote adds note to text after one blank line. An empty text gets the note
// alone.
func withNote(text, note string) string {
	switch {
	case note == "":
		return text
	case text == "":
		return note
	}
	return text + "\n\n" + note
}

// errorText returns the model text for err from the tool name.
func errorText(name string, err error) string {
	msg := err.Error()
	if errors.Is(err, ErrToolError) {
		msg = strings.TrimPrefix(msg, ErrToolError.Error()+": ")
		if strings.TrimSpace(msg) == "" || msg == ErrToolError.Error() {
			msg = fmt.Sprintf("tool %s failed", name)
		}
		return ctxmgr.SanitizeUTF8(msg)
	}
	if strings.TrimSpace(msg) == "" {
		msg = "unknown error"
	}
	return ctxmgr.SanitizeUTF8(fmt.Sprintf("tool %s failed: %s", name, msg))
}

// decodeArgs decodes the JSON arguments of a call into v. A bad shape is an
// error for the model.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return toolErr("invalid arguments: %v", err)
	}
	return nil
}
