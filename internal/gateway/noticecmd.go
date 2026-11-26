package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// The texts of /notice.
const (
	noticeUsage       = "Use /notice undo|redo|ok|pick <id>.<stamp>."
	noticeOff         = "Change notices are not available here."
	noticeTooOld      = "This button is too old."
	noticeOlder       = "This notice is older than 30 days. Use /memory log or /skills log."
	noticeNoMemoryCmd = "Memory commands are not available here."
	noticeMaxAge      = 30 * 24 * time.Hour
)

// noticeHeldAnswer saves or drops a held memory write for the buttons of a held card. The
// memory commands set it. Nil means that the memory commands are off.
var noticeHeldAnswer func(ctx context.Context, c *CommandCtx, heldID int64, save bool)

func init() {
	Register(Command{Name: "notice", Help: "Answer a change notice: /notice undo|redo|ok|pick <id>.<stamp> [n]", Run: cmdNotice, WhileBusy: true})
}

// noticeRef is the "<id>.<stamp>" part of a /notice command.
type noticeRef struct {
	id    int64
	stamp string
}

func (r noticeRef) String() string { return fmt.Sprintf("%d.%s", r.id, r.stamp) }

// parseNoticeRef parses "<id>.<stamp>": a positive id and 4 lower-case hex characters.
func parseNoticeRef(s string) (noticeRef, bool) {
	idText, stamp, ok := strings.Cut(s, ".")
	if !ok || len(stamp) != 4 {
		return noticeRef{}, false
	}
	for _, r := range stamp {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return noticeRef{}, false
		}
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 || idText != strconv.FormatInt(id, 10) {
		return noticeRef{}, false
	}
	return noticeRef{id: id, stamp: stamp}, true
}

// noticeReply sends r as an edit of the message of the pressed button.
func (c *CommandCtx) noticeReply(r Reply) {
	r.Edit = true
	c.send(r)
}

func cmdNotice(ctx context.Context, c *CommandCtx, args string) error {
	words := strings.Fields(args)
	if len(words) < 2 || len(words) > 3 {
		c.send(Reply{Text: noticeUsage})
		return nil
	}
	verb := words[0]
	ref, ok := parseNoticeRef(words[1])
	nth := 0
	switch verb {
	case "undo":
		if len(words) == 3 {
			v, err := strconv.Atoi(words[2])
			if err != nil || v <= 0 {
				ok = false
			}
			nth = v
		}
	case "redo", "ok", "pick", "show", "save", "drop":
		if len(words) != 2 {
			ok = false
		}
	default:
		ok = false
	}
	if !ok {
		c.send(Reply{Text: noticeUsage})
		return nil
	}
	l := c.Deps.Learn
	if l == nil || l.Notices == nil {
		c.noticeReply(Reply{Text: noticeOff})
		return nil
	}
	n, err := l.Notices.Get(ctx, ref.id)
	if err != nil || n.Stamp != ref.stamp {
		c.noticeReply(Reply{Text: noticeTooOld})
		return nil
	}
	if nowFunc().Sub(n.At) > noticeMaxAge {
		c.noticeReply(Reply{Text: noticeOlder})
		return nil
	}
	switch verb {
	case "ok":
		if err := l.Notices.SetState(ctx, n.ID, "kept"); err != nil {
			return err
		}
		c.noticeReply(Reply{Text: n.Text + "\n✓ Kept."})
	case "undo":
		return c.noticeUndo(ctx, l, n, nth)
	case "redo":
		return c.noticeRedo(ctx, l, n)
	case "pick":
		labels := make([]string, len(n.Items))
		for i, it := range n.Items {
			labels[i] = c.itemLabel(ctx, l, it)
		}
		c.noticeReply(pickReply(n, labels))
	case "show":
		c.noticeReply(CardReply(n, c.interactive()))
	default:
		return c.noticeHeld(ctx, n, verb == "save")
	}
	return nil
}

