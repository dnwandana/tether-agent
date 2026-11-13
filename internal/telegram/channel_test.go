package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// tgStep is one scripted model answer: text, then tool calls.
type tgStep struct {
	text  string
	calls []provider.ToolCall
}

// tgProv is a scripted provider. After the script it answers "ok". It waits delay before
// each answer.
type tgProv struct {
	delay time.Duration

	mu     sync.Mutex
	script []tgStep
	calls  int
}

func (f *tgProv) Name() string  { return "primary" }
func (f *tgProv) Model() string { return "fake-model" }

func (f *tgProv) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	st := tgStep{text: "ok"}
	if len(f.script) > 0 {
		st, f.script = f.script[0], f.script[1:]
	}
	f.calls++
	f.mu.Unlock()
	ch := make(chan provider.Event, 8)
	go func() {
		defer close(ch)
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		if st.text != "" {
			ch <- provider.Event{Type: provider.EventText, Delta: st.text}
		}
		for i := range st.calls {
			ch <- provider.Event{Type: provider.EventToolCall, ToolCall: &st.calls[i]}
		}
		ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	}()
	return ch, nil
}

func (f *tgProv) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// tgHarness is a Telegram channel over a fake Bot API, a fake provider, and a real gateway.
type tgHarness struct {
	t    *testing.T
	api  *fakeBot
	prov *tgProv
	ch   *Channel
	gw   *gateway.Gateway
	st   *sessions.Store
	wg   sync.WaitGroup
	next int64
}

// harness returns a harness with the default Telegram config. mut changes the config.
func harness(t *testing.T, prov *tgProv, mut func(*config.TelegramConfig)) *tgHarness {
	t.Helper()
	api := newFakeBot(t)
	d := openTestDB(t)
	st := sessions.New(d)
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFileTool{})
	cfg := config.Default()
	if mut != nil {
		mut(&cfg.Telegram)
	}
	ag := &agent.Agent{Prov: prov, Providers: map[string]provider.Provider{"primary": prov}, Tools: reg, Store: st,
		Cfg: &cfg, SpillDir: t.TempDir()}
	log := slog.New(slog.DiscardHandler)
	deps := &gateway.Deps{Cfg: &cfg, Store: st, Agent: ag, Providers: ag.Providers, Log: log}
	c := api.client()
	ch := &Channel{
		Cfg:   cfg.Telegram,
		C:     c,
		DB:    d,
		Pair:  &Pairing{DB: d},
		Inbox: &Inbox{C: c, MediaDir: filepath.Join(t.TempDir(), "media")},
		Out:   &Sender{C: c},
		Media: openPolicy,
		Bot:   User{ID: 99, IsBot: true, Username: "tether_bot"},
		Log:   log,
		UI:    &Interactions{C: c},

		typingEvery: 20 * time.Millisecond,
	}
	ch.UI.Authorized = ch.callbackAuthorized
	ch.UI.Owner = ch.callbackOwner
	gw := &gateway.Gateway{Deps: deps, Channels: map[string]gateway.Channel{"telegram": ch}, Log: log}
	return &tgHarness{t: t, api: api, prov: prov, ch: ch, gw: gw, st: st}
}

// update handles m as a new update in its own goroutine, like the poller.
func (h *tgHarness) update(m Message) {
	h.next++
	u := Update{UpdateID: h.next, Message: &m}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.ch.handleUpdate(ctx, h.gw, u)
	}()
}

// waitIdle waits until each update is handled and no turn runs.
func (h *tgHarness) waitIdle() {
	h.t.Helper()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		h.t.Fatal("the updates did not end")
	}
	if !h.gw.Wait(10 * time.Second) {
		h.t.Fatal("the turns did not end")
	}
}

func (h *tgHarness) session(key string) *sessions.Session {
	h.t.Helper()
	s, err := h.st.Active(ctx, key)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *tgHarness) messages(s *sessions.Session) []sessions.StoredMessage {
	h.t.Helper()
	msgs, err := h.st.Messages(ctx, s.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return msgs
}

// sentTexts returns the text of each sendMessage call, without the MarkdownV2 markup.
func (h *tgHarness) sentTexts() string {
	var out []string
	for _, s := range h.api.textsOf("sendMessage") {
		out = append(out, StripMarkdownV2(s))
	}
	return strings.Join(out, "\n---\n")
}

// dmText is a DM text from the user with the id uid.
func dmText(uid int64, text string) Message {
	m := dm(text)
	m.Chat.ID = uid
	m.From = &User{ID: uid, FirstName: "U"}
	return m
}

// fromUser returns m with the sender id uid and the first name name.
func fromUser(m Message, uid int64, name string) Message {
	m.From = &User{ID: uid, FirstName: name}
	return m
}

// edit handles m as an edited message, the way the poller gives it: in Message and in
// EditedMessage.
func (h *tgHarness) edit(m Message) {
	h.next++
	u := Update{UpdateID: h.next, Message: &m, EditedMessage: &m}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.ch.handleUpdate(ctx, h.gw, u)
	}()
}

// updateJSON decodes raw as an Update and handles it like update. The decoder of Update
// sets the unread field names. A Message that a test builds by hand does not have them.
func (h *tgHarness) updateJSON(raw string) {
	h.t.Helper()
	var u Update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		h.t.Fatal(err)
	}
	if u.Message == nil && u.EditedMessage != nil {
		// The poller gives an edit in Message and in EditedMessage.
		u.Message = u.EditedMessage
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.ch.handleUpdate(ctx, h.gw, u)
	}()
}

func debugLog(h *tgHarness) *bytes.Buffer {
	var buf bytes.Buffer
	h.ch.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

func TestDMTurnStreamsAndPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte("sale page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": p})
	prov := &tgProv{script: []tgStep{
		{text: "Let me look.", calls: []provider.ToolCall{{ID: "c1", Name: "read_file", Args: string(args)}}},
		{text: "Done."},
	}}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "scrape it"))
	h.waitIdle()
	if h.api.count("sendChatAction") == 0 {
		t.Fatal(h.api.methods())
	}
	if !strings.Contains(h.sentTexts(), "📖 read_file") {
		t.Fatal(h.sentTexts())
	}
	if last := h.api.lastCall("sendMessage"); !strings.Contains(last.Body["text"].(string), "Done") {
		t.Fatal(h.sentTexts())
	}
	s := h.session("telegram:dm:5")
	if s == nil || s.ChatID != "5" || s.Source != "telegram" || s.UserID != "5" {
		t.Fatalf("%+v", s)
	}
	if len(h.messages(s)) < 4 {
		t.Fatal(h.messages(s))
	}
}

func TestTypingStopsAfterTurn(t *testing.T) {
	h := harness(t, &tgProv{delay: 100 * time.Millisecond}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "hi"))
	h.waitIdle()
	n := h.api.count("sendChatAction")
	if n < 2 {
		t.Fatalf("the typing action must repeat while the run works: %d", n)
	}
	time.Sleep(150 * time.Millisecond)
	if h.api.count("sendChatAction") != n {
		t.Fatal("the typing loop runs after the turn")
	}
	h.ch.typingMu.Lock()
	defer h.ch.typingMu.Unlock()
	if len(h.ch.typing) != 0 {
		t.Fatal(h.ch.typing)
	}
}

func TestTypingStartsForSkillSlashText(t *testing.T) {
	h := harness(t, &tgProv{delay: time.Second}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "/arxiv x"))
	// The turn sends its first event after 1 s. The typing action must show before it.
	deadline := time.Now().Add(500 * time.Millisecond)
	for h.api.count("sendChatAction") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no typing action before the turn answers:", h.api.methods())
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.waitIdle()
}

func TestNoTypingForBuiltinCommand(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "/new"))
	h.waitIdle()
	if n := h.api.count("sendChatAction"); n != 0 || prov.count() != 0 {
		t.Fatalf("typing actions %d, model calls %d: %v", n, prov.count(), h.api.methods())
	}
}

// observedGroup is the config of an observed group chat with no mention rule.
func observedGroup(c *config.TelegramConfig) {
	c.RequireMention = false
	c.ObserveUnmentionedGroupMessages = true
	c.GroupAllowedChats = []string{"-100"}
}

