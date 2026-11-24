package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
)

// SecurityFixed holds the view-only lists of the Security page. The caller builds it once from
// the running config.
type SecurityFixed struct {
	ProtectedPaths []string                  `json:"protected_paths"`
	HumanOnly      []string                  `json:"human_only"`
	MCPClasses     map[string]map[string]int `json:"mcp_classes"` // server, then class, then the tool count
	ACPPresets     []string                  `json:"acp_presets"`
	ACPWorkdirs    []string                  `json:"acp_workdirs"`
	Toolsets       map[string][]string       `json:"toolsets"` // channel, then the toolset names
}

type ScopeModeView struct {
	Scope      string    `json:"scope"`
	ScopeLabel string    `json:"scope_label"`
	Mode       string    `json:"mode"`
	SetAt      time.Time `json:"set_at"`
}

type GrantView struct {
	Rule        string    `json:"rule"`
	Description string    `json:"description"` // from security.PatternByID; "" for an unknown rule
	Preview     string    `json:"preview"`
	Workdir     string    `json:"workdir"`
	Via         string    `json:"via"`
	Programs    []string  `json:"programs"`
	Server      string    `json:"server"`
	ToolName    string    `json:"tool_name"`
	Created     time.Time `json:"created"`
	Remove      string    `json:"remove"` // the /approvals command that removes this grant
}

type SecretView struct {
	Name     string    `json:"name"`
	State    string    `json:"state"` // set, not set, or check
	Commands []string  `json:"commands"`
	FailedAt time.Time `json:"failed_at"` // the auth-failure mark; zero with no mark
	Pins     []PinView `json:"pins"`      // the allowed programs, with their SHA-256 hashes
	Set      string    `json:"set"`       // "/secrets set NAME"
	Unpin    string    `json:"unpin"`     // "/secrets unpin NAME"
}

// PinView is one allowed program of a secret.
type PinView struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SecurityView struct {
	Mode           string          `json:"mode"` // the mode of the dashboard scope: read-only, guarded, or full
	Modes          []ScopeModeView `json:"modes"`
	Grants         []GrantView     `json:"grants"`
	RemoveAll      string          `json:"remove_all"`
	GrantsOff      bool            `json:"grants_off"`
	GrantsOffCause string          `json:"grants_off_cause"`
	Secrets        []SecretView    `json:"secrets"`
	Fixed          SecurityFixed   `json:"fixed"`
	ProcessMode    string          `json:"process_mode"` // approvals.mode: manual, smart, or off
	CronMode       string          `json:"cron_mode"`    // approvals.cron_mode: deny or approve
	LogScopes      []string        `json:"log_scopes"`   // the scopes that the approval log holds
	// LogScopeLabels holds the readable label of each key of LogScopes.
	LogScopeLabels map[string]string `json:"log_scope_labels"`
}

type LogEntry struct {
	At         time.Time `json:"at"`
	SessionID  string    `json:"session_id"`
	Scope      string    `json:"scope"`
	ScopeLabel string    `json:"scope_label"`
	Kind       string    `json:"kind"`
	Tool       string    `json:"tool"`
	Server     string    `json:"server"`
	ToolName   string    `json:"tool_name"`
	Rule       string    `json:"rule"`
	// RuleDescription is the text of the rule. It is the rule id when no text is known.
	RuleDescription string `json:"rule_description"`
	Class           string `json:"class"`
	Decision        string `json:"decision"`
	Source          string `json:"source"`
	Answer          string `json:"answer"`
	Preview         string `json:"preview"`
}