// noticeHeld answers the buttons of a held card.
func (c *CommandCtx) noticeHeld(ctx context.Context, n learn.Notice, save bool) error {
	if len(n.Items) != 1 || n.Items[0].Kind != "held" {
		c.noticeReply(Reply{Text: noticeTooOld})
		return nil
	}
	if noticeHeldAnswer == nil {
		c.noticeReply(Reply{Text: noticeNoMemoryCmd})
		return nil
	}
	noticeHeldAnswer(ctx, c, n.Items[0].ID, save)
	// The card is kept after the answer, so it shows no Drop and Save buttons again. A held
	// write that is still held, for example after a failed save, keeps the card open.
	if l := c.Deps.Learn; l != nil && l.Notices != nil && l.Held != nil {
		if h, err := l.Held.Get(ctx, n.Items[0].ID); err == nil && h.State != "held" {
			if err := l.Notices.SetState(ctx, n.ID, "kept"); err != nil {
				c.Deps.logger().Error("gateway: cannot keep the held card", "notice", n.ID, "err", err.Error())
			}
		}
	}
	return nil
}

// memoryLabel names a memory target for the owner.
func memoryLabel(target string) string {
	switch target {
	case memory.TargetRule:
		return "Rules"
	case memory.TargetMemory:
		return "Memory"
	case memory.TargetUser:
		return "User profile"
	}
	if topic, ok := strings.CutPrefix(target, "note:"); ok {
		return "note " + topic
	}
	return target
}

// itemLabel returns the plain label of a notice item. The caller escapes it for text.
func (c *CommandCtx) itemLabel(ctx context.Context, l *LearnDeps, it learn.NoticeItem) string {
	switch it.Kind {
	case "memory":
		if l.Memory != nil && l.Memory.History != nil {
			if e, err := l.Memory.History.Get(ctx, it.ID); err == nil {
				return memoryLabel(e.Target)
			}
		}
		return "Memory"
	case "skill":
		if l.Copies != nil {
			if cp, err := l.Copies.GetCopy(it.ID); err == nil {
				return cp.Name
			}
		}
		return "skill"
	case "archive":
		if f, ok := c.archiveFlag(ctx, l, it.ID); ok {
			return f.Name
		}
		return "skill"
	}
	return it.Kind
}

// archiveFlag finds the curator flag with the id.
func (c *CommandCtx) archiveFlag(ctx context.Context, l *LearnDeps, id int64) (skills.Flag, bool) {
	if l.Curator == nil {
		return skills.Flag{}, false
	}
	flags, err := l.Curator.Archived(ctx)
	if err != nil {
		return skills.Flag{}, false
	}
	for _, f := range flags {
		if f.ID == id {
			return f, true
		}
	}
	return skills.Flag{}, false
}

// ownerOrigin is the origin of a skill change that the owner asks for.
var ownerOrigin = skills.Origin{Source: "owner"}

