package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// serveModels adds the provider entry to c, with an httptest server that serves ids at
// /models, or the status code when it is not 200. The config model of the entry is model.
// It returns the count of list calls.
func serveModels(t *testing.T, c *CommandCtx, entry, model string, code int, ids ...string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		items := make([]map[string]string, len(ids))
		for i, id := range ids {
			items[i] = map[string]string{"id": id}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(srv.Close)
	c.Deps.Cfg.Providers[entry] = config.Provider{Format: "openai", BaseURL: srv.URL, Model: model}
	c.Deps.Providers[entry] = &fakeProv{}
	c.Deps.Models = provider.NewModelCache(srv.Client())
	return &calls
}

// modelIDs returns vendor/m01 to vendor/m<n>.
func modelIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("vendor/m%02d", i+1)
	}
	return ids
}

// stampOf returns the stamp of a list with one group of ids.
func stampOf(ids []string) string {
	return provider.ModelList{Groups: []provider.ModelGroup{{Models: ids}}}.Stamp()
}

var modelLastRow = func(entry string) []Button {
	return []Button{{Text: "↩ Providers", Data: "/model providers"}, {Text: "↻ Refresh", Data: "/model " + entry + " refresh"}}
}

func TestModelUseAction(t *testing.T) {
	if got := modelUseAction("primary", 12, "7f3a"); got != "/model primary use 12 7f3a" {
		t.Fatal(got)
	}
}

