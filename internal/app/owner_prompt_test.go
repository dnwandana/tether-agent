package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestNewTurnEnvOwnerPrompter(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  allowed_users: [\"5\"]\n")
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	dm := &sessions.Session{ID: "s1", Key: "telegram:dm:5", Source: "telegram", ChatID: "5", Kind: "chat"}
	env := a.NewTurnEnv(dm, "telegram", "5")
	g := env.Gate.(tools.GateApprover)
	if env.UserID != "5" || g.OwnerPrompter == nil || g.OwnerPrompter != g.Prompter {
		t.Fatalf("a private chat asks the owner in the same chat: %+v", g)
	}
	group := &sessions.Session{ID: "s2", Key: "telegram:group:-100:9", Source: "telegram", ChatID: "-100", ThreadID: "9", Kind: "chat"}
	g = a.NewTurnEnv(group, "telegram", "7").Gate.(tools.GateApprover)
	if _, ok := g.OwnerPrompter.(*ownerPrompter); !ok {
		t.Fatalf("a group turn needs the owner prompter: %#v", g.OwnerPrompter)
	}
	cli := a.NewTurnEnv(nil, "cli", "").Gate.(tools.GateApprover)
	if cli.OwnerPrompter == nil || cli.OwnerPrompter != cli.Prompter {
		t.Fatalf("%+v", cli)
	}
	if cron := a.NewEnv(&sessions.Session{ID: "c1", Key: "cron:j1", Source: "cron", Kind: "cron"}, "cron").Gate.(tools.GateApprover); cron.OwnerPrompter != nil {
		t.Fatal("a cron run has no owner prompter")
	}
}

func TestOwnerPrompterNoOwnerChat(t *testing.T) {
	url, calls, mu := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  group_allowed_chats: [\"-100\"]\n")
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	group := &sessions.Session{ID: "s2", Key: "telegram:group:-100", Source: "telegram", ChatID: "-100", Kind: "chat"}
	p := a.NewTurnEnv(group, "telegram", "7").Gate.(tools.GateApprover).OwnerPrompter
	scope, err := p.PromptApproval(ctx, security.ApprovalRequest{PinName: "PYPI_TOKEN"})
	if scope != security.ScopeDeny || !errors.Is(err, telegram.ErrNoPrivateChat) {
		t.Fatal(scope, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(strings.Join(*calls, " "), "sendMessage") {
		t.Fatal("no owner chat: nothing goes to the group")
	}
}

func TestOwnerPrompterTellsGroup(t *testing.T) {
	url, calls, mu := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  allowed_users: [\"5\"]\n  group_allowed_chats: [\"-100\"]\n")
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	group := &sessions.Session{ID: "s2", Key: "telegram:group:-100", Source: "telegram", ChatID: "-100", Kind: "chat"}
	p := a.NewTurnEnv(group, "telegram", "5").Gate.(tools.GateApprover).OwnerPrompter
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	// Nobody taps, so the prompt ends with the context.
	_, _ = p.PromptApproval(tctx, security.ApprovalRequest{PinName: "PYPI_TOKEN", PinPath: "/p", PinHash: "x"})
	mu.Lock()
	defer mu.Unlock()
	if n := strings.Count(strings.Join(*calls, " "), "sendMessage"); n < 2 {
		t.Fatalf("want the group line and the private prompt: %v", *calls)
	}
}
