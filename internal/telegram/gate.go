package telegram

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/dnwandana/tether-agent/internal/config"
)

// ChatKind is the kind of chat that a message comes from.
type ChatKind string

// The chat kinds. A "group" and a "supergroup" are both KindGroup.
const (
	KindDM      ChatKind = "dm"
	KindGroup   ChatKind = "group"
	KindChannel ChatKind = "channel"
)

// Normalized holds the routing fields of a message as strings.
type Normalized struct {
	Kind     ChatKind
	ChatID   string
	ThreadID string
	UserID   string
	UserName string
	Text     string
	IsForum  bool
}

// Decision is the result of Gate. Accept means that the agent answers the
// message. Observe means that the message goes to the transcript only. Reason
// names the step that decided.
type Decision struct {
	Accept, Observe bool
	Text            string
	Reason          string
}

// Normalize converts a message to routing fields. It applies the thread id
// rules of Hermes _build_message_event: Telegram also sets
// message_thread_id for reply anchors, and these must not split sessions.
func Normalize(m Message) Normalized {
	n := Normalized{
		ChatID:  strconv.FormatInt(m.Chat.ID, 10),
		IsForum: m.Chat.IsForum,
		Text:    m.Text,
	}
	if n.Text == "" {
		n.Text = m.Caption
	}
	switch m.Chat.Type {
	case "group", "supergroup":
		n.Kind = KindGroup
	case "channel":
		n.Kind = KindChannel
	default:
		n.Kind = KindDM
	}
	if m.MessageThreadID != 0 {
		raw := strconv.Itoa(m.MessageThreadID)
		switch {
		case n.Kind == KindGroup && (m.IsTopicMessage || m.Chat.IsForum):
			n.ThreadID = raw
		case n.Kind == KindDM && m.IsTopicMessage:
			n.ThreadID = raw
		}
	}
	if n.Kind == KindGroup && n.ThreadID == "" && m.Chat.IsForum {
		n.ThreadID = generalTopicID
	}
	if m.From != nil {
		n.UserID = strconv.FormatInt(m.From.ID, 10)
		n.UserName = strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	} else {
		switch n.Kind {
		case KindDM:
			n.UserID = n.ChatID
			n.UserName = m.Chat.FirstName
		case KindChannel:
			n.UserID = n.ChatID
			n.UserName = m.Chat.Title
		}
	}
	return n
}

// mediaPresent reports if the message has a media field that Tether Agent
// reads. Gate and hasMedia use this one list.
func mediaPresent(m Message) bool {
	return len(m.Photo) > 0 || m.Document != nil || m.Voice != nil || m.Video != nil ||
		m.Audio != nil || m.Sticker != nil || m.Location != nil || m.Venue != nil
}

// hasContent reports if the message has text, a caption, or media. It reads m,
// not the normalized text, so a caption and a bare mention count.
func hasContent(m Message) bool {
	return m.Text != "" || m.Caption != "" || mediaPresent(m)
}

