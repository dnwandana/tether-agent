package telegram

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// autoEntities makes the entities that Telegram sends for text: a "mention"
// for each @handle word, and a "bot_command" for a leading /command word.
func autoEntities(text string) []MessageEntity {
	var out []MessageEntity
	pos := 0
	for _, w := range strings.SplitAfter(text, " ") {
		word := strings.TrimRight(w, " ,:")
		switch {
		case strings.HasPrefix(word, "@") && len(word) > 1:
			out = append(out, MessageEntity{Type: "mention", Offset: UTF16Len(text[:pos]), Length: UTF16Len(word)})
		case strings.HasPrefix(word, "/") && pos == 0:
			out = append(out, MessageEntity{Type: "bot_command", Offset: 0, Length: UTF16Len(word)})
		}
		pos += len(w)
	}
	return out
}

func ana() *User { return &User{ID: 123, FirstName: "Ana"} }

func dm(text string) Message {
	return Message{MessageID: 1, Chat: Chat{ID: 123, Type: "private", FirstName: "Ana"}, From: ana(), Text: text, Entities: autoEntities(text)}
}

func groupIn(chat, text string) Message {
	id, _ := strconv.ParseInt(chat, 10, 64)
	return Message{MessageID: 2, Chat: Chat{ID: id, Type: "supergroup", Title: "G"}, From: ana(), Text: text, Entities: autoEntities(text)}
}

func group(text string) Message { return groupIn("-100", text) }

// groupRaw is a group message with no entities, like some mobile clients send.
func groupRaw(text string) Message {
	m := group(text)
	m.Entities = nil
	return m
}

func topic(thread, text string) Message {
	m := group(text)
	m.Chat.IsForum = true
	m.MessageThreadID, _ = strconv.Atoi(thread)
	m.IsTopicMessage = true
	return m
}

func forumNoThread(text string) Message {
	m := group(text)
	m.Chat.IsForum = true
	return m
}

func replyToBot(text string) Message {
	m := group(text)
	m.ReplyToMessage = &Message{MessageID: 9, From: &User{ID: 99, IsBot: true}, Chat: m.Chat}
	return m
}

