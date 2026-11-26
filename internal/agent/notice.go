package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// NoteProviderBusy is the notice before the first retry wait of more than 10 s in one model
// call. The values are the entry, the cause, and the wait in seconds.
const NoteProviderBusy = "⏳ `%s` is busy (%s). The next try is in %d s."

// NoteQuotaUsedUp is the notice of a used-up plan quota with a known reset time. The values
// are the entry, the reset time, the next entry, and the model of the next entry.
const NoteQuotaUsedUp = "⚠️ The quota of `%s` is used up. It resets at %s. Until then, the calls go to `%s` (`%s`)."

// NoteQuotaUnknown is the notice of a used-up plan quota with no known reset time. The values
// are the entry, the next entry, the model of the next entry, and the entry again.
const NoteQuotaUnknown = "⚠️ The quota of `%s` is used up. The reset time is not known. The calls go to `%s` (`%s`) until `%s` works again."

// noticeText returns the text of a chain notice, or "" when the notice has no text.
func noticeText(n provider.Notice, now time.Time) string {
	switch n.Kind {
	case provider.NoticeWait:
		return fmt.Sprintf(NoteProviderBusy, n.Entry, n.Cause, int(n.Wait.Round(time.Second)/time.Second))
	case provider.NoticeQuota:
		switch {
		case n.NextEntry == "":
			return ""
		case n.ResetAt.IsZero():
			return fmt.Sprintf(NoteQuotaUnknown, n.Entry, n.NextEntry, n.NextModel, n.Entry)
		}
		return fmt.Sprintf(NoteQuotaUsedUp, n.Entry, resetText(n.ResetAt, now), n.NextEntry, n.NextModel)
	}
	return ""
}

// resetText formats a reset time in the zone of now. It adds the day when the date differs.
func resetText(at, now time.Time) string {
	at = at.In(now.Location())
	ay, am, ad := at.Date()
	ny, nm, nd := now.Date()
	if ay == ny && am == nm && ad == nd {
		return at.Format("15:04 MST")
	}
	return at.Format("Mon 2 Jan 15:04 MST")
}

// liveNotices reports whether the model calls of the turn send notices to the sink. A turn of
// a member sends none, so a pending quota notice waits for a turn of the owner.
func liveNotices(t Turn) bool {
	return t.Sink != nil && t.review == nil && !t.Member && t.Session.Kind == "chat"
}

// withNotices returns ctx with a notify function that sends the chain notices to sink.
// It returns ctx unchanged when live is false.
func withNotices(ctx context.Context, live bool, sink Sink) context.Context {
	if !live {
		return ctx
	}
	return provider.WithNotify(ctx, func(n provider.Notice) {
		if s := noticeText(n, time.Now()); s != "" {
			sink.Notice(s)
		}
	})
}

// Notice is a change card for the channel. A Notice with ID 0 has no row in the notices
// table, so the channel adds no buttons. The channel adds the buttons from the row.
type Notice struct {
	Text  string
	ID    int64
	Stamp string
}

// NoticeStore saves a change card as a notices row.
type NoticeStore interface {
	Create(ctx context.Context, sessionKey, text string, items []learn.NoticeItem) (learn.Notice, error)
}

// The limits of a change card.
const (
	// cardMaxItems is the number of changes that one card shows.
	cardMaxItems = 10
	// cardWasMax is the number of runes of an old text that a card shows.
	cardWasMax = 300
)

// cardEscapes holds the characters that a backslash makes literal in a card. It has the
// same characters as markdownEscapes in internal/gateway/markdown.go and mdEscapes in
// internal/telegram/format.go. The agent cannot import gateway.
const cardEscapes = "\\*_`[]|~"

// cardEscape puts a backslash before each Markdown marker character of s.
func cardEscape(s string) string {
	if !strings.ContainsAny(s, cardEscapes) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(cardEscapes, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// cardSpan returns text as the content of a code span: one line, with each backtick
// written as an apostrophe. It cuts the text at max runes when max is above 0, and it marks
// the hidden runes. An empty text gives "".
func cardSpan(text string, max int) string {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "`", "'")), " ")
	if max > 0 {
		if r := []rune(text); len(r) > max {
			text = string(r[:max]) + "…"
		}
	}
	return security.ShowHidden(text)
}

