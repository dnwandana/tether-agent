package telegram

import (
	"strconv"
	"strings"
	"testing"

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
