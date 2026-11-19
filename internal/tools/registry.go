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
	"github.com/dnwandana/tether-agent/internal/security"
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
	Progress ProgressFunc
	// Values holds per-session tool state, for example the read tracker.
	Values map[string]any
	// Todo holds the todo limits of the session. A zero field uses the const.
	Todo TodoLimits
	// ScriptCall is true for a call from an execute_code script. The script reads a
	// successful result as data, so the result gets no note.
	ScriptCall bool
	// Member is true for the tool context of a member turn. The zero value means owner,
	// so each Env of a member turn must set it.
	Member bool
	// ModeScope is the /mode scope of the chat or the topic of the turn. "" for a cron run.
	ModeScope string
	// LogScope is the scope of the approval log rows. A cron run sets it from the origin chat
	// of its job.
	LogScope string
	// UserID is the user of the inbound message of the turn. A goal turn, an enqueued turn,
	// and a cron run have no user.
	UserID string
	// RootSessionID is the session of the top-level run of a child run. It is empty for a
	// top-level run. Use RunRootSession to read it.
	RootSessionID string
	// JobName and JobPurpose are the name and the raw prompt of the cron job of the run.
	JobName, JobPurpose string
	// DraftOnly is true for the run of a draft-only cron job and for each of its children.
	DraftOnly bool
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

// RunRootSession returns the session of the top-level run of env: RootSessionID, or
// SessionID for a top-level run. A nil env gives "".
func (e *Env) RunRootSession() string {
	if e == nil {
		return ""
	}
	if e.RootSessionID != "" {
		return e.RootSessionID
	}
	return e.SessionID
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

// CommandCheck is one shell command for the approval gate. Workdir is the resolved, clean,
// absolute workdir, or "" for no base folder. Path is the PATH value of the child
// environment. Summary is the cleaned description of the model, or "". Secrets holds the
// bound names that the command can get. StagedCredentials holds the credential files that a
// "git add" command stages. StageUnchecked is true when the check of the staged files failed.
type CommandCheck struct {
	Command, Workdir, Path, Summary string
	Secrets                         []string
	StagedCredentials               []string
	StageUnchecked                  bool
}

// Approver checks a shell command against the approval gate.
type Approver interface {
	CheckCommand(ctx context.Context, env *Env, c CommandCheck) error
}

// ScriptApprover is an optional interface of Env.Gate. It checks the full text of an
// execute_code script. execute_code refuses a script when the gate does not implement it.
type ScriptApprover interface {
	CheckScript(ctx context.Context, env *Env, code, summary string) error
}

// PermissionAsker asks the user about one permission request of a coding agent. The
// acp_delegate tool reads it from env.Gate with a type assertion.
type PermissionAsker interface {
	AskPermission(ctx context.Context, env *Env, req security.ApprovalRequest) (security.Scope, error)
}

// MCPApprover is an optional interface of Env.Gate. An MCP tool of a class other than read
// reads it with a type assertion. With no MCPApprover, such a call does not run. The result
// args is the argument object to send, or nil to send the arguments of the model.
type MCPApprover interface {
	CheckMCP(ctx context.Context, env *Env, call security.MCPCall) (json.RawMessage, error)
}

// PinRequest asks the owner to allow one program for a bound secret, or to allow the
// printed credentials that a command names after the turn read outside content. Name, Path,
// and SHA256 describe the program. Handles and Sources describe the credentials.
type PinRequest struct {
	Name, Path, SHA256, Command string
	Handles                     []string
	Sources                     []string
}

// SecretPinner is an optional interface of Env.Gate. The terminal tool reads it with a type
// assertion. Without it, a program that has no pin does not get the secret.
type SecretPinner interface {
	AskPin(ctx context.Context, env *Env, r PinRequest) (bool, error)
}

// SecretQuestion asks the owner for the value of one bound secret. Commands holds the
// bound command prefixes. Reason is the cleaned reason of the model.
type SecretQuestion struct {
	Name     string
	Commands []string
	Reason   string
}

// SecretAsker is an optional interface of Env.Ask. The UI saves the value itself. The
// value never comes back to the tool.
type SecretAsker interface {
	AskSecret(ctx context.Context, q SecretQuestion) (saved bool, err error)
}

// SecretMarks records the bound secrets that got an auth failure.
type SecretMarks interface {
	Mark(ctx context.Context, name string, at time.Time) error
	Clear(ctx context.Context, name string) error
}

// ACPFloor is an optional interface of Env.Gate. The acp_delegate tool reads it with a type
// assertion. It checks the command of a permission request of a coding agent against the
// floor of the gate. label is the short text for the progress line.
type ACPFloor interface {
	ACPFloor(env *Env, command string) (label string, err error)
}

// Result is the result of one tool call for the model.
type Result struct {
	Text    string
	IsError bool
	// Outside is true when Text comes from outside the machine. The agent puts it into the
	// untrusted block.
	Outside bool
	// Note is a note of Tether Agent that goes after the untrusted block of an outside result.
	// It is set only when Outside is true. For other results, the note is in Text.
	Note string
}

// RemoteError is an error whose text comes from outside: an MCP error result, an HTTP error
// status, or a page script error. Its text goes into the untrusted block.
type RemoteError struct{ Text string }

// Error returns the remote text.
func (e *RemoteError) Error() string { return e.Text }

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
	if env.ModeScope != "" && !ReadOnlyAllowed(t, args) {
		if mr, ok := env.Gate.(ModeReader); ok {
			mode, err := mr.ModeCheck(ctx, env.ModeScope)
			if err != nil {
				// Closed toward read-only: refuse and log one line.
				if env.Log != nil {
					env.Log.Warn("tools: cannot read the mode, so the call is refused", "scope", env.ModeScope, "err", err)
				}
			}
			if err != nil || mode == security.ModeReadOnly {
				return Result{Text: readOnlyRefusal(t, args), IsError: true}
			}
		}
	}
	// The check runs under the recover. A panic result replaces the whole
	// result, so it gets no note.
	note := argsNote(name, t, args, env)
	out, err := t.Run(ctx, args, env)
	countTool(env, name)
	noteOutside(name, t, args, env, err)
	var remote *RemoteError
	outside := !env.ScriptCall && IsOutsideTool(name) && (err == nil || errors.As(err, &remote))
	if err != nil {
		text := errorText(name, err)
		if outside {
			return Result{Text: ctxmgr.SanitizeUTF8(text), IsError: true, Outside: true, Note: note}
		}
		return Result{Text: ctxmgr.SanitizeUTF8(withNote(text, note)), IsError: true}
	}
	if env.ScriptCall {
		note = ""
	}
	if outside {
		return Result{Text: ctxmgr.SanitizeUTF8(out), Outside: true, Note: note}
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