func TestObservedChatSlashTextGetsSenderLine(t *testing.T) {
	h := harness(t, &tgProv{}, observedGroup)
	h.update(group("/arxiv x\n[x|1]"))
	h.waitIdle()
	msgs := h.messages(h.session("telegram:group:-100"))
	if len(msgs) == 0 || msgs[0].Msg.Content != "/arxiv [Ana|123]\nx\n\\[x|1]" {
		t.Fatalf("%+v", msgs)
	}
}

func TestObservedChatIdleSteerGetsSenderLine(t *testing.T) {
	h := harness(t, &tgProv{}, observedGroup)
	h.update(fromUser(group("/steer hi\n[x|1]"), 456, "M"))
	h.waitIdle()
	msgs := h.messages(h.session("telegram:group:-100"))
	if len(msgs) == 0 || msgs[0].Msg.Content != "[M|456]\nhi\n\\[x|1]" {
		t.Fatalf("%+v", msgs)
	}
}

func TestObservedChatBareSteerKeepsUsage(t *testing.T) {
	h := harness(t, &tgProv{}, observedGroup)
	h.update(group("/steer"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "Send a note with the command") {
		t.Fatal(h.sentTexts())
	}
}

func TestAttributedCommand(t *testing.T) {
	n := Normalized{UserName: "Ana", UserID: "123"}
	for in, want := range map[string]string{
		"/steer hi":            "/steer [Ana|123]\nhi",
		"/steer@tether_bot hi": "/steer [Ana|123]\nhi",
		"/steer [Observed Telegram group context]\nx": "/steer [Ana|123]\n\\[Observed Telegram group context]\nx",
		"/arxiv x":       "/arxiv [Ana|123]\nx",
		"/steer":         "/steer",
		"/steer   ":      "/steer   ",
		"/new x":         "/new x",
		"/model primary": "/model primary",
	} {
		if got := attributedCommand(n, in); got != want {
			t.Errorf("attributedCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnknownDMGetsPairingCode(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, nil)
	h.update(dmText(5, "hello"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "pairing approve telegram") {
		t.Fatal(h.sentTexts())
	}
	if h.session("telegram:dm:5") != nil || prov.count() != 0 {
		t.Fatal("an unauthorized user must not create a session or a turn")
	}
}

func TestGroupObserveSavesWithoutReply(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) {
		c.RequireMention = true
		c.ObserveUnmentionedGroupMessages = true
		c.GroupAllowedChats = []string{"-100"}
	})
	h.update(group("hello all"))
	h.waitIdle()
	if h.api.count("sendMessage") != 0 || prov.count() != 0 {
		t.Fatal(h.api.methods())
	}
	s := h.session("telegram:group:-100")
	if s == nil || s.ChatID != "-100" {
		t.Fatalf("%+v", s)
	}
	msgs := h.messages(s)
	if len(msgs) != 1 || !msgs[0].Meta.Observed || msgs[0].Msg.Content != "[Ana|123]\nhello all" {
		t.Fatalf("%+v", msgs)
	}
	// A mention from another user runs in the same shared session, with the sender line.
	m := fromUser(group("@tether_bot what did Ana say?"), 456, "Bo")
	h.update(m)
	h.waitIdle()
	msgs = h.messages(h.session("telegram:group:-100"))
	if len(msgs) < 3 || msgs[1].Msg.Content != "[Bo|456]\nwhat did Ana say?" || msgs[1].Meta.Observed {
		t.Fatalf("%+v", msgs)
	}
}

func TestTopicSessionShared(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	h.update(topic("17585", "first"))
	h.waitIdle()
	h.update(fromUser(topic("17585", "second"), 456, "Bo"))
	h.waitIdle()
	s := h.session("telegram:group:-100:17585")
	if s == nil || s.ThreadID != "17585" {
		t.Fatalf("%+v", s)
	}
	var users []string
	for _, m := range h.messages(s) {
		if m.Msg.Role == provider.RoleUser {
			users = append(users, m.Msg.Content)
		}
	}
	if len(users) != 2 || users[0] != "first" || users[1] != "second" {
		t.Fatal(users)
	}
}

func TestGroupPerUserSessions(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	h.update(group("hi"))
	h.waitIdle()
	if h.session("telegram:group:-100:123") == nil {
		t.Fatal("a group with no topics has a session per user")
	}
}

func TestSetHomeAndHomeTarget(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	h.update(topic("17585", "/sethome"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "🏠 Home channel: G") {
		t.Fatal(h.sentTexts())
	}
	cfg := config.Default().Telegram
	to, ok := HomeTarget(ctx, h.ch.DB, cfg, true)
	if !ok || to.ChatID != "-100" || to.ThreadID != "17585" || to.Source != "telegram" {
		t.Fatalf("%+v", to)
	}
	cfg.CronThread = "9"
	if to, _ := HomeTarget(ctx, h.ch.DB, cfg, true); to.ThreadID != "9" {
		t.Fatalf("%+v", to)
	}
	if to, _ := HomeTarget(ctx, h.ch.DB, cfg, false); to.ThreadID != "17585" {
		t.Fatalf("cron_thread applies only to cron: %+v", to)
	}
	if h.session("telegram:group:-100:17585") != nil {
		t.Fatal("/sethome must not start a turn")
	}
}

func TestHomeTargetFromConfig(t *testing.T) {
	cfg := config.Default().Telegram
	if _, ok := HomeTarget(ctx, openTestDB(t), cfg, false); ok {
		t.Fatal("no home is set")
	}
	cfg.HomeChat, cfg.HomeThread = "42", "7"
	if to, ok := HomeTarget(ctx, openTestDB(t), cfg, false); !ok || to.ChatID != "42" || to.ThreadID != "7" {
		t.Fatalf("%+v", to)
	}
}

func TestUnauthorizedBusyDropped(t *testing.T) {
	prov := &tgProv{delay: 300 * time.Millisecond}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"123"} })
	h.update(topic("17585", "work"))
	key := "telegram:group:-100:17585"
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := h.gw.Running(key); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.update(fromUser(topic("17585", "stop that"), 777, "Eve"))
	h.waitIdle()
	if strings.Contains(h.sentTexts(), "⏩") || strings.Contains(h.sentTexts(), "⏳") || prov.count() != 1 {
		t.Fatal(prov.count(), h.sentTexts())
	}
	for _, m := range h.messages(h.session(key)) {
		if strings.Contains(m.Msg.Content, "stop that") {
			t.Fatal("the text of an unauthorized user reached the run")
		}
	}
}

func TestAutoTitleAfterFirstAnswer(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "please scrape the sale page"))
	h.waitIdle()
	if s := h.session("telegram:dm:5"); s == nil || s.Title != "please scrape the sale page" {
		t.Fatalf("%+v", s)
	}
}

func TestStartReplies(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "/start"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "ready") || h.session("telegram:dm:5") != nil {
		t.Fatal(h.sentTexts())
	}
}

func TestReloadMCPAliasReachesGateway(t *testing.T) {
	var got string
	gateway.Register(gateway.Command{Name: "tg-test-cmd", Run: func(_ context.Context, c *gateway.CommandCtx, _ string) error {
		got = c.Key
		c.Reply("pong")
		return nil
	}})
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "/tg-test-cmd"))
	h.waitIdle()
	if got != "telegram:dm:5" || !strings.Contains(h.sentTexts(), "pong") {
		t.Fatal(got, h.sentTexts())
	}
	if CanonicalCommand("/reload_mcp") != "/reload-mcp" {
		t.Fatal("alias")
	}
}

