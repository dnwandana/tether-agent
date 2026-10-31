package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestParseModelArgs(t *testing.T) {
	for in, want := range map[string]modelArgs{
		"":                {Action: modelShow},
		"clear":           {Action: modelClear},
		"providers":       {Action: modelProviders},
		"box":             {Action: modelList, Entry: "box", Page: 1},
		"  box  3 ":       {Action: modelList, Entry: "box", Page: 3},
		"box refresh":     {Action: modelRefresh, Entry: "box"},
		"box config":      {Action: modelConfig, Entry: "box"},
		"box use 12 7f3a": {Action: modelUse, Entry: "box", Index: 12, Stamp: "7f3a"},
		"box:vendor/m1":   {Action: modelSet, Entry: "box", Model: "vendor/m1"},
		"box:":            {Action: modelSet, Entry: "box"},
		"clear 2":         {Action: modelList, Entry: "clear", Page: 2},
	} {
		if got, ok := parseModelArgs(in); !ok || got != want {
			t.Errorf("%q: got %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"box 0", "box -1", "box x", "box refresh now", "a b c",
		"zai:glm 4.6", "box:m 2", "box use 0 7f3a", "box use 1", "box use x 7f3a",
		"box use 1 7f3", "box use 1 zzzz", "box:m use 1 7f3a"} {
		if got, ok := parseModelArgs(in); ok {
			t.Errorf("%q: got %+v, want a parse failure", in, got)
		}
	}
}

func TestModelListCommand(t *testing.T) {
	ctx := context.Background()
	c, sent := interactiveCtx(t)
	calls := serveModels(t, c, "box", "vendor/m02", http.StatusOK, modelIDs(20)...)
	run(t, c, "/model box")
	r := lastSent(t, sent)
	if r.Text != "🧠 **box · 20 models**" || !r.Edit || r.Buttons[4][1].Text != "1 / 3" {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/model box 2")
	if r := lastSent(t, sent); r.Buttons[4][1].Text != "2 / 3" {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, c, "/model box 9")
	if r := lastSent(t, sent); r.Buttons[2][1].Text != "3 / 3" {
		t.Fatalf("%+v", r.Buttons)
	}
	if calls.Load() != 1 {
		t.Fatalf("the cache must answer: %d calls", calls.Load())
	}
	run(t, c, "/model box refresh")
	if r := lastSent(t, sent); calls.Load() != 2 || r.Buttons[4][1].Text != "1 / 3" {
		t.Fatalf("%d calls, %+v", calls.Load(), r.Buttons)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "" {
		t.Fatalf("a list sets no model: %q", s.ModelOverride)
	}
}

func TestModelConfigAndSetForms(t *testing.T) {
	ctx := context.Background()
	c, sent := interactiveCtx(t)
	calls := serveModels(t, c, "box", "vendor/m02", http.StatusOK, "vendor/m02")
	run(t, c, "/model box config")
	r := lastSent(t, sent)
	if r.Text != "🧠 **Model: box**\n`vendor/m02`\nThis session uses it from the next message." || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "box" || calls.Load() != 0 {
		t.Fatalf("%q, %d calls", s.ModelOverride, calls.Load())
	}
	run(t, c, "/model box:vendor/x")
	r = lastSent(t, sent)
	if r.Text != "🧠 **Model: box**\n`vendor/x`\nThis session uses it from the next message." || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "box:vendor/x" {
		t.Fatal(s.ModelOverride)
	}
}

func TestModelProvidersForm(t *testing.T) {
	c, sent := modelCtx(t)
	run(t, c, "/model providers")
	if r := lastSent(t, sent); !r.Edit || !strings.HasSuffix(r.Text, "\nTap a provider to see its models.") || len(r.Buttons) != 2 {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/model")
	if r := lastSent(t, sent); r.Edit {
		t.Fatalf("%+v", r)
	}
}

func TestModelUnknownForms(t *testing.T) {
	c, sent := modelCtx(t)
	for _, args := range []string{"zai:glm 4.6", "zai 0", "zai use 1", "a b c"} {
		run(t, c, "/model "+args)
		want := "⚠️ **Unknown form: " + args + "**\nSend /model, /model <provider>, /model <provider>:<model>, or /model clear."
		if r := lastSent(t, sent); r.Text != want {
			t.Fatalf("%s: got:\n%s", args, r.Text)
		}
	}
	run(t, c, "/model my_x 1 2")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ **Unknown form: my\\_x 1 2**\n") {
		t.Fatalf("got:\n%s", r.Text)
	}
	for _, args := range []string{"nope 2", "nope refresh", "nope config", "nope:x"} {
		run(t, c, "/model "+args)
		if r := lastSent(t, sent); r.Text != "⚠️ **Unknown provider: nope**\nThe providers are: openrouter, primary, zai." {
			t.Fatalf("%s: got:\n%s", args, r.Text)
		}
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
}

func TestModelListCommandCLI(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	serveModels(t, c, "box", "b/y", http.StatusOK, "a/x", "b/y")
	run(t, c, "/model box")
	if want := "🧠 **box · 2 models**\n   1) a/x\n   2) b/y  ✓\nSend /model box:<model> to use a model.\n"; out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

// pickList is the list that the fake server of pickCtx sends. A test can change it.
type pickList struct {
	mu     sync.Mutex
	ids    []string
	status int // 0 sends the list; another value is the HTTP status of a failure
}

func (l *pickList) set(ids ...string)   { l.mu.Lock(); l.ids = ids; l.mu.Unlock() }
func (l *pickList) failWith(status int) { l.mu.Lock(); l.status = status; l.mu.Unlock() }

// pickCtx returns an interactive ctx whose entry "primary" gets the ids from a fake
// openai-format server. The cache of the ctx uses the client of the server.
func pickCtx(t *testing.T, ids ...string) (*CommandCtx, *[]Reply, *pickList) {
	t.Helper()
	c, sent := interactiveCtx(t)
	pl := &pickList{ids: ids}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pl.mu.Lock()
		defer pl.mu.Unlock()
		if pl.status != 0 {
			http.Error(w, "failed", pl.status)
			return
		}
		items := make([]map[string]string, 0, len(pl.ids))
		for _, id := range pl.ids {
			items = append(items, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(srv.Close)
	c.Deps.Cfg.Providers["primary"] = config.Provider{Format: "openai", BaseURL: srv.URL, Model: ids[0]}
	c.Deps.Models = provider.NewModelCache(srv.Client())
	return c, sent, pl
}

// otherStamp returns a 4-digit hex stamp that is not s.
func otherStamp(s string) string {
	if s == "0000" {
		return "ffff"
	}
	return "0000"
}

var tenModels = []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10"}

func TestModelUseSetsOverride(t *testing.T) {
	c, sent, _ := pickCtx(t, tenModels...)
	run(t, c, "/model primary use 3 "+stampOf(tenModels))
	r := lastSent(t, sent)
	if want := "🧠 **Model: primary**\n`m3`\nThis session uses it from the next message."; r.Text != want || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if want := [][]Button{{{Text: "↺ Use the default", Data: "/model clear"}}}; !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "primary:m3" {
		t.Fatal(s.ModelOverride)
	}
}

func TestModelUseWrongStamp(t *testing.T) {
	ctx := context.Background()
	c, sent, _ := pickCtx(t, tenModels...)
	run(t, c, "/model primary use 3 "+otherStamp(stampOf(tenModels)))
	r := lastSent(t, sent)
	want := c.modelListReply(ctx, c.Session, "primary", 1, "The list changed. Select the model again.")
	if r.Text != want.Text || !reflect.DeepEqual(r.Buttons, want.Buttons) || !r.Edit {
		t.Fatalf("got %+v\nwant %+v", r, want)
	}
	if !strings.Contains(r.Text, "The list changed. Select the model again.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
}

func TestModelUseOldButtonAfterRefresh(t *testing.T) {
	ctx := context.Background()
	c, sent, pl := pickCtx(t, tenModels...)
	old := stampOf(tenModels)
	run(t, c, "/model primary")
	pl.set("m2", "m1")
	run(t, c, "/model primary refresh")
	run(t, c, "/model primary use 1 "+old)
	if r := lastSent(t, sent); !strings.Contains(r.Text, "The list changed. Select the model again.") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
	run(t, c, "/model primary use 1 "+stampOf([]string{"m2", "m1"}))
	if s, _ := c.Deps.Store.Get(ctx, c.Session.ID); s.ModelOverride != "primary:m2" {
		t.Fatal(s.ModelOverride)
	}
}

func TestModelUseOutOfRange(t *testing.T) {
	c, sent, _ := pickCtx(t, tenModels...)
	stamp := stampOf(tenModels)
	for _, i := range []string{"11", "999"} {
		run(t, c, "/model primary use "+i+" "+stamp)
		r := lastSent(t, sent)
		if want := "⚠️ **No model at position " + i + "**\nSend /model primary to see the list."; r.Text != want || !r.Edit {
			t.Fatalf("%+v", r)
		}
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
	run(t, c, "/model nope use 1 "+stamp)
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ **Unknown provider: nope**") {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestModelUseListFailure(t *testing.T) {
	c, sent, pl := pickCtx(t, tenModels...)
	pl.failWith(http.StatusInternalServerError)
	run(t, c, "/model primary use 1 "+stampOf(tenModels))
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ **No model list from primary**") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if s, _ := c.Deps.Store.Get(context.Background(), c.Session.ID); s.ModelOverride != "" {
		t.Fatal(s.ModelOverride)
	}
}

// TestModelUseActionFitsCallbackData checks the longest model button: position 999, an entry
// name of modelEntryMaxBytes, and a Telegram user ID of 16 digits. The prefix copies the
// "cmd:{user}:" form of encodeCmd in internal/telegram/cmdbuttons.go, and 64 is its
// maxCallbackData.
func TestModelUseActionFitsCallbackData(t *testing.T) {
	entry := strings.Repeat("e", modelEntryMaxBytes)
	action := modelUseAction(entry, 999, "7f3a")
	data := "cmd:" + "1234567890123456" + ":" + action
	if action != "/model "+entry+" use 999 7f3a" || len(action) != 40 || len(data) > 64 {
		t.Fatalf("%q has %d bytes", data, len(data))
	}
}