func TestGateTable(t *testing.T) {
	base := config.Default().Telegram
	cases := []struct {
		name            string
		mut             func(*config.TelegramConfig)
		msg             Message
		accept, observe bool
		reason          string
	}{
		{"dm always", nil, dm("hi"), true, false, "dm"},
		{"channel ignored", nil, Message{Chat: Chat{ID: -5, Type: "channel"}, Text: "post"}, false, false, "channel"},
		{"allowed_topics miss", func(c *config.TelegramConfig) { c.AllowedTopics = []string{"5"} }, topic("7", "hi"), false, false, "allowed_topics"},
		{"missing topic counts as 1", func(c *config.TelegramConfig) { c.AllowedTopics = []string{"1"} }, forumNoThread("hi"), true, false, "require_mention_off"},
		{"ignored thread", func(c *config.TelegramConfig) { c.IgnoredThreads = []string{"7"} }, topic("7", "hi"), false, false, "ignored_threads"},
		{"other bot mentioned", nil, group("@otherbot do it"), false, false, "exclusive_bot_mentions"},
		{"cmd at other bot", nil, group("/new@otherbot"), false, false, "exclusive_bot_mentions"},
		{"chat not allowed", func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"} }, groupIn("-200", "hi"), false, false, "allowed_chats"},
		{"guest mode mention", func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"}; c.GuestMode = true }, groupIn("-200", "@tether_bot hi"), true, false, "mention"},
		{"free response", func(c *config.TelegramConfig) { c.RequireMention = true; c.FreeResponseChats = []string{"-100"} }, groupIn("-100", "hi"), true, false, "free_response_chats"},
		{"require_mention off default", nil, group("hi"), true, false, "require_mention_off"},
		{"reply to bot", func(c *config.TelegramConfig) { c.RequireMention = true }, replyToBot("hi"), true, false, "reply_to_bot"},
		{"mention", func(c *config.TelegramConfig) { c.RequireMention = true }, group("@tether_bot hi"), true, false, "mention"},
		{"pattern", func(c *config.TelegramConfig) { c.RequireMention = true; c.MentionPatterns = []string{"^tether[,:]"} }, group("Tether, hi"), true, false, "mention_patterns"},
		{"drop + observe", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.ObserveUnmentionedGroupMessages = true
			c.GroupAllowedChats = []string{"-100"}
		}, groupIn("-100", "chatter"), false, true, "observe"},
		{"cmd needs mention when required", func(c *config.TelegramConfig) { c.RequireMention = true }, group("/new"), false, false, "drop"},
		{"cmd at this bot", func(c *config.TelegramConfig) { c.RequireMention = true }, group("/new@tether_bot"), true, false, "mention"},
	}
	for _, c := range cases {
		cfg := base
		if c.mut != nil {
			c.mut(&cfg)
		}
		d := Gate(cfg, "tether_bot", 99, c.msg, Normalize(c.msg))
		if d.Accept != c.accept || d.Observe != c.observe || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestGateAcceptedTextHasMentionStripped(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	m := group("@tether_bot, scrape it")
	if d := Gate(cfg, "tether_bot", 99, m, Normalize(m)); !d.Accept || d.Text != "scrape it" {
		t.Fatalf("%+v", d)
	}
	// A caption is the text of a media message.
	m = Message{Chat: Chat{ID: -100, Type: "group"}, From: ana(), Caption: "photo for @tether_bot", Photo: []PhotoSize{{FileID: "p"}}}
	m.CaptionEntities = autoEntities(m.Caption)
	if d := Gate(cfg, "tether_bot", 99, m, Normalize(m)); !d.Accept || d.Reason != "mention" || d.Text != "photo for" {
		t.Fatalf("%+v", d)
	}
}

func TestStripMention(t *testing.T) {
	if got := StripMention("@Tether_Bot, scrape it", "tether_bot"); got != "scrape it" {
		t.Fatal(got)
	}
	if got := StripMention("@tether_bot", "tether_bot"); got != "@tether_bot" {
		t.Fatal("empty → keep original:", got)
	}
	if got := StripMention("ask @tether_bot: now", "tether_bot"); got != "ask now" {
		t.Fatal(got)
	}
	if got := StripMention("@tether_botx hi", "tether_bot"); got != "@tether_botx hi" {
		t.Fatal("other handle must stay:", got)
	}
}

func TestObservedTextFormat(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	cfg.ObserveUnmentionedGroupMessages = true
	cfg.GroupAllowedChats = []string{"-100"}
	m := group("chatter")
	d := Gate(cfg, "tether_bot", 99, m, Normalize(m))
	if !d.Observe || d.Text != "[Ana|123]\nchatter" {
		t.Fatalf("%+v", d)
	}
}

func TestNormalizeThreadRules(t *testing.T) {
	cases := []struct {
		name   string
		msg    Message
		kind   ChatKind
		thread string
	}{
		{"forum topic", topic("17585", "x"), KindGroup, "17585"},
		{"forum no thread gets General", forumNoThread("x"), KindGroup, "1"},
		{"plain group reply anchor dropped", func() Message { m := group("x"); m.MessageThreadID = 55; return m }(), KindGroup, ""},
		{"group topic message kept", func() Message { m := group("x"); m.MessageThreadID = 55; m.IsTopicMessage = true; return m }(), KindGroup, "55"},
		{"dm thread dropped", func() Message { m := dm("x"); m.MessageThreadID = 9; return m }(), KindDM, ""},
		{"dm topic kept", func() Message { m := dm("x"); m.MessageThreadID = 9; m.IsTopicMessage = true; return m }(), KindDM, "9"},
		{"channel", Message{Chat: Chat{ID: -5, Type: "channel", Title: "News"}}, KindChannel, ""},
	}
	for _, c := range cases {
		n := Normalize(c.msg)
		if n.Kind != c.kind || n.ThreadID != c.thread {
			t.Errorf("%s: %+v", c.name, n)
		}
	}
	m := group("hi")
	m.From = &User{ID: 7, FirstName: "Alice", LastName: "Example"}
	n := Normalize(m)
	if n.ChatID != "-100" || n.UserID != "7" || n.UserName != "Alice Example" || n.Text != "hi" {
		t.Fatalf("%+v", n)
	}
	n = Normalize(Message{Chat: Chat{ID: 5, Type: "private", FirstName: "Bo"}, Caption: "cap"})
	if n.UserID != "5" || n.UserName != "Bo" || n.Text != "cap" {
		t.Fatalf("no sender: %+v", n)
	}
}

// The cases below are ported from Hermes tests/gateway/test_telegram_group_gating.py
// and test_telegram_mention_boundaries.py. The bot is @hermes_bot with id 999.
func TestGateHermesCases(t *testing.T) {
	type mut = func(*config.TelegramConfig)
	reqMention := func(c *config.TelegramConfig) { c.RequireMention = true }
	withReply := func(m Message) Message {
		m.ReplyToMessage = &Message{MessageID: 10, From: &User{ID: 999}, Chat: m.Chat}
		return m
	}
	thread := func(m Message, id int) Message {
		m.MessageThreadID = id
		m.IsTopicMessage = true
		m.Chat.IsForum = true
		return m
	}
	cases := []struct {
		name   string
		bot    string
		mut    mut
		msg    Message
		accept bool
	}{
		{"open group", "hermes_bot", nil, group("hello everyone"), true},
		{"require mention drops chatter", "hermes_bot", reqMention, group("hello everyone"), false},
		{"require mention accepts mention", "hermes_bot", reqMention, group("hi @hermes_bot"), true},
		{"require mention accepts reply", "hermes_bot", reqMention, withReply(group("replying")), true},
		{"bare command dropped", "hermes_bot", reqMention, group("/status"), false},
		{"command at this bot", "hermes_bot", reqMention, group("/status@hermes_bot"), true},
		{"command at other bot", "hermes_bot", reqMention, group("/status@other_bot"), false},
		{"command open group", "hermes_bot", nil, group("/status"), true},
		{"multi bot: not named, reply ignored", "default_bot", reqMention, withReply(group("@research_bot @ops_bot hi")), false},
		{"multi bot: first named", "research_bot", reqMention, group("@research_bot @ops_bot hi"), true},
		{"multi bot: second named", "ops_bot", reqMention, group("@research_bot @ops_bot hi"), true},
		{"multi bot no entities: not named", "default_bot", reqMention, withReply(groupRaw("@research_bot @ops_bot hi")), false},
		{"multi bot no entities: named", "ops_bot", reqMention, groupRaw("@research_bot @ops_bot hi"), true},
		{"intern bot other", "Interntestnumber2bot", nil, withReply(groupRaw("@Interntestnumber1bot you're not supposed to do the blog")), false},
		{"intern bot self", "Interntestnumber1bot", nil, groupRaw("@Interntestnumber1bot you're not supposed to do the blog"), true},
		{"command at other intern bot", "Interntestnumber2bot", nil, group("/stop@Interntestnumber1bot"), false},
		{"command at self intern bot", "Interntestnumber1bot", nil, group("/stop@Interntestnumber1bot"), true},
		{"email is not a mention", "hermes_bot", reqMention, groupRaw("email ops@hermes_bot.example"), false},
		{"prefix is not a mention", "hermes_bot", reqMention, groupRaw("prefix@hermes_bot hi"), false},
		{"raw mention fallback", "hermes_bot", reqMention, groupRaw("hi @hermes_bot"), true},
		{"superstring handle", "hermes_bot", reqMention, groupRaw("@hermes_botx hello"), false},
		{"underscore suffix", "hermes_bot", reqMention, groupRaw("see @hermes_bot_admin for help"), false},
		{"handle in URL", "hermes_bot", reqMention, groupRaw("see https://example.com/@hermes_bot for details"), false},
		{"handle in code", "hermes_bot", reqMention, groupRaw("use the string `@hermes_bot` in config"), false},
		{"upper-case mention", "hermes_bot", reqMention, group("hi @HERMES_BOT"), true},
		{"exclusive off: legacy groups", "default_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.ExclusiveBotMentions = false }, withReply(groupRaw("@research_bot hi")), true},
		{"free response chat", "hermes_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.FreeResponseChats = []string{"-200"} }, groupIn("-200", "hello everyone"), true},
		{"free response other chat", "hermes_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.FreeResponseChats = []string{"-200"} }, groupIn("-201", "hello everyone"), false},
		{"guest: mention outside allowed", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.AllowedChats = []string{"-200"}
			c.GuestMode = true
			c.MentionPatterns = []string{`^\s*chompy\b`}
		}, groupIn("-201", "hi @hermes_bot"), true},
		{"guest: reply outside allowed", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.AllowedChats = []string{"-200"}
			c.GuestMode = true
		}, withReply(groupIn("-201", "reply")), false},
		{"guest: pattern outside allowed", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.AllowedChats = []string{"-200"}
			c.GuestMode = true
			c.MentionPatterns = []string{`^\s*chompy\b`}
		}, groupIn("-201", "chompy status"), false},
		{"guest off: mention outside allowed", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.AllowedChats = []string{"-200"}
		}, groupIn("-201", "hi @hermes_bot"), false},
		{"guest mention in ignored thread", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.AllowedChats = []string{"-200"}
			c.GuestMode = true
			c.IgnoredThreads = []string{"42"}
		}, thread(groupIn("-201", "hi @hermes_bot"), 42), false},
		{"ignored thread 31", "hermes_bot", func(c *config.TelegramConfig) {
			c.FreeResponseChats = []string{"-200"}
			c.IgnoredThreads = []string{"31", "42"}
		}, thread(groupIn("-200", "hello everyone"), 31), false},
		{"other thread 99", "hermes_bot", func(c *config.TelegramConfig) {
			c.FreeResponseChats = []string{"-200"}
			c.IgnoredThreads = []string{"31", "42"}
		}, thread(groupIn("-200", "hello everyone"), 99), true},
		{"allowed topic 8", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"}; c.AllowedTopics = []string{"8"} }, thread(group("hello"), 8), true},
		{"other topic 11", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"}; c.AllowedTopics = []string{"8"} }, thread(group("hello"), 11), false},
		{"mention in other topic 11", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"}; c.AllowedTopics = []string{"8"} }, thread(group("hi @hermes_bot"), 11), false},
		{"allowed topics skip DMs", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedTopics = []string{"8"} }, dm("hello"), true},
		{"missing thread is General", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedTopics = []string{"1"} }, group("hello"), true},
		{"topic 8 not General", "hermes_bot", func(c *config.TelegramConfig) { c.AllowedTopics = []string{"1"} }, thread(group("hello"), 8), false},
		{"wake word", "hermes_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.MentionPatterns = []string{`^\s*chompy\b`} }, group("chompy status"), true},
		{"wake word with spaces", "hermes_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.MentionPatterns = []string{`^\s*chompy\b`} }, group("   chompy help"), true},
		{"wake word not at start", "hermes_bot", func(c *config.TelegramConfig) { c.RequireMention = true; c.MentionPatterns = []string{`^\s*chompy\b`} }, group("hey chompy"), false},
		{"invalid pattern ignored", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.MentionPatterns = []string{`(`, `^\s*chompy\b`}
		}, group("chompy status"), true},
		{"invalid pattern no match", "hermes_bot", func(c *config.TelegramConfig) {
			c.RequireMention = true
			c.MentionPatterns = []string{`(`, `^\s*chompy\b`}
		}, group("hello everyone"), false},
	}
	for _, c := range cases {
		cfg := config.Default().Telegram
		if c.mut != nil {
			c.mut(&cfg)
		}
		d := Gate(cfg, c.bot, 999, c.msg, Normalize(c.msg))
		if d.Accept != c.accept {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestGateMentionEntities(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	gate := func(m Message) bool { return Gate(cfg, "hermes_bot", 999, m, Normalize(m)).Accept }

	// text_mention targets a user with no public handle.
	m := group("hey you")
	m.Entities = []MessageEntity{{Type: "text_mention", Offset: 4, Length: 3, User: &User{ID: 999}}}
	if !gate(m) {
		t.Fatal("text_mention of the bot")
	}
	m.Entities[0].User.ID = 12345
	if gate(m) {
		t.Fatal("text_mention of another user")
	}
	// Malformed entities do not match, and with entities present the raw-text
	// fallback is not used.
	for _, e := range []MessageEntity{{Type: "mention", Offset: -1, Length: 11}, {Type: "mention", Offset: 0, Length: 0}, {Type: "mention", Offset: 50, Length: 5}} {
		m := group("@hermes_bot hi")
		m.Entities = []MessageEntity{e}
		if gate(m) {
			t.Fatalf("malformed entity %+v", e)
		}
	}
	// Entity offsets count UTF-16 units: an emoji before the mention.
	m = group("😀 @hermes_bot hi")
	if m.Entities[0].Offset != 3 || !gate(m) {
		t.Fatalf("emoji offset: %+v", m.Entities)
	}
}

func TestObserveRules(t *testing.T) {
	observe := func(mut func(*config.TelegramConfig), m Message) Decision {
		cfg := config.Default().Telegram
		cfg.RequireMention = true
		cfg.ObserveUnmentionedGroupMessages = true
		mut(&cfg)
		return Gate(cfg, "hermes_bot", 999, m, Normalize(m))
	}
	// Hermes: observe needs an explicit group_allowed_chats entry.
	if d := observe(func(c *config.TelegramConfig) { c.AllowedChats = []string{"-100"} }, group("side chatter")); d.Observe {
		t.Fatal("no group_allowed_chats", d)
	}
	// With allowed_chats set, the chat must be in both lists.
	if d := observe(func(c *config.TelegramConfig) {
		c.AllowedChats = []string{"-200"}
		c.GroupAllowedChats = []string{"-200"}
	}, groupIn("-201", "side chatter")); d.Observe {
		t.Fatal("chat outside the lists", d)
	}
	d := observe(func(c *config.TelegramConfig) {
		c.AllowedChats = []string{"-100"}
		c.GroupAllowedChats = []string{"-100"}
	}, group("side chatter"))
	if !d.Observe || d.Accept || d.Text != "[Ana|123]\nside chatter" {
		t.Fatal("observe", d)
	}
	// A slash command is never observed.
	if d := observe(func(c *config.TelegramConfig) { c.GroupAllowedChats = []string{"-100"} }, group("/status")); d.Observe {
		t.Fatal("command observed", d)
	}
}

func TestGateNoContent(t *testing.T) {
	base := config.Default().Telegram // require_mention is false
	empty := func(m Message) Message { m.Text, m.Entities = "", nil; return m }
	withMedia := func(f func(*Message)) Message { m := empty(group("")); f(&m); return m }
	cases := []struct {
		name   string
		msg    Message
		accept bool
		reason string
	}{
		{"dm service", empty(dm("")), false, "no_content"},
		{"group join", empty(group("")), false, "no_content"},
		{"forum service", empty(forumNoThread("")), false, "no_content"},
		{"photo no caption", withMedia(func(m *Message) { m.Photo = sizes(320) }), true, "require_mention_off"},
		{"album part", withMedia(func(m *Message) { m.Photo, m.MediaGroupID = sizes(320), "g1" }), true, "require_mention_off"},
		{"voice", withMedia(func(m *Message) { m.Voice = &Voice{FileID: "v"} }), true, "require_mention_off"},
		{"document", withMedia(func(m *Message) { m.Document = &Document{FileID: "d"} }), true, "require_mention_off"},
		{"caption only", withMedia(func(m *Message) { m.Caption = "look" }), true, "require_mention_off"},
		{"bare mention", group("@tether_bot"), true, "require_mention_off"},
		{"video", withMedia(func(m *Message) { m.Video = &Video{FileID: "v"} }), true, "require_mention_off"},
		{"audio", withMedia(func(m *Message) { m.Audio = &Audio{FileID: "a"} }), true, "require_mention_off"},
		{"sticker", withMedia(func(m *Message) { m.Sticker = &Sticker{FileID: "s"} }), true, "require_mention_off"},
		{"location", withMedia(func(m *Message) { m.Location = &Location{Latitude: 1} }), true, "require_mention_off"},
		{"venue", withMedia(func(m *Message) { m.Venue = &Venue{Title: "x"} }), true, "require_mention_off"},
	}
	for _, c := range cases {
		d := Gate(base, "tether_bot", 99, c.msg, Normalize(c.msg))
		if d.Accept != c.accept || d.Observe || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestMediaPresentCoversEveryType(t *testing.T) {
	if mediaPresent(Message{}) || hasMedia([]Message{{Text: "hi"}}) {
		t.Fatal("no media expected")
	}
	for _, m := range []Message{{Photo: sizes(1)}, {Document: &Document{}}, {Voice: &Voice{}}, {Video: &Video{}},
		{Audio: &Audio{}}, {Sticker: &Sticker{}}, {Location: &Location{}}, {Venue: &Venue{}}} {
		if !mediaPresent(m) || !hasMedia([]Message{m}) {
			t.Errorf("%+v: media expected", m)
		}
	}
}

func TestRichContentCheck(t *testing.T) {
	cases := []struct {
		name, blocks   string
		content, media bool
		text           string
	}{
		{"text", `[{"type":"paragraph","text":"hello"}]`, true, false, "hello"},
		{"divider only", `[{"type":"divider"}]`, false, false, ""},
		{"empty heading", `[{"type":"heading","size":2,"text":""}]`, false, false, ""},
		{"no blocks", `[]`, false, false, ""},
		{"photo only", `[{"type":"photo","photo":[{"file_id":"p1"}]}]`, true, true, ""},
		{"photo with a caption", `[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"look"}}]`, true, true, "[photo]\nlook"},
		{"photo with no usable object", `[{"type":"photo","photo":[]}]`, false, false, ""},
		{"bold mention", `[{"type":"paragraph","text":[{"type":"bold","text":"@tether_bot"}," hi"]}]`, true, false, "**@tether_bot** hi"},
	}
	for _, c := range cases {
		m := richMsg(c.blocks)
		m.MessageID, m.Chat, m.From = 1, Chat{ID: 123, Type: "private", FirstName: "Ana"}, ana()
		if got := hasContent(m); got != c.content {
			t.Errorf("%s: hasContent = %v, want %v", c.name, got, c.content)
		}
		if got := mediaPresent(m); got != c.media {
			t.Errorf("%s: mediaPresent = %v, want %v", c.name, got, c.media)
		}
		if got := hasMedia([]Message{m}); got != c.media {
			t.Errorf("%s: hasMedia = %v, want %v", c.name, got, c.media)
		}
		if got := Normalize(m).Text; got != c.text {
			t.Errorf("%s: n.Text = %q, want %q", c.name, got, c.text)
		}
	}
}

func TestRichContentGate(t *testing.T) {
	base := config.Default().Telegram // require_mention is false
	rich := func(m Message, blocks string) Message {
		m.Text, m.Entities = "", nil
		m.RichMessage = richMsg(blocks).RichMessage
		return m
	}
	cases := []struct {
		name string
		msg  Message
		want Decision
	}{
		{"dm text", rich(dm(""), `[{"type":"heading","size":1,"text":"Report"},{"type":"paragraph","text":["Plain, ",{"type":"bold","text":"bold"}]}]`),
			Decision{Accept: true, Text: "# Report\n\nPlain, **bold**", Reason: "dm"}},
		{"dm divider only", rich(dm(""), `[{"type":"divider"}]`), Decision{Reason: "no_content"}},
		{"group divider only", rich(group(""), `[{"type":"divider"}]`), Decision{Reason: "no_content"}},
		{"dm photo only", rich(dm(""), `[{"type":"photo","photo":[{"file_id":"p1"}]}]`), Decision{Accept: true, Reason: "dm"}},
		{"group text", rich(group(""), `[{"type":"paragraph","text":"hello"}]`),
			Decision{Accept: true, Text: "hello", Reason: "require_mention_off"}},
		{"bold mention", rich(dm(""), `[{"type":"paragraph","text":[{"type":"bold","text":"@tether_bot"}," hi"]}]`),
			Decision{Accept: true, Text: "hi", Reason: "dm"}},
		{"mention at the end", rich(dm(""), `[{"type":"paragraph","text":"what now @tether_bot"}]`),
			Decision{Accept: true, Text: "what now", Reason: "dm"}},
		{"only the mention", rich(dm(""), `[{"type":"paragraph","text":"@tether_bot"}]`),
			Decision{Accept: true, Text: "@tether_bot", Reason: "dm"}},
		{"bold command", rich(dm(""), `[{"type":"paragraph","text":{"type":"bold","text":"/new"}}]`),
			Decision{Accept: true, Text: "**/new**", Reason: "dm"}},
		{"photo with a caption for the bot", rich(dm(""), `[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"@tether_bot look"}}]`),
			Decision{Accept: true, Text: "[photo]\nlook", Reason: "dm"}},
	}
	for _, c := range cases {
		if d := Gate(base, "tether_bot", 99, c.msg, Normalize(c.msg)); d != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, d, c.want)
		}
	}
}

func TestRichContentIgnoredWithText(t *testing.T) {
	base := config.Default().Telegram
	raw := richMsg(`[{"type":"paragraph","text":"rich"},{"type":"photo","photo":[{"file_id":"p1"}]}]`).RichMessage

	// A message with text: each reader ignores the rich message, also its media.
	m := dm("typed")
	m.RichMessage = raw
	n := Normalize(m)
	if n.Text != "typed" || mediaPresent(m) || !hasContent(m) {
		t.Errorf("text: n.Text = %q, mediaPresent = %v, hasContent = %v", n.Text, mediaPresent(m), hasContent(m))
	}
	if d := Gate(base, "tether_bot", 99, m, n); d != (Decision{Accept: true, Text: "typed", Reason: "dm"}) {
		t.Errorf("text: %+v", d)
	}

	// A message with a caption and no classic media field.
	m = Message{MessageID: 1, Chat: Chat{ID: 123, Type: "private"}, From: ana(), Caption: "cap", RichMessage: raw}
	if n := Normalize(m); n.Text != "cap" || mediaPresent(m) || !hasContent(m) {
		t.Errorf("caption: n.Text = %q, mediaPresent = %v, hasContent = %v", n.Text, mediaPresent(m), hasContent(m))
	}
}

func TestRichContentRegression(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	join := Message{MessageID: 3, Chat: Chat{ID: -100, Type: "supergroup", Title: "G"}, From: ana(), NewChatMembers: []User{{ID: 5, FirstName: "Bo"}}}
	photo := Message{Chat: Chat{ID: -100, Type: "group"}, From: ana(), Caption: "photo for @tether_bot", Photo: []PhotoSize{{FileID: "p"}}}
	photo.CaptionEntities = autoEntities(photo.Caption)
	cases := []struct {
		name string
		msg  Message
		want Decision
	}{
		{"service message", join, Decision{Reason: "no_content"}},
		{"dm plain text", dm("hi"), Decision{Accept: true, Text: "hi", Reason: "dm"}},
		{"dm plain text with spaces", dm("  hi  "), Decision{Accept: true, Text: "hi", Reason: "dm"}},
		{"group mention", group("@tether_bot, scrape it"), Decision{Accept: true, Text: "scrape it", Reason: "mention"}},
		{"group text with no mention", group("chatter"), Decision{Reason: "drop"}},
		{"photo with a caption", photo, Decision{Accept: true, Text: "photo for", Reason: "mention"}},
	}
	for _, c := range cases {
		if d := Gate(cfg, "tether_bot", 99, c.msg, Normalize(c.msg)); d != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, d, c.want)
		}
	}
	if n := Normalize(photo); n.Text != "photo for @tether_bot" || !mediaPresent(photo) {
		t.Errorf("photo: n.Text = %q, mediaPresent = %v", n.Text, mediaPresent(photo))
	}
}

// richInGroup is a group message from Ana whose only content is a rich message with the
// given blocks. Text and Caption are empty.
func richInGroup(blocksJSON string) Message {
	m := richMsg(blocksJSON)
	m.MessageID, m.Chat, m.From = 2, Chat{ID: -100, Type: "supergroup", Title: "G"}, ana()
	return m
}

func TestGateRichAddressing(t *testing.T) {
	const bot32 = "abcdefghijklmnopqrstuvwxyz012bot"
	if len(bot32) != 32 {
		t.Fatal(len(bot32))
	}
	const photo = `{"type":"photo","photo":[{"file_id":"p1","file_unique_id":"u1","width":320,"height":240}],` +
		`"caption":{"text":"look @tether_bot"}}`
	type mut = func(*config.TelegramConfig)
	cases := []struct {
		name   string
		bot    string
		mut    mut
		blocks string
		accept bool
		reason string
	}{
		{"mention span", "tether_bot", nil,
			`[{"type":"paragraph","text":[{"type":"mention","text":"@tether_bot","username":"tether_bot"}," hello"]}]`,
			true, "mention"},
		{"plain string", "tether_bot", nil,
			`[{"type":"paragraph","text":"hello @tether_bot"}]`, true, "mention"},
		{"text_mention of the bot id", "tether_bot", nil,
			`[{"type":"paragraph","text":["hey ",{"type":"text_mention","text":"Tether","user":{"id":42,"is_bot":true,"first_name":"Tether"}}]}]`,
			true, "mention"},
		{"text_mention of another user", "tether_bot", nil,
			`[{"type":"paragraph","text":["hey ",{"type":"text_mention","text":"Bo","user":{"id":7,"first_name":"Bo"}}]}]`,
			false, "drop"},
		{"command at the bot", "tether_bot", nil,
			`[{"type":"paragraph","text":"/status@tether_bot"}]`, true, "mention"},
		{"command with no bot", "tether_bot", nil,
			`[{"type":"paragraph","text":"/status"}]`, false, "drop"},
		{"code span", "tether_bot", nil,
			`[{"type":"paragraph","text":["use ",{"type":"code","text":"@tether_bot"}]}]`, false, "drop"},
		{"pre block", "tether_bot", nil,
			`[{"type":"pre","language":"go","text":"@tether_bot"}]`, false, "drop"},
		{"url label", "tether_bot", nil,
			`[{"type":"paragraph","text":["see ",{"type":"url","text":"@tether_bot","url":"https://example.com/"}]}]`,
			false, "drop"},
		{"url value", "tether_bot", nil,
			`[{"type":"paragraph","text":["see ",{"type":"url","text":"the page","url":"https://example.com/?u=@tether_bot"}]}]`,
			false, "drop"},
		{"photo caption", "tether_bot", nil, `[` + photo + `]`, true, "mention"},
		{"emoji before the mention", "tether_bot", nil,
			`[{"type":"paragraph","text":"😀 @tether_bot hi"}]`, true, "mention"},
		{"user name of 32 characters", bot32, nil,
			`[{"type":"paragraph","text":"hi @` + bot32 + `"}]`, true, "mention"},
		{"other bot only", "tether_bot", func(c *config.TelegramConfig) { c.ExclusiveBotMentions = true },
			`[{"type":"paragraph","text":"@other_bot do it"}]`, false, "exclusive_bot_mentions"},
		{"command at another bot", "tether_bot", func(c *config.TelegramConfig) { c.ExclusiveBotMentions = true },
			`[{"type":"paragraph","text":"/status@other_bot"}]`, false, "exclusive_bot_mentions"},
		{"pattern in a paragraph", "tether_bot", func(c *config.TelegramConfig) { c.MentionPatterns = []string{`^hey tether`} },
			`[{"type":"paragraph","text":[{"type":"bold","text":"hey"}," tether, status"]}]`, true, "mention_patterns"},
		{"pattern only in a hidden URL", "tether_bot", func(c *config.TelegramConfig) { c.MentionPatterns = []string{`chompy`} },
			`[{"type":"paragraph","text":["see ",{"type":"url","text":"the page","url":"https://example.com/chompy"}]}]`,
			false, "drop"},
	}
	for _, c := range cases {
		cfg := config.Default().Telegram
		cfg.RequireMention = true
		if c.mut != nil {
			c.mut(&cfg)
		}
		m := richInGroup(c.blocks)
		d := Gate(cfg, c.bot, 42, m, Normalize(m))
		if d.Accept != c.accept || d.Observe || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestSourcesRichMessage(t *testing.T) {
	m := richInGroup(`[{"type":"paragraph","text":"😀 @tether_bot hi"}]`)
	srcs := sources(m)
	if len(srcs) != 3 {
		t.Fatalf("want 3 sources, got %d", len(srcs))
	}
	s := srcs[2]
	if s.text != "😀 @tether_bot hi" || len(s.entities) != 1 {
		t.Fatalf("%q %+v", s.text, s.entities)
	}
	// The emoji is 2 UTF-16 code units, so the mention starts at offset 3.
	if e := s.entities[0]; e.Type != "mention" || e.Offset != 3 || e.Length != 11 {
		t.Fatalf("%+v", e)
	}
	// A text_mention entity keeps the user of its span.
	tm := richInGroup(`[{"type":"paragraph","text":["hey ",{"type":"text_mention","text":"Tether","user":{"id":42,"is_bot":true,"first_name":"Tether"}}]}]`)
	var user *User
	for _, e := range sources(tm)[2].entities {
		if e.Type == "text_mention" {
			user = e.User
		}
	}
	if user == nil || user.ID != 42 {
		t.Fatalf("%+v", user)
	}
	// When Text is not empty, the rich content is not active.
	m.Text = "hello"
	if n := len(sources(m)); n != 2 {
		t.Fatalf("a message with a text: %d sources", n)
	}
	if n := len(sources(group("hi"))); n != 2 {
		t.Fatalf("a plain message: %d sources", n)
	}
}

func TestIsCommandRich(t *testing.T) {
	cases := map[string]bool{
		`[{"type":"paragraph","text":"/new"}]`:                                 true,
		`[{"type":"paragraph","text":[{"type":"bold","text":"/new"}]}]`:        true,
		`[{"type":"paragraph","text":"say /new"}]`:                             false,
		`[{"type":"paragraph","text":["run ",{"type":"code","text":"/new"}]}]`: false,
		`[{"type":"pre","text":"/new"}]`:                                       false,
	}
	for blocks, want := range cases {
		if got := isCommand(richInGroup(blocks)); got != want {
			t.Errorf("%s: got %v, want %v", blocks, got, want)
		}
	}
}

func TestGateRichObserve(t *testing.T) {
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	cfg.ObserveUnmentionedGroupMessages = true
	cfg.GroupAllowedChats = []string{"-100"}
	gate := func(blocks string) (Decision, Normalized) {
		m := richInGroup(blocks)
		n := Normalize(m)
		return Gate(cfg, "tether_bot", 42, m, n), n
	}
	// The observed text is the Markdown text with the sender line.
	d, _ := gate(`[{"type":"heading","size":1,"text":"Report"},{"type":"paragraph","text":["some ",{"type":"bold","text":"chatter"}]}]`)
	if !d.Observe || d.Accept || d.Reason != "observe" || d.Text != "[Ana|123]\n# Report\n\nsome **chatter**" {
		t.Fatalf("%+v", d)
	}
	// A message that starts with a command is not observed, also a command with a style.
	for _, blocks := range []string{
		`[{"type":"paragraph","text":"/new"}]`,
		`[{"type":"paragraph","text":[{"type":"bold","text":"/new"}]}]`,
		`[{"type":"paragraph","text":[{"type":"bot_command","text":"/new","bot_command":"/new"}," please"]}]`,
	} {
		if d, _ := gate(blocks); d.Observe || d.Accept || d.Reason != "drop" {
			t.Fatalf("%s: %+v", blocks, d)
		}
	}
	// A rich message with only a photo has no text. Gate gives Observe, and Channel.observe
	// saves nothing for an empty n.Text.
	d, n := gate(`[{"type":"photo","photo":[{"file_id":"p1","file_unique_id":"u1","width":320,"height":240}]}]`)
	if !d.Observe || d.Accept || d.Reason != "observe" || n.Text != "" {
		t.Fatalf("%+v %+v", d, n)
	}
}

func TestGateRichCost(t *testing.T) {
	text, err := json.Marshal(strings.Repeat("@ab ", 8192)) // 32768 characters
	if err != nil {
		t.Fatal(err)
	}
	m := richInGroup(`[{"type":"paragraph","text":` + string(text) + `}]`)
	cfg := config.Default().Telegram
	cfg.RequireMention = true
	cfg.ExclusiveBotMentions = true
	start := time.Now()
	d := Gate(cfg, "tether_bot", 42, m, Normalize(m))
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("Gate took %v for 8192 mentions. The gate work must be linear.", took)
	}
	if d.Accept || d.Observe || d.Reason != "drop" {
		t.Fatalf("%+v", d)
	}
}
