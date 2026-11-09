// Command fakeagent is a fake ACP agent for the tests of internal/acp. It reads a
// JSON script from the file in TETHER_FAKEAGENT_SCRIPT. It appends one JSON line for
// each inbound message and each permission outcome to the file in TETHER_FAKEAGENT_LOG.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// script is the same as fakeScript in internal/acp/fake_test.go.
type script struct {
	ProtocolVersion int               `json:"protocol_version"`
	LoadSession     bool              `json:"load_session"`
	Resume          bool              `json:"resume"`
	Close           bool              `json:"close"`
	ModeConfig      bool              `json:"mode_config"`
	Modes           []string          `json:"modes"`
	ModeGroups      bool              `json:"mode_groups"`
	EffectiveMode   map[string]string `json:"effective_mode"`
	Models          []string          `json:"models"`
	AuthRequired    bool              `json:"auth_required"`
	SessionID       string            `json:"session_id"`
	Replay          []step            `json:"replay"`
	Turns           []turn            `json:"turns"`
	AfterTurn       []step            `json:"after_turn"`
	ExitOnEOF       bool              `json:"exit_on_eof"`
	SpawnGrandchild bool              `json:"spawn_grandchild"`
	Stderr          string            `json:"stderr"`
	CrashOnPrompt   bool              `json:"crash_on_prompt"`
}

type turn struct {
	Steps      []step `json:"steps"`
	StopReason string `json:"stop_reason"`
}

type toolCall struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	RawInput  map[string]any `json:"raw_input"`
	Locations []string       `json:"locations"`
	Diff      *struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	} `json:"diff"`
}

