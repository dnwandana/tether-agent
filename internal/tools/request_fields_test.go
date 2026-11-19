package tools

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

func TestRunRootSession(t *testing.T) {
	for _, c := range []struct {
		name string
		env  *Env
		want string
	}{
		{"nil env", nil, ""},
		{"top-level run", &Env{SessionID: "s1"}, "s1"},
		{"child run", &Env{SessionID: "child", RootSessionID: "root"}, "root"},
	} {
		if got := c.env.RunRootSession(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGateApproverPassesSecretFields(t *testing.T) {
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	c := CommandCheck{Command: "rm -rf /tmp/x", Secrets: []string{"GH_TOKEN", "PYPI_TOKEN"},
		StagedCredentials: []string{".env", "deploy/server.pem"}, StageUnchecked: true}
	_ = g.CheckCommand(context.Background(), &Env{SessionID: "s1"}, c)
	if len(p.reqs) != 1 {
		t.Fatalf("got %d requests", len(p.reqs))
	}
	r := p.reqs[0]
	if !reflect.DeepEqual(r.Secrets, c.Secrets) || !reflect.DeepEqual(r.StagedCredentials, c.StagedCredentials) ||
		!r.StageUnchecked {
		t.Fatalf("%+v", r)
	}
	// A command with no bound secret gets no names.
	p.reqs = nil
	_ = g.CheckCommand(context.Background(), &Env{SessionID: "s1"}, CommandCheck{Command: "rm -rf /tmp/y"})
	if len(p.reqs) != 1 || p.reqs[0].Secrets != nil || p.reqs[0].StagedCredentials != nil || p.reqs[0].StageUnchecked {
		t.Fatalf("%+v", p.reqs)
	}
}

// fakeSecretUI implements each new optional interface.
type fakeSecretUI struct{}

func (fakeSecretUI) AskPin(context.Context, *Env, PinRequest) (bool, error)  { return false, nil }
func (fakeSecretUI) AskSecret(context.Context, SecretQuestion) (bool, error) { return false, nil }
func (fakeSecretUI) Mark(context.Context, string, time.Time) error           { return nil }
func (fakeSecretUI) Clear(context.Context, string) error                     { return nil }

var (
	_ SecretPinner = fakeSecretUI{}
	_ SecretAsker  = fakeSecretUI{}
	_ SecretMarks  = fakeSecretUI{}
)

func TestNewRequestShapes(t *testing.T) {
	r := PinRequest{Name: "PYPI_TOKEN", Path: "/home/user/.local/bin/twine", SHA256: "3f2a91c0",
		Command: "twine upload dist/*", Handles: []string{"TMP_SECRET_1"}, Sources: []string{"web_fetch"}}
	q := SecretQuestion{Name: "PYPI_TOKEN", Commands: []string{"twine upload", "uv publish"},
		Reason: "publish tether-tools 0.4.1 to PyPI"}
	env := Env{UserID: "42", RootSessionID: "root", JobName: "spend-report", JobPurpose: "Sum the spend.", DraftOnly: true}
	req := security.ApprovalRequest{
		Secrets: []string{"PYPI_TOKEN"}, StagedCredentials: []string{".env"}, StageUnchecked: true,
		PinName: r.Name, PinPath: r.Path, PinHash: r.SHA256, HandleNames: r.Handles,
		OutsideSources: r.Sources, OutsideHosts: []string{"example.com"}, ToolCounts: map[string]int{"web_fetch": 2},
		JobName: env.JobName, JobPurpose: env.JobPurpose, DraftOnly: env.DraftOnly,
	}
	if q.Name != req.PinName || req.ToolCounts["web_fetch"] != 2 || env.RunRootSession() != "root" {
		t.Fatalf("%+v %+v %+v", q, req, env)
	}
}