// cardHeader returns the first line of the card of one memory change.
func cardHeader(target, action string) string {
	switch target {
	case memory.TargetRule:
		switch action {
		case memory.ActionAdd, memory.ActionMove:
			return "📌 Rule saved"
		case memory.ActionRemove:
			return "📌 Rule removed"
		}
		return "📌 Rule changed"
	case memory.TargetUser:
		return "💾 User profile updated (" + action + ")"
	}
	if topic, ok := strings.CutPrefix(target, "note:"); ok {
		return "💾 Memory note updated (" + action + "): " + cardEscape(topic)
	}
	return "💾 Memory updated (" + action + ")"
}

// cardLines returns the lines of the card of one memory change.
func cardLines(e memory.Edit) []string {
	was, now := cardSpan(e.OldText, cardWasMax), cardSpan(e.NewText, 0)
	var lines []string
	wasLine := func() {
		if was != "" {
			lines = append(lines, "Was: `"+was+"`")
		}
	}
	nowLine := func() {
		if now != "" {
			lines = append(lines, "Now: `"+now+"`")
		}
	}
	switch e.Action {
	case memory.ActionMove:
		if topic, ok := strings.CutPrefix(e.Dest, "note:"); ok {
			action := e.Action
			if e.Target == memory.TargetRule {
				action = memory.ActionRemove
			}
			lines = append(lines, cardHeader(e.Target, action))
			wasLine()
			lines = append(lines, "Moved to note "+cardEscape(topic))
		} else {
			lines = append(lines, cardHeader(e.Dest, e.Action))
			nowLine()
		}
	case memory.ActionAdd:
		lines = append(lines, cardHeader(e.Target, e.Action))
		nowLine()
	case memory.ActionRemove:
		lines = append(lines, cardHeader(e.Target, e.Action))
		wasLine()
	default:
		lines = append(lines, cardHeader(e.Target, e.Action))
		wasLine()
		nowLine()
	}
	if q := cardSpan(e.Quote, 0); q != "" {
		lines = append(lines, `You said: "`+cardEscape(q)+`"`)
	}
	return lines
}

// CardText returns the text of one change card, or "" when nothing changed. It skips an
// expire, an undo, and a redo. It shows the first cardMaxItems changes: the memory edits
// first, then the skill copies. The extra lines go last.
func CardText(edits []memory.Edit, copies []skills.Copy, extra []string) string {
	var kept []memory.Edit
	for _, e := range edits {
		switch e.Action {
		case "expire", "undo", "redo":
		default:
			kept = append(kept, e)
		}
	}
	total := len(kept) + len(copies)
	shownEdits, shownCopies := kept, copies
	if total > cardMaxItems {
		if len(kept) >= cardMaxItems {
			shownEdits, shownCopies = kept[:cardMaxItems], nil
		} else {
			shownCopies = copies[:cardMaxItems-len(kept)]
		}
	}
	var lines []string
	for _, e := range shownEdits {
		lines = append(lines, cardLines(e)...)
	}
	lines = append(lines, skillLines(shownCopies)...)
	if total > cardMaxItems {
		lines = append(lines, fmt.Sprintf("… and %d more. /memory log", total-cardMaxItems))
	}
	lines = append(lines, extra...)
	return strings.Join(lines, "\n")
}

// skillLines returns one line for each skill action. It puts the names of one action on
// one line, at the place of the first copy of that action.
func skillLines(copies []skills.Copy) []string {
	type line struct {
		prefix int
		names  []string
	}
	var lines []line
	group := map[int]int{}
	for _, c := range copies {
		p := 1
		switch c.Action {
		case "create":
			p = 0
		case "delete":
			p = 2
		}
		if i, ok := group[p]; ok {
			// Two changes of one skill give one name.
			if name := cardEscape(c.Name); !slices.Contains(lines[i].names, name) {
				lines[i].names = append(lines[i].names, name)
			}
			continue
		}
		group[p] = len(lines)
		lines = append(lines, line{prefix: p, names: []string{cardEscape(c.Name)}})
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l.names) == 1 {
			out[i] = skillPrefixes[l.prefix].one + l.names[0]
		} else {
			out[i] = skillPrefixes[l.prefix].many + strings.Join(l.names, ", ")
		}
	}
	return out
}

