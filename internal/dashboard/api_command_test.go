package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// cmdProv answers "ok". When hold is not nil, each answer waits until hold closes.
type cmdProv struct{ hold chan struct{} }

func (p *cmdProv) Name() string  { return "primary" }
func (p *cmdProv) Model() string { return "fake-model" }
func (p *cmdProv) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if p.hold != nil {
			select {
			case <-p.hold:
			case <-ctx.Done():
				out <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		out <- provider.Event{Type: provider.EventText, Delta: "ok"}
		out <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	}()
	return out, nil
}

// cmdHarness is a running dashboard channel with a real gateway and the command routes on
// a test server. The routes have no auth middleware.
type cmdHarness struct {
	ch       *Channel
	gw       *gateway.Gateway
	srv      *httptest.Server
	stopRecv context.CancelFunc
}

func newCmdHarness(t *testing.T, prov provider.Provider) *cmdHarness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := sessions.New(d)
	cfg := config.Default()
	cfg.Dashboard = config.DashboardConfig{Enabled: true, Listen: "127.0.0.1:0", SessionHours: 24, MaxUploadMB: 20}
	log := slog.New(slog.DiscardHandler)
	same := func(s string) string { return s }
	ch, err := NewChannel(Deps{Cfg: cfg.Dashboard, DB: d, Sessions: st, Redact: same,
		Partial: func(string) int { return 0 }, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Prov: prov, Providers: map[string]provider.Provider{"primary": prov},
		Tools: tools.NewRegistry(), Store: st, Cfg: &cfg, SpillDir: t.TempDir()}
	gw := &gateway.Gateway{Deps: &gateway.Deps{Cfg: &cfg, Store: st, Agent: ag, Providers: ag.Providers, Log: log},
		Channels: map[string]gateway.Channel{ChannelName: ch}, Log: log}
	workCtx, stopWork := context.WithCancel(context.Background())
	recvCtx, stopRecv := context.WithCancel(workCtx)
	ch.SetTurnContext(workCtx)
	ran := make(chan struct{})
	go func() { defer close(ran); _ = ch.Run(recvCtx, gw) }()
	cmdWait(t, func() bool { return ch.Gateway() != nil })
	mux := http.NewServeMux()
	NewCommandAPI(ch, same).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		stopWork()
		<-ran
		_ = ch.Shutdown(context.Background())
	})
	return &cmdHarness{ch: ch, gw: gw, srv: srv, stopRecv: stopRecv}
}

func cmdWait(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the condition did not come true in 5 s")
		}
	}
}

func (h *cmdHarness) post(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(h.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// cmdEvents opens the event stream of h and sends each event as {name, data}. It returns
// after the hello event.
func cmdEvents(t *testing.T, h *Hub) <-chan [2]string {
	t.Helper()
	srv := httptest.NewServer(h.Handler(func() Snapshot { return Snapshot{} }))
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close(); srv.Close() })
	out := make(chan [2]string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		name := ""
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				out <- [2]string{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		checkScan(t, sc)
	}()
	cmdEvent(t, out, "hello", "")
	return out
}

func cmdEvent(t *testing.T, ev <-chan [2]string, name, contains string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-ev:
			if e[0] == name && strings.Contains(e[1], contains) {
				return e[1]
			}
		case <-timeout:
			t.Fatalf("no %s event with %q", name, contains)
		}
	}
}

func TestCommandRouteFirstReply(t *testing.T) {
	gateway.Register(gateway.Command{Name: "dashtest", WhileBusy: true, Run: func(_ context.Context, c *gateway.CommandCtx, _ string) error {
		c.Send(gateway.Reply{Text: "⏸ Paused **nightly-backup**.",
			Buttons: [][]gateway.Button{{{Text: "▶ Resume", Data: "/jobs resume 12"}}}})
		return nil
	}})
	t.Cleanup(func() { gateway.Unregister("dashtest") })
	h := newCmdHarness(t, &cmdProv{})
	ev := cmdEvents(t, h.ch.Hub())
	code, got := h.post(t, "/api/command", `{"text": "/dashtest", "button": 0}`)
	id, _ := got["request_id"].(string)
	if code != http.StatusOK || len(id) != 16 || got["text"] != "⏸ Paused **nightly-backup**." || got["edit"] != false {
		t.Fatal(code, got)
	}
	if b, _ := json.Marshal(got["buttons"]); string(b) != `[[{"data":"/jobs resume 12","text":"▶ Resume"}]]` {
		t.Fatal(string(b))
	}
	cmdEvent(t, ev, "ack", `"request_id":"`+id+`"`)
}

func TestCommandRouteLateReply(t *testing.T) {
	release := make(chan struct{})
	gateway.Register(gateway.Command{Name: "dashslow", WhileBusy: true, Run: func(_ context.Context, c *gateway.CommandCtx, _ string) error {
		<-release
		c.Reply("🗜 Done.")
		return nil
	}})
	t.Cleanup(func() { gateway.Unregister("dashslow") })
	old := firstReplyWait
	firstReplyWait = 50 * time.Millisecond
	t.Cleanup(func() { firstReplyWait = old })
	h := newCmdHarness(t, &cmdProv{})
	ev := cmdEvents(t, h.ch.Hub())
	code, got := h.post(t, "/api/command", `{"text": "/dashslow"}`)
	id, _ := got["request_id"].(string)
	if code != http.StatusAccepted || len(id) != 16 || len(got) != 1 {
		close(release)
		t.Fatal(code, got)
	}
	close(release)
	if data := cmdEvent(t, ev, "ack", `"request_id":"`+id+`"`); !strings.Contains(data, "🗜 Done.") {
		t.Fatal(data)
	}
}