type permission struct {
	ToolCall toolCall `json:"tool_call"`
	Options  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"options"`
}

type step struct {
	ToolCall    *toolCall   `json:"tool_call"`
	Message     string      `json:"message"`
	Permission  *permission `json:"permission"`
	SleepMS     int         `json:"sleep_ms"`
	WaitCancel  bool        `json:"wait_cancel"`
	Flood       int         `json:"flood"`
	NilToolCall bool        `json:"nil_tool_call"`
	ModeUpdate  string      `json:"mode_update"`
}

type event struct {
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
	PID     int             `json:"pid,omitempty"`
}

var defaultModes = []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}

type agent struct {
	s    script
	conn *acpsdk.AgentSideConnection

	logMu sync.Mutex
	log   *os.File

	mu       sync.Mutex
	mode     string
	model    string
	sessions int                      // the number of session/new calls
	prompts  int                      // the number of session/prompt calls
	turnStop map[string]chan struct{} // the open turn of each session
	early    map[string]bool          // a cancel that came before its prompt
}

var (
	_ acpsdk.Agent       = (*agent)(nil)
	_ acpsdk.AgentLoader = (*agent)(nil)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(2)
	}
}

func run() error {
	var s script
	b, err := os.ReadFile(os.Getenv("TETHER_FAKEAGENT_SCRIPT"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	f, err := os.OpenFile(os.Getenv("TETHER_FAKEAGENT_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	a := &agent{s: s, log: f, turnStop: map[string]chan struct{}{}, early: map[string]bool{}}
	a.mode = a.modes()[0]
	if len(s.Models) > 0 {
		a.model = s.Models[0]
	}
	if s.Stderr != "" {
		fmt.Fprintln(os.Stderr, s.Stderr)
	}
	if s.SpawnGrandchild {
		// No Setpgid: the child stays in the process group of the fake agent.
		cmd := exec.Command("sleep", "300")
		if err := cmd.Start(); err != nil {
			return err
		}
		a.write(event{Method: "grandchild", PID: cmd.Process.Pid})
	}
	a.conn = acpsdk.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	a.conn.SetLogger(slog.New(slog.DiscardHandler))
	<-a.conn.Done()
	if s.ExitOnEOF {
		return nil
	}
	// Like an adapter that does not stop on EOF: wait for a signal.
	for {
		time.Sleep(time.Hour)
	}
}

func (a *agent) write(e event) {
	b, _ := json.Marshal(e)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	_, _ = a.log.Write(append(b, '\n'))
}

func (a *agent) logCall(method string, params any) {
	b, _ := json.Marshal(params)
	a.write(event{Method: method, Params: b})
}

// modes returns the offered mode ids.
func (a *agent) modes() []string {
	if len(a.s.Modes) > 0 {
		return a.s.Modes
	}
	return defaultModes
}

// effective returns the mode that a request for mode gives. Call it with a.mu held.
func (a *agent) effective(mode string) string {
	if m, ok := a.s.EffectiveMode[mode]; ok {
		return m
	}
	return mode
}

// state returns the config options and the mode state of a session.
func (a *agent) state() ([]acpsdk.SessionConfigOption, *acpsdk.SessionModeState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var opts []acpsdk.SessionConfigOption
	if len(a.s.Models) > 0 {
		opts = append(opts, selectOption("model", a.model, a.s.Models, false))
	}
	if a.s.ModeConfig {
		opts = append(opts, selectOption("mode", a.mode, a.modes(), a.s.ModeGroups))
	}
	st := &acpsdk.SessionModeState{CurrentModeId: acpsdk.SessionModeId(a.mode)}
	for _, m := range a.modes() {
		st.AvailableModes = append(st.AvailableModes, acpsdk.SessionMode{Id: acpsdk.SessionModeId(m), Name: m})
	}
	return opts, st
}

func selectOption(id, current string, values []string, grouped bool) acpsdk.SessionConfigOption {
	vals := []acpsdk.SessionConfigSelectOption{}
	for _, v := range values {
		vals = append(vals, acpsdk.SessionConfigSelectOption{Name: v, Value: acpsdk.SessionConfigValueId(v)})
	}
	var o acpsdk.SessionConfigSelectOptions
	if grouped {
		half := len(vals) / 2
		g := acpsdk.SessionConfigSelectOptionsGrouped{
			{Group: "a", Name: "Group A", Options: vals[:half]},
			{Group: "b", Name: "Group B", Options: vals[half:]},
		}
		o.Grouped = &g
	} else {
		u := acpsdk.SessionConfigSelectOptionsUngrouped(vals)
		o.Ungrouped = &u
	}
	cat := acpsdk.SessionConfigOptionCategory(id)
	return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{
		Id: acpsdk.SessionConfigId(id), Name: id, Category: &cat,
		CurrentValue: acpsdk.SessionConfigValueId(current), Options: o, Type: "select",
	}}
}

func (a *agent) Initialize(_ context.Context, p acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	a.logCall(acpsdk.AgentMethodInitialize, p)
	v := a.s.ProtocolVersion
	if v == 0 {
		v = 1
	}
	caps := acpsdk.AgentCapabilities{LoadSession: a.s.LoadSession}
	if a.s.Resume {
		caps.SessionCapabilities.Resume = &acpsdk.SessionResumeCapabilities{}
	}
	if a.s.Close {
		caps.SessionCapabilities.Close = &acpsdk.SessionCloseCapabilities{}
	}
	return acpsdk.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersion(v),
		AgentCapabilities: caps,
		AgentInfo:         &acpsdk.Implementation{Name: "fakeagent", Version: "0.0.1"},
	}, nil
}

func (a *agent) Authenticate(_ context.Context, p acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	a.logCall(acpsdk.AgentMethodAuthenticate, p)
	return acpsdk.AuthenticateResponse{}, nil
}

func (a *agent) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodLogout)
}

func (a *agent) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionList)
}

func (a *agent) NewSession(_ context.Context, p acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionNew, p)
	if a.s.AuthRequired {
		return acpsdk.NewSessionResponse{}, acpsdk.NewAuthRequired(nil)
	}
	a.mu.Lock()
	a.sessions++
	n := a.sessions
	a.mu.Unlock()
	id := a.s.SessionID
	if id == "" {
		id = "fake-session-" + strconv.Itoa(n)
	}
	opts, modes := a.state()
	return acpsdk.NewSessionResponse{SessionId: acpsdk.SessionId(id), ConfigOptions: opts, Modes: modes}, nil
}

func (a *agent) LoadSession(ctx context.Context, p acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionLoad, p)
	if !a.s.LoadSession {
		return acpsdk.LoadSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionLoad)
	}
	if a.s.AuthRequired {
		return acpsdk.LoadSessionResponse{}, acpsdk.NewAuthRequired(nil)
	}
	a.run(ctx, string(p.SessionId), a.s.Replay, nil)
	opts, modes := a.state()
	return acpsdk.LoadSessionResponse{ConfigOptions: opts, Modes: modes}, nil
}

func (a *agent) ResumeSession(_ context.Context, p acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionResume, p)
	if !a.s.Resume {
		return acpsdk.ResumeSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionResume)
	}
	if a.s.AuthRequired {
		return acpsdk.ResumeSessionResponse{}, acpsdk.NewAuthRequired(nil)
	}
	opts, modes := a.state()
	return acpsdk.ResumeSessionResponse{ConfigOptions: opts, Modes: modes}, nil
}

func (a *agent) CloseSession(_ context.Context, p acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionClose, p)
	if !a.s.Close {
		return acpsdk.CloseSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionClose)
	}
	return acpsdk.CloseSessionResponse{}, nil
}

func (a *agent) SetSessionConfigOption(_ context.Context, p acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionSetConfigOption, p)
	if p.ValueId == nil {
		return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "only value ids"})
	}
	id, val := string(p.ValueId.ConfigId), string(p.ValueId.Value)
	a.mu.Lock()
	switch {
	case id == "model" && slices.Contains(a.s.Models, val):
		a.model = val
	case id == "mode" && a.s.ModeConfig && slices.Contains(a.modes(), val):
		a.mode = a.effective(val)
	default:
		a.mu.Unlock()
		return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "unknown value " + val})
	}
	a.mu.Unlock()
	opts, _ := a.state()
	return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}

func (a *agent) SetSessionMode(_ context.Context, p acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	a.logCall(acpsdk.AgentMethodSessionSetMode, p)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.Contains(a.modes(), string(p.ModeId)) {
		return acpsdk.SetSessionModeResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "unknown mode"})
	}
	a.mode = a.effective(string(p.ModeId))
	return acpsdk.SetSessionModeResponse{}, nil
}

func (a *agent) Cancel(_ context.Context, p acpsdk.CancelNotification) error {
	a.logCall(acpsdk.AgentMethodSessionCancel, p)
	a.mu.Lock()
	defer a.mu.Unlock()
	sid := string(p.SessionId)
	if ch, ok := a.turnStop[sid]; ok {
		close(ch)
		delete(a.turnStop, sid)
		return nil
	}
	a.early[sid] = true
	return nil
}

func (a *agent) Prompt(ctx context.Context, p acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	sid := string(p.SessionId)
	// Register the cancel channel before the work starts. A session/cancel can
	// come before this handler runs, and the SDK then does not cancel ctx.
	stop := make(chan struct{})
	a.mu.Lock()
	if a.early[sid] {
		delete(a.early, sid)
		close(stop)
	} else {
		a.turnStop[sid] = stop
	}
	n := a.prompts
	a.prompts++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.turnStop[sid] == stop {
			delete(a.turnStop, sid)
		}
		a.mu.Unlock()
	}()
	a.logCall(acpsdk.AgentMethodSessionPrompt, p)
	if a.s.CrashOnPrompt {
		fmt.Fprintln(os.Stderr, "fakeagent: crash on prompt")
		os.Exit(1)
	}
	if a.s.AuthRequired {
		return acpsdk.PromptResponse{}, acpsdk.NewAuthRequired(nil)
	}
	var t turn
	if n < len(a.s.Turns) {
		t = a.s.Turns[n]
	}
	cancelled := a.run(ctx, sid, t.Steps, stop)
	if n == 0 && len(a.s.AfterTurn) > 0 {
		go func() {
			time.Sleep(100 * time.Millisecond)
			a.run(context.Background(), sid, a.s.AfterTurn, nil)
		}()
	}
	reason := t.StopReason
	if reason == "" {
		reason = string(acpsdk.StopReasonEndTurn)
	}
	if cancelled {
		reason = string(acpsdk.StopReasonCancelled)
	}
	return acpsdk.PromptResponse{StopReason: acpsdk.StopReason(reason)}, nil
}

// run runs the steps for session sid. It returns true when the turn was
// cancelled. A nil stop channel never fires.
func (a *agent) run(ctx context.Context, sid string, steps []step, stop <-chan struct{}) bool {
	var wg sync.WaitGroup
	ended := func() bool {
		select {
		case <-stop:
			return true
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}
	for i, st := range steps {
		if ended() {
			break
		}
		switch {
		case st.ToolCall != nil:
			a.update(sid, startToolCall(*st.ToolCall))
		case st.Message != "":
			a.update(sid, acpsdk.UpdateAgentMessageText(st.Message))
		case st.Permission != nil:
			wg.Add(1)
			go func(p permission) {
				defer wg.Done()
				a.ask(sid, permissionToolCall(p.ToolCall), permissionOptions(p))
			}(*st.Permission)
		case st.SleepMS > 0:
			select {
			case <-time.After(time.Duration(st.SleepMS) * time.Millisecond):
			case <-stop:
			case <-ctx.Done():
			}
		case st.WaitCancel:
			select {
			case <-stop:
			case <-ctx.Done():
			}
		case st.Flood > 0:
			for j := range st.Flood {
				id := acpsdk.ToolCallId("flood-" + strconv.Itoa(j))
				a.update(sid, acpsdk.StartToolCall(id, "flood "+strconv.Itoa(j), acpsdk.WithStartKind(acpsdk.ToolKindRead)))
			}
		case st.NilToolCall:
			id := acpsdk.ToolCallId("nil-" + strconv.Itoa(i))
			a.update(sid, acpsdk.UpdateToolCall(id))
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.ask(sid, acpsdk.ToolCallUpdate{ToolCallId: id}, []acpsdk.PermissionOption{
					{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
					{OptionId: "reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
				})
			}()
		case st.ModeUpdate != "":
			a.mu.Lock()
			a.mode = st.ModeUpdate
			a.mu.Unlock()
			a.update(sid, acpsdk.SessionUpdate{CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{
				CurrentModeId: acpsdk.SessionModeId(st.ModeUpdate),
			}})
		}
	}
	wg.Wait()
	return ended()
}

func (a *agent) update(sid string, u acpsdk.SessionUpdate) {
	_ = a.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{SessionId: acpsdk.SessionId(sid), Update: u})
}

// ask sends one permission request and logs the answer. The request has its
// own context: a cancelled turn must still get "cancelled" from the client.
func (a *agent) ask(sid string, tc acpsdk.ToolCallUpdate, opts []acpsdk.PermissionOption) {
	resp, err := a.conn.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: acpsdk.SessionId(sid), ToolCall: tc, Options: opts,
	})
	out := "cancelled"
	switch {
	case err != nil:
		out = "error: " + err.Error()
	case resp.Outcome.Selected != nil:
		out = "selected:" + string(resp.Outcome.Selected.OptionId)
	}
	params, _ := json.Marshal(map[string]string{"tool_call_id": string(tc.ToolCallId)})
	a.write(event{Method: "permission", Params: params, Outcome: out})
}

func locations(paths []string) []acpsdk.ToolCallLocation {
	var l []acpsdk.ToolCallLocation
	for _, p := range paths {
		l = append(l, acpsdk.ToolCallLocation{Path: p})
	}
	return l
}

func diffContent(tc toolCall) []acpsdk.ToolCallContent {
	if tc.Diff == nil {
		return nil
	}
	return []acpsdk.ToolCallContent{acpsdk.ToolDiffContent(tc.Diff.Path, tc.Diff.New, tc.Diff.Old)}
}

func startToolCall(tc toolCall) acpsdk.SessionUpdate {
	opts := []acpsdk.ToolCallStartOpt{
		acpsdk.WithStartKind(acpsdk.ToolKind(tc.Kind)),
		acpsdk.WithStartStatus(acpsdk.ToolCallStatusPending),
	}
	if tc.RawInput != nil {
		opts = append(opts, acpsdk.WithStartRawInput(tc.RawInput))
	}
	if len(tc.Locations) > 0 {
		opts = append(opts, acpsdk.WithStartLocations(locations(tc.Locations)))
	}
	if c := diffContent(tc); c != nil {
		opts = append(opts, acpsdk.WithStartContent(c))
	}
	return acpsdk.StartToolCall(acpsdk.ToolCallId(tc.ID), tc.Title, opts...)
}

func permissionToolCall(tc toolCall) acpsdk.ToolCallUpdate {
	u := acpsdk.ToolCallUpdate{
		ToolCallId: acpsdk.ToolCallId(tc.ID),
		Title:      acpsdk.Ptr(tc.Title),
		Locations:  locations(tc.Locations),
		Content:    diffContent(tc),
	}
	if tc.Kind != "" {
		u.Kind = acpsdk.Ptr(acpsdk.ToolKind(tc.Kind))
	}
	if tc.RawInput != nil {
		u.RawInput = tc.RawInput
	}
	return u
}

func permissionOptions(p permission) []acpsdk.PermissionOption {
	opts := []acpsdk.PermissionOption{}
	for _, o := range p.Options {
		opts = append(opts, acpsdk.PermissionOption{
			OptionId: acpsdk.PermissionOptionId(o.ID), Name: o.Name, Kind: acpsdk.PermissionOptionKind(o.Kind),
		})
	}
	return opts
}
