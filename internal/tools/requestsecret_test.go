package tools

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// secretUI is an Asker and a SecretAsker. It records the question and gives saved and err.
// With wait, it waits until the context ends.
type secretUI struct {
	saved bool
	err   error
	wait  bool
	got   *SecretQuestion
}

func (secretUI) Ask(context.Context, Question) (Answer, error) { return Answer{}, nil }

func (s secretUI) AskSecret(ctx context.Context, q SecretQuestion) (bool, error) {
	if s.got != nil {
		*s.got = q
	}
	if s.wait {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.saved, s.err
}

// plainAsker is an Asker with no secret card.
type plainAsker struct{}

func (plainAsker) Ask(context.Context, Question) (Answer, error) { return Answer{}, nil }

var secretTool = RequestSecretTool{Bound: map[string][]string{"PYPI_TOKEN": {"twine upload", "uv publish"}}}

const onlyOwnerText = "request_secret works only in a chat turn of the owner. Tell the owner that PYPI_TOKEN is missing."

func TestRequestSecretSaved(t *testing.T) {
	var q SecretQuestion
	env := &Env{Kind: "chat", Ask: secretUI{saved: true, got: &q}}
	args := `{"name":"PYPI_TOKEN","reason":"publish\u001b[31m tether-tools\n0.4.1 to PyPI"}`
	if out := runToolEnv(t, secretTool, args, env); out != "Saved PYPI_TOKEN. You did not see the value. Run the command again." {
		t.Fatalf("got %q", out)
	}
	want := SecretQuestion{Name: "PYPI_TOKEN", Commands: []string{"twine upload", "uv publish"},
		Reason: "publish [31m tether-tools 0.4.1 to PyPI"}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("got %+v\nwant %+v", q, want)
	}
	// A long reason is cut to 240 runes.
	runToolEnv(t, secretTool, `{"name":"PYPI_TOKEN","reason":"`+strings.Repeat("é", 300)+`"}`, env)
	if n := len([]rune(q.Reason)); n != 240 {
		t.Fatalf("reason has %d runes", n)
	}
}

func TestRequestSecretRefusals(t *testing.T) {
	ui := secretUI{saved: true}
	for _, c := range []struct {
		name string
		tool RequestSecretTool
		args string
		env  *Env
		want string
	}{
		{"not bound", secretTool, `{"name":"GH_TOKEN","reason":"r"}`, &Env{Kind: "chat", Ask: ui},
			`"GH_TOKEN" is not a bound secret. The owner binds secrets in exec_secrets in config.yaml.`},
		{"empty map", RequestSecretTool{}, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "chat", Ask: ui},
			`"PYPI_TOKEN" is not a bound secret. The owner binds secrets in exec_secrets in config.yaml.`},
		{"cron", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "cron", Ask: ui}, onlyOwnerText},
		{"child of cron", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "subagent", RootKind: "cron", Ask: ui}, onlyOwnerText},
		{"child", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "subagent", Ask: ui}, onlyOwnerText},
		{"review", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "review", Ask: ui}, onlyOwnerText},
		{"member", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "chat", Member: true, Ask: ui}, onlyOwnerText},
		{"script", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "chat", ScriptCall: true, Ask: ui}, onlyOwnerText},
		{"no secret card", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "chat", Ask: plainAsker{}}, onlyOwnerText},
		{"no asker", secretTool, `{"name":"PYPI_TOKEN","reason":"r"}`, &Env{Kind: "chat"}, onlyOwnerText},
	} {
		if got := runToolEnvErr(t, c.tool, c.args, c.env); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestRequestSecretTimeoutAndErrors(t *testing.T) {
	old := requestSecretWait
	requestSecretWait = 50 * time.Millisecond
	t.Cleanup(func() { requestSecretWait = old })
	const timeoutText = "The owner did not give PYPI_TOKEN in 5 min."
	args := `{"name":"PYPI_TOKEN","reason":"r"}`
	if out := runToolEnv(t, secretTool, args, &Env{Kind: "chat", Ask: secretUI{wait: true}}); out != timeoutText {
		t.Fatalf("got %q", out)
	}
	if out := runToolEnv(t, secretTool, args, &Env{Kind: "chat", Ask: secretUI{}}); out != timeoutText {
		t.Fatalf("got %q", out)
	}
	const noChat = "I cannot write to the owner in a private chat. Ask the owner to start a private chat with me, then try again."
	if got := runToolEnvErr(t, secretTool, args, &Env{Kind: "chat", Ask: secretUI{err: errors.New(noChat)}}); got != noChat {
		t.Fatalf("got %q", got)
	}
}

func TestRequestSecretLists(t *testing.T) {
	if secretTool.Name() != "request_secret" || secretTool.Toolset() != SetTerminal || secretTool.ParallelSafe() {
		t.Fatal("name, toolset, or parallel flag")
	}
	if !slices.Contains(ReadOnlyDenied(), "request_secret") || slices.Contains(ScriptCallableTools, "request_secret") {
		t.Fatal("request_secret is denied in read-only mode and no script can call it")
	}
	if got := toolNeeds("request_secret"); got != "a chat turn of the owner (CLI, or Telegram with interactive buttons)." {
		t.Fatalf("got %q", got)
	}
}