func TestModelListPages(t *testing.T) {
	ctx := context.Background()
	c, _ := interactiveCtx(t)
	ids := modelIDs(20)
	serveModels(t, c, "box", "vendor/m02", http.StatusOK, ids...)
	s, err := c.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := stampOf(ids)
	use := func(i int, label string) Button { return Button{Text: label, Data: modelUseAction("box", i, st)} }
	dot := Button{Text: noAction, Data: noAction}
	r := c.modelListReply(ctx, s, "box", 1, "")
	if r.Text != "🧠 **box · 20 models**" || !r.Edit {
		t.Fatalf("%+v", r)
	}
	want := [][]Button{
		{use(1, "m01"), use(2, "✓ m02")},
		{use(3, "m03"), use(4, "m04")},
		{use(5, "m05"), use(6, "m06")},
		{use(7, "m07"), use(8, "m08")},
		{dot, {Text: "1 / 3", Data: noAction}, {Text: "Next ▶", Data: "/model box 2"}},
		modelLastRow("box"),
	}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	want = [][]Button{
		{use(17, "m17"), use(18, "m18")},
		{use(19, "m19"), use(20, "m20")},
		{{Text: "◀ Prev", Data: "/model box 2"}, {Text: "3 / 3", Data: noAction}, dot},
		modelLastRow("box"),
	}
	// A page past the end shows the last page.
	for _, page := range []int{3, 9} {
		if r := c.modelListReply(ctx, s, "box", page, ""); !reflect.DeepEqual(r.Buttons, want) {
			t.Fatalf("page %d: %+v", page, r.Buttons)
		}
	}
	// The override of this entry gets the ✓. The override of another entry does not.
	s.ModelOverride = "box:vendor/m05"
	if r := c.modelListReply(ctx, s, "box", 1, ""); r.Buttons[2][0].Text != "✓ m05" || r.Buttons[0][1].Text != "m02" {
		t.Fatalf("%+v", r.Buttons)
	}
	s.ModelOverride = "other:vendor/m05"
	if r := c.modelListReply(ctx, s, "box", 1, ""); r.Buttons[0][1].Text != "✓ m02" {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestModelListOnePage(t *testing.T) {
	ctx := context.Background()
	c, _ := interactiveCtx(t)
	long := "vendor/" + strings.Repeat("a", 40)
	ids := []string{"a/x", "b/y", long}
	serveModels(t, c, "primary", "", http.StatusOK, ids...)
	pc := c.Deps.Cfg.Providers["primary"]
	pc.Preset = "openrouter"
	c.Deps.Cfg.Providers["primary"] = pc
	s, _ := c.current(ctx)
	st := stampOf(ids)
	r := c.modelListReply(ctx, s, "primary", 1, "The list changed. Select the model again.")
	if r.Text != "🧠 **primary (OpenRouter) · 3 models**\nThe list changed. Select the model again." {
		t.Fatalf("got:\n%s", r.Text)
	}
	// A list with one page has no navigation row.
	want := [][]Button{
		{{Text: "x", Data: modelUseAction("primary", 1, st)}, {Text: "y", Data: modelUseAction("primary", 2, st)}},
		{{Text: strings.Repeat("a", 31) + "…", Data: modelUseAction("primary", 3, st)}},
		modelLastRow("primary"),
	}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestModelGroupLine(t *testing.T) {
	two := provider.ModelList{ToolsOnly: true, Groups: []provider.ModelGroup{
		{Title: "Most used this week", Models: modelIDs(10)},
		{Title: "Best for agents", Models: modelIDs(15)[10:]},
	}}
	if got := modelGroupLine(two); got != "Most used this week (1 to 10), then best for agents (11 to 15). Only models with tool support." {
		t.Fatal(got)
	}
	one := provider.ModelList{ToolsOnly: true, Groups: []provider.ModelGroup{{Models: modelIDs(3)}}}
	if got := modelGroupLine(one); got != "Only models with tool support." {
		t.Fatal(got)
	}
	one.ToolsOnly = false
	if got := modelGroupLine(one); got != "" {
		t.Fatal(got)
	}
}

func TestModelListCLI(t *testing.T) {
	ctx := context.Background()
	c, _ := cmdCtx(t, "cli:t")
	serveModels(t, c, "box", "b/y", http.StatusOK, "a/x", "b/y", "glm_4.6")
	s, _ := c.current(ctx)
	r := c.modelListReply(ctx, s, "box", 1, "")
	want := "🧠 **box · 3 models**\n   1) a/x\n   2) b/y  ✓\n   3) glm\\_4.6\nSend /model box:<model> to use a model."
	if r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s\n%+v", r.Text, r.Buttons)
	}
}

func TestModelListLongEntry(t *testing.T) {
	ctx := context.Background()
	c, _ := interactiveCtx(t)
	// 7 runes of 3 bytes: 21 bytes. The rule counts bytes.
	long := "模型模型模型模"
	serveModels(t, c, long, "", http.StatusOK, "a/x", "b/y")
	s, _ := c.current(ctx)
	r := c.modelListReply(ctx, s, long, 1, "")
	want := "🧠 **" + long + " · 2 models**\n   1) a/x\n   2) b/y\nSend /model " + long + ":<model> to use a model."
	if r.Text != want || !reflect.DeepEqual(r.Buttons, [][]Button{modelLastRow(long)}) {
		t.Fatalf("got:\n%s\n%+v", r.Text, r.Buttons)
	}
	// A name of 20 bytes gets model buttons.
	ok := "abcdefghijklmnopqrst"
	serveModels(t, c, ok, "", http.StatusOK, "a/x", "b/y")
	if r := c.modelListReply(ctx, s, ok, 1, ""); len(r.Buttons) != 2 || r.Buttons[0][0].Data != modelUseAction(ok, 1, stampOf([]string{"a/x", "b/y"})) {
		t.Fatalf("%+v", r.Buttons)
	}
}

// TestModelListVeryLongEntry checks an entry name whose refresh and config actions do not fit
// in the callback data: the reply has no button for them and gives the typed command.
func TestModelListVeryLongEntry(t *testing.T) {
	ctx := context.Background()
	c, _ := interactiveCtx(t)
	long := strings.Repeat("e", modelActionMaxBytes)
	serveModels(t, c, long, "m1", http.StatusOK, "a/x")
	s, _ := c.current(ctx)
	providers := [][]Button{{{Text: "↩ Providers", Data: "/model providers"}}}
	if r := c.modelListReply(ctx, s, long, 1, ""); !reflect.DeepEqual(r.Buttons, providers) {
		t.Fatalf("%+v", r.Buttons)
	}
	serveModels(t, c, long, "m1", http.StatusNotFound)
	r := c.modelListReply(ctx, s, long, 1, "")
	if !strings.Contains(r.Text, "Send /model "+long+" config to use the model in config.yaml.") ||
		!reflect.DeepEqual(r.Buttons, providers) {
		t.Fatalf("got:\n%s\n%+v", r.Text, r.Buttons)
	}
	for _, rows := range r.Buttons {
		for _, b := range rows {
			if len("cmd:1234567890123456:"+b.Data) > 64 {
				t.Fatalf("%q is too long", b.Data)
			}
		}
	}
}

func TestModelListFailure(t *testing.T) {
	ctx := context.Background()
	c, _ := interactiveCtx(t)
	calls := serveModels(t, c, "minimax", "MiniMax-M3", http.StatusNotFound)
	s, _ := c.current(ctx)
	r := c.modelListReply(ctx, s, "minimax", 1, "")
	want := "⚠️ **No model list from minimax**\nHTTP 404\nTap the button to use the model in config.yaml. Send /model minimax:<model> to use another model."
	if r.Text != want || !r.Edit {
		t.Fatalf("got:\n%s", r.Text)
	}
	wantB := [][]Button{{{Text: "Use MiniMax-M3", Data: "/model minimax config"}}, {{Text: "↩ Providers", Data: "/model providers"}}}
	if !reflect.DeepEqual(r.Buttons, wantB) {
		t.Fatalf("%+v", r.Buttons)
	}
	// A failure is not cached, so the next call asks the provider again.
	c.modelListReply(ctx, s, "minimax", 1, "")
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	// An entry with Markdown characters is escaped. No config model gives no Use button.
	serveModels(t, c, "my_box", "", http.StatusNotFound)
	r = c.modelListReply(ctx, s, "my_box", 1, "")
	if !strings.HasPrefix(r.Text, "⚠️ **No model list from my\\_box**\nHTTP 404\n") || !strings.Contains(r.Text, "Send /model my\\_box:<model>") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if !reflect.DeepEqual(r.Buttons, [][]Button{{{Text: "↩ Providers", Data: "/model providers"}}}) {
		t.Fatalf("%+v", r.Buttons)
	}
	cli, _ := cmdCtx(t, "cli:t")
	serveModels(t, cli, "minimax", "MiniMax-M3", http.StatusNotFound)
	cs, _ := cli.current(ctx)
	want = "⚠️ **No model list from minimax**\nHTTP 404\nSend /model minimax config to use the model in config.yaml. Send /model minimax:<model> to use another model."
	if r := cli.modelListReply(ctx, cs, "minimax", 1, ""); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
}

// TestModelListFailureNoModel checks a failed list call of an entry with no model in
// config.yaml: the reply has no sentence about config.yaml and no Use button.
func TestModelListFailureNoModel(t *testing.T) {
	ctx := context.Background()
	want := "⚠️ **No model list from box**\nHTTP 500\nSend /model box:<model> to use a model."
	c, _ := interactiveCtx(t)
	serveModels(t, c, "box", "", http.StatusInternalServerError)
	s, _ := c.current(ctx)
	r := c.modelListReply(ctx, s, "box", 1, "")
	if r.Text != want || !r.Edit || strings.Contains(r.Text, "config.yaml") {
		t.Fatalf("got:\n%s", r.Text)
	}
	if wantB := [][]Button{{{Text: "↩ Providers", Data: "/model providers"}}}; !reflect.DeepEqual(r.Buttons, wantB) {
		t.Fatalf("%+v", r.Buttons)
	}
	cli, out := cmdCtx(t, "cli:t")
	serveModels(t, cli, "box", "", http.StatusInternalServerError)
	run(t, cli, "/model box")
	if out.String() != want+"\n" {
		t.Fatalf("got:\n%s", out)
	}
}
