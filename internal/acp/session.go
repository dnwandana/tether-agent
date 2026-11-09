package acp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// SessionInfo is the state of one ACP session: its id, the offered modes, the current mode,
// and the offered models.
type SessionInfo struct {
	ID          string
	Modes       []string
	ModeConfig  bool // the session has a "mode" config option
	CurrentMode string
	Models      []string
}

// ModeError reports a mode that the agent does not offer.
type ModeError struct {
	Agent, Mode string
	Offered     []string
}

func (e *ModeError) Error() string {
	return fmt.Sprintf("acp: agent %s does not offer mode %q (offered: %s). Set a different permission_preset.",
		e.Agent, e.Mode, strings.Join(e.Offered, ", "))
}

// modeNotStricterError reports that the agent uses a mode after the mode change that is not
// stricter than the requested mode, or that it reports no mode. The ACP session can run
// with a looser mode than the user allowed, so the manager does not use it again.
type modeNotStricterError struct {
	Agent, Got, Want string
}

func (e *modeNotStricterError) Error() string {
	return fmt.Sprintf("acp: agent %s reports mode %q after the change to %q. That mode is not stricter, so Tether Agent does not send the task.",
		e.Agent, e.Got, e.Want)
}

// NewSession starts a new ACP session in cwd.
func (c *Client) NewSession(ctx context.Context, cwd string) (SessionInfo, error) {
	resp, err := c.conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		return SessionInfo{}, c.callErr(err)
	}
	return sessionInfo(string(resp.SessionId), resp.ConfigOptions, resp.Modes), nil
}

// ResumeSession opens the ACP session id again with no replay of its history.
func (c *Client) ResumeSession(ctx context.Context, id, cwd string) (SessionInfo, error) {
	// The SDK drops an empty McpServers list from session/resume (omitempty). Send it anyway.
	resp, err := c.conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		SessionId: acpsdk.SessionId(id), Cwd: cwd, McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		return SessionInfo{}, c.callErr(err)
	}
	return sessionInfo(id, resp.ConfigOptions, resp.Modes), nil
}

// LoadSession opens the ACP session id again. The agent replays the history as session
// updates before its response. Call it with no attached turn handler, so that the client
// drops the replay. The SDK handles each update that comes before the response before the
// call returns, so a handler that is attached after the call sees no replayed update.
func (c *Client) LoadSession(ctx context.Context, id, cwd string) (SessionInfo, error) {
	resp, err := c.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId: acpsdk.SessionId(id), Cwd: cwd, McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		return SessionInfo{}, c.callErr(err)
	}
	return sessionInfo(id, resp.ConfigOptions, resp.Modes), nil
}

// CloseSession closes the ACP session id. It sends nothing when the agent does not offer
// session/close.
func (c *Client) CloseSession(ctx context.Context, id string) error {
	if !c.caps.Close {
		return nil
	}
	_, err := c.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: acpsdk.SessionId(id)})
	return c.callErr(err)
}

