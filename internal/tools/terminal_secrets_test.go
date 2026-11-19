package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
)

// boundValue is the .env value of PYPI_TOKEN in the tests.
const boundValue = "pypi-AgEIcHlwaS5vcmcCJGQ5ZTk2"

// echoScript prints the value of PYPI_TOKEN, or "nothing".
const echoScript = "echo \"twine got ${PYPI_TOKEN:-nothing}\"\n"

// boundFixture returns a terminal tool with PYPI_TOKEN bound to "twine upload", a .env store
// that holds boundValue, and a pin store. It writes a program twine with the body script into
// a new bin folder at the start of PATH. It returns the tool and the real path of twine.
func boundFixture(t *testing.T, script string) (*TerminalTool, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	twine := filepath.Join(bin, "twine")
	if err := os.WriteFile(twine, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(twine)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("PYPI_TOKEN="+boundValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return &TerminalTool{
		DefaultTimeout: time.Minute,
		SpillDir:       filepath.Join(dir, "spill"),
		Bound:          map[string][]string{"PYPI_TOKEN": {"twine upload"}},
		Store:          security.NewDotEnvStore(envFile, map[string]string{"PYPI_TOKEN": boundValue}, nil),
		Pins:           &security.PinStore{DB: d},
	}, real
}

// pinGate lets each command run and records each check. It answers each pin question with
// allow and err.
type pinGate struct {
	allow  bool
	err    error
	checks *[]CommandCheck
	asks   *[]PinRequest
}

func (g pinGate) CheckCommand(_ context.Context, _ *Env, c CommandCheck) error {
	*g.checks = append(*g.checks, c)
	return nil
}

func (g pinGate) AskPin(_ context.Context, _ *Env, r PinRequest) (bool, error) {
	*g.asks = append(*g.asks, r)
	return g.allow, g.err
}

// newPinGate returns a pinGate and its two records.
func newPinGate(allow bool, err error) (pinGate, *[]CommandCheck, *[]PinRequest) {
	g := pinGate{allow: allow, err: err, checks: &[]CommandCheck{}, asks: &[]PinRequest{}}
	return g, g.checks, g.asks
}

func TestBoundSecretPinAsksOnce(t *testing.T) {
	tool, twine := boundFixture(t, echoScript)
	g, checks, asks := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != "twine got "+boundValue+"\n" {
		t.Fatalf("%q", out)
	}
	if len(*asks) != 1 {
		t.Fatalf("%+v", *asks)
	}
	a := (*asks)[0]
	if a.Name != "PYPI_TOKEN" || a.Path != twine || len(a.SHA256) != 64 || a.Command != "twine upload dist/x" {
		t.Fatalf("%+v", a)
	}
	if !reflect.DeepEqual((*checks)[0].Secrets, []string{"PYPI_TOKEN"}) {
		t.Fatalf("%+v", (*checks)[0])
	}
	// The pin holds, so the second run asks nothing.
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != "twine got "+boundValue+"\n" || len(*asks) != 1 {
		t.Fatalf("%q, %d asks", out, len(*asks))
	}
	// A new hash asks again.
	if err := os.WriteFile(twine, []byte("#!/bin/sh\n# changed\n"+echoScript), 0o700); err != nil {
		t.Fatal(err)
	}
	runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env)
	if len(*asks) != 2 {
		t.Fatalf("a new hash must ask again: %d asks", len(*asks))
	}
	// A value saved in .env reaches the next command.
	const newValue = "pypi-NewValueOf32Characters-0123"
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(twine)), ".env"), []byte("PYPI_TOKEN="+newValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != "twine got "+newValue+"\n" {
		t.Fatalf("%q", out)
	}
}

