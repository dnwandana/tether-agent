package acp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// maxChoiceRunes is the limit of one choice text of the Ask fallback.
const maxChoiceRunes = 40

// Hooks connects one coding task to the turn of the tool call.
type Hooks struct {
	// Progress gets the kind and the title of each tool_call of the agent.
	Progress func(kind, title string)
	// Approve asks the user about a permission request. It is
	// GateApprover.AskPermission of the turn. A nil Approve rejects each request.
	Approve func(ctx context.Context, req security.ApprovalRequest) (security.Scope, error)
	// Ask asks the user a question with choices. It is the question of the turn. The
	// bridge uses it when a permission request has no allow_once or no reject_once option.
	Ask func(ctx context.Context, text string, choices []string, timeout time.Duration) (string, error)
	// ApprovalTimeout is approvals.timeout_s. It is the timeout of an Ask question.
	ApprovalTimeout time.Duration
	// Protected holds the protected paths. A request with such a path is refused with no
	// prompt. Empty skips the check.
	Protected []string
	// CWD is the resolved work folder of the ACP session. A path outside it asks each time.
	// Empty skips the check.
	CWD string
	// Floor is the floor of the gate. A non-nil error refuses a command. Nil uses
	// security.MatchHardline.
	Floor func(command string) (label string, err error)
	// Member is true for a member turn. The Ask fallback reads it.
	Member bool
}

// bridge answers the permission requests of one Prompt call. It lets one
// request ask at a time, so the approval timeout of a request starts only
// when its prompt shows.
type bridge struct {
	turnCtx context.Context
	agentID string
	h       Hooks
	sem     chan struct{}   // size 1: the open approval
	stopCtx context.Context // ends at cancelAll
	stop    context.CancelFunc
	log     *slog.Logger
	// progress shows one event of the bridge. A nil progress shows nothing.
	progress func(kind, title string)
}

// newBridge returns the bridge of one turn. A nil log discards the log lines.
func newBridge(turnCtx context.Context, agentID string, h Hooks, log *slog.Logger) *bridge {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	stopCtx, stop := context.WithCancel(context.Background())
	return &bridge{turnCtx: turnCtx, agentID: agentID, h: h, sem: make(chan struct{}, 1),
		stopCtx: stopCtx, stop: stop, log: log, progress: h.Progress}
}

// cancelAll makes the open request and each waiting request return cancelled. Later
// requests return cancelled at once.
func (b *bridge) cancelAll() { b.stop() }

// decide returns the answer to one permission request. ctx is the handler ctx of the SDK.
func (b *bridge) decide(ctx context.Context, acpSessionID string, in permissionInput) permissionOutcome {
	cancelled := permissionOutcome{Cancelled: true}
	if b.stopCtx.Err() != nil {
		return cancelled
	}
	select {
	case b.sem <- struct{}{}:
	case <-b.stopCtx.Done():
		return cancelled
	case <-b.turnCtx.Done():
		return cancelled
	case <-ctx.Done():
		return cancelled
	}
	defer func() { <-b.sem }()
	// A select with two ready cases picks one at random. Check again.
	if b.stopCtx.Err() != nil || b.turnCtx.Err() != nil || ctx.Err() != nil {
		return cancelled
	}
	// The handler ctx can outlive a /stop, so the ask ctx also ends with the turn and at
	// cancelAll.
	askCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(b.turnCtx, cancel)()
	defer context.AfterFunc(b.stopCtx, cancel)()
	out := b.ask(askCtx, acpSessionID, in)
	// ACP requires the answer cancelled after session/cancel, also when an allow came late.
	if b.stopCtx.Err() != nil || askCtx.Err() != nil {
		return cancelled
	}
	return out
}