// heldCall is a memory write that the tool held for the owner.
type heldCall struct {
	Action, Target, Topic, Content, OldText string
}

// heldOf returns the held write of a memory call, when the result says that the tool held it.
func heldOf(c provider.ToolCall, res tools.Result) (heldCall, bool) {
	if c.Name != "memory" || res.IsError || !strings.HasPrefix(res.Text, tools.HeldResultPrefix) {
		return heldCall{}, false
	}
	var h heldCall
	if json.Unmarshal([]byte(c.Args), &struct {
		Action  *string `json:"action"`
		Target  *string `json:"target"`
		Topic   *string `json:"topic"`
		Content *string `json:"content"`
		OldText *string `json:"old_text"`
	}{&h.Action, &h.Target, &h.Topic, &h.Content, &h.OldText}) != nil {
		return heldCall{}, false
	}
	return h, true
}

// heldCardText returns the text of the card of one held memory write.
func heldCardText(h heldCall, facts tools.OutsideFacts) string {
	sources := strings.Join(tools.HoldSources(facts), ", ")
	if len(facts.Hosts) > 0 {
		sources += " " + strings.Join(facts.Hosts, ", ")
	}
	target := h.Target
	if h.Topic != "" {
		target = "note " + h.Topic
	}
	detail := h.Content
	if h.Action == memory.ActionRemove || detail == "" {
		detail = h.OldText
	}
	text := "💾 Memory write held: this turn read outside content (" + sources + ")."
	return text + "\n" + cardEscape(h.Action) + " to " + cardEscape(target) + ": `" + cardSpan(detail, 0) + "`"
}

// sendCard sends the change card of a turn or of a review: the memory edits that refs name
// and the skill copies. It sends nothing when no change shows. The card goes to card, or to
// notify when card is nil. The held writes of refs and held send their own cards after the
// change card.
func (a *Agent) sendCard(ctx context.Context, sessionKey string, refs []tools.EditRef, copies []skills.Copy,
	held []heldCall, facts tools.OutsideFacts, card func(Notice), notify func(string)) {
	send := func(text string, items []learn.NoticeItem) {
		n := Notice{Text: text}
		if a.Notices != nil {
			row, err := a.Notices.Create(context.WithoutCancel(ctx), sessionKey, text, items)
			if err != nil {
				a.logger().Warn("agent: cannot save the notice", "session", sessionKey, "err", err.Error())
			} else {
				n.ID, n.Stamp = row.ID, row.Stamp
			}
		}
		switch {
		case card != nil:
			card(n)
		case notify != nil:
			notify(text)
		}
	}
	var edits []memory.Edit
	var items []learn.NoticeItem
	var heldIDs []int64
	for _, ref := range refs {
		switch ref.Kind {
		case "held":
			heldIDs = append(heldIDs, ref.ID)
		case "memory":
			if a.Memory == nil || a.Memory.History == nil {
				continue
			}
			e, err := a.Memory.History.Get(ctx, ref.ID)
			if err != nil {
				a.logger().Warn("agent: cannot read the edit for the card", "edit", ref.ID, "err", err.Error())
				continue
			}
			edits = append(edits, e)
			if e.Action != "expire" && e.Action != "undo" && e.Action != "redo" {
				items = append(items, learn.NoticeItem{Kind: "memory", ID: e.ID})
			}
		}
	}
	for _, c := range copies {
		if c.ID > 0 {
			items = append(items, learn.NoticeItem{Kind: "skill", ID: c.ID})
		}
	}
	if text := CardText(edits, copies, nil); text != "" {
		send(text, items)
	}
	for i, id := range heldIDs {
		if i < len(held) {
			send(heldCardText(held[i], facts), []learn.NoticeItem{{Kind: "held", ID: id}})
		}
	}
}
