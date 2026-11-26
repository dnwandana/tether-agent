package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ErrTidyRunning is the error of RunTidy when a memory tidy runs already.
//
//lint:ignore ST1005 the text goes to the owner as a sentence.
var ErrTidyRunning = errors.New("A memory tidy runs already.")

// tidyDeny is the tool result of a call to a tool other than memory in a tidy run.
const tidyDeny = "tidy runs may only use memory"

// tidyNothing is the text of the card of a tidy that changed nothing.
const tidyNothing = "Memory tidy: nothing to change."

// TidyPrompt asks the tidy run to merge and move memory entries. It never asks for new facts.
const TidyPrompt = "Tidy the memory of the owner. The memory files and the inbox notes are below. " +
	"Use only the memory tool, and do these things:\n" +
	"1. Merge entries that say the same thing: replace one entry with the merged text, and remove the others.\n" +
	"2. Move a snapshot or an old fact to a note: action move, to \"note\", the target file, and a topic.\n" +
	"3. Move a standing rule of the owner from an inbox note into the owner rules: action move, to \"file\", " +
	"target rule, the topic of the note, and the owner's exact words in quote.\n" +
	"4. Do not add new facts, and do not change what an entry means.\n" +
	"A remove does not delete text. It moves the entry to the note archive-<target>. " +
	"When nothing needs a change, say \"Nothing to change.\" and stop."

// tidyFiles lists the memory files of the prompt and of the fill lines, in order.
var tidyFiles = []struct{ target, header, fill string }{
	{memory.TargetRule, "OWNER RULES", "Owner rules"},
	{memory.TargetMemory, "MEMORY", "Memory"},
	{memory.TargetUser, "USER PROFILE", "User profile"},
}

// tidyUsed returns the rune count of the entries of the target, joined with memory.Separator.
func tidyUsed(entries []string) int {
	return utf8.RuneCountInString(strings.Join(entries, memory.Separator))
}

