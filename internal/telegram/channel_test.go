package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/provider"
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
	if !strings.Contains(h.sentTexts(), "Home channel set") {
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
	env.Progress(tools.ChildProgressLine("1/2", "read_file", "notes.txt"))
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
			Progress: func(line string) { h.ch.Progress(sess.Key, line) }}
	}
	h.update(dmText(5, "split the work"))
	h.waitIdle()
	var all []string
	all = append(all, h.api.textsOf("sendMessage")...)
	all = append(all, h.api.textsOf("editMessageText")...)
	if !strings.Contains(strings.Join(all, "\n"), `🔀 [1/2] read_file "notes.txt"`) {
		t.Fatal(all)
	}
	h.ch.Progress("telegram:dm:5", "late line")
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