// Configure sets the model and then the mode of session s. It runs before each prompt. The
// note is not empty when the agent uses a stricter mode than mode.
func (c *Client) Configure(ctx context.Context, s *SessionInfo, model, mode string) (note string, err error) {
	if model != "" {
		resp, err := c.conn.SetSessionConfigOption(ctx, setValue(s.ID, "model", model))
		if err != nil {
			e := c.callErr(err)
			if c.fatalErr(e) || ctx.Err() != nil {
				// The call did not end, so the agent did not reject the model.
				return "", e
			}
			offered := "none"
			if len(s.Models) > 0 {
				offered = strings.Join(s.Models, ", ")
			}
			//lint:ignore ST1005 the text is a fixed user message with full sentences.
			return "", fmt.Errorf("acp: agent %s does not accept model %q (offered: %s).", c.agentID, model, offered)
		}
		s.applyOptions(resp.ConfigOptions)
	}

	effective := mode
	switch {
	case s.ModeConfig && slices.Contains(s.Modes, mode):
		resp, err := c.conn.SetSessionConfigOption(ctx, setValue(s.ID, "mode", mode))
		if err != nil {
			return "", c.callErr(err)
		}
		s.applyOptions(resp.ConfigOptions)
		got, _ := modeValue(resp.ConfigOptions)
		if got != mode {
			// Check for an empty value first: ModeRank("") is -1, which counts as stricter.
			if got == "" || c.agent.ModeRank(got) >= c.agent.ModeRank(mode) {
				return "", &modeNotStricterError{Agent: c.agentID, Got: got, Want: mode}
			}
			effective = got
			note = fmt.Sprintf("mode %s, not %s: the model does not support %s", got, mode, mode)
		}
	case !s.ModeConfig && slices.Contains(s.Modes, mode):
		// session/set_mode returns no value, so trust the requested mode.
		if _, err := c.conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
			SessionId: acpsdk.SessionId(s.ID), ModeId: acpsdk.SessionModeId(mode),
		}); err != nil {
			return "", c.callErr(err)
		}
	default:
		return "", &ModeError{Agent: c.agentID, Mode: mode, Offered: slices.Clone(s.Modes)}
	}
	s.CurrentMode = effective
	return note, nil
}

// fatalErr reports whether e, a result of callErr, tells that the agent needs a login or
// that the connection stopped.
func (c *Client) fatalErr(e error) bool {
	if _, ok := errors.AsType[*NotLoggedInError](e); ok {
		return true
	}
	select {
	case <-c.conn.Done():
		return true
	default:
		return false
	}
}

// setValue builds a session/set_config_option request that sets configID to value.
func setValue(sessionID, configID, value string) acpsdk.SetSessionConfigOptionRequest {
	return acpsdk.SetSessionConfigOptionRequest{ValueId: &acpsdk.SetSessionConfigOptionValueId{
		SessionId: acpsdk.SessionId(sessionID),
		ConfigId:  acpsdk.SessionConfigId(configID),
		Value:     acpsdk.SessionConfigValueId(value),
	}}
}

// sessionInfo builds the SessionInfo of a session/new, session/load, or
// session/resume response.
func sessionInfo(id string, opts []acpsdk.SessionConfigOption, modes *acpsdk.SessionModeState) SessionInfo {
	s := SessionInfo{ID: id}
	if modes != nil {
		for _, m := range modes.AvailableModes {
			s.Modes = append(s.Modes, string(m.Id))
		}
		s.CurrentMode = string(modes.CurrentModeId)
	}
	s.applyOptions(opts)
	return s
}

// applyOptions reads the mode and model entries of opts into s.
func (s *SessionInfo) applyOptions(opts []acpsdk.SessionConfigOption) {
	for _, o := range opts {
		sel := o.Select
		if sel == nil {
			continue
		}
		if sel.Id == "mode" {
			s.ModeConfig = true
			s.Modes = flatten(sel.Options)
			s.CurrentMode = string(sel.CurrentValue)
		}
		if sel.Id == "model" || (sel.Category != nil && *sel.Category == acpsdk.SessionConfigOptionCategoryModel) {
			s.Models = flatten(sel.Options)
		}
	}
}

// flatten returns the values of a select option. codex-acp groups its modes
// when permission profiles exist.
func flatten(o acpsdk.SessionConfigSelectOptions) []string {
	var vals []string
	if o.Ungrouped != nil {
		for _, v := range *o.Ungrouped {
			vals = append(vals, string(v.Value))
		}
	}
	if o.Grouped != nil {
		for _, g := range *o.Grouped {
			for _, v := range g.Options {
				vals = append(vals, string(v.Value))
			}
		}
	}
	return vals
}

// modeValue returns the currentValue of the "mode" entry of opts.
func modeValue(opts []acpsdk.SessionConfigOption) (string, bool) {
	for _, o := range opts {
		if o.Select != nil && o.Select.Id == "mode" {
			return string(o.Select.CurrentValue), true
		}
	}
	return "", false
}
