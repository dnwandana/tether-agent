package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
)

// heldTTL is the life of a held original. heldMax is the number of entries.
const (
	heldTTL = 10 * time.Minute
	heldMax = 32
)

// originalPrefix is the callback data prefix of the "post the full answer" button.
const originalPrefix = "ob"

// The texts of the held original.
const (
	postOriginalButton = "✅ Post the full answer there"
	postedText         = "Posted."
	originalGoneText   = "This text is gone. Ask me again."
)

// heldOriginal is one kept answer.
type heldOriginal struct {
	chatID   int64
	threadID string
	text     string
	files    []string
	at       time.Time
	noteChat int64
	noteMsg  int
}

// HeldOriginals keeps the original text and files of a masked answer in memory, so the owner
// can post them with one tap. It is safe for concurrent use. The zero value is ready.
type HeldOriginals struct {
	mu    sync.Mutex
	items map[string]*heldOriginal
	order []string
	// now is a test hook. Nil uses time.Now.
	now func() time.Time
}

func (h *HeldOriginals) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// dropLocked removes the expired entries and the ids that have no entry. The caller holds mu.
func (h *HeldOriginals) dropLocked() {
	now := h.clock()
	kept := h.order[:0]
	for _, id := range h.order {
		it := h.items[id]
		if it == nil {
			continue
		}
		if now.Sub(it.at) > heldTTL {
			delete(h.items, id)
			continue
		}
		kept = append(kept, id)
	}
	h.order = kept
}

// Put keeps the text and the files for the chat and the topic, and returns the id.
func (h *HeldOriginals) Put(chatID int64, threadID, text string, files []string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.items == nil {
		h.items = map[string]*heldOriginal{}
	}
	h.dropLocked()
	var id string
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id = hex.EncodeToString(b[:])
		if _, taken := h.items[id]; !taken {
			break
		}
	}
	for len(h.order) >= heldMax {
		delete(h.items, h.order[0])
		h.order = h.order[1:]
	}
	h.items[id] = &heldOriginal{chatID: chatID, threadID: threadID, text: text,
		files: append([]string(nil), files...), at: h.clock()}
	h.order = append(h.order, id)
	return id
}

// Take returns the entry and removes it. ok is false for an unknown or expired id.
func (h *HeldOriginals) Take(id string) (chatID int64, threadID, text string, files []string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked()
	it := h.items[id]
	if it == nil {
		return 0, "", "", nil, false
	}
	delete(h.items, id)
	return it.chatID, it.threadID, it.text, it.files, true
}

// setNote keeps the chat and the message of the owner note of an entry.
func (h *HeldOriginals) setNote(id string, chatID int64, msgID int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if it := h.items[id]; it != nil {
		it.noteChat, it.noteMsg = chatID, msgID
	}
}

// note returns the chat and the message of the owner note. ok is false for an unknown or
// expired id.
func (h *HeldOriginals) note(id string) (chatID int64, msgID int, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked()
	it := h.items[id]
	if it == nil {
		return 0, 0, false
	}
	return it.noteChat, it.noteMsg, true
}

// maskedNote returns the owner note of a masked answer, in common Markdown.
func maskedNote(title string, findings []security.Finding, files []string) string {
	var items []string
	for _, f := range findings {
		l := strings.TrimSuffix(strings.TrimPrefix(f.Label, "["), "]")
		items = append(items, "• "+gateway.EscapeMarkdown(l))
	}
	for _, f := range files {
		items = append(items, "• file "+gateway.EscapeMarkdown(filepath.Base(f)))
	}
	noun := "items"
	if len(items) == 1 {
		noun = "item"
	}
	return fmt.Sprintf("🛡️ I masked %d %s in my answer in \"%s\":\n%s", len(items), noun,
		gateway.EscapeMarkdown(title), strings.Join(items, "\n"))
}