// undoItem undoes one item. It returns true when the item is undone, also when it was
// undone before.
func (c *CommandCtx) undoItem(ctx context.Context, l *LearnDeps, it learn.NoticeItem) (done, restored bool, err error) {
	switch it.Kind {
	case "memory":
		if l.Memory == nil || l.Memory.History == nil {
			return false, false, errors.New("memory history is not available")
		}
		e, err := l.Memory.History.Get(ctx, it.ID)
		if err != nil {
			return false, false, err
		}
		_, err = l.Memory.Undo(ctx, it.ID, e.Stamp)
		if errors.Is(err, memory.ErrUndone) {
			return true, false, nil
		}
		return err == nil, false, err
	case "skill":
		if l.Skills == nil || l.Copies == nil {
			return false, false, errors.New("skill history is not available")
		}
		cp, err := l.Copies.GetCopy(it.ID)
		if err != nil {
			return false, false, err
		}
		_, err = l.Skills.UndoCopy(ownerOrigin, it.ID, cp.Stamp)
		if errors.Is(err, skills.ErrCopyUndone) {
			return true, false, nil
		}
		return err == nil, false, err
	case "archive":
		f, ok := c.archiveFlag(ctx, l, it.ID)
		if !ok {
			return true, false, nil
		}
		if err := l.Curator.Restore(ctx, f.Name); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	return false, false, fmt.Errorf("unknown notice item kind %q", it.Kind)
}

// redoItem does one item again. Archive items are not part of a redo.
func (c *CommandCtx) redoItem(ctx context.Context, l *LearnDeps, it learn.NoticeItem) (done bool, err error) {
	switch it.Kind {
	case "memory":
		if l.Memory == nil || l.Memory.History == nil {
			return false, errors.New("memory history is not available")
		}
		e, err := l.Memory.History.Get(ctx, it.ID)
		if err != nil {
			return false, err
		}
		if e.UndoneBy == 0 {
			return true, nil
		}
		_, err = l.Memory.Redo(ctx, it.ID, e.Stamp)
		if errors.Is(err, memory.ErrNotUndone) {
			// A Redo of another row of the same group did this row too.
			return true, nil
		}
		return err == nil, err
	case "skill":
		if l.Skills == nil || l.Copies == nil {
			return false, errors.New("skill history is not available")
		}
		cp, err := l.Copies.GetCopy(it.ID)
		if err != nil {
			return false, err
		}
		if cp.UndoneBy == 0 {
			return true, nil
		}
		_, err = l.Skills.RedoCopy(ownerOrigin, it.ID, cp.Stamp)
		return err == nil, err
	}
	return true, nil
}

// partialLine is the reply line of a change that did not reach all items.
func partialLine(verb string, k, n int, failed []string, logCmd string) string {
	esc := make([]string, len(failed))
	for i, f := range failed {
		esc[i] = EscapeMarkdown(f)
	}
	mark := "↩"
	if verb == "Redone" {
		mark = "↪"
	}
	return fmt.Sprintf("%s %s %d of %d. Changed since this notice: %s. Use %s.", mark, verb, k, n, strings.Join(esc, ", "), logCmd)
}

// noticeUndo undoes the items of n, or item nth (1-based) when nth is above 0.
func (c *CommandCtx) noticeUndo(ctx context.Context, l *LearnDeps, n learn.Notice, nth int) error {
	if n.State == "undone" {
		n.Text += "\n↩ Undone."
		c.noticeReply(CardReply(n, c.interactive()))
		return nil
	}
	if nth > len(n.Items) {
		c.noticeReply(Reply{Text: fmt.Sprintf("There is no item %d.", nth)})
		return nil
	}
	var idx []int
	if nth > 0 {
		idx = []int{nth - 1}
	} else {
		for i := range slices.Backward(n.Items) {
			idx = append(idx, i)
		}
	}
	var (
		k        int
		failed   []string
		logCmd   = "/memory log"
		restored bool
		label    string
	)
	for _, i := range idx {
		it := n.Items[i]
		label = c.itemLabel(ctx, l, it)
		done, rest, err := c.undoItem(ctx, l, it)
		restored = restored || rest
		if done {
			k++
			continue
		}
		if !errors.Is(err, memory.ErrChanged) && !errors.Is(err, memory.ErrThreat) &&
			!errors.Is(err, skills.ErrCopyChanged) {
			c.Deps.logger().Error("gateway: a notice item could not be undone", "notice", n.ID, "kind", it.Kind, "id", it.ID, "err", err)
		}
		failed = append(failed, label)
		if it.Kind != "memory" {
			logCmd = "/skills log"
		}
	}
	if restored && l.RefreshMenu != nil {
		l.RefreshMenu(ctx)
	}
	if len(failed) > 0 {
		if k > 0 && nth == 0 {
			if err := l.Notices.SetState(ctx, n.ID, "undone"); err != nil {
				return err
			}
			n.State = "undone"
		}
		n.Text += "\n" + partialLine("Undone", k, len(idx), failed, logCmd)
		c.noticeReply(CardReply(n, c.interactive()))
		return nil
	}
	if nth > 0 {
		n.Text += "\n↩ Undone: " + EscapeMarkdown(label) + "."
		c.noticeReply(CardReply(n, c.interactive()))
		return nil
	}
	if err := l.Notices.SetState(ctx, n.ID, "undone"); err != nil {
		return err
	}
	n.State = "undone"
	n.Text += "\n↩ Undone."
	c.noticeReply(CardReply(n, c.interactive()))
	return nil
}

// noticeRedo does the memory and skill items of an undone card again.
func (c *CommandCtx) noticeRedo(ctx context.Context, l *LearnDeps, n learn.Notice) error {
	if n.State != "undone" {
		c.noticeReply(CardReply(n, c.interactive()))
		return nil
	}
	var (
		count, k int
		failed   []string
		logCmd   = "/memory log"
	)
	for _, it := range n.Items {
		if it.Kind == "archive" {
			continue
		}
		count++
		done, err := c.redoItem(ctx, l, it)
		if done {
			k++
			continue
		}
		if !errors.Is(err, memory.ErrChanged) && !errors.Is(err, memory.ErrThreat) &&
			!errors.Is(err, skills.ErrCopyChanged) {
			c.Deps.logger().Error("gateway: a notice item could not be redone", "notice", n.ID, "kind", it.Kind, "id", it.ID, "err", err)
		}
		failed = append(failed, c.itemLabel(ctx, l, it))
		if it.Kind != "memory" {
			logCmd = "/skills log"
		}
	}
	if len(failed) > 0 {
		if k > 0 {
			if err := l.Notices.SetState(ctx, n.ID, "open"); err != nil {
				return err
			}
			n.State = "open"
		}
		n.Text += "\n" + partialLine("Redone", k, count, failed, logCmd)
		c.noticeReply(CardReply(n, c.interactive()))
		return nil
	}
	if err := l.Notices.SetState(ctx, n.ID, "open"); err != nil {
		return err
	}
	n.State = "open"
	n.Text += "\n↪ Redone."
	c.noticeReply(CardReply(n, c.interactive()))
	return nil
}

// pickReply lists the items of n as buttons. Button i undoes item i+1 only.
func pickReply(n learn.Notice, labels []string) Reply {
	ref := noticeRef{id: n.ID, stamp: n.Stamp}
	var rows [][]Button
	for i := range n.Items {
		label := ""
		if i < len(labels) {
			label = labels[i]
		}
		rows = append(rows, []Button{{Text: fmt.Sprintf("↩ %d. %s", i+1, label), Data: fmt.Sprintf("/notice undo %s %d", ref, i+1)}})
	}
	rows = append(rows, []Button{{Text: "« Back", Data: "/notice show " + ref.String()}})
	return Reply{Text: n.Text, Buttons: rows}
}

// cardAction is one button of a card and the line of the same action for a channel with
// no buttons.
type cardAction struct {
	label, cmd string
	line, text string
}

// CardReply returns the reply of a change card: its text with the buttons of its state. A
// channel with no buttons gets the commands as lines after the text.
func CardReply(n learn.Notice, interactive bool) Reply {
	ref := noticeRef{id: n.ID, stamp: n.Stamp}.String()
	curator := len(n.Items) > 0
	for _, it := range n.Items {
		if it.Kind != "archive" {
			curator = false
		}
	}
	held := len(n.Items) == 1 && n.Items[0].Kind == "held"
	undo, all, one := "Undo", "Undo all", "Undo one"
	if curator {
		undo, all, one = "Restore", "Restore all", "Restore one"
	}
	undoCmd := "/notice undo " + ref
	ok := cardAction{label: "✓ OK", cmd: "/notice ok " + ref, line: "OK", text: "/notice ok " + ref}
	var acts []cardAction
	switch {
	case held && n.State == "open":
		acts = []cardAction{
			{"❌ Drop", "/notice drop " + ref, "Drop", "/notice drop " + ref},
			{"✅ Save", "/notice save " + ref, "Save", "/notice save " + ref},
		}
	case held:
	case n.State == "open" && len(n.Items) <= 1:
		acts = []cardAction{{"↩ " + undo, undoCmd, undo, undoCmd}, ok}
	case n.State == "open":
		line := undo
		if curator {
			line = all
		}
		acts = []cardAction{
			{"↩ " + all, undoCmd, line, undoCmd},
			{one + " ›", "/notice pick " + ref, one, undoCmd + " <n>"},
			ok,
		}
	case n.State == "undone":
		if !curator {
			acts = append(acts, cardAction{"↪ Redo", "/notice redo " + ref, "Redo", "/notice redo " + ref})
		}
		acts = append(acts, ok)
	}
	if len(acts) == 0 {
		return Reply{Text: n.Text}
	}
	if interactive {
		rows := make([][]Button, 0, len(acts))
		for _, a := range acts {
			rows = append(rows, []Button{{Text: a.label, Data: a.cmd}})
		}
		return Reply{Text: n.Text, Buttons: rows}
	}
	lines := make([]string, 0, len(acts))
	for _, a := range acts {
		lines = append(lines, a.line+": "+a.text)
	}
	return Reply{Text: n.Text + "\n\n" + strings.Join(lines, "\n")}
}
