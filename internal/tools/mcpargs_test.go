package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

// recGuard is a SendGuarder that records its input and holds each send.
type recGuard struct{ got []security.GuardInput }

func (g *recGuard) Check(_ context.Context, in security.GuardInput) (bool, string) {
	g.got = append(g.got, in)
	return false, "no"
}

func sendCall(args string) security.MCPCall {
	return security.MCPCall{Server: "gmail", Tool: "send_email", Class: "send", ConfigHash: "0123456789abcdef",
		Args: json.RawMessage(args)}
}

func TestGateApproverCopiesRunFacts(t *testing.T) {
	guard := &recGuard{}
	g := GateApprover{Gate: &security.Gate{Mode: "manual", CronMode: "approve", Guard: guard}}
	env := &Env{SessionID: "c1", Kind: "cron", JobName: "competitor-prices", JobPurpose: "Check the prices.",
		Values: map[string]any{}}
	MarkOutside(env, "web_fetch", "pastebin.com")
	_, err := g.CheckMCP(ctx, env, sendCall(`{"to":"x@y.io","body":"hi"}`))
	var be *security.BlockedError
	if !errors.As(err, &be) || be.Reason != "BLOCKED: model_guard held gmail.send_email: no" || len(guard.got) != 1 {
		t.Fatal(err, len(guard.got))
	}
	in := guard.got[0]
	if in.JobName != "competitor-prices" || in.JobPurpose != "Check the prices." ||
		len(in.Sources) != 1 || in.Sources[0] != "web_fetch" || len(in.Hosts) != 1 || in.Hosts[0] != "pastebin.com" {
		t.Fatalf("%+v", in)
	}
	env.DraftOnly = true
	_, err = g.CheckMCP(ctx, env, sendCall(`{"to":"x@y.io"}`))
	if !errors.As(err, &be) || !strings.HasPrefix(be.Reason, "BLOCKED: this job is draft-only.") {
		t.Fatal(err)
	}
}

func TestGateApproverReturnsRedactedArgs(t *testing.T) {
	p := &recPrompter{scope: security.ScopeRedacted}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	args, err := g.CheckMCP(ctx, &Env{SessionID: "s"}, sendCall(`{"to":"x@y.io","body":"card 4111 1111 1111 1111"}`))
	if err != nil || !strings.Contains(string(args), "[card ••••1111]") || strings.Contains(string(args), "4111 1111") {
		t.Fatalf("%s %v", args, err)
	}
	p.scope = security.ScopeOnce
	if args, err := g.CheckMCP(ctx, &Env{SessionID: "s"}, sendCall(`{"to":"x@y.io","body":"card 4111 1111 1111 1111"}`)); err != nil || args != nil {
		t.Fatalf("%s %v", args, err)
	}
}