type LogPage struct {
	Rows   []LogEntry `json:"rows"`
	Total  int        `json:"total"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
}

type DeviceRow struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Browser    string    `json:"browser"` // the user agent
	IP         string    `json:"ip"`
	Created    time.Time `json:"created"`
	LastSeen   time.Time `json:"last_seen"`
	Expires    time.Time `json:"expires"`
	ThisDevice bool      `json:"this_device"`
}

// SecurityAPI serves the Security page and the signed-in devices.
type SecurityAPI struct {
	gate    *security.Gate
	secrets gateway.SecretsDeps
	fixed   SecurityFixed
	devices *Store
	now     func() time.Time

	// ChatTitle reads the title cache of the Telegram chats. It never calls Telegram. Nil gives
	// the raw scope keys.
	ChatTitle func(chatID, threadID string) (chat, topic string, ok bool)
	// Redact masks a cached chat or topic name, as on the Sessions page. Nil keeps the name.
	Redact func(string) string
}

// telegramLabel returns "Telegram › <chat> › <topic>" for a Telegram chat. With no cached
// name it gives "Telegram › chat <id>", and with a thread but no topic name " › topic <thread>".
// The thread "" and "0" mean no topic. red masks a cached name; nil keeps it.
func telegramLabel(title func(chatID, threadID string) (chat, topic string, ok bool), chatID, threadID string, red func(string) string) string {
	if red == nil {
		red = func(s string) string { return s }
	}
	var chat, topic string
	var ok bool
	if title != nil {
		chat, topic, ok = title(chatID, threadID)
	}
	label := "Telegram › chat " + chatID
	if ok && chat != "" {
		label = "Telegram › " + red(chat)
	}
	if ok && topic != "" {
		label += " › " + red(topic)
	} else if threadID != "" && threadID != "0" {
		label += " › topic " + threadID
	}
	return label
}

// scopeLabel returns a readable name for a scope key. A Telegram key with no cached name gives
// the chat id. Another unknown key gives the raw key.
func (a *SecurityAPI) scopeLabel(scope string) string {
	if scope == ChannelName {
		return "Dashboard"
	}
	parts := strings.Split(scope, ":")
	// Telegram scopes are "telegram:<chat>:<thread>". A chat id can start with "-".
	if len(parts) == 3 && parts[0] == "telegram" {
		return telegramLabel(a.ChatTitle, parts[1], parts[2], a.Redact)
	}
	return scope
}

func NewSecurityAPI(gate *security.Gate, secrets gateway.SecretsDeps, fixed SecurityFixed, devices *Store, now func() time.Time) *SecurityAPI {
	return &SecurityAPI{gate: gate, secrets: secrets, fixed: fixed, devices: devices, now: now}
}

func (a *SecurityAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/security", a.view)
	mux.HandleFunc("GET /api/security/log", a.log)
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		a.deviceList(w, r, http.StatusOK)
	})
	mux.HandleFunc("POST /api/devices/revoke-others", a.revokeOthers)
	mux.HandleFunc("POST /api/devices/{id}/revoke", a.revoke)
}

// view serves GET /api/security. Each read finishes before the next one starts.
func (a *SecurityAPI) view(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v := SecurityView{
		Mode:    a.gate.ModeOf(ctx, ChannelName),
		Modes:   []ScopeModeView{},
		Grants:  []GrantView{},
		Secrets: []SecretView{},
		Fixed:   a.fixed,

		ProcessMode: a.gate.Mode,
		CronMode:    a.gate.CronMode,
	}
	if v.Mode == "" {
		v.Mode = security.ModeGuarded
	}
	modes, err := a.gate.ScopeModes(ctx)
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range modes {
		v.Modes = append(v.Modes, ScopeModeView{Scope: m.Scope, ScopeLabel: a.scopeLabel(m.Scope), Mode: m.Mode, SetAt: m.SetAt})
	}
	rows, err := a.gate.ListAlways(ctx)
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
		g := GrantView{
			Rule: row.PatternID, Preview: row.Preview, Workdir: row.Workdir, Via: row.Via,
			Programs: row.Programs, Server: row.Server, ToolName: row.ToolName, Created: row.CreatedAt,
			Remove: fmt.Sprintf("/approvals remove %d %s", i+1, grantCode(row.ID)),
		}
		if g.Programs == nil {
			g.Programs = []string{}
		}
		if p, ok := security.PatternByID(row.PatternID); ok {
			g.Description = p.Description
		}
		v.Grants = append(v.Grants, g)
	}
	if len(rows) > 0 {
		v.RemoveAll = "/approvals remove all " + grantCode(strings.Join(ids, "\n"))
	}
	v.GrantsOff, v.GrantsOffCause = a.gate.GrantsOff()
	secrets, err := a.secretViews(ctx)
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	v.Secrets = secrets
	scopes, err := a.gate.LogScopes(ctx)
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	v.LogScopes = append([]string{}, scopes...)
	v.LogScopeLabels = make(map[string]string, len(scopes))
	for _, scope := range scopes {
		v.LogScopeLabels[scope] = a.scopeLabel(scope)
	}
	securityJSON(w, http.StatusOK, v)
}

// secretViews builds the secret rows. It never reads a secret value.
func (a *SecurityAPI) secretViews(ctx context.Context) ([]SecretView, error) {
	out := []SecretView{}
	if a.secrets.Bound == nil {
		return out, nil
	}
	bound := a.secrets.Bound()
	var marks map[string]time.Time
	if a.secrets.Marks != nil {
		m, err := a.secrets.Marks(ctx)
		if err != nil {
			return nil, err
		}
		marks = m
	}
	names := make([]string, 0, len(bound))
	for name := range bound {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sv := SecretView{
			Name: name, State: "not set", Commands: bound[name], Pins: []PinView{},
			Set: "/secrets set " + name, Unpin: "/secrets unpin " + name,
		}
		if sv.Commands == nil {
			sv.Commands = []string{}
		}
		if a.secrets.HasValue != nil && a.secrets.HasValue(name) {
			sv.State = "set"
			if at, ok := marks[name]; ok {
				sv.State = "check"
				sv.FailedAt = at
			}
		}
		if a.secrets.Pins != nil {
			pins, err := a.secrets.Pins(ctx, name)
			if err != nil {
				return nil, err
			}
			for _, p := range pins {
				sv.Pins = append(sv.Pins, PinView{Path: p.Path, SHA256: p.SHA256})
			}
		}
		out = append(out, sv)
	}
	return out, nil
}

// log serves GET /api/security/log.
func (a *SecurityAPI) log(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, limit := 0, 50
	var err1, err2 error
	if s := q.Get("offset"); s != "" {
		offset, err1 = strconv.Atoi(s)
	}
	if s := q.Get("limit"); s != "" {
		limit, err2 = strconv.Atoi(s)
	}
	if err1 != nil || err2 != nil || offset < 0 || limit < 0 {
		securityErr(w, http.StatusBadRequest, "offset and limit must be whole numbers.")
		return
	}
	if limit > 200 {
		limit = 200
	}
	decision, source := q.Get("decision"), q.Get("source")
	if !slices.Contains([]string{"", "allowed", "denied", "blocked"}, decision) {
		securityErr(w, http.StatusBadRequest, "decision must be allowed, denied, or blocked.")
		return
	}
	if source != "" && source != "timeout" {
		securityErr(w, http.StatusBadRequest, "source must be timeout.")
		return
	}
	rows, total, err := a.gate.ListLogPage(r.Context(), security.LogFilter{
		Scope: q.Get("scope"), Decision: decision, Source: source, Query: q.Get("q"),
	}, offset, limit)
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	page := LogPage{Rows: make([]LogEntry, 0, len(rows)), Total: total, Offset: offset, Limit: limit}
	for _, row := range rows {
		desc, ok := security.RuleText(row.Rule)
		if !ok {
			desc = row.Rule
		}
		page.Rows = append(page.Rows, LogEntry{
			RuleDescription: desc,
			At:              row.At, SessionID: row.SessionID, Scope: row.Scope, ScopeLabel: a.scopeLabel(row.Scope), Kind: row.Kind, Tool: row.Tool, Server: row.Server,
			ToolName: row.ToolName, Rule: row.Rule, Class: row.Class, Decision: row.Decision,
			Source: row.Source, Answer: row.Answer, Preview: row.Preview,
		})
	}
	securityJSON(w, http.StatusOK, page)
}

// liveDevices returns the devices that are not revoked and not expired.
func (a *SecurityAPI) liveDevices(ctx context.Context) ([]Device, error) {
	if a.devices == nil {
		return nil, nil
	}
	now := a.now()
	all, err := a.devices.List(ctx, now)
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range all {
		if !d.Revoked && d.Expires.After(now) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (a *SecurityAPI) deviceList(w http.ResponseWriter, r *http.Request, status int) {
	devs, err := a.liveDevices(r.Context())
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	self, _ := DeviceFrom(r.Context())
	out := make([]DeviceRow, 0, len(devs))
	for _, d := range devs {
		out = append(out, DeviceRow{
			ID: d.ID, Label: d.Label, Browser: d.UserAgent, IP: d.IP, Created: d.Created,
			LastSeen: d.LastSeen, Expires: d.Expires, ThisDevice: self.ID != "" && d.ID == self.ID,
		})
	}
	securityJSON(w, status, out)
}

func (a *SecurityAPI) revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	devs, err := a.liveDevices(r.Context())
	if err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !slices.ContainsFunc(devs, func(d Device) bool { return d.ID == id }) {
		securityErr(w, http.StatusNotFound, "No signed-in device has this id.")
		return
	}
	if err := a.devices.Revoke(r.Context(), id); err != nil {
		securityErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.deviceList(w, r, http.StatusOK)
}

func (a *SecurityAPI) revokeOthers(w http.ResponseWriter, r *http.Request) {
	self, ok := DeviceFrom(r.Context())
	if !ok {
		securityErr(w, http.StatusUnauthorized, "Your session ended. Request access again.")
		return
	}
	if a.devices != nil {
		if _, err := a.devices.RevokeOthers(r.Context(), self.ID, a.now()); err != nil {
			securityErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	a.deviceList(w, r, http.StatusOK)
}

func securityJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func securityErr(w http.ResponseWriter, status int, msg string) {
	securityJSON(w, status, map[string]string{"error": msg})
}

// grantCode returns the first 8 hex characters of the SHA-256 hash of s.
func grantCode(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:8]
}

func init() { registerAPI(SecurityView{}, LogPage{}, DeviceRow{}, PinView{}) }