// Gate decides if the bot answers a message. It runs the gate steps of Hermes
// _should_process_message in order. botUser is the bot user name
// without "@", and botID is the bot user id.
func Gate(cfg config.TelegramConfig, botUser string, botID int64, m Message, n Normalized) Decision {
	accept := func(reason string) Decision {
		return Decision{Accept: true, Text: StripMention(n.Text, botUser), Reason: reason}
	}
	drop := func(reason string) Decision { return Decision{Reason: reason} }

	// 0) A service message or a type that Tether Agent does not read has no
	// content. It must not become an empty turn.
	if !hasContent(m) {
		return drop("no_content")
	}

	switch n.Kind {
	case KindDM:
		return accept("dm")
	case KindChannel:
		return drop("channel")
	}

	// 1) allowed_topics. Telegram leaves out the thread id for the General
	// topic, so a missing id counts as "1".
	rawThread := ""
	if m.MessageThreadID != 0 {
		rawThread = strconv.Itoa(m.MessageThreadID)
	}
	if len(cfg.AllowedTopics) > 0 {
		topicID := rawThread
		if topicID == "" {
			topicID = generalTopicID
		}
		if !inList(cfg.AllowedTopics, topicID) {
			return drop("allowed_topics")
		}
	}
	// 2) ignored_threads.
	if rawThread != "" && inList(cfg.IgnoredThreads, rawThread) {
		return drop("ignored_threads")
	}
	// 3) exclusive_bot_mentions: a message for another bot only.
	self := strings.ToLower(strings.TrimPrefix(botUser, "@"))
	if cfg.ExclusiveBotMentions && self != "" {
		named := botMentions(m)
		if len(named) > 0 && !slices.Contains(named, self) {
			return drop("exclusive_bot_mentions")
		}
	}
	// 4) allowed_chats, with the guest_mode exception for a direct mention.
	mentioned := mentionsBot(m, self, botID)
	guestMention := cfg.GuestMode && mentioned
	if len(cfg.AllowedChats) > 0 && !inList(cfg.AllowedChats, n.ChatID) {
		if guestMention {
			return accept("mention")
		}
		return drop("allowed_chats")
	}
	if guestMention {
		return accept("mention")
	}
	// 5) and 6) Chats that get an answer for every message.
	if inList(cfg.FreeResponseChats, n.ChatID) {
		return accept("free_response_chats")
	}
	if !cfg.RequireMention {
		return accept("require_mention_off")
	}
	// 7) to 9) Direct triggers.
	if r := m.ReplyToMessage; r != nil && r.From != nil && r.From.ID == botID {
		return accept("reply_to_bot")
	}
	if mentioned {
		return accept("mention")
	}
	if matchesPatterns(cfg.MentionPatterns, m) {
		return accept("mention_patterns")
	}
	// 10) Drop, or keep the message as context only. Hermes observes only
	// in chats that group_allowed_chats names, and never a command.
	if cfg.ObserveUnmentionedGroupMessages && observeAllowed(cfg, n.ChatID) && !isCommand(m) {
		user := n.UserID
		if user == "" {
			user = "unknown"
		}
		name := n.UserName
		if name == "" {
			name = user
		}
		return Decision{Observe: true, Text: "[" + name + "|" + user + "]\n" + n.Text, Reason: "observe"}
	}
	return drop("drop")
}

// observeAllowed reports whether observed context is allowed in the chat.
// The chat must be in group_allowed_chats, and also in allowed_chats when
// that list is set (Hermes _telegram_observe_allowed_chats).
func observeAllowed(cfg config.TelegramConfig, chatID string) bool {
	if !slices.Contains(trimmed(cfg.GroupAllowedChats), chatID) {
		return false
	}
	return len(cfg.AllowedChats) == 0 || inList(cfg.AllowedChats, chatID)
}

// isCommand reports whether the message starts with a bot command.
func isCommand(m Message) bool {
	for _, e := range m.Entities {
		if e.Type == "bot_command" && e.Offset == 0 {
			return true
		}
	}
	return false
}

func inList(list []string, v string) bool {
	return slices.Contains(trimmed(list), v)
}

