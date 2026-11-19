package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

var _ SecretPinner = GateApprover{}

func TestGateApproverAskPinBuildsRequest(t *testing.T) {
	p := &recPrompter{scope: security.ScopeOnce}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1", SessionKey: "telegram:42", Kind: "subagent", RootKind: "chat",
		ModeScope: "telegram:42:0", Values: map[string]any{"child_name": "2/5"}}
	r := PinRequest{Name: "PYPI_TOKEN", Path: "/home/user/.local/bin/twine", SHA256: "3f2a91c0",
		Command: "twine upload dist/*", Handles: []string{"TMP_SECRET_1"}, Sources: []string{"web_fetch"}}
	ok, err := g.AskPin(context.Background(), env, r)
	if !ok || err != nil || len(p.reqs) != 1 {
		t.Fatalf("got %v %v %+v", ok, err, p.reqs)
	}
	q := p.reqs[0]
	if q.PinName != r.Name || q.PinPath != r.Path || q.PinHash != r.SHA256 || q.Command != r.Command ||
		!reflect.DeepEqual(q.HandleNames, r.Handles) || !reflect.DeepEqual(q.OutsideSources, r.Sources) ||
		q.SessionID != "s1" || q.Kind != "subagent" || q.RootKind != "chat" || q.ChildName != "2/5" || q.Scope != "telegram:42:0" {
		t.Fatalf("%+v", q)
	}
}

func TestGateApproverAskPinNoGateOrPrompter(t *testing.T) {
	if ok, err := (GateApprover{}).AskPin(context.Background(), &Env{}, PinRequest{Name: "X"}); ok || err == nil {
		t.Fatalf("a nil gate must fail closed: %v %v", ok, err)
	}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}}
	if ok, err := g.AskPin(context.Background(), &Env{}, PinRequest{Name: "X"}); ok || !errors.Is(err, security.ErrNoPrompter) {
		t.Fatalf("got %v %v", ok, err)
	}
}
