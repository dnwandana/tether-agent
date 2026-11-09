package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var claudeModes = []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}

func TestNewSessionSendsEmptyMCPServers(t *testing.T) {
	c, logPath := startFake(t, fakeScript{})
	dir := t.TempDir()
	info, err := c.NewSession(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "fake-session-1" {
		t.Fatalf("ID = %q", info.ID)
	}
	evs := fakeEventsOf(readFakeLog(t, logPath), "session/new")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Params), `"mcpServers":[]`) ||
		!strings.Contains(string(evs[0].Params), dir) {
		t.Fatalf("session/new events = %+v", evs)
	}
}

func TestSessionInfoFromResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    fakeScript
		want SessionInfo
	}{
		{"modes only", fakeScript{},
			SessionInfo{ID: "fake-session-1", Modes: claudeModes, CurrentMode: "default"}},
		{"mode config", fakeScript{ModeConfig: true, Models: []string{"opus", "sonnet"}},
			SessionInfo{ID: "fake-session-1", Modes: claudeModes, ModeConfig: true, CurrentMode: "default", Models: []string{"opus", "sonnet"}}},
		{"grouped modes", fakeScript{ModeConfig: true, ModeGroups: true},
			SessionInfo{ID: "fake-session-1", Modes: claudeModes, ModeConfig: true, CurrentMode: "default"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := startFake(t, tc.s)
			info, err := c.NewSession(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(info, tc.want) {
				t.Fatalf("info = %+v, want %+v", info, tc.want)
			}
		})
	}
}

// configured starts a session and runs Configure with model and the mode of preset.
func configured(t *testing.T, s fakeScript, model, preset string) (*Client, SessionInfo, string, string, error) {
	t.Helper()
	c, logPath := startFake(t, s)
	info, err := c.NewSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mode, ok := c.agent.ModeFor(preset)
	if !ok {
		t.Fatalf("no mode for preset %q", preset)
	}
	note, err := c.Configure(context.Background(), &info, model, mode)
	return c, info, note, logPath, err
}

func TestConfigureSetsModelThenMode(t *testing.T) {
	_, info, note, logPath, err := configured(t, fakeScript{ModeConfig: true, Models: []string{"opus", "sonnet"}}, "sonnet", "auto")
	if err != nil || note != "" || info.CurrentMode != "auto" {
		t.Fatalf("Configure = %q, %v; CurrentMode %q", note, err, info.CurrentMode)
	}
	evs := fakeEventsOf(readFakeLog(t, logPath), "session/set_config_option")
	if len(evs) != 2 {
		t.Fatalf("set_config_option events = %d", len(evs))
	}
	for i, want := range []struct{ id, value string }{{"model", "sonnet"}, {"mode", "auto"}} {
		var p struct {
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		}
		if err := json.Unmarshal(evs[i].Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.ConfigID != want.id || p.Value != want.value {
			t.Fatalf("event %d = %s, want %s=%s", i, evs[i].Params, want.id, want.value)
		}
	}
}

func TestConfigureStricterModeGivesNote(t *testing.T) {
	_, info, note, _, err := configured(t, fakeScript{ModeConfig: true, EffectiveMode: map[string]string{"auto": "acceptEdits"}}, "", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mode acceptEdits, not auto: the model does not support auto"; note != want {
		t.Fatalf("note = %q, want %q", note, want)
	}
	if info.CurrentMode != "acceptEdits" {
		t.Fatalf("CurrentMode = %q", info.CurrentMode)
	}
}

func TestConfigureLooserOrMissingModeFails(t *testing.T) {
	for name, eff := range map[string]map[string]string{
		"looser":  {"acceptEdits": "bypassPermissions"},
		"missing": {"acceptEdits": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, _, err := configured(t, fakeScript{ModeConfig: true, EffectiveMode: eff}, "", "risky")
			if err == nil || !strings.Contains(err.Error(), "is not stricter") {
				t.Fatalf("Configure() error = %v", err)
			}
		})
	}
}

func TestConfigureSetModeFallback(t *testing.T) {
	_, info, note, logPath, err := configured(t, fakeScript{}, "", "auto")
	if err != nil || note != "" || info.CurrentMode != "auto" {
		t.Fatalf("Configure = %q, %v; CurrentMode %q", note, err, info.CurrentMode)
	}
	evs := readFakeLog(t, logPath)
	if len(fakeEventsOf(evs, "session/set_config_option")) != 0 {
		t.Fatal("set_config_option was sent with no mode config option")
	}
	sm := fakeEventsOf(evs, "session/set_mode")
	if len(sm) != 1 || !strings.Contains(string(sm[0].Params), `"modeId":"auto"`) {
		t.Fatalf("set_mode events = %+v", sm)
	}
}