func TestAnswerMediaSentAfterText(t *testing.T) {
	png := tempFile(t, "chart.png")
	h := harness(t, &tgProv{script: []tgStep{{text: "Here is the chart.\nMEDIA:" + png}}},
		func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(dmText(5, "chart please"))
	h.waitIdle()
	ms := h.api.methods()
	last := -1
	for i, m := range ms {
		if m == "sendMessage" {
			last = i
		}
	}
	if h.api.count("sendPhoto") != 1 || last < 0 || ms[len(ms)-1] != "sendPhoto" {
		t.Fatal(ms)
	}
	if strings.Contains(h.sentTexts(), "MEDIA:") {
		t.Fatal(h.sentTexts())
	}
}

func TestChannelSendFormatsAndThreads(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100", ThreadID: "17585"},
		gateway.Outbound{Text: "**Report** ready."})
	if err != nil {
		t.Fatal(err)
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["parse_mode"] != "MarkdownV2" || c.Body["message_thread_id"] != float64(17585) ||
		!strings.Contains(c.Body["text"].(string), "*Report*") {
		t.Fatal(c.Body)
	}
	if err := h.ch.Send(ctx, gateway.Target{ChatID: "x"}, gateway.Outbound{Text: "a"}); err == nil {
		t.Fatal("a bad chat id must fail")
	}
}

func TestChatInfo(t *testing.T) {
	cfg := config.Default().Telegram
	info := ChatInfo(cfg, User{ID: 99, Username: "tether_bot"})
	if s := info(&sessions.Session{Source: "telegram", Key: "telegram:dm:5", ChatID: "5"}); s != "Telegram private chat" {
		t.Fatal(s)
	}
	s := info(&sessions.Session{Source: "telegram", Key: "telegram:group:-100:17585", ChatID: "-100", ThreadID: "17585", UserID: "1"})
	if !strings.Contains(s, "topic 17585") || !strings.Contains(s, "shared") || !strings.Contains(s, "@tether_bot") {
		t.Fatal(s)
	}
	if s := info(&sessions.Session{Source: "cli", Key: "cli:me"}); s != "" {
		t.Fatal(s)
	}
}

func TestChatInfoTellsTheObservedBlock(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.ObserveUnmentionedGroupMessages = true
	cfg.GroupAllowedChats = []string{"-100"}
	s := ChatInfo(cfg, User{ID: 99, Username: "tether_bot"})(&sessions.Session{Source: "telegram", Key: "telegram:group:-100", ChatID: "-100"})
	// Hermes _telegram_group_observe_channel_prompt: the agent loop sends the observed
	// messages in a context-only block before the current message.
	if !strings.Contains(s, "context-only block before the current message") ||
		!strings.Contains(s, "Treat only the current message as a request") {
		t.Fatal(s)
	}
}

func TestPhotoCaptionWithoutMentionReachesAgent(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.api.addFile("p1280", "photos/file_1.jpg", []byte("JPEG"))
	m := dmText(5, "")
	m.Text, m.Entities = "", nil
	m.Caption, m.Photo = "@tether_bot what is this?", sizes(1280)
	h.update(m)
	h.waitIdle()
	s := h.session("telegram:dm:5")
	if s == nil {
		t.Fatal("no session")
	}
	// With no vision model, the agent replaces the image marker with a note.
	user := h.messages(s)[0].Msg.Content
	if !strings.HasPrefix(user, "[image") || !strings.HasSuffix(user, "what is this?") || strings.Contains(user, "@tether_bot") {
		t.Fatal(user)
	}
}

// progressTool is a fake delegate_task that sends one child progress line.
type progressTool struct{}

func (progressTool) Name() string       { return "delegate_task" }
func (progressTool) Toolset() string    { return "file" }
func (progressTool) ParallelSafe() bool { return true }
func (progressTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "delegate_task", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (progressTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	env.Progress(1, "1/2", "read_file", "notes.txt")
	return "ok", nil
}

func TestChildProgressLinesInStatus(t *testing.T) {
	prov := &tgProv{script: []tgStep{
		{calls: []provider.ToolCall{{ID: "c1", Name: "delegate_task", Args: `{}`}}},
		{text: "Done."},
	}}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.gw.Deps.Agent.Tools.Register(progressTool{})
	h.gw.Deps.NewEnv = func(sess *sessions.Session, _ string) *tools.Env {
		return &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{},
			Progress: func(b uint64, c, tl, p string) { h.ch.Progress(sess.Key, b, c, tl, p) }}
	}
	h.update(dmText(5, "split the work"))
	h.waitIdle()
	var all []string
	all = append(all, h.api.textsOf("sendMessage")...)
	all = append(all, h.api.textsOf("editMessageText")...)
	if !strings.Contains(strings.Join(all, "\n"), `🔀 [1/2] read_file "notes.txt"`) {
		t.Fatal(all)
	}
	h.ch.Progress("telegram:dm:5", 1, "1/2", "read_file", "late line")
	if strings.Contains(h.sentTexts(), "late line") {
		t.Fatal("a line after the turn must not show")
	}
}

func TestHandleUpdateDoesNotWaitForTheTurn(t *testing.T) {
	// The poller handles the updates of a chat one at a time, so a turn must not block the
	// next update of the chat (for example /stop).
	h := harness(t, &tgProv{delay: 2 * time.Second}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	start := time.Now()
	h.ch.handleUpdate(ctx, h.gw, Update{UpdateID: 1, Message: ptr(dmText(5, "slow job"))})
	if time.Since(start) > time.Second {
		t.Fatal("handleUpdate waited for the turn:", time.Since(start))
	}
	key := h.ch.sessionKey(Normalize(dmText(5, "x")))
	if _, ok := h.gw.Running(key); !ok {
		waitForRun(t, h.gw, key)
	}
	h.ch.handleUpdate(ctx, h.gw, Update{UpdateID: 2, Message: ptr(dmText(5, "/stop"))})
	if !h.gw.Wait(time.Second) {
		t.Fatal("/stop did not end the turn")
	}
	if !strings.Contains(h.sentTexts(), "Stopped") {
		t.Fatal(h.sentTexts())
	}
}

func ptr[T any](v T) *T { return &v }

// waitForRun waits until key has a running turn.
func waitForRun(t *testing.T, gw *gateway.Gateway, key string) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for {
		if _, ok := gw.Running(key); ok {
			return
		}
		if time.Now().After(end) {
			t.Fatal("no run for", key)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSetTurnContextReachesThePoller(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	type key struct{}
	tctx := context.WithValue(ctx, key{}, "turns")
	h.ch.SetTurnContext(tctx)
	if p := h.ch.poller(); p.HandleCtx != tctx || p.C != h.ch.C {
		t.Fatalf("%+v", p)
	}
}

// fakeTTS is a text_to_speech tool that returns a MEDIA tag for path, like the real tool.
type fakeTTS struct{ path string }

func (fakeTTS) Name() string       { return "text_to_speech" }
func (fakeTTS) Toolset() string    { return tools.SetMedia }
func (fakeTTS) ParallelSafe() bool { return false }
func (fakeTTS) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "text_to_speech", Description: "tts", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (f fakeTTS) Run(context.Context, json.RawMessage, *tools.Env) (string, error) {
	return "MEDIA:" + f.path + " [[audio_as_voice]]", nil
}

// ttsTurn runs one agent turn in which the model calls text_to_speech and then answers
// answer. The turn shows in a real renderer. The renderer gets the final text with the
// media tags of the turn, as the gateway gives it (agent.AppendMediaTags).
func ttsTurn(t *testing.T, h *tgHarness, audio, answer string) {
	t.Helper()
	ag := h.gw.Deps.Agent
	ag.Tools.Register(fakeTTS{path: audio})
	h.prov.script = []tgStep{{calls: []provider.ToolCall{{ID: "c1", Name: "text_to_speech", Args: `{}`}}}, {text: answer}}
	sess, err := h.st.Create(ctx, sessions.Session{Key: "telegram:dm:7", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	r := h.ch.newRenderer(ctx, 7, "", false)
	res, err := ag.RunTurn(ctx, agent.Turn{Session: sess, UserText: "say hi",
		Toolsets: tools.Resolve([]string{tools.SetAll}, nil, nil), Sink: r})
	r.Done(agent.AppendMediaTags(res.Final, res.Media), err)
}

// fileSends returns the file uploads of the fake Bot API.
func fileSends(api *fakeBot) []apiCall {
	var out []apiCall
	for _, c := range api.all() {
		if c.Body["_file_field"] != nil {
			out = append(out, c)
		}
	}
	return out
}

func TestTTSFileSentWhenAnswerHasNoTag(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ogg := tempFile(t, "tts-1.ogg")
	ttsTurn(t, h, ogg, "Here is your audio.")
	sends := fileSends(h.api)
	if len(sends) != 1 || sends[0].Method != "sendVoice" || sends[0].Body["chat_id"] != "7" {
		t.Fatalf("file sends = %+v, want one sendVoice", sends)
	}
	txt := h.sentTexts() + "\n" + StripMarkdownV2(strings.Join(h.api.textsOf("editMessageText"), "\n"))
	if strings.Contains(txt, "MEDIA:") || strings.Contains(txt, "[[") || !strings.Contains(txt, "Here is your audio.") {
		t.Fatalf("texts = %q, want the answer without the tags", txt)
	}
}

func TestTTSFileNotDuplicatedWhenAnswerHasTag(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ogg := tempFile(t, "tts-1.ogg")
	ttsTurn(t, h, ogg, "Here it is.\nMEDIA:"+ogg)
	if sends := fileSends(h.api); len(sends) != 1 {
		t.Fatalf("file sends = %+v, want one", sends)
	}
}

func TestTTSFileRefusedByPolicyStaysRefused(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ogg := tempFile(t, "tts-1.ogg")
	h.ch.Media = &MediaPolicy{Denied: []string{filepath.Dir(ogg)}}
	ttsTurn(t, h, ogg, "Here is your audio.")
	if sends := fileSends(h.api); len(sends) != 0 {
		t.Fatalf("file sends = %+v, want none", sends)
	}
	// The streamer edits its message to the final text.
	edits := strings.Join(h.api.textsOf("editMessageText"), "\n")
	if txt := StripMarkdownV2(edits); !strings.Contains(txt, "[blocked file: "+ogg+"]") {
		t.Fatalf("edits = %q, want the blocked-file note", txt)
	}
}
func TestE2ETTSFileSentThroughGateway(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	ogg := tempFile(t, "tts-9.ogg")
	h.gw.Deps.Agent.Tools.Register(fakeTTS{path: ogg})
	prov.script = []tgStep{{calls: []provider.ToolCall{{ID: "c1", Name: "text_to_speech", Args: `{}`}}}, {text: "Done."}}
	h.update(dmText(5, "say hi"))
	h.waitIdle()
	if s := fileSends(h.api); len(s) != 1 || s[0].Method != "sendVoice" {
		t.Fatalf("%+v / %v", s, h.api.methods())
	}
}

// cmdTap returns a press of a command button with data on the message msgID. A negative
// chat is a group.
func cmdTap(chat, from int64, msgID int, data string) CallbackQuery {
	typ := "private"
	if chat < 0 {
		typ = "supergroup"
	}
	return CallbackQuery{ID: "cb", From: User{ID: from, FirstName: "U"},
		Message: &Message{MessageID: msgID, Chat: Chat{ID: chat, Type: typ}}, Data: data}
}

// tap handles cq as a new update in its own goroutine, like the poller.
func (h *tgHarness) tap(cq CallbackQuery) {
	h.next++
	u := Update{UpdateID: h.next, CallbackQuery: &cq}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.ch.handleUpdate(ctx, h.gw, u)
	}()
}

// answers returns the text of each answerCallbackQuery call, in order.
func (h *tgHarness) answers() []string {
	var out []string
	for _, c := range h.api.all() {
		if c.Method == "answerCallbackQuery" {
			s, _ := c.Body["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

// addSessions creates n sessions for the key, with the titles "s1" to "sn".
func (h *tgHarness) addSessions(key, chat, user string, n int) []*sessions.Session {
	h.t.Helper()
	var out []*sessions.Session
	for i := 1; i <= n; i++ {
		s, err := h.st.Create(ctx, sessions.Session{Key: key, Source: "telegram", ChatID: chat, UserID: user,
			Title: "s" + strconv.Itoa(i)})
		if err != nil {
			h.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func ownerHarness(t *testing.T, prov *tgProv) *tgHarness {
	return harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
}

func TestCmdTapRunsCommand(t *testing.T) {
	h := ownerHarness(t, &tgProv{})
	h.addSessions("telegram:dm:7", "7", "7", 2)
	h.tap(cmdTap(7, 7, 55, encodeCmd("7", "/sessions")))
	h.waitIdle()
	if a := h.answers(); len(a) != 1 || a[0] != "" {
		t.Fatal(a)
	}
	if h.api.count("sendMessage") != 1 || h.api.count("editMessageText") != 0 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("sendMessage")
	if txt, _ := c.Body["text"].(string); !strings.HasPrefix(txt, ToMarkdownV2("📋 **Your sessions**")) {
		t.Fatal(txt)
	}
	data := buttonData(c.Body)
	if len(data) == 0 {
		t.Fatal("the session list has no buttons")
	}
	for _, d := range data {
		if !strings.HasPrefix(d, "cmd:7:") {
			t.Fatal(data)
		}
	}
}

func TestCmdTapPageEditsMessage(t *testing.T) {
	h := ownerHarness(t, &tgProv{})
	h.addSessions("telegram:dm:7", "7", "7", 10)
	h.tap(cmdTap(7, 7, 55, encodeCmd("7", "/sessions 2")))
	h.waitIdle()
	if h.api.count("editMessageText") != 1 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
	if c := h.api.lastCall("editMessageText"); c.Body["message_id"] != float64(55) || c.Body["chat_id"] != float64(7) {
		t.Fatalf("%+v", c.Body)
	}
}

func TestCmdTapUnauthorizedDoesNothing(t *testing.T) {
	prov := &tgProv{}
	h := ownerHarness(t, prov)
	h.tap(cmdTap(99, 99, 55, encodeCmd("99", "/new")))
	h.waitIdle()
	if a := h.answers(); len(a) != 1 || a[0] != notAuthorized {
		t.Fatal(a)
	}
	if h.session("telegram:dm:99") != nil || h.api.count("sendMessage") != 0 || prov.count() != 0 {
		t.Fatal(h.api.methods())
	}
}

func TestCmdTapOtherUserRefused(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) {
		c.AllowAllUsers = true
		c.GroupSessionsPerUser = true
	})
	key := h.ch.sessionKey(Normalized{Kind: KindGroup, ChatID: "-100", UserID: "7"})
	s := h.addSessions(key, "-100", "7", 1)[0]
	h.tap(cmdTap(-100, 8, 55, encodeCmd("7", "/delete "+s.ID+" confirm")))
	h.tap(cmdTap(-100, 8, 55, encodeCmd("7", "cancel")))
	h.waitIdle()
	if a := h.answers(); len(a) != 2 || a[0] != otherUser || a[1] != otherUser {
		t.Fatal(a)
	}
	if _, err := h.st.Get(ctx, s.ID); err != nil {
		t.Fatal("the session of 7 must stay:", err)
	}
	if h.api.count("sendMessage") != 0 || h.api.count("editMessageText") != 0 {
		t.Fatal(h.api.methods())
	}
}

func TestCmdTapBusyRuleApplies(t *testing.T) {
	prov := &tgProv{delay: 300 * time.Millisecond}
	h := ownerHarness(t, prov)
	h.update(dmText(7, "hi"))
	deadline := time.Now().Add(5 * time.Second)
	for prov.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	h.tap(cmdTap(7, 7, 55, encodeCmd("7", "/rename x")))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "cannot run during a task") {
		t.Fatal(h.sentTexts())
	}
	if s := h.session("telegram:dm:7"); s == nil || s.Title == "x" {
		t.Fatalf("%+v", s)
	}
}

func TestCmdTapCancelAndNoop(t *testing.T) {
	h := ownerHarness(t, &tgProv{})
	h.tap(cmdTap(7, 7, 55, encodeCmd("7", "cancel")))
	h.waitIdle()
	if h.api.count("editMessageText") != 1 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("editMessageText")
	if txt, _ := c.Body["text"].(string); c.Body["message_id"] != float64(55) || StripMarkdownV2(txt) != "Not deleted." {
		t.Fatalf("%+v", c.Body)
	}
	if _, ok := c.Body["reply_markup"]; ok {
		t.Fatalf("the buttons must go: %+v", c.Body)
	}
	h.tap(cmdTap(7, 7, 55, encodeCmd("7", "·")))
	h.waitIdle()
	if a := h.answers(); len(a) != 2 || a[0] != "" || a[1] != "" {
		t.Fatal(a)
	}
	if h.api.count("editMessageText") != 1 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
}

func TestCmdTapInvalidData(t *testing.T) {
	prov := &tgProv{}
	h := ownerHarness(t, prov)
	// A text that is not a command does not start a turn.
	h.tap(cmdTap(7, 7, 55, "cmd:7:hello"))
	h.tap(cmdTap(7, 7, 55, "cmd:x"))
	cq := cmdTap(7, 7, 55, encodeCmd("7", "/new"))
	cq.Message = nil
	h.tap(cq)
	h.waitIdle()
	a := h.answers()
	if len(a) != 3 {
		t.Fatal(a)
	}
	for _, s := range a {
		if s != invalidCallback {
			t.Fatal(a)
		}
	}
	if prov.count() != 0 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
}

func TestStartHelpWithButtons(t *testing.T) {
	h := ownerHarness(t, &tgProv{})
	h.update(dmText(7, "/start"))
	h.waitIdle()
	if h.api.count("sendMessage") != 1 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["text"] != ToMarkdownV2(StartHelp) {
		t.Fatalf("%q", c.Body["text"])
	}
	want := []string{"cmd:7:/sessions", "cmd:7:/model", "cmd:7:/usage"}
	if got := buttonData(c.Body); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatal(got)
	}
	if StartHelp != "👋 **Tether Agent is ready**\nSend a message to start a conversation.\n\n"+
		"/new · start a new session\n/sessions · see your sessions\n/stop · stop the current task\n/help · see all commands" {
		t.Fatal(StartHelp)
	}
}

func TestSetHomeText(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	m := group("/sethome")
	m.Chat.Title = "Alice"
	h.update(m)
	h.waitIdle()
	want := ToMarkdownV2("🏠 **Home channel: Alice**\nCron results and messages from other channels come to this chat.")
	got, _ := h.api.lastCall("sendMessage").Body["text"].(string)
	if got != want || strings.Contains(got, "100") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	// Markdown characters in the chat name show literally.
	m.Chat.Title = "my_team *x*"
	h.update(m)
	h.waitIdle()
	got, _ = h.api.lastCall("sendMessage").Body["text"].(string)
	if !strings.HasPrefix(got, `🏠 *Home channel: my\_team \*x\**`+"\n") || checkMDV2(got) != nil {
		t.Fatalf("%q", got)
	}

	_ = h.ch.DB.Close()
	h.update(m)
	h.waitIdle()
	got, _ = h.api.lastCall("sendMessage").Body["text"].(string)
	if txt := StripMarkdownV2(got); !strings.HasPrefix(txt, "⚠️ Cannot save the home channel\n") ||
		!strings.HasSuffix(txt, ". Try again later.") {
		t.Fatalf("%q", txt)
	}
}

func TestJoinEventMakesNoTurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	var m Message
	raw := `{"message_id":1,"from":{"id":5,"is_bot":false,"first_name":"Alice"},
	 "chat":{"id":-1001234567890,"title":"Test Group","is_forum":true,"type":"supergroup"},
	 "date":1793175153,"new_chat_members":[{"id":99,"is_bot":true,"first_name":"Tether Agent"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	h.update(m)
	h.update(dmText(5, "")) // a DM contact or poll: no decoded content
	h.waitIdle()
	if prov.count() != 0 || h.api.count("sendMessage") != 0 {
		t.Fatalf("turns %d, sent %q", prov.count(), h.sentTexts())
	}
	if s := h.session("telegram:group:-1001234567890:1"); s != nil {
		t.Fatalf("session %+v", s)
	}
}

func TestEditedLiveLocationMakesNoTurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	loc := dmText(5, "")
	loc.Text, loc.Entities, loc.Location = "", nil, &Location{Latitude: 1, Longitude: 2}
	h.update(loc) // the first live location message is not an edit
	h.waitIdle()
	h.edit(loc)
	h.edit(loc)
	h.waitIdle()
	if prov.count() != 1 {
		t.Fatalf("turns %d, want 1", prov.count())
	}
	if !strings.Contains(buf.String(), `"reason":"live_location_update"`) {
		t.Fatal(buf.String())
	}
}

func TestEditedTextStillGetsATurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.edit(dmText(5, "fixed typo"))
	h.waitIdle()
	if prov.count() != 1 {
		t.Fatalf("turns %d, want 1", prov.count())
	}
}

func TestGateDecisionsLogged(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	h.update(dmText(5, "hi"))
	h.update(fromUser(forumNoThread("hello"), 5, "U"))
	join := fromUser(group(""), 5, "U")
	join.Entities = nil
	h.update(join)
	h.waitIdle()
	var accepted, dropped []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		switch rec["msg"] {
		case "telegram: message accepted":
			accepted = append(accepted, rec)
		case "telegram: message not accepted":
			dropped = append(dropped, rec)
		}
	}
	if len(accepted) != 2 || len(dropped) != 1 || dropped[0]["reason"] != "no_content" {
		t.Fatalf("accepted %v, dropped %v", accepted, dropped)
	}
	for _, rec := range accepted {
		switch rec["reason"] {
		case "dm":
			if rec["chat"] != "5" || rec["thread"] != nil {
				t.Errorf("dm line %v", rec)
			}
		case "require_mention_off":
			if rec["thread"] != "1" {
				t.Errorf("forum line %v", rec)
			}
		default:
			t.Errorf("reason %v", rec)
		}
	}
}

func serviceMsg(t *testing.T, raw string) Message {
	t.Helper()
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBotJoinAndRemovalLogged(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	const chat = `"chat":{"id":-1001234567890,"title":"Test Group","is_forum":true,"type":"supergroup"}`
	const from = `"from":{"id":111111111,"first_name":"Alice"}`
	h.update(serviceMsg(t, `{"message_id":1,`+from+`,`+chat+`,"new_chat_members":[{"id":99,"is_bot":true}]}`))
	h.update(serviceMsg(t, `{"message_id":2,`+from+`,`+chat+`,"new_chat_members":[{"id":7,"first_name":"Rani"}]}`))
	h.update(serviceMsg(t, `{"message_id":3,`+from+`,`+chat+`,"left_chat_member":{"id":99,"is_bot":true}}`))
	h.waitIdle()
	out := buf.String()
	for _, want := range []string{
		`"level":"INFO","msg":"telegram: the bot was added to a group","chat":"-1001234567890","title":"Test Group","by":"111111111"`,
		`"level":"INFO","msg":"telegram: the bot was removed from a group","chat":"-1001234567890","title":"Test Group","by":"111111111"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Count(out, "the bot was added") != 1 {
		t.Error("a join of another member must not log")
	}
	// The sender is not an allowed user and the group is unknown: no reply, no pairing code,
	// no session, no turn.
	if prov.count() != 0 || h.api.count("sendMessage") != 0 || h.session("telegram:group:-1001234567890:1") != nil {
		t.Fatalf("turns %d, sent %q", prov.count(), h.sentTexts())
	}
}

func TestMigrationLogged(t *testing.T) {
	const old, next = "-1234567890", "-1001234567890"
	migrate := `{"message_id":4,"from":{"id":5,"first_name":"Alice"},"chat":{"id":-1234567890,"title":"Test Group","type":"group"},"migrate_to_chat_id":-1001234567890}`
	from := `{"message_id":1,"from":{"id":5,"first_name":"Alice"},"chat":{"id":-1001234567890,"title":"Test Group","type":"supergroup"},"migrate_from_chat_id":-1234567890}`
	all := "allowed_chats, free_response_chats, group_allowed_chats, home_chat, home channel, cron jobs"

	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) {
		c.AllowedChats = []string{old}
		c.FreeResponseChats = []string{old}
		c.GroupAllowedChats = []string{old}
		c.HomeChat = old
	})
	buf := debugLog(h)
	if err := SetHome(ctx, h.ch.DB, old, ""); err != nil {
		t.Fatal(err)
	}
	h.ch.CronChats = func(context.Context) ([]string, error) { return []string{"-77", old}, nil }
	h.update(serviceMsg(t, migrate))
	h.update(serviceMsg(t, from))
	h.waitIdle()
	want := `"level":"WARN","msg":"telegram: the group chat moved to a new chat id. Put the new id in the config.","old_chat":"` +
		old + `","new_chat":"` + next + `","old_id_in":"` + all + `"`
	if out := buf.String(); !strings.Contains(out, want) || strings.Count(out, "moved to a new chat id") != 1 {
		t.Fatalf("want %s in\n%s", want, out)
	}

	// No place holds the old id, and allowed_chats lists only another chat: INFO, no old_id_in.
	q := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedChats = []string{"-999"} })
	qbuf := debugLog(q)
	q.update(serviceMsg(t, migrate))
	q.waitIdle()
	info := `"level":"INFO","msg":"telegram: the group chat moved to a new chat id","old_chat":"` + old + `","new_chat":"` + next + `"}`
	if !strings.Contains(qbuf.String(), info) {
		t.Fatalf("want %s in\n%s", info, qbuf.String())
	}

	// allowed_chats does not list the old id, but another place holds it: the WARN line still
	// comes, because logServiceEvent runs before Gate.
	w := harness(t, &tgProv{}, func(c *config.TelegramConfig) {
		c.AllowedChats = []string{"-999"}
		c.FreeResponseChats = []string{old}
	})
	wbuf := debugLog(w)
	w.update(serviceMsg(t, migrate))
	w.waitIdle()
	warn := `"level":"WARN","msg":"telegram: the group chat moved to a new chat id. Put the new id in the config.","old_chat":"` +
		old + `","new_chat":"` + next + `","old_id_in":"free_response_chats"`
	if !strings.Contains(wbuf.String(), warn) {
		t.Fatalf("want %s in\n%s", warn, wbuf.String())
	}
}

// voiceDM is a DM voice note from the user with the id uid. The harness file "v1" serves it.
func voiceDM(h *tgHarness, uid int64) Message {
	h.api.addFile("v1", "voice/file_1.oga", []byte("OGG"))
	m := dmText(uid, "")
	m.MessageID, m.Text, m.Entities = 41, "", nil
	m.Voice = &Voice{FileID: "v1", FileUniqueID: "uv", FileSize: 3}
	return m
}

// blockSTT makes speech to text of h wait until release closes or the context ends. started
// closes when the first transcription starts.
func blockSTT(h *tgHarness) (started, release chan struct{}) {
	started, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.ch.Inbox.Transcribe = func(ctx context.Context, _ string) (string, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return "turn on the lights", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return started, release
}

func waitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func TestStopDrainWaitsForAMessageInSpeechToText(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	started, release := blockSTT(h)
	h.update(voiceDM(h, 5))
	waitClosed(t, started, "the transcription did not start")
	// The gateway has no turn yet, but the message is not lost: the drain waits for it.
	if h.gw.Wait(100 * time.Millisecond) {
		t.Fatal("the drain must wait for a message in speech to text")
	}
	close(release)
	h.waitIdle()
	s := h.session("telegram:dm:5")
	if s == nil {
		t.Fatal("no session")
	}
	if got := h.messages(s)[0].Msg.Content; !strings.Contains(got, "turn on the lights") {
		t.Fatal(got)
	}
	if h.ch.Inflight() != 0 {
		t.Fatal(h.ch.Inflight())
	}
}

// droppedNotice is the text of the drop notice without the MarkdownV2 markup.
var droppedNotice = StripMarkdownV2(ToMarkdownV2(droppedAtStopText))

// wantDropNotice checks that the user got the drop notice as a reply to the message msgID,
// and that the log tells the chat.
func wantDropNotice(t *testing.T, h *tgHarness, logb *bytes.Buffer, msgID int) {
	t.Helper()
	if !strings.Contains(h.sentTexts(), droppedNotice) {
		t.Fatal(h.sentTexts())
	}
	c := h.api.lastCall("sendMessage")
	rp, _ := c.Body["reply_parameters"].(map[string]any)
	if rp == nil || rp["message_id"] != float64(msgID) {
		t.Fatal(c.Body)
	}
	if got := logb.String(); !strings.Contains(got, `"msg":"telegram: the gateway stops, so the message is dropped"`) ||
		!strings.Contains(got, `"chat":"5"`) {
		t.Fatal(got)
	}
	if h.prov.count() != 0 || h.session("telegram:dm:5") != nil {
		t.Fatal("a dropped message must make no turn and no session")
	}
}

func TestMessageCancelledInSpeechToTextGetsANotice(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	logb := debugLog(h)
	started, _ := blockSTT(h)
	wctx, stopWork := context.WithCancel(ctx)
	defer stopWork()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ch.handleUpdate(wctx, h.gw, Update{UpdateID: 1, Message: new(voiceDM(h, 5))})
	}()
	waitClosed(t, started, "the transcription did not start")
	// The drain time ends: the gateway stops and cancels the work.
	h.gw.BeginStopping(ctx)
	stopWork()
	waitClosed(t, done, "the update did not end")
	wantDropNotice(t, h, logb, 41)
}

func TestMessageAfterTheStopGetsANotice(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	logb := debugLog(h)
	h.gw.BeginStopping(ctx)
	m := dmText(5, "hello")
	m.MessageID = 42
	h.update(m)
	h.waitIdle()
	wantDropNotice(t, h, logb, 42)
}

// richDM is a DM from the user uid whose only content is a rich message with the given
// blocks.
func richDM(uid int64, blocksJSON string) Message {
	m := richMsg(blocksJSON)
	m.MessageID, m.Chat, m.From = 1, Chat{ID: uid, Type: "private", FirstName: "U"}, &User{ID: uid, FirstName: "U"}
	return m
}

// richDMPhoto is the JSON of a photo block. The fake file "cp1" serves it.
const richDMPhoto = `{"type":"photo","photo":[{"file_id":"cp1","file_unique_id":"cu1","width":1280,"height":960,"file_size":4}]}`

func TestRichDMTurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(richDM(5, `[{"type":"heading","size":2,"text":"Plan"},{"type":"paragraph","text":["do ",{"type":"bold","text":"this"}]}]`))
	h.waitIdle()
	s := h.session("telegram:dm:5")
	if s == nil || prov.count() != 1 {
		t.Fatalf("session %+v, turns %d", s, prov.count())
	}
	// The stored user message is the Markdown text.
	msgs := h.messages(s)
	if len(msgs) < 2 || msgs[0].Msg.Role != provider.RoleUser || msgs[0].Msg.Content != "## Plan\n\ndo **this**" {
		t.Fatalf("%+v", msgs)
	}
}

func TestRichPhotoTurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.api.addFile("cp1", "photos/cp1.jpg", []byte("JPEG"))
	h.update(richDM(5, `[{"type":"paragraph","text":"@tether_bot what is this?"},`+richDMPhoto+`]`))
	h.waitIdle()
	s := h.session("telegram:dm:5")
	if s == nil || prov.count() != 1 {
		t.Fatalf("session %+v, turns %d", s, prov.count())
	}
	// The turn has one image. With no vision model, the agent replaces the image marker with
	// a note. The mention of the bot is not in the text.
	user := h.messages(s)[0].Msg.Content
	if !strings.HasPrefix(user, "what is this?\n\n[image") || strings.Count(user, "[image") != 1 ||
		strings.Contains(user, "@tether_bot") {
		t.Fatal(user)
	}
	if h.api.count("getFile") != 1 || h.api.count("download") != 1 {
		t.Fatal(h.api.methods())
	}
}

func TestEditedRichMessageGetsATurn(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.edit(richDM(5, `[{"type":"paragraph","text":"fixed typo"}]`))
	h.waitIdle()
	if prov.count() != 1 {
		t.Fatalf("turns %d, want 1", prov.count())
	}
}

func TestRichCommandRuns(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.update(richDM(5, `[{"type":"paragraph","text":"/new"}]`))
	h.waitIdle()
	// /new makes a session with no message, and it makes no model call.
	s := h.session("telegram:dm:5")
	if s == nil || len(h.messages(s)) != 0 || prov.count() != 0 || h.api.count("sendChatAction") != 0 {
		t.Fatalf("session %+v, turns %d, calls %v", s, prov.count(), h.api.methods())
	}
}

func TestRichMessageAnswersClarify(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	h.api.addFile("cp1", "photos/cp1.jpg", []byte("JPEG"))
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan tools.Answer, 1)
	go func() {
		a, _ := h.ch.UI.For(5, "", "").Ask(actx, tools.Question{Text: "Favourite color?"})
		out <- a
	}()
	waitSends(t, h.api, 1)
	// A rich message with a media part is not an answer. It goes to the agent.
	h.update(richDM(5, `[{"type":"paragraph","text":"red"},`+richDMPhoto+`]`))
	h.waitIdle()
	// A rich message that starts with "/" is not an answer. It runs as a command.
	h.update(richDM(5, `[{"type":"paragraph","text":"/new"}]`))
	h.waitIdle()
	if prov.count() != 1 {
		t.Fatalf("turns %d, want 1", prov.count())
	}
	// A rich message with text and no media part is the answer, without the bot mention.
	h.update(richDM(5, `[{"type":"paragraph","text":["@tether_bot my answer is ",{"type":"bold","text":"blue"}]}]`))
	h.waitIdle()
	select {
	case a := <-out:
		if a.Text != "my answer is **blue**" {
			t.Fatalf("%+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the rich message did not answer the question")
	}
	if prov.count() != 1 {
		t.Fatal("the answer must not go to the agent")
	}
}

func TestRichUnknownTypesLogged(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	h.update(richDM(5, `[{"type":"sidebar","text":"a note"}]`))
	h.waitIdle()
	want := `"level":"DEBUG","msg":"telegram: the rich message has unknown types","chat":"5","types":"sidebar"`
	if out := buf.String(); !strings.Contains(out, want) || strings.Count(out, "the rich message has unknown types") != 1 {
		t.Fatalf("want one line %s in\n%s", want, out)
	}
	// The fallback gives the text of the unknown block, so the message makes a turn.
	if prov.count() != 1 {
		t.Fatalf("turns %d, want 1", prov.count())
	}
}

func TestRichFailureLogged(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	m := richDM(5, `[{"type":"paragraph","text":"hello"}]`)
	// A cached document with the failed flag stands for a converter panic.
	m.rich = &richDoc{failed: true}
	h.update(m)
	// A channel post returns before the converter, so it writes no line.
	post := richDM(5, `[{"type":"paragraph","text":"post"}]`)
	post.Chat = Chat{ID: -5, Type: "channel", Title: "News"}
	post.rich = &richDoc{failed: true}
	h.update(post)
	h.waitIdle()
	out := buf.String()
	want := `"level":"WARN","msg":"telegram: the rich message cannot be read","chat":"5"`
	if !strings.Contains(out, want) || strings.Count(out, "the rich message cannot be read") != 1 {
		t.Fatalf("want one line %s in\n%s", want, out)
	}
	// The message has no rich content after the failure, so the gate drops it.
	if !strings.Contains(out, `"reason":"no_content"`) || prov.count() != 0 || h.session("telegram:dm:5") != nil {
		t.Fatalf("turns %d, log\n%s", prov.count(), out)
	}
}

func TestPlainAndClassicPhotoWriteNoRichLine(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	h.api.addFile("p1280", "photos/file_1.jpg", []byte("JPEG"))
	h.update(dmText(5, "hi"))
	h.waitIdle()
	photo := dmText(5, "")
	photo.Text, photo.Entities = "", nil
	photo.Caption, photo.Photo = "@tether_bot what is this?", sizes(1280)
	h.update(photo)
	h.waitIdle()
	var users []string
	for _, m := range h.messages(h.session("telegram:dm:5")) {
		if m.Msg.Role == provider.RoleUser {
			users = append(users, m.Msg.Content)
		}
	}
	if prov.count() != 2 || len(users) != 2 || users[0] != "hi" || !strings.HasPrefix(users[1], "[image") ||
		!strings.HasSuffix(users[1], "\n\nwhat is this?") || strings.Contains(users[1], "@tether_bot") {
		t.Fatalf("turns %d, users %q", prov.count(), users)
	}
	if out := buf.String(); strings.Contains(out, "the rich message cannot be read") ||
		strings.Contains(out, "the rich message has unknown types") {
		t.Fatal(out)
	}
}

// notAccepted returns the records of the line "telegram: message not accepted" in buf.
func notAccepted(buf *bytes.Buffer) []map[string]any {
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "telegram: message not accepted" {
			out = append(out, rec)
		}
	}
	return out
}