// tidyPrompt returns the last user message of the tidy run: TidyPrompt, the memory files, the
// inbox notes, and the topic list.
func tidyPrompt(m *memory.Store) (string, error) {
	parts := []string{TidyPrompt}
	for _, f := range tidyFiles {
		entries, err := m.ReadEntries(f.target)
		if err != nil {
			return "", fmt.Errorf("cannot read the %s file: %w", f.target, err)
		}
		lines := []string{fmt.Sprintf("## %s (target %s, %d/%d)", f.header, f.target, tidyUsed(entries), m.Limit(f.target))}
		for _, e := range entries {
			lines = append(lines, "- "+memory.EntryID(e)+": "+e)
		}
		if len(entries) == 0 {
			lines = append(lines, "(empty)")
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	topics, err := m.Topics()
	if err != nil {
		return "", err
	}
	for _, topic := range topics {
		if !strings.HasPrefix(topic, "inbox-") {
			continue
		}
		body, err := m.Read(topic, 0, memory.MaxReadChars)
		if err != nil {
			return "", fmt.Errorf("cannot read the note %s: %w", topic, err)
		}
		parts = append(parts, "## NOTE "+topic+"\n"+body)
	}
	list := "(none)"
	if len(topics) > 0 {
		list = strings.Join(topics, ", ")
	}
	return strings.Join(append(parts, "## TOPICS\n"+list), "\n\n"), nil
}

// tidyUsage returns the rune count of each memory file, in the order of tidyFiles.
func (a *Agent) tidyUsage() []int {
	out := make([]int, len(tidyFiles))
	for i, f := range tidyFiles {
		out[i] = tidyUsed(a.Memory.Entries(f.target))
	}
	return out
}

// tidyFill returns the three fill lines of the card.
func (a *Agent) tidyFill(before []int) []string {
	after := a.tidyUsage()
	lines := make([]string, len(tidyFiles))
	for i, f := range tidyFiles {
		lines[i] = fmt.Sprintf("%s %d → %d of %d", f.fill, before[i], after[i], a.Memory.Limit(f.target))
	}
	return lines
}

// RunTidy starts a memory tidy of the session and returns at once. The run goes on in the
// background, counts against review.max_concurrent, and stops with Agent.Background. It
// sends one card to card when it ends. Read-only mode does not stop it, because the owner
// starts it. A second call while a tidy runs returns ErrTidyRunning.
func (a *Agent) RunTidy(ctx context.Context, sess *sessions.Session, card func(Notice)) error {
	if sess == nil {
		return errors.New("agent: the tidy has no session")
	}
	if a.Memory == nil || !a.hasTool("memory") {
		return errors.New("agent: the tidy needs the memory tool")
	}
	if a.reviewProviderDown(sess) {
		return ErrReviewProviderDown
	}
	base := a.Background
	if base == nil {
		base = context.Background()
	}
	if err := base.Err(); err != nil {
		return fmt.Errorf("agent: no tidy now: %w", err)
	}
	if !a.tidyRunning.CompareAndSwap(false, true) {
		return ErrTidyRunning
	}
	// started is true when the run began, or when the end of Background made it useless.
	// Only the one that sets it clears the flag, so goReview cannot leave the flag set.
	var started atomic.Bool
	stop := context.AfterFunc(base, func() {
		if started.CompareAndSwap(false, true) {
			a.tidyRunning.Store(false)
		}
	})
	a.goReview(ctx, func(ctx context.Context) {
		defer stop()
		if !started.CompareAndSwap(false, true) {
			return
		}
		defer a.tidyRunning.Store(false)
		a.runTidy(ctx, sess, card)
	})
	return nil
}

// runTidy runs the tidy turn and sends the card.
func (a *Agent) runTidy(ctx context.Context, sess *sessions.Session, card func(Notice)) {
	system, schemas, parentEnv := a.reviewShape(ctx, sess)
	prompt, err := tidyPrompt(a.Memory)
	if err != nil {
		a.logger().Warn("agent: tidy not started", "session", sess.ID, "err", err.Error())
		return
	}
	tsess, err := a.forkSession(ctx, sess, sessions.KindTidy, "memory tidy", sess.ModelOverride)
	if err != nil {
		a.logger().Warn("agent: tidy not started", "session", sess.ID, "err", err.Error())
		return
	}
	before := a.tidyUsage()
	env := childEnv(parentEnv, tsess)
	// The owner starts the tidy, so read-only mode of the scope does not stop it.
	env.ModeScope = ""
	defer releaseApprovals(env)
	allow := map[string]bool{"memory": true}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(delegationLimits(a.config()).ChildTimeoutS)*time.Second)
	defer cancel()
	// The defer also sends the card when the run fails or times out.
	defer func() { a.sendTidyCard(ctx, sess.Key, env, before, card) }()
	a.logger().Info("tidy start", "session", sess.ID)
	_, err = a.RunTurn(cctx, Turn{
		Session:  tsess,
		UserText: prompt,
		Env:      env,
		System:   system,
		MaxTurns: a.reviewRounds(),
		review: &reviewOpts{
			schemas: schemas,
			allow:   allow,
			deny:    tidyDeny,
		},
	})
	if err != nil {
		a.logger().Warn("tidy failed", "session", sess.ID, "err", err.Error())
	}
}

// sendTidyCard sends the card of the memory edits of the tidy run, with the fill lines. With
// no edit it sends the card "Memory tidy: nothing to change.".
func (a *Agent) sendTidyCard(ctx context.Context, key string, env *tools.Env, before []int, card func(Notice)) {
	if card == nil {
		return
	}
	var edits []memory.Edit
	var items []learn.NoticeItem
	if a.Memory.History != nil {
		for _, ref := range tools.Edits(env) {
			if ref.Kind != "memory" {
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
	if CardText(edits, nil, nil) == "" {
		card(Notice{Text: tidyNothing})
		return
	}
	n := Notice{Text: CardText(edits, nil, a.tidyFill(before))}
	if a.Notices != nil {
		row, err := a.Notices.Create(context.WithoutCancel(ctx), key, n.Text, items)
		if err != nil {
			a.logger().Warn("agent: cannot save the notice", "session", key, "err", err.Error())
		} else {
			n.ID, n.Stamp = row.ID, row.Stamp
		}
	}
	card(n)
}