// noteOwner finds the owner chat of userID and calls noteMasked. With no owner chat it logs
// one line and sends nothing.
func (ch *Channel) noteOwner(ctx context.Context, userID string, chatID int64, threadID, title, original string,
	findings []security.Finding, files []string, noSound bool) {
	if len(findings) == 0 && len(files) == 0 {
		return
	}
	owner, ok := ch.OwnerChat(ctx, userID)
	if !ok {
		ch.logger().Warn("telegram: no owner chat for the note of a masked answer", "chat", chatID)
		return
	}
	if title == "" {
		title = strconv.FormatInt(chatID, 10)
	}
	ch.noteMasked(ctx, owner, chatID, threadID, title, original, findings, files, noSound)
}

// noteMasked keeps the original and sends the note with the button to noteChat. findings
// must come from original. Only the secret findings are masked in the kept text.
func (ch *Channel) noteMasked(ctx context.Context, noteChat, chatID int64, threadID, title, original string,
	findings []security.Finding, files []string, noSound bool) {
	if ch.Held == nil {
		ch.logger().Warn("telegram: no store for the originals of a masked answer", "chat", chatID)
		return
	}
	var secrets []security.Finding
	for _, f := range findings {
		if f.Kind == "secret" {
			secrets = append(secrets, f)
		}
	}
	id := ch.Held.Put(chatID, threadID, security.RedactFindings(original, secrets), files)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: postOriginalButton, CallbackData: originalPrefix + ":" + id},
	}}}
	msgID, err := ch.sendText(ctx, noteChat, "", maskedNote(title, findings, files), sendOpts{keyboard: kb, noSound: noSound})
	if err != nil {
		ch.logger().Warn("telegram: cannot send the note of a masked answer", "chat", noteChat, "err", err.Error())
		return
	}
	ch.Held.setNote(id, noteChat, msgID)
}

// outboundCallback handles an "ob:" tap. It returns false for other data.
func (ch *Channel) outboundCallback(ctx context.Context, cq CallbackQuery) bool {
	kind, id, _, ok := parseCallback(cq.Data)
	if !ok || kind != originalPrefix {
		return false
	}
	answer := func(text string) {
		if err := ch.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			ch.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	if ch.Held == nil {
		answer(originalGoneText)
		return true
	}
	noteChat, noteMsg, found := ch.Held.note(id)
	if !found {
		answer(originalGoneText)
		return true
	}
	if !ch.callbackOwner(ctx, cq) {
		ch.logger().Warn("telegram: a button press from a user that is not an owner was refused", "user", cq.From.ID)
		answer(notAuthorized)
		return true
	}
	if cq.Message == nil || cq.Message.Chat.ID != noteChat {
		ch.logger().Warn("telegram: a button press from another chat was refused", "prompt", id)
		answer(wrongChat)
		return true
	}
	chatID, threadID, text, files, found := ch.Held.Take(id)
	if !found {
		answer(originalGoneText)
		return true
	}
	answer("")
	if strings.TrimSpace(text) != "" {
		if _, err := ch.sendText(ctx, chatID, threadID, text, sendOpts{}); err != nil {
			ch.logger().Warn("telegram: cannot post the original answer", "chat", chatID, "err", err.Error())
		}
	}
	if len(files) > 0 && ch.Out != nil {
		out := make([]OutFile, 0, len(files))
		for _, f := range files {
			out = append(out, OutFile{Path: f, Kind: mediaKind(f)})
		}
		if err := ch.Out.SendFiles(ctx, chatID, threadID, out); err != nil {
			ch.logger().Warn("telegram: cannot post the files of the original answer", "chat", chatID, "err", err.Error())
		}
	}
	if noteMsg == 0 {
		noteMsg = cq.Message.MessageID
	}
	if _, err := replyClient(ch.C).EditMessageText(ctx, EditMessageTextParams{
		ChatID: noteChat, MessageID: noteMsg, Text: postedText}); err != nil {
		ch.logger().Debug("telegram: cannot edit the note of a masked answer", "err", err.Error())
	}
	return true
}