func TestNotAcceptedLineNamesTheUnreadFields(t *testing.T) {
	const (
		dm5   = `"from":{"id":5,"first_name":"U"},"chat":{"id":5,"type":"private"},"date":1`
		dm6   = `"from":{"id":6,"first_name":"V"},"chat":{"id":6,"type":"private"},"date":1`
		grp5  = `"from":{"id":5,"first_name":"U"},"chat":{"id":-100,"type":"supergroup","title":"G"},"date":1`
		note  = `,"video_note":{"file_id":"SECRET-FILE","length":240,"duration":3}`
		empty = `,"rich_message":{"blocks":[{"type":"divider"}]}`
	)
	cases := []struct {
		name, msg     string
		level, reason string
		fields        any // nil when the line has no fields attribute
	}{
		{"video note from an allowed user", dm5 + note, "INFO", "no_content", "video_note"},
		{"video note from a user that is not allowed", dm6 + note, "DEBUG", "no_content", "video_note"},
		{"join", grp5 + `,"new_chat_members":[{"id":7,"first_name":"SECRET-NAME"}],"new_chat_participant":{"id":7},"new_chat_member":{"id":7}`,
			"DEBUG", "no_content", nil},
		{"rich message with only a divider", dm5 + empty, "INFO", "no_content", "rich_message"},
		{"rich message with only a divider, and one more field", dm5 + empty + `,"has_protected_content":true`,
			"INFO", "no_content", "has_protected_content,rich_message"},
		{"pin", dm5 + `,"pinned_message":{"message_id":1,"chat":{"id":5,"type":"private"},"text":"SECRET-PIN"}`,
			"INFO", "no_content", "pinned_message"},
		{"poll and dice", dm5 + `,"poll":{"id":"1","question":"SECRET-POLL"},"dice":{"emoji":"🎲","value":3}`,
			"INFO", "no_content", "dice,poll"},
		{"group message that drops", grp5 + `,"text":"SECRET-TEXT","has_protected_content":true`, "DEBUG", "drop", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prov := &tgProv{}
			h := harness(t, prov, func(cfg *config.TelegramConfig) {
				cfg.AllowedUsers = []string{"5"}
				cfg.RequireMention = true
			})
			buf := debugLog(h)
			h.updateJSON(`{"update_id":1,"message":{"message_id":1,` + c.msg + `}}`)
			h.waitIdle()
			recs := notAccepted(buf)
			if len(recs) != 1 {
				t.Fatalf("want one line, got %v in\n%s", recs, buf)
			}
			r := recs[0]
			if r["level"] != c.level || r["reason"] != c.reason || r["fields"] != c.fields {
				t.Fatalf("line %v, want level %s, reason %s, fields %v", r, c.level, c.reason, c.fields)
			}
			if strings.Contains(buf.String(), "SECRET") {
				t.Fatalf("the log holds a value of the message:\n%s", buf)
			}
			if prov.count() != 0 || h.api.count("sendMessage") != 0 {
				t.Fatalf("turns %d, sent %q", prov.count(), h.sentTexts())
			}
		})
	}
}