func trimmed(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// botHandle matches a Telegram bot user name: 5 to 32 characters that end in
// "bot".
var botHandle = regexp.MustCompile(`(?i)^[a-z0-9_]{2,29}bot$`)

// rawBotMention finds "@...bot" handles in text that has no entities.
var rawBotMention = regexp.MustCompile(`(?i)@([a-z0-9_]{2,29}bot)\b`)

type textSource struct {
	text     string
	entities []MessageEntity
}

func sources(m Message) []textSource {
	return []textSource{{m.Text, m.Entities}, {m.Caption, m.CaptionEntities}}
}

// entityText returns the part of text that e marks. Telegram counts the
// offset and the length in UTF-16 code units.
func entityText(text string, e MessageEntity) (string, bool) {
	if e.Offset < 0 || e.Length <= 0 {
		return "", false
	}
	u := utf16.Encode([]rune(text))
	if e.Offset+e.Length > len(u) {
		return "", false
	}
	return string(utf16.Decode(u[e.Offset : e.Offset+e.Length])), true
}

// botMentions returns the lower-case bot user names that the message
// addresses: "mention" entities, "/cmd@bot" commands, and, for text with no
// entities, "@...bot" words (Hermes _extract_bot_mention_usernames).
func botMentions(m Message) []string {
	var out []string
	add := func(h string) {
		h = strings.ToLower(h)
		if botHandle.MatchString(h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	for _, src := range sources(m) {
		for _, e := range src.entities {
			if e.Type != "mention" && e.Type != "bot_command" {
				continue
			}
			s, ok := entityText(src.text, e)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if e.Type == "mention" {
				add(strings.TrimLeft(s, "@"))
			} else if i := strings.IndexByte(s, '@'); i >= 0 {
				add(strings.TrimSpace(s[i+1:]))
			}
		}
	}
	for _, src := range sources(m) {
		if src.text == "" || len(src.entities) > 0 {
			continue
		}
		for _, loc := range rawBotMention.FindAllStringSubmatchIndex(src.text, -1) {
			// An @ after a word character, "`", or "/" is an e-mail address,
			// code, or a URL path, not a mention.
			if loc[0] > 0 && noMentionBefore(src.text[loc[0]-1]) {
				continue
			}
			add(src.text[loc[2]:loc[3]])
		}
	}
	return out
}

// noMentionBefore reports whether an "@" after c is not a mention: c is a word
// character (an e-mail address), "`" (code), or "/" (a URL path).
func noMentionBefore(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '`' || c == '/'
}

// mentionsBot reports whether the message addresses this bot: a "mention"
// entity, a "text_mention" of the bot id, a "/cmd@bot" command, or a raw
// "@bot" word in text with no entities (Hermes _message_mentions_bot).
func mentionsBot(m Message, self string, botID int64) bool {
	want := "@" + self
	for _, src := range sources(m) {
		for _, e := range src.entities {
			switch e.Type {
			case "mention":
				if s, ok := entityText(src.text, e); ok && self != "" && strings.ToLower(strings.TrimSpace(s)) == want {
					return true
				}
			case "text_mention":
				if e.User != nil && e.User.ID == botID {
					return true
				}
			case "bot_command":
				s, ok := entityText(src.text, e)
				if !ok || self == "" {
					continue
				}
				if i := strings.IndexByte(s, '@'); i >= 0 && strings.ToLower(strings.TrimSpace(s[i:])) == want {
					return true
				}
			}
		}
	}
	if self != "" && botHandle.MatchString(self) {
		return slices.Contains(botMentions(m), self)
	}
	return false
}

// patternCache keeps the compiled mention_patterns, so each pattern is
// compiled once. An invalid pattern maps to nil and is skipped.
var patternCache sync.Map // string → *regexp.Regexp

func matchesPatterns(patterns []string, m Message) bool {
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		var re *regexp.Regexp
		if v, ok := patternCache.Load(p); ok {
			re = v.(*regexp.Regexp)
		} else {
			re, _ = regexp.Compile("(?i)" + p)
			patternCache.Store(p, re)
		}
		if re == nil {
			continue
		}
		for _, s := range []string{m.Text, m.Caption} {
			if s != "" && re.MatchString(s) {
				return true
			}
		}
	}
	return false
}

// mentionCache keeps the compiled strip regex for each bot user name.
var mentionCache sync.Map // string → *regexp.Regexp

// StripMention removes each "@<bot>" mention, with the "," ":" or "-" and the
// spaces after it, and trims the text. When nothing is left, it returns the
// original text.
func StripMention(text, botUser string) string {
	botUser = strings.TrimPrefix(botUser, "@")
	if text == "" || botUser == "" {
		return text
	}
	var re *regexp.Regexp
	if v, ok := mentionCache.Load(botUser); ok {
		re = v.(*regexp.Regexp)
	} else {
		re = regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(botUser) + `\b[,:\-]*\s*`)
		mentionCache.Store(botUser, re)
	}
	if cleaned := strings.TrimSpace(re.ReplaceAllString(text, "")); cleaned != "" {
		return cleaned
	}
	return text
}