func TestBoundSecretWithheld(t *testing.T) {
	const notYet = "is not allowed yet. Run the command once in a Telegram chat turn, so that the owner can allow it]"
	for _, c := range []struct {
		name, command string
		gate          func() (Approver, *[]PinRequest)
		member        bool
		script        bool
		want          func(twine string) string
	}{
		{"deny", "twine upload dist/x",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(false, nil); return g, a }, false, false,
			func(p string) string {
				return "twine got nothing\n[PYPI_TOKEN was not given: the owner did not allow the program " + p + "]"
			}},
		{"no prompter", "twine upload dist/x",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(false, security.ErrNoPrompter); return g, a }, false, false,
			func(p string) string {
				return "twine got nothing\n[PYPI_TOKEN was not given: the program " + p + " " + notYet
			}},
		{"gate with no pinner", "twine upload dist/x",
			func() (Approver, *[]PinRequest) { return allowAll{}, &[]PinRequest{} }, false, false,
			func(p string) string {
				return "twine got nothing\n[PYPI_TOKEN was not given: the program " + p + " " + notYet
			}},
		{"other part", "twine upload dist/x; echo done",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(true, nil); return g, a }, false, false,
			func(string) string {
				return "twine got nothing\ndone\n[PYPI_TOKEN was not given: the command has more than one part. A bound secret goes only to a command that runs alone]"
			}},
		{"member", "twine upload dist/x",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(true, nil); return g, a }, true, false,
			func(string) string {
				return "twine got nothing\n[PYPI_TOKEN was not given: this run cannot use bound secrets]"
			}},
		{"script call", "twine upload dist/x",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(true, nil); return g, a }, false, true,
			func(string) string {
				return "twine got nothing\n[PYPI_TOKEN was not given: this run cannot use bound secrets]"
			}},
		{"no candidate", "echo hi",
			func() (Approver, *[]PinRequest) { g, _, a := newPinGate(true, nil); return g, a }, false, false,
			func(string) string { return "hi\n" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			tool, twine := boundFixture(t, echoScript)
			// A process value of a bound name never reaches the command.
			t.Setenv("PYPI_TOKEN", "from-process")
			env := newEnv(t)
			g, asks := c.gate()
			env.Gate, env.Member, env.ScriptCall = g, c.member, c.script
			args, _ := json.Marshal(map[string]string{"command": c.command})
			if out := runToolEnv(t, tool, string(args), env); out != c.want(twine) {
				t.Fatalf("got %q\nwant %q", out, c.want(twine))
			}
			if (c.member || c.script || c.name == "other part" || c.name == "no candidate") && len(*asks) != 0 {
				t.Fatalf("no pin question may come: %+v", *asks)
			}
		})
	}
}

func TestBoundSecretMissingValue(t *testing.T) {
	tool, _ := boundFixture(t, echoScript)
	tool.Bound["GH_TOKEN"] = []string{"twine upload"}
	g, checks, _ := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	want := "twine got " + boundValue + "\n[GH_TOKEN was not given: GH_TOKEN is bound but has no value. Ask the owner with request_secret]"
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
	if !reflect.DeepEqual((*checks)[0].Secrets, []string{"PYPI_TOKEN"}) {
		t.Fatalf("%+v", (*checks)[0])
	}
}

func TestBoundSecretNotOnGoalGatePath(t *testing.T) {
	tool, _ := boundFixture(t, echoScript)
	g, _, asks := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	out, code, err := tool.Exec(context.Background(), env, "twine upload dist/x", "", time.Minute)
	if err != nil || code != 0 || out != "twine got nothing\n" || len(*asks) != 0 {
		t.Fatalf("%q %d %v %+v", out, code, err, *asks)
	}
}

func TestBoundSecretBackground(t *testing.T) {
	tool, _ := boundFixture(t, echoScript)
	g, _, _ := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	var r struct {
		LogPath string `json:"log_path"`
	}
	if err := json.Unmarshal([]byte(runToolEnv(t, tool, `{"command":"twine upload dist/x","background":true}`, env)), &r); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(r.LogPath)
		if strings.Contains(string(b), "twine got "+boundValue) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background command did not get the secret: %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