// The attributes are chat, reason, and fields, in this order. An edit gives the same line.
func TestNotAcceptedLineAttributeOrder(t *testing.T) {
	h := harness(t, &tgProv{}, func(cfg *config.TelegramConfig) { cfg.AllowedUsers = []string{"5"} })
	buf := debugLog(h)
	h.updateJSON(`{"update_id":1,"edited_message":{"message_id":1,"from":{"id":5,"first_name":"U"},
	 "chat":{"id":5,"type":"private"},"date":1,"video_note":{"file_id":"v"}}}`)
	h.waitIdle()
	want := `"level":"INFO","msg":"telegram: message not accepted","chat":"5","reason":"no_content","fields":"video_note"}`
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("want %s in\n%s", want, buf)
	}
}

// flagCommand registers the gateway command t-flags for the test. Each call adds
// "owner=<bool> shared=<bool>" to the result.
func flagCommand(t *testing.T) func() []string {
	var mu sync.Mutex
	var got []string
	gateway.Register(gateway.Command{Name: "t-flags", MemberOK: true, WhileBusy: true,
		Run: func(_ context.Context, c *gateway.CommandCtx, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, fmt.Sprintf("owner=%v shared=%v", c.Owner, c.Shared))
			return nil
		}})
	t.Cleanup(func() { gateway.Unregister("t-flags") })
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(got) }
}

