package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// skillCmd returns the skill command of slug with the description desc.
func skillCmd(slug, desc string) gateway.SkillCommand {
	return gateway.SkillCommand{Slug: slug, MenuName: strings.ReplaceAll(slug, "-", "_"),
		Skill: skills.Skill{Name: slug, Description: desc}}
}

func TestBotCommandsSkills(t *testing.T) {
	long := strings.Repeat("a", 33) // a menu name over 32 characters
	full := strings.Repeat("b", 32) // a menu name of exactly 32 characters
	got := BotCommands([]gateway.SkillCommand{
		skillCmd(long, "Too long"),
		skillCmd("arxiv", "Search arXiv"),
		skillCmd(full, ""),
		skillCmd("git-helper", "Use git \u2014 well \u2013 here \u2010 now \u2212 ok"),
		{Slug: "new", MenuName: "new", Skill: skills.Skill{Name: "new", Description: "Clash"}},
		skillCmd("notes", strings.Repeat("é", 41)),
		skillCmd("plan", "Line one\nline two"),
		skillCmd("plan", "Second plan"),
		skillCmd("short", strings.Repeat("c", 40)),
	}, 0)
	want := append(BotCommands(nil, 0),
		BotCommand{Command: "arxiv", Description: "Search arXiv"},
		BotCommand{Command: full, Description: "Run the " + full + " skill"},
		BotCommand{Command: "git_helper", Description: "Use git - well - here - now - ok"},
		BotCommand{Command: "notes", Description: strings.Repeat("é", 37) + "..."},
		BotCommand{Command: "plan", Description: "Line one line two"},
		BotCommand{Command: "short", Description: strings.Repeat("c", 40)},
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(got[:22], BotCommands(nil, 0)) {
		t.Fatal("the built-ins must come first, in the same order")
	}
}

func TestBotCommandsCap(t *testing.T) {
	var cmds []gateway.SkillCommand
	for i := range 50 {
		cmds = append(cmds, skillCmd(fmt.Sprintf("s%02d", i), "x"))
	}
	got := BotCommands(cmds, 0)
	if len(got) != menuMax || got[len(got)-1].Command != "s37" {
		t.Fatalf("%d entries, last %+v", len(got), got[len(got)-1])
	}
	if _, hidden := botMenu(cmds, 0); hidden != 12 {
		t.Fatal(hidden)
	}
}

// menuServer is a fake Bot API for setMyCommands. It records the size of each menu. With
// failAll, each call fails. Else a call with more than the built-ins fails.
func menuServer(t *testing.T, failAll bool) (*httptest.Server, *[]int) {
	t.Helper()
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Commands []BotCommand `json:"commands"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sizes = append(sizes, len(body.Commands))
		w.Header().Set("Content-Type", "application/json")
		if failAll || len(body.Commands) > len(BotCommands(nil, 0)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: BOT_COMMAND_INVALID"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &sizes
}

func TestRegisterCommandsFallsBackToBuiltins(t *testing.T) {
	srv, sizes := menuServer(t, false)
	err := (&Poller{C: client(srv)}).RegisterCommands(ctx, []gateway.SkillCommand{skillCmd("arxiv", "Search arXiv")})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{23, 22}; !reflect.DeepEqual(*sizes, want) {
		t.Fatal(*sizes)
	}
	// With no skill entries, a failed call is not sent again.
	srv, sizes = menuServer(t, true)
	if err := (&Poller{C: client(srv)}).RegisterCommands(ctx, nil); err == nil {
		t.Fatal("want the error of setMyCommands")
	}
	if len(*sizes) != 1 {
		t.Fatal(*sizes)
	}
}

func TestRegisterCommandsStopsOnCanceledContext(t *testing.T) {
	srv, _ := menuServer(t, false)
	var logs bytes.Buffer
	p := &Poller{C: client(srv), Log: slog.New(slog.NewTextHandler(&logs, nil))}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.RegisterCommands(cctx, []gateway.SkillCommand{skillCmd("arxiv", "Search arXiv")}); err == nil {
		t.Fatal("want the error of the canceled call")
	}
	// A stop of the gateway is not a refusal of the menu, so no fallback runs.
	if strings.Contains(logs.String(), "cannot set the skill commands") {
		t.Fatal(logs.String())
	}
}

func TestRegisterMenuUsesSkillCommands(t *testing.T) {
	var got []any
	srv := tgServer(t, func(method string, body map[string]any) any {
		if method == "setMyCommands" {
			got = body["commands"].([]any)
		}
		return true
	})
	ch := &Channel{C: client(srv)}
	deps := &gateway.Deps{Log: slog.New(slog.DiscardHandler),
		Skills: func() ([]skills.Skill, []skills.Warning, error) {
			return []skills.Skill{{Name: "arxiv", Description: "Search arXiv"}}, nil, nil
		}}
	ch.registerMenu(ctx, ch.poller(), &gateway.Gateway{Deps: deps})
	if len(got) != len(BotCommands(nil, 0))+1 {
		t.Fatal(got)
	}
	got = nil
	ch.registerMenu(ctx, ch.poller(), &gateway.Gateway{})
	if len(got) != len(BotCommands(nil, 0)) {
		t.Fatal(got)
	}
}

func TestBotCommandsMax(t *testing.T) {
	var cmds []gateway.SkillCommand
	for i := range 50 {
		cmds = append(cmds, skillCmd(fmt.Sprintf("s%02d", i), "x"))
	}
	// 22 built-in commands and 8 skill commands.
	if got := BotCommands(cmds, 30); len(got) != 30 || got[29].Command != "s07" {
		t.Fatalf("%d entries, last %+v", len(got), got[len(got)-1])
	}
	if _, hidden := botMenu(cmds, 30); hidden != 42 {
		t.Fatal(hidden)
	}
	if got := BotCommands(cmds, 0); len(got) != menuMax {
		t.Fatalf("zero max: %d entries", len(got))
	}
	if got := BotCommands(cmds, 100); len(got) != 72 {
		t.Fatalf("max 100: %d entries", len(got))
	}
}

func TestPollerMenuMax(t *testing.T) {
	var got []any
	srv := tgServer(t, func(method string, body map[string]any) any {
		if method == "setMyCommands" {
			got = body["commands"].([]any)
		}
		return true
	})
	var cmds []gateway.SkillCommand
	for i := range 10 {
		cmds = append(cmds, skillCmd(fmt.Sprintf("s%02d", i), "x"))
	}
	if err := (&Poller{C: client(srv), MenuMax: 25}).RegisterCommands(ctx, cmds); err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("%d entries, want 25", len(got))
	}
	ch := &Channel{C: client(srv), Cfg: config.TelegramConfig{MenuMaxCommands: 25}}
	if p := ch.poller(); p.MenuMax != 25 {
		t.Fatalf("poller MenuMax %d", p.MenuMax)
	}
}