func TestConfigureModeNotOffered(t *testing.T) {
	_, _, _, logPath, err := configured(t, fakeScript{ModeConfig: true, Modes: []string{"default", "acceptEdits", "plan"}}, "", "auto")
	var me *ModeError
	if !errors.As(err, &me) {
		t.Fatalf("Configure() error = %v, want *ModeError", err)
	}
	want := `acp: agent test-agent does not offer mode "auto" (offered: default, acceptEdits, plan). Set a different permission_preset.`
	if me.Error() != want {
		t.Fatalf("Error() = %q", me.Error())
	}
	evs := readFakeLog(t, logPath)
	if len(fakeEventsOf(evs, "session/set_config_option"))+len(fakeEventsOf(evs, "session/set_mode")) != 0 {
		t.Fatal("a mode was sent for a mode that is not offered")
	}
}

func TestConfigureUnknownModel(t *testing.T) {
	_, _, _, _, err := configured(t, fakeScript{ModeConfig: true, Models: []string{"opus", "sonnet"}}, "gpt-x", "auto")
	want := `acp: agent test-agent does not accept model "gpt-x" (offered: opus, sonnet).`
	if err == nil || err.Error() != want {
		t.Fatalf("Configure() error = %v, want %q", err, want)
	}
}

func TestConfigureModelCallCancelled(t *testing.T) {
	c, _ := startFake(t, fakeScript{ModeConfig: true, Models: []string{"opus", "sonnet"}})
	info, err := c.NewSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Configure(ctx, &info, "sonnet", "auto")
	if err == nil || strings.Contains(err.Error(), "does not accept model") {
		t.Fatalf("Configure() error = %v, want the error of the call", err)
	}
}

func TestNewSessionNotLoggedIn(t *testing.T) {
	c, _ := startFake(t, fakeScript{AuthRequired: true})
	_, err := c.NewSession(context.Background(), t.TempDir())
	if _, ok := errors.AsType[*NotLoggedInError](err); !ok {
		t.Fatalf("NewSession() error = %v, want *NotLoggedInError", err)
	}
}

func TestResumeSession(t *testing.T) {
	c, logPath := startFake(t, fakeScript{Resume: true, ModeConfig: true})
	info, err := c.ResumeSession(context.Background(), "old-session", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "old-session" || !info.ModeConfig {
		t.Fatalf("info = %+v", info)
	}
	if evs := fakeEventsOf(readFakeLog(t, logPath), "session/resume"); len(evs) != 1 ||
		!strings.Contains(string(evs[0].Params), `"sessionId":"old-session"`) {
		t.Fatalf("session/resume events = %+v", evs)
	}
}

func TestLoadSessionDropsReplay(t *testing.T) {
	replay := []fakeStep{
		{ToolCall: &fakeToolCall{ID: "r1", Kind: "read", Title: "Read old.go"}},
		{Message: "old answer"},
	}
	s := fakeScript{LoadSession: true, Replay: replay}

	// Control: a handler that is attached during the load sees the replay.
	// So the fake agent sends it.
	c1, _ := startFake(t, s)
	early := &recordHandler{sid: "old-session"}
	c1.attach(early)
	if _, err := c1.LoadSession(context.Background(), "old-session", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if ups, _ := early.got(); len(ups) != 2 {
		t.Fatalf("control: %d updates, want 2", len(ups))
	}

	c2, logPath := startFake(t, s)
	info, err := c2.LoadSession(context.Background(), "old-session", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	late := &recordHandler{sid: info.ID}
	c2.attach(late)
	time.Sleep(200 * time.Millisecond)
	if ups, _ := late.got(); len(ups) != 0 {
		t.Fatalf("a replayed update reached the handler: %+v", ups)
	}
	if evs := fakeEventsOf(readFakeLog(t, logPath), "session/load"); len(evs) != 1 ||
		!strings.Contains(string(evs[0].Params), `"mcpServers":[]`) {
		t.Fatalf("session/load events = %+v", evs)
	}
}

func TestCloseSession(t *testing.T) {
	for _, closeCap := range []bool{true, false} {
		c, logPath := startFake(t, fakeScript{Close: closeCap})
		if err := c.CloseSession(context.Background(), "s1"); err != nil {
			t.Fatal(err)
		}
		want := 0
		if closeCap {
			want = 1
		}
		if got := len(fakeEventsOf(readFakeLog(t, logPath), "session/close")); got != want {
			t.Fatalf("close=%v: session/close events = %d, want %d", closeCap, got, want)
		}
	}
}