func TestObservedChatKeepsTheOwnerFlag(t *testing.T) {
	got := flagCommand(t)
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) {
		c.ObserveUnmentionedGroupMessages = true
		c.GroupAllowedChats = []string{"-100"}
		c.GroupAllowedUsers = []string{"123"}
	})
	h.update(group("/t-flags@tether_bot"))
	h.waitIdle()
	h.update(fromUser(group("/t-flags@tether_bot"), 456, "M"))
	h.waitIdle()
	if want := []string{"owner=true shared=true", "owner=false shared=true"}; !slices.Equal(got(), want) {
		t.Fatal(got())
	}
}

func TestSharedFlagFollowsTheSessionKey(t *testing.T) {
	got := flagCommand(t)
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"123"} })
	for _, m := range []Message{dm("/t-flags"), group("/t-flags@tether_bot"), topic("17585", "/t-flags@tether_bot")} {
		h.update(m)
		h.waitIdle()
	}
	want := []string{"owner=true shared=false", "owner=true shared=false", "owner=true shared=true"}
	if !slices.Equal(got(), want) {
		t.Fatal(got())
	}
}

func TestMemberCommandsInTelegram(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.GroupAllowedChats = []string{"-100"} })
	h.update(fromUser(group("/sethome"), 456, "M"))
	h.waitIdle()
	h.update(fromUser(group("/approve"), 456, "M"))
	h.waitIdle()
	h.update(fromUser(group("/model@tether_bot"), 456, "M"))
	h.waitIdle()
	for _, want := range []string{"⛔ Only the owner can use /sethome.", "⛔ Only the owner can answer this prompt.",
		"⛔ Only the owner can use /model."} {
		if !strings.Contains(h.sentTexts(), want) {
			t.Fatal(want, "\n", h.sentTexts())
		}
	}
	if _, ok := HomeTarget(ctx, h.ch.DB, config.Default().Telegram, false); ok {
		t.Fatal("a member moved the home channel")
	}
	// A member tap on the button of an owner-only command gets the owner-only text.
	// The reply can be a new message or an edit of the tapped message.
	h.tap(cmdTap(-100, 456, 55, encodeCmd("456", "/model")))
	h.waitIdle()
	all := h.sentTexts()
	for _, s := range h.api.textsOf("editMessageText") {
		all += "\n" + StripMarkdownV2(s)
	}
	if strings.Count(all, "⛔ Only the owner can use /model.") != 2 {
		t.Fatal(all)
	}
}