// ask maps one request to an Approve call, or to an Ask call when the
// request has no allow_once or no reject_once option.
//
// It runs these steps in order:
//  1. A request of any kind with a command that fails the floor is refused. No session
//     grant and no hook call can permit it.
//  2. A request with a protected path is refused.
//  3. A path outside the work folder, or an edit with no path, makes the request ask each
//     time.
//  4. The request goes to Approve, or to Ask when an option is missing.
func (b *bridge) ask(ctx context.Context, acpSessionID string, in permissionInput) permissionOutcome {
	if in.Command != "" {
		if b.h.Floor != nil {
			if label, err := b.h.Floor(in.Command); err != nil {
				b.log.Warn("acp: refused a command at the floor", "agent", b.agentID, "label", label)
				if b.progress != nil {
					b.progress("blocked", label)
				}
				return rejectOutcome(in.Options)
			}
		} else if p, hit := security.MatchHardline(in.Command); hit {
			b.log.Warn("acp: refused a hardline command", "agent", b.agentID, "pattern", p.ID)
			if b.progress != nil {
				b.progress("blocked", "hardline: "+p.Description)
			}
			return rejectOutcome(in.Options)
		}
	}
	paths := requestPaths(in, b.h.CWD)
	if len(b.h.Protected) > 0 {
		for _, p := range paths {
			if security.CheckWritable(p, b.h.Protected, false) != nil {
				b.log.Warn("acp: refused a protected path", "agent", b.agentID, "path", p)
				if b.progress != nil {
					b.progress("blocked", "protected path "+p)
				}
				return rejectOutcome(in.Options)
			}
		}
	}
	pn := notesFor(in, paths, b.h.CWD)
	req, allowID, rejectID, ok := buildApproval(b.agentID, acpSessionID, in, pn)
	if !ok {
		if b.h.Member {
			// The question goes to the user of the turn, not to an owner.
			return rejectOutcome(in.Options)
		}
		return b.askChoice(ctx, in, pn)
	}
	reject := permissionOutcome{OptionID: rejectID}
	if b.h.Approve == nil {
		return reject
	}
	scope, err := b.h.Approve(ctx, req)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return permissionOutcome{Cancelled: true}
	}
	if err != nil {
		// The Prompter failed. Do not allow what the user did not see.
		return reject
	}
	switch scope {
	case security.ScopeOnce, security.ScopeSession, security.ScopeAlways:
		return permissionOutcome{OptionID: allowID}
	default:
		return reject
	}
}

// rejectOutcome selects the first reject_once option, else the first option whose kind
// starts with reject_, else the cancelled outcome.
func rejectOutcome(opts []option) permissionOutcome {
	for _, o := range opts {
		if o.Kind == "reject_once" {
			return permissionOutcome{OptionID: o.ID}
		}
	}
	for _, o := range opts {
		if strings.HasPrefix(o.Kind, "reject_") {
			return permissionOutcome{OptionID: o.ID}
		}
	}
	return permissionOutcome{Cancelled: true}
}

// maxFallbackBodyRunes is the length limit of the code fence in the fallback question.
const maxFallbackBodyRunes = 1000

// askChoice asks with Hooks.Ask and the option names of the agent. The question shows the
// description, the dangerous note, the path notes, and the body in a code fence. Text from
// the repository must not change the question.
func (b *bridge) askChoice(ctx context.Context, in permissionInput, pn pathNotes) permissionOutcome {
	fallback := permissionOutcome{Cancelled: true}
	for _, o := range in.Options {
		if strings.HasPrefix(o.Kind, "reject_") {
			fallback = permissionOutcome{OptionID: o.ID}
			break
		}
	}
	var choices []string
	ids := map[string]string{}
	for _, o := range in.Options {
		// Tether Agent keeps the grants itself, so it never offers allow_always.
		if o.Kind == "allow_always" {
			continue
		}
		text := cutRunes(security.CleanSummary(o.Name), maxChoiceRunes)
		if text == "" {
			text = o.ID
		}
		if _, dup := ids[text]; dup || text == "" {
			continue
		}
		ids[text] = o.ID
		choices = append(choices, text)
	}
	if b.h.Ask == nil || len(choices) == 0 {
		return fallback
	}
	question := b.agentID + " asks for a permission: " + security.CleanSummary(in.Title) + "\n" +
		describe(b.agentID, in, pn) + "\n```\n" + fenceBody(bodyFor(in)) + "\n```"
	answer, err := b.h.Ask(ctx, question, choices, b.h.ApprovalTimeout)
	if ctx.Err() != nil {
		return permissionOutcome{Cancelled: true}
	}
	if err != nil {
		return fallback
	}
	if id, ok := ids[answer]; ok {
		return permissionOutcome{OptionID: id}
	}
	return fallback
}

// fenceBody makes the text safe for a code fence. It shows hidden runes, cuts the text, and
// replaces each run of three or more backticks with three single quotes.
func fenceBody(s string) string {
	s = cutRunes(security.ShowHidden(s), maxFallbackBodyRunes)
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			sb.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i >= 3 {
			sb.WriteString("'''")
		} else {
			sb.WriteString(s[i:j])
		}
		i = j
	}
	return sb.String()
}

// cutRunes returns the first n runes of s.
func cutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
