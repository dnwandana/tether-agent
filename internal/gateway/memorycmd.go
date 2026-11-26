package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/security"
)

// The texts of /memory.
const (
	memoryUsage = "Use /memory, /memory log, /memory held, /memory delete <id>, /memory undo <id>.<stamp>, " +
		"/memory redo <id>.<stamp>, /memory save <id>.<stamp>, /memory drop <id>.<stamp>, or /memory tidy."
	memoryHeldAnswered = "This held write is answered already."
	memoryPageSize     = 8
	memoryRowWidth     = 4
	memoryEntryCut     = 80
	memoryLogCut       = 60
	memoryAskCut       = 300
	memoryLogRows      = 10
)

func init() {
	Register(Command{Name: "memory", Help: "See and change the memory: /memory [log|held|delete <id>|tidy]", Run: cmdMemory, WhileBusy: true})
	noticeHeldAnswer = answerHeld
}

// memoryCut returns s as one line, cut at max runes with an ellipsis.
func memoryCut(s string, max int) string {
	s = oneLine(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func cmdMemory(ctx context.Context, c *CommandCtx, args string) error {
	l := c.Deps.Learn
	if l == nil || l.Memory == nil {
		c.send(Reply{Text: noticeNoMemoryCmd})
		return nil
	}
	words := strings.Fields(args)
	if len(words) == 0 {
		return c.memoryList(ctx, l, 1, false)
	}
	switch words[0] {
	case "page":
		if len(words) == 2 {
			if n, err := strconv.Atoi(words[1]); err == nil && n > 0 {
				return c.memoryList(ctx, l, n, true)
			}
		}
	case "log":
		if len(words) == 1 {
			return c.memoryLog(ctx, l)
		}
	case "held":
		if len(words) == 1 {
			return c.memoryHeld(ctx, l)
		}
	case "delete":
		if len(words) == 2 {
			return c.memoryDelete(ctx, l, words[1], false)
		}
		if len(words) == 3 && words[2] == "confirm" {
			return c.memoryDelete(ctx, l, words[1], true)
		}
	case "undo", "redo":
		if len(words) == 2 {
			if ref, ok := parseNoticeRef(words[1]); ok {
				return c.memoryReverse(ctx, l, ref, words[0] == "redo")
			}
		}
	case "save", "drop":
		if len(words) == 2 {
			if ref, ok := parseNoticeRef(words[1]); ok {
				return c.memoryAnswer(ctx, l, ref, words[0] == "save")
			}
		}
	case "tidy":
		if len(words) == 1 {
			return c.memoryTidy(ctx, l)
		}
	}
	c.send(Reply{Text: memoryUsage})
	return nil
}

// sendPrivateFirst sends r to the private chat of the owner when the caller writes in a
// group that has a member. Else, or when the send fails, it sends r to the caller.
func sendPrivateFirst(ctx context.Context, c *CommandCtx, r Reply) {
	if c.privateSend(ctx, r) {
		c.send(Reply{Text: "Sent to your private chat."})
		return
	}
	c.send(r)
}

// privateSend sends r to the private chat of the owner for a group with a member. It
// returns true when the send worked.
func (c *CommandCtx) privateSend(ctx context.Context, r Reply) bool {
	l := c.Deps.Learn
	if c.Source != "telegram" || l == nil || l.GroupHasMember == nil || l.SendPrivate == nil || !l.GroupHasMember(ctx, c.Key) {
		return false
	}
	r.Edit = false
	if err := l.SendPrivate(ctx, c.Key, r); err != nil {
		c.Deps.logger().Error("gateway: cannot send a memory reply to the private chat", "key", c.Key, "err", err.Error())
		return false
	}
	return true
}

// memoryTargets lists the files of the memory list in the order of the list.
var memoryTargets = []struct{ target, title string }{
	{memory.TargetRule, "Owner rules"},
	{memory.TargetMemory, "Memory"},
	{memory.TargetUser, "User profile"},
}

// memoryEntry is one entry of the memory list.
type memoryEntry struct {
	target, text string
}

// memoryList shows the entries of the three files, 8 buttons on a page.
func (c *CommandCtx) memoryList(ctx context.Context, l *LearnDeps, page int, edit bool) error {
	today := nowFunc().Format("2006-01-02")
	interactive := c.interactive()
	var (
		b   strings.Builder
		all []memoryEntry
		n   int
	)
	for _, f := range memoryTargets {
		entries := l.Memory.Entries(f.target)
		fill := utf8.RuneCountInString(strings.Join(entries, memory.Separator))
		fmt.Fprintf(&b, "\n**%s** (%d/%d)", f.title, fill, l.Memory.Limit(f.target))
		if len(entries) == 0 {
			b.WriteString("\n(empty)")
		}
		for _, e := range entries {
			n++
			all = append(all, memoryEntry{f.target, e})
			body, day, ok := memory.ParseUntil(e)
			if !ok {
				body = e
			}
			fmt.Fprintf(&b, "\n%d. %s", n, EscapeMarkdown(memoryCut(body, memoryEntryCut)))
			if ok {
				fmt.Fprintf(&b, " · until %s", day)
			}
			if memory.Expired(e, today) {
				b.WriteString(" (expired)")
			}
			if !interactive {
				fmt.Fprintf(&b, " · %s", memory.EntryID(e))
			}
		}
	}
	text := "💾 **Memory**" + b.String()
	if !interactive {
		c.send(Reply{Text: text + "\n\nDelete: /memory delete <id>"})
		return nil
	}
	first := (page - 1) * memoryPageSize
	if first >= len(all) && page > 1 {
		first, page = 0, 1
	}
	last := min(first+memoryPageSize, len(all))
	var rows [][]Button
	var row []Button
	for i := first; i < last; i++ {
		row = append(row, Button{Text: fmt.Sprintf("🗑 %d", i+1), Data: "/memory delete " + memory.EntryID(all[i].text)})
		if len(row) == memoryRowWidth {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if last < len(all) {
		rows = append(rows, []Button{{Text: "More ›", Data: fmt.Sprintf("/memory page %d", page+1)}})
	}
	r := Reply{Text: text, Buttons: rows, Edit: edit}
	sendPrivateFirst(ctx, c, r)
	return nil
}

// memoryUndoable reports whether a history row can be undone. A row that an Undo reversed
// and a Redo did again can be undone again.
func memoryUndoable(ctx context.Context, m *memory.Store, e memory.Edit) bool {
	switch e.Action {
	case "add", "replace", "remove", "move", "expire":
		undone, err := m.IsUndone(ctx, e)
		return err == nil && !undone
	}
	return false
}

// memoryLog shows the last changes of the memory with an Undo for each change that can be
// undone.
func (c *CommandCtx) memoryLog(ctx context.Context, l *LearnDeps) error {
	if l.Memory.History == nil {
		c.send(Reply{Text: "The memory history is not available."})
		return nil
	}
	rows, err := l.Memory.History.Recent(ctx, memoryLogRows)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.send(Reply{Text: "No memory change is recorded yet."})
		return nil
	}
	var (
		lines   []string
		buttons [][]Button
		cmds    []string
	)
	for i, e := range rows {
		text := e.NewText
		if text == "" {
			text = e.OldText
		}
		lines = append(lines, fmt.Sprintf("%s · %s · %s · %s · %s", e.At.Local().Format("Jan 2 15:04"),
			EscapeMarkdown(e.Source), EscapeMarkdown(e.Action), EscapeMarkdown(e.Target),
			EscapeMarkdown(memoryCut(text, memoryLogCut))))
		if !memoryUndoable(ctx, l.Memory, e) {
			continue
		}
		action := fmt.Sprintf("/memory undo %d.%s", e.ID, e.Stamp)
		buttons = append(buttons, []Button{{Text: fmt.Sprintf("↩ Undo %d", i+1), Data: action}})
		cmds = append(cmds, fmt.Sprintf("Undo %d: %s", i+1, action))
	}
	text := "💾 **Memory log**\n" + strings.Join(lines, "\n")
	if !c.interactive() {
		if len(cmds) > 0 {
			text += "\n\n" + strings.Join(cmds, "\n")
		}
		c.send(Reply{Text: text})
		return nil
	}
	sendPrivateFirst(ctx, c, Reply{Text: text, Buttons: buttons})
	return nil
}

// findEntry finds the entry with the ID in the three files.
func findEntry(m *memory.Store, id string) (target, entry string, ok bool) {
	for _, f := range memoryTargets {
		for _, e := range m.Entries(f.target) {
			if memory.EntryID(e) == id {
				return f.target, e, true
			}
		}
	}
	return "", "", false
}

// memoryDelete asks for the delete of an entry, or deletes it after the confirm.
func (c *CommandCtx) memoryDelete(ctx context.Context, l *LearnDeps, id string, confirm bool) error {
	target, entry, ok := findEntry(l.Memory, id)
	if !ok {
		c.send(Reply{Text: fmt.Sprintf("No entry has the ID %s. Send /memory to see the IDs.", EscapeMarkdown(id))})
		return nil
	}
	if !confirm {
		question := "🗑 **Delete this entry?**\n" + EscapeMarkdown(memoryCut(entry, memoryAskCut))
		cmd := "/memory delete " + id + " confirm"
		switch {
		case c.interactive():
			c.send(Reply{Text: question, Buttons: [][]Button{
				{{Text: "Cancel", Data: "cancel"}},
				{{Text: "🗑 Delete", Data: cmd}},
			}})
			return nil
		case c.Confirm == nil:
			c.send(Reply{Text: question + "\nTo delete it, send: " + cmd})
			return nil
		case !c.Confirm(question):
			c.send(Reply{Text: "Not deleted."})
			return nil
		}
	}
	sessionID := ""
	if c.Session != nil {
		sessionID = c.Session.ID
	}
	res, err := l.Memory.ApplyContext(ctx, memory.Op{Action: memory.ActionRemove, Target: target, OldText: entry,
		Exact: true, Source: memory.SourceOwner, SessionID: sessionID})
	if err != nil {
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
		return nil
	}
	if !c.sendEditCard(ctx, l, res.EditIDs) {
		c.send(Reply{Text: "🗑 Deleted."})
	}
	return nil
}

// sendEditCard saves and sends the change card of the history rows ids. It returns false
// when no card shows.
func (c *CommandCtx) sendEditCard(ctx context.Context, l *LearnDeps, ids []int64) bool {
	if l.Memory.History == nil || len(ids) == 0 {
		return false
	}
	var edits []memory.Edit
	var items []learn.NoticeItem
	for _, id := range ids {
		e, err := l.Memory.History.Get(ctx, id)
		if err != nil {
			c.Deps.logger().Error("gateway: cannot read a memory edit", "edit", id, "err", err.Error())
			continue
		}
		edits = append(edits, e)
		items = append(items, learn.NoticeItem{Kind: "memory", ID: id})
	}
	text := agent.CardText(edits, nil, nil)
	if text == "" {
		return false
	}
	if l.Notices == nil {
		sendPrivateFirst(ctx, c, Reply{Text: text})
		return true
	}
	n, err := l.Notices.Create(ctx, c.Key, text, items)
	if err != nil {
		c.Deps.logger().Error("gateway: cannot save the notice of a memory change", "err", err.Error())
		sendPrivateFirst(ctx, c, Reply{Text: text})
		return true
	}
	sendPrivateFirst(ctx, c, CardReply(n, c.interactive()))
	return true
}

// memoryReverse undoes or redoes the history row of ref.
func (c *CommandCtx) memoryReverse(ctx context.Context, l *LearnDeps, ref noticeRef, redo bool) error {
	var err error
	if redo {
		_, err = l.Memory.Redo(ctx, ref.id, ref.stamp)
	} else {
		_, err = l.Memory.Undo(ctx, ref.id, ref.stamp)
	}
	switch {
	case err == nil && redo:
		c.noticeReply(Reply{Text: "↪ Redone."})
	case err == nil:
		c.noticeReply(Reply{Text: "↩ Undone."})
	case errors.Is(err, memory.ErrStamp):
		c.noticeReply(Reply{Text: noticeTooOld})
	case errors.Is(err, memory.ErrChanged):
		c.noticeReply(Reply{Text: "The entry changed after this edit. Nothing was changed. Use /memory log."})
	case errors.Is(err, memory.ErrUndone) && redo:
		c.noticeReply(Reply{Text: "This edit is redone already."})
	case errors.Is(err, memory.ErrUndone):
		c.noticeReply(Reply{Text: "This edit is undone already."})
	case errors.Is(err, memory.ErrThreat):
		c.noticeReply(Reply{Text: "The text matched the memory threat scan. Nothing was restored."})
	default:
		c.noticeReply(Reply{Text: EscapeMarkdown(err.Error())})
	}
	return nil
}

// heldText returns the text of the card of a held write.
func heldText(h learn.HeldWrite) string {
	sources := strings.Join(h.Sources, ", ")
	if len(h.Hosts) > 0 {
		hosts := make([]string, len(h.Hosts))
		for i, host := range h.Hosts {
			hosts[i] = EscapeMarkdown(host)
		}
		sources += " " + strings.Join(hosts, ", ")
	}
	target := h.Op.Target
	if h.Op.Topic != "" {
		target = "note " + h.Op.Topic
	}
	detail := h.Op.Content
	if h.Op.Action == memory.ActionRemove || detail == "" {
		detail = h.Op.OldText
	}
	detail = security.ShowHidden(strings.Join(strings.Fields(strings.ReplaceAll(detail, "`", "'")), " "))
	return "💾 Memory write held: this turn read outside content (" + sources + ").\n" +
		EscapeMarkdown(h.Op.Action) + " to " + EscapeMarkdown(target) + ": `" + detail + "`"
}

// heldReply returns the text of a held write with its buttons, or with the command lines
// for a channel with no buttons.
func heldReply(h learn.HeldWrite, interactive bool) Reply {
	ref := noticeRef{id: h.ID, stamp: h.Stamp}.String()
	save, drop := "/memory save "+ref, "/memory drop "+ref
	if interactive {
		return Reply{Text: heldText(h), Buttons: [][]Button{
			{{Text: "❌ Drop", Data: drop}},
			{{Text: "✅ Save", Data: save}},
		}}
	}
	return Reply{Text: heldText(h) + "\n\nSave: " + save + "\nDrop: " + drop}
}

// memoryHeld lists the held writes, one reply for each.
func (c *CommandCtx) memoryHeld(ctx context.Context, l *LearnDeps) error {
	if l.Held == nil {
		c.send(Reply{Text: "No memory write is held."})
		return nil
	}
	rows, err := l.Held.List(ctx, "held")
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.send(Reply{Text: "No memory write is held."})
		return nil
	}
	sentPrivate := false
	for _, h := range rows {
		r := heldReply(h, c.interactive())
		if c.privateSend(ctx, heldReply(h, true)) {
			sentPrivate = true
			continue
		}
		c.send(r)
	}
	if sentPrivate {
		c.send(Reply{Text: "Sent to your private chat."})
	}
	return nil
}

// memoryAnswer saves or drops the held write of ref.
func (c *CommandCtx) memoryAnswer(ctx context.Context, l *LearnDeps, ref noticeRef, save bool) error {
	if l.Held == nil {
		c.noticeReply(Reply{Text: noticeTooOld})
		return nil
	}
	h, err := l.Held.Get(ctx, ref.id)
	if err != nil || h.Stamp != ref.stamp {
		c.noticeReply(Reply{Text: noticeTooOld})
		return nil
	}
	answerHeld(ctx, c, ref.id, save)
	return nil
}

// answerHeld saves or drops the held write with the id. It does not check a stamp, so the
// buttons of a held card and the commands of /memory share it.
func answerHeld(ctx context.Context, c *CommandCtx, heldID int64, save bool) {
	l := c.Deps.Learn
	if l == nil || l.Memory == nil || l.Held == nil {
		c.noticeReply(Reply{Text: noticeNoMemoryCmd})
		return
	}
	h, err := l.Held.Get(ctx, heldID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			c.Deps.logger().Error("gateway: cannot read a held write", "held", heldID, "err", err.Error())
		}
		c.noticeReply(Reply{Text: noticeTooOld})
		return
	}
	if h.State != "held" {
		c.noticeReply(Reply{Text: memoryHeldAnswered})
		return
	}
	text := heldText(h)
	if !save {
		if err := l.Held.SetState(ctx, h.ID, "dropped"); err != nil {
			c.noticeReply(Reply{Text: EscapeMarkdown(err.Error())})
			return
		}
		c.noticeReply(Reply{Text: text + "\n❌ Dropped. Nothing was saved."})
		return
	}
	op := h.Op
	if l.Memory.Threat != nil {
		if why := l.Memory.Threat(op.Content); why != "" {
			c.noticeReply(Reply{Text: text + "\nThe text matched the memory threat scan: " +
				EscapeMarkdown(strings.TrimSuffix(why, ".")) + ". Nothing was saved."})
			return
		}
	}
	if !heldEntryPresent(l.Memory, h) {
		c.noticeReply(Reply{Text: text + "\nThe entry changed after the hold. Nothing was saved."})
		return
	}
	op.Outside = h.Sources
	res, err := l.Memory.ApplyContext(ctx, op)
	if err != nil {
		c.noticeReply(Reply{Text: text + "\n" + EscapeMarkdown(err.Error())})
		return
	}
	if err := l.Held.SetState(ctx, h.ID, "saved"); err != nil {
		c.Deps.logger().Error("gateway: cannot mark a held write as saved", "held", h.ID, "err", err.Error())
	}
	c.noticeReply(Reply{Text: text + "\n✅ Saved."})
	c.sendEditCard(ctx, l, res.EditIDs)
}

// heldEntryPresent checks that the entry that a held replace, remove, or move names is
// still there. An add needs no entry.
func heldEntryPresent(m *memory.Store, h learn.HeldWrite) bool {
	op := h.Op
	switch op.Action {
	case memory.ActionReplace, memory.ActionRemove, memory.ActionMove:
	default:
		return true
	}
	if h.MatchedEntry == "" {
		return true
	}
	if op.Topic != "" && (op.Action != memory.ActionMove || op.To == "file") {
		text, err := m.Read(op.Topic, 0, 0)
		return err == nil && strings.Contains(text, h.MatchedEntry)
	}
	return slices.Contains(m.Entries(op.Target), h.MatchedEntry)
}

// memoryTidy starts a memory tidy for the active session.
func (c *CommandCtx) memoryTidy(ctx context.Context, l *LearnDeps) error {
	if l.Tidy == nil {
		c.send(Reply{Text: "Memory tidy is not available here."})
		return nil
	}
	sess := c.Session
	if sess == nil && c.Deps.Store != nil {
		if s, err := c.store().Active(ctx, c.Key); err == nil {
			sess = s
		}
	}
	if sess == nil {
		c.send(Reply{Text: "Start a session first."})
		return nil
	}
	// The card comes after the command ends, so it uses a context that the end does not cancel.
	// In a group with a member, it goes to the private chat of the owner, like the card of a turn.
	card := func(n agent.Notice) {
		bg := context.WithoutCancel(ctx)
		if n.ID == 0 || l.Notices == nil {
			sendPrivateFirst(bg, c, Reply{Text: n.Text})
			return
		}
		row, err := l.Notices.Get(bg, n.ID)
		if err != nil {
			sendPrivateFirst(bg, c, Reply{Text: n.Text})
			return
		}
		sendPrivateFirst(bg, c, CardReply(row, c.interactive()))
	}
	switch err := l.Tidy(ctx, sess, card); {
	case errors.Is(err, agent.ErrTidyRunning):
		c.send(Reply{Text: "A memory tidy runs already."})
	case err != nil:
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
	default:
		c.send(Reply{Text: "Memory tidy started. A card follows."})
	}
	return nil
}