func TestApproveCommandAnswersOnlyItsTopic(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"123"} })
	out := make(chan security.Scope, 1)
	go func() {
		sc, _ := h.ch.UI.For(-100, "17585", "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:group:-100:17585", Command: "rm -rf /x", Description: "d"})
		out <- sc
	}()
	waitSends(t, h.api, 1)
	h.update(topic("18", "/approve"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "No command waits for approval") {
		t.Fatal(h.sentTexts())
	}
	h.update(topic("17585", "/approve"))
	h.waitIdle()
	if sc := <-out; sc != security.ScopeOnce {
		t.Fatal(sc)
	}
}

// waitFor waits until cond is true, or fails the test after 5 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedTopicClarifyBindsTheTurnUser(t *testing.T) {
	prov := &tgProv{script: []tgStep{
		{text: "hello"},
		{calls: []provider.ToolCall{{ID: "c1", Name: "clarify", Args: `{"question":"Which color?"}`}}},
		{text: "done"},
	}}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.GroupAllowedUsers = []string{"7", "8"} })
	h.gw.Deps.Agent.Tools.Register(tools.ClarifyTool{})
	h.gw.Deps.NewTurnEnv = func(sess *sessions.Session, _ string, userID string) *tools.Env {
		chat, _ := strconv.ParseInt(sess.ChatID, 10, 64)
		ui := h.ch.UI.For(chat, sess.ThreadID, userID)
		return &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}, Ask: ui}
	}
	h.update(fromUser(topic("17585", "@tether_bot hi"), 7, "A"))
	h.waitIdle()
	h.update(fromUser(topic("17585", "@tether_bot pick a color"), 8, "B"))
	waitFor(t, func() bool { h.ch.UI.mu.Lock(); defer h.ch.UI.mu.Unlock(); return len(h.ch.UI.pending) == 1 })
	// The user who created the session does not answer the question of user 8.
	h.update(fromUser(topic("17585", "red"), 7, "A"))
	h.update(fromUser(topic("17585", "blue"), 8, "B"))
	h.waitIdle()
	s := h.session("telegram:group:-100:17585")
	if s.UserID != "7" {
		t.Fatal(s.UserID)
	}
	var result string
	for _, m := range h.messages(s) {
		if m.Msg.Role == provider.RoleTool {
			result = m.Msg.Content
		}
	}
	if !strings.Contains(result, "blue") || strings.Contains(result, "red") {
		t.Fatal(result)
	}
}

