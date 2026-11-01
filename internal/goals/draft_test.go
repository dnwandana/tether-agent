package goals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// draftProv is a provider that answers each call with reply, or fails with err. It keeps
// each request.
type draftProv struct {
	reply string
	err   error
	reqs  []provider.Request
}

func (p *draftProv) Name() string  { return "judge" }
func (p *draftProv) Model() string { return "judge-model" }

func (p *draftProv) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: p.reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func TestDraftContractParsesTheReply(t *testing.T) {
	cases := []struct{ name, reply string }{
		{"plain", `{"outcome": "auth on JWT", "verification": "auth suite green", "constraints": "", "boundaries": "internal/auth", "stop_when": ""}`},
		{"fenced", "```json\n{\"outcome\": \"auth on JWT\", \"verification\": \"auth suite green\", \"boundaries\": \"internal/auth\"}\n```"},
		{"prose", `Here is the contract: {"outcome": " auth on JWT ", "verification": "auth suite green", "boundaries": "internal/auth"} Done.`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &draftProv{reply: c.reply}
			var recorded []string
			j := &Judge{Timeout: time.Second, Record: func(_ context.Context, prompt string, _ provider.Provider, _ provider.Result) {
				recorded = append(recorded, prompt)
			}}
			got, ok := DraftContract(context.Background(), j, p, "  Migrate auth to JWT \n")
			want := Contract{Outcome: "auth on JWT", Verification: "auth suite green", Boundaries: "internal/auth"}
			if !ok || got != want {
				t.Fatalf("got %+v %v, want %+v", got, ok, want)
			}
			if len(p.reqs) != 1 {
				t.Fatalf("got %d calls, want 1", len(p.reqs))
			}
			req := p.reqs[0]
			if req.System != DraftContractSystemPrompt || len(req.Messages) != 1 || req.Messages[0].Role != provider.RoleUser ||
				req.Messages[0].Content != "Objective:\nMigrate auth to JWT" || req.Effort != "low" || req.MaxTokens != 4096 {
				t.Fatalf("%+v", req)
			}
			if len(recorded) != 1 || recorded[0] != "Objective:\nMigrate auth to JWT" {
				t.Fatalf("the judge session must record the call: %q", recorded)
			}
		})
	}
}

func TestDraftContractGivesNoContract(t *testing.T) {
	cases := []struct {
		name string
		p    *draftProv
	}{
		{"empty contract", &draftProv{reply: `{"outcome": "", "verification": "  ", "constraints": null}`}},
		{"not JSON", &draftProv{reply: "I cannot draft a contract for this."}},
		{"JSON list", &draftProv{reply: `["outcome"]`}},
		{"error", &draftProv{err: errors.New("HTTP 401: the key is not valid")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DraftContract(context.Background(), &Judge{Timeout: time.Second}, c.p, "ship it")
			if ok || got != (Contract{}) {
				t.Fatalf("got %+v %v, want no contract", got, ok)
			}
		})
	}
}

func TestDraftContractEmptyObjectiveMakesNoCall(t *testing.T) {
	p := &draftProv{reply: `{"outcome": "x"}`}
	if _, ok := DraftContract(context.Background(), &Judge{Timeout: time.Second}, p, " \n "); ok || len(p.reqs) != 0 {
		t.Fatalf("ok=%v calls=%d", ok, len(p.reqs))
	}
}

func TestDraftContractCutsTheObjective(t *testing.T) {
	p := &draftProv{reply: `{"outcome": "x"}`}
	DraftContract(context.Background(), &Judge{Timeout: time.Second}, p, strings.Repeat("é", 4000)+"TAIL")
	if got, want := p.reqs[0].Messages[0].Content, "Objective:\n"+strings.Repeat("é", 4000)+"… [truncated]"; got != want {
		t.Fatalf("the objective must be cut at 4000 chars, got %d chars", len([]rune(got)))
	}
	p = &draftProv{reply: `{"outcome": "x"}`}
	DraftContract(context.Background(), &Judge{Timeout: time.Second}, p, strings.Repeat("a", 4000))
	if got, want := p.reqs[0].Messages[0].Content, "Objective:\n"+strings.Repeat("a", 4000); got != want {
		t.Fatal("an objective of exactly 4000 chars is not cut")
	}
}
