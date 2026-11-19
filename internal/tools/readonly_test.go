package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

// modeGate is an Approver with a fixed topic mode. It keeps the scopes that Dispatch asked for.
type modeGate struct {
	mode   string
	scopes *[]string
}

func (modeGate) CheckCommand(context.Context, *Env, CommandCheck) error { return nil }
func (g modeGate) ModeCheck(_ context.Context, scope string) (string, error) {
	if g.scopes != nil {
		*g.scopes = append(*g.scopes, scope)
	}
	if g.mode == "error" {
		return "", errors.New("database is closed")
	}
	return g.mode, nil
}

// mixedStub is a mixed tool that allows the calls whose arguments are {"action":"read"}.
type mixedStub struct{ fakeTool }

func (mixedStub) ReadOnlyCall(args json.RawMessage) bool { return string(args) == `{"action":"read"}` }

// mcpStub is an MCP tool with a class.
type mcpStub struct {
	fakeTool
	class string
}

func (m mcpStub) MCPClass() string { return m.class }

func readOnlyEnv(mode string, scopes *[]string) *Env {
	return &Env{ModeScope: "telegram:5:0", Gate: modeGate{mode: mode, scopes: scopes}}
}

func TestReadOnlyDenied(t *testing.T) {
	want := []string{"acp_delegate", "browser_click", "browser_fill", "browser_run_js", "execute_code",
		"patch", "request_secret", "skill_manage", "terminal", "write_file"}
	if got := ReadOnlyDenied(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestDispatchRefusesInReadOnly(t *testing.T) {
	const tail = " cannot run here. Tell the owner, who can send /mode guarded."
	for _, c := range []struct {
		tool    Tool
		args    string
		refused bool
		text    string
	}{
		{fakeTool{name: "write_file", out: "ok"}, `{}`, true, "This topic is read-only. The tool write_file" + tail},
		{fakeTool{name: "read_file", out: "ok"}, `{}`, false, "ok"},
		{fakeTool{name: "new_tool", out: "ok"}, `{}`, true, "This topic is read-only. The tool new_tool" + tail},
		{mixedStub{fakeTool{name: "memory", out: "ok"}}, `{"action":"read"}`, false, "ok"},
		{mixedStub{fakeTool{name: "memory", out: "ok"}}, `{"action":"add"}`, true,
			"This topic is read-only. The tool memory (add)" + tail},
		{mcpStub{fakeTool{name: "mcp_slack_list", out: "ok"}, "read"}, `{}`, false, "ok"},
		{mcpStub{fakeTool{name: "mcp_slack_post", out: "ok"}, "send"}, `{}`, true,
			"This topic is read-only. The tool mcp_slack_post (send)" + tail},
	} {
		var scopes []string
		res := dispatch(t, c.tool, c.args, readOnlyEnv(security.ModeReadOnly, &scopes))
		if res.IsError != c.refused || res.Text != c.text {
			t.Errorf("%s %s: got %+v", c.tool.Name(), c.args, res)
		}
		// An allowed call makes no query. A refused call reads the mode of its scope once.
		var want []string
		if c.refused {
			want = []string{"telegram:5:0"}
		}
		if !reflect.DeepEqual(scopes, want) {
			t.Errorf("%s: got the mode queries %v, want %v", c.tool.Name(), scopes, want)
		}
	}
}

func TestDispatchOtherModesAndNoScope(t *testing.T) {
	tool := fakeTool{name: "write_file", out: "ok"}
	for _, mode := range []string{"", security.ModeGuarded, security.ModeFull} {
		if res := dispatch(t, tool, `{}`, readOnlyEnv(mode, nil)); res.IsError || res.Text != "ok" {
			t.Errorf("mode %q: %+v", mode, res)
		}
	}
	// An empty scope makes no query.
	var scopes []string
	env := readOnlyEnv(security.ModeReadOnly, &scopes)
	env.ModeScope = ""
	if res := dispatch(t, tool, `{}`, env); res.IsError || len(scopes) != 0 {
		t.Fatalf("%+v %v", res, scopes)
	}
	// A gate with no ModeReader counts as guarded.
	if res := dispatch(t, tool, `{}`, &Env{ModeScope: "telegram:5:0"}); res.IsError {
		t.Fatalf("%+v", res)
	}
}

func TestDispatchRefusesOnModeQueryError(t *testing.T) {
	// A failed mode query refuses each tool that is not on the allow list (closed toward
	// read-only). An allowed tool still runs.
	env := readOnlyEnv("error", nil)
	if res := dispatch(t, fakeTool{name: "write_file"}, `{}`, env); !res.IsError {
		t.Fatalf("write_file ran after a mode query error: %+v", res)
	}
	if res := dispatch(t, fakeTool{name: "read_file"}, `{}`, env); res.IsError {
		t.Fatalf("read_file was refused: %+v", res)
	}
}

func TestReadOnlyAllowedMixedToolWithNoMethod(t *testing.T) {
	// A mixed tool name with no ReadOnlyCall method is refused for each action.
	if ReadOnlyAllowed(fakeTool{name: "memory"}, json.RawMessage(`{"action":"read"}`)) {
		t.Fatal("a mixed tool with no ReadOnlyCall must be refused")
	}
}

var _ ModeReader = GateApprover{}
var _ ModeReader = GateApprover{}

func TestReadOnlyCallActions(t *testing.T) {
	cookies, ok := (&BrowserTools{}).Tool("browser_cookies").(ReadOnlyCaller)
	if !ok {
		t.Fatal("browser_cookies must implement ReadOnlyCaller")
	}
	for _, c := range []struct {
		name string
		tool ReadOnlyCaller
		args string
		want bool
	}{
		{"memory", MemoryTool{}, `{"action":"read"}`, true},
		{"memory", MemoryTool{}, `{"action":"search","query":"go"}`, true},
		{"memory", MemoryTool{}, `{"action":"add","target":"user","content":"x"}`, false},
		{"memory", MemoryTool{}, `{"action":"replace"}`, false},
		{"memory", MemoryTool{}, `{"action":"remove"}`, false},
		// encoding/json takes the last key that matches with no regard to case, so Run adds.
		{"memory", MemoryTool{}, `{"action":"read","ACTION":"add"}`, false},
		{"memory", MemoryTool{}, `{"action":"add","Action":"read"}`, true},
		{"memory", MemoryTool{}, `{"action":1}`, false},
		{"memory", MemoryTool{}, `{}`, false},
		{"cronjob", CronJobTool{}, `{"action":"list"}`, true},
		{"cronjob", CronJobTool{}, `{"action":"runs","job_id":"j1"}`, true},
		{"cronjob", CronJobTool{}, `{"action":"create","schedule":"30m","prompt":"p"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"update","job_id":"j1"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"pause","job_id":"j1"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"resume","job_id":"j1"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"remove","job_id":"j1"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"run","job_id":"j1"}`, false},
		{"cronjob", CronJobTool{}, `{"action":"list","ACTION":"remove","job_id":"j1"}`, false},
		{"browser_cookies", cookies, `{"action":"get"}`, true},
		{"browser_cookies", cookies, `{"action":"set","name":"a","value":"b"}`, false},
		{"browser_cookies", cookies, `{"action":"clear"}`, false},
		{"browser_cookies", cookies, `{"action":"get","Action":"clear"}`, false},
	} {
		if got := c.tool.ReadOnlyCall(json.RawMessage(c.args)); got != c.want {
			t.Errorf("%s.ReadOnlyCall(%s) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
	for _, name := range []string{"browser_navigate", "browser_click", "browser_read"} {
		if _, ok := (&BrowserTools{}).Tool(name).(ReadOnlyCaller); ok {
			t.Errorf("%s must not implement ReadOnlyCaller", name)
		}
	}
}

// TestReadOnlyRefusesMemoryAddWithCaseKey: {"action":"read","ACTION":"add"} is refused in
// read-only mode, and the memory does not change.
func TestReadOnlyRefusesMemoryAddWithCaseKey(t *testing.T) {
	s := openStore(t)
	tool := MemoryTool{Store: s}
	args := `{"action":"read","ACTION":"add","target":"user","content":"The user uses Go"}`
	res := dispatch(t, tool, args, readOnlyEnv(security.ModeReadOnly, nil))
	if !res.IsError || res.Text != "This topic is read-only. The tool memory (add) cannot run here. "+
		"Tell the owner, who can send /mode guarded." {
		t.Fatalf("%+v", res)
	}
	if s.Profile() != "" {
		t.Fatalf("a refused call must not change the memory: %q", s.Profile())
	}
	if res := dispatch(t, tool, `{"action":"search","query":"go"}`, readOnlyEnv(security.ModeReadOnly, nil)); res.IsError {
		t.Fatalf("a read must run in read-only mode: %+v", res)
	}
}