func TestObservedChatClarifyBindsTheMember(t *testing.T) {
	prov := &tgProv{script: []tgStep{
		{calls: []provider.ToolCall{{ID: "c1", Name: "clarify", Args: `{"question":"Which color?"}`}}},
		{text: "done"},
	}}
	h := harness(t, prov, func(c *config.TelegramConfig) {
		c.ObserveUnmentionedGroupMessages = true
		c.GroupAllowedChats = []string{"-100"}
		c.GroupAllowedUsers = []string{"123"}
	})
	h.gw.Deps.Agent.Tools.Register(tools.ClarifyTool{})
	h.gw.Deps.NewTurnEnv = func(sess *sessions.Session, _ string, userID string) *tools.Env {
		chat, _ := strconv.ParseInt(sess.ChatID, 10, 64)
		ui := h.ch.UI.For(chat, sess.ThreadID, userID)
		return &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}, Ask: ui}
	}
	// User 456 is a member: only group_allowed_chats admits the user.
	h.update(fromUser(group("@tether_bot pick a color"), 456, "M"))
	waitFor(t, func() bool { h.ch.UI.mu.Lock(); defer h.ch.UI.mu.Unlock(); return len(h.ch.UI.pending) == 1 })
	h.ch.UI.mu.Lock()
	var bound string
	for _, p := range h.ch.UI.pending {
		bound = p.userID
	}
	h.ch.UI.mu.Unlock()
	if bound != "456" {
		t.Fatalf("the question binds to %q, want the member 456", bound)
	}
	// The shared session has no user, but the member answers the question of the turn.
	h.update(fromUser(group("blue"), 456, "M"))
	h.waitIdle()
	s := h.session("telegram:group:-100")
	if s.UserID != "" {
		t.Fatal(s.UserID)
	}
	var result string
	for _, m := range h.messages(s) {
		if m.Msg.Role == provider.RoleTool {
			result = m.Msg.Content
		}
	}
	if !strings.Contains(result, "blue") {
		t.Fatal(result)
	}
}

func TestRendererStatusFromConfig(t *testing.T) {
	api := newFakeBot(t)
	cfg := config.Default().Telegram
	cfg.StatusIntervalS = 0.25
	ch := &Channel{C: api.client(), Cfg: cfg, ToolPreviewChars: 12}
	r := ch.newRenderer(ctx, 42, "", true)
	if r.status.Interval != 250*time.Millisecond || r.status.PreviewChars != 12 {
		t.Fatal(r.status.Interval, r.status.PreviewChars)
	}
	ch = &Channel{C: api.client(), Cfg: config.TelegramConfig{}}
	r = ch.newRenderer(ctx, 42, "", true)
	if r.status.Interval != 0 || r.status.PreviewChars != 0 {
		t.Fatal("zero values keep the defaults of Status:", r.status.Interval, r.status.PreviewChars)
	}
}

func TestAlbumWaitFromConfig(t *testing.T) {
	ch := &Channel{Cfg: config.TelegramConfig{AlbumWaitS: 0.2}}
	if got := ch.albumWait(); got != 200*time.Millisecond {
		t.Fatal(got)
	}
	ch = &Channel{}
	if got := ch.albumWait(); got != 0 {
		t.Fatal("0 keeps the default of AlbumBuffer:", got)
	}
}