func TestCommandRouteOnlyBuiltIn(t *testing.T) {
	h := newCmdHarness(t, &cmdProv{})
	for _, text := range []string{"hello", "/no-such-skill", "/etc/hosts", ""} {
		code, got := h.post(t, "/api/command", `{"text": `+strconv.Quote(text)+`}`)
		if code != http.StatusBadRequest || got["error"] != "This route takes only a built-in command. Send other text to /api/messages." {
			t.Fatal(text, code, got)
		}
	}
	// /approve is not a gateway command. It runs on the open cards before Submit.
	want, ok := h.ch.Cards().Local("/approve")
	code, got := h.post(t, "/api/command", `{"text": "/approve"}`)
	if !ok || code != http.StatusOK || got["text"] != want {
		t.Fatal(code, got, want)
	}
}

func TestCommandRouteKeepsBusyRule(t *testing.T) {
	prov := &cmdProv{hold: make(chan struct{})}
	h := newCmdHarness(t, prov)
	defer close(prov.hold)
	h.gw.Submit(h.ch.WorkCtx(), h.ch.Inbound("hi"), func() gateway.Renderer { return h.ch.NewRenderer("") }, nil)
	cmdWait(t, func() bool { return slices.Contains(h.gw.BusyKeys(), SessionKey) })
	code, got := h.post(t, "/api/command", `{"text": "/rename Plans"}`)
	if code != http.StatusOK || got["text"] != fmt.Sprintf(gateway.BusyCommandReply, "rename") {
		t.Fatal(code, got)
	}
	if b, _ := json.Marshal(got["buttons"]); string(b) != `[[{"data":"/stop","text":"⏹ Stop the task"}]]` {
		t.Fatal(string(b))
	}
}

func TestCommandRouteStopping(t *testing.T) {
	h := newCmdHarness(t, &cmdProv{})
	h.stopRecv()
	cmdWait(t, h.ch.Stopping)
	code, got := h.post(t, "/api/command", `{"text": "/help"}`)
	if code != http.StatusServiceUnavailable || got["error"] != "The gateway is stopping." {
		t.Fatal(code, got)
	}
}

// cmdCard opens an approval card in a goroutine. It returns the card id and the answer.
func cmdCard(t *testing.T, h *cmdHarness, command string) (string, <-chan security.Scope) {
	t.Helper()
	before := len(h.ch.Cards().Open())
	res := make(chan security.Scope, 1)
	go func() {
		s, _ := h.ch.Cards().PromptApproval(h.ch.WorkCtx(), security.ApprovalRequest{SessionKey: SessionKey,
			Command: command, Description: "delete files", Workdir: "/tmp", Tool: "terminal"})
		res <- s
	}()
	cmdWait(t, func() bool { return len(h.ch.Cards().Open()) > before })
	open := h.ch.Cards().Open()
	return open[len(open)-1].ID, res
}

func TestCardRouteAnswer(t *testing.T) {
	h := newCmdHarness(t, &cmdProv{})
	id, res := cmdCard(t, h, "rm -rf build")
	if len(id) != 32 {
		t.Fatalf("a card id is 16 random bytes in hex: %q", id)
	}
	// A turn in the drain can wait for a card, so the card route works after recvCtx ends.
	h.stopRecv()
	cmdWait(t, h.ch.Stopping)
	if code, got := h.post(t, "/api/cards/"+id, `{"answer": "once"}`); code != http.StatusOK {
		t.Fatal(code, got)
	}
	if s := <-res; s != security.ScopeOnce {
		t.Fatal(s)
	}
	code, got := h.post(t, "/api/cards/"+id, `{"answer": "deny"}`)
	if code != http.StatusConflict || got["error"] != "This card is already answered." {
		t.Fatal(code, got)
	}
}

func TestCardRouteBulk(t *testing.T) {
	h := newCmdHarness(t, &cmdProv{})
	a, ra := cmdCard(t, h, "rm -rf a")
	b, rb := cmdCard(t, h, "rm -rf b")
	c, _ := cmdCard(t, h, "rm -rf c")
	code, got := h.post(t, "/api/cards/bulk", `{"answer": "deny", "ids": ["`+a+`", "`+b+`"]}`)
	if code != http.StatusOK || got["answered"] != float64(2) {
		t.Fatal(code, got)
	}
	if sa, sb := <-ra, <-rb; sa != security.ScopeDeny || sb != security.ScopeDeny {
		t.Fatal(sa, sb)
	}
	if open := h.ch.Cards().Open(); len(open) != 1 || open[0].ID != c {
		t.Fatalf("only the posted ids get the answer: %+v", open)
	}
	code, got = h.post(t, "/api/cards/bulk", `{"answer": "always", "ids": ["`+c+`"]}`)
	if code != http.StatusBadRequest || got["error"] != "Bulk answers are deny or once." {
		t.Fatal(code, got)
	}
}
