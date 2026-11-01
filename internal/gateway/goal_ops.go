package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// The replies of /goal wait, /goal unwait, /goal gate, and /subgoal. Hermes gives each
// text except the texts of the gate confirm.
const (
	waitNoGoalReply    = "/goal wait: no active goal to park"
	waitBadPIDReply    = "/goal wait: pid must be a positive integer"
	waitDeadPIDReply   = "/goal wait: pid is not alive on this host"
	unwaitDoneReply    = "▶ Wait barrier cleared — goal loop resumes."
	unwaitNoneReply    = "No wait barrier set."
	gateNoGoalList     = "(no active goal)"
	gateNotAddedReply  = "Gate not added."
	gateConfirmInvalid = "This confirm is not valid now. Send /goal gate add <command> again."
	noSubgoalsLine     = "(no subgoals — use /subgoal <text> to add criteria)"
	noSubgoalsToClear  = "No subgoals to clear."
)

// Errors that stop an Update of a goal op, so nothing is written. The command does not
// return them.
var (
	errNoActiveGoal = errors.New("no active goal")
	errBadPID       = errors.New("gateway: the pid is not positive")
	errDeadPID      = errors.New("gateway: the pid is not alive")
	errNoBarrier    = errors.New("gateway: no wait barrier")
	errNoSubgoals   = errors.New("gateway: no subgoals")
	errOpFailed     = errors.New("gateway: the edit failed")
)

func init() {
	registerGoalVerb(goals.VerbWait, goalWait)
	registerGoalVerb(goals.VerbUnwait, goalUnwait)
	registerGoalVerb(goals.VerbGate, goalGate)
	Register(Command{Name: "subgoal", Help: "Add a criterion to the goal: /subgoal <text> | remove <n> | clear",
		Run: cmdSubgoal, WhileBusy: true})
}

// goalWait parks an active goal on a live pid, like Hermes wait_on.
func goalWait(ctx context.Context, c *CommandCtx, sess *sessions.Session, cmd goals.Command) (Reply, error) {
	st, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		switch {
		case !exists || st.Status != goals.StatusActive:
			return errNoActiveGoal
		case cmd.PID <= 0:
			return errBadPID
		case !goals.PIDAlive(cmd.PID):
			return errDeadPID
		}
		st.WaitOnPID(cmd.PID, cmd.Reason, nowFunc())
		return nil
	})
	switch {
	case errors.Is(err, errNoActiveGoal):
		return Reply{Text: waitNoGoalReply}, nil
	case errors.Is(err, errBadPID):
		return Reply{Text: waitBadPIDReply}, nil
	case errors.Is(err, errDeadPID):
		return Reply{Text: waitDeadPIDReply}, nil
	case err != nil:
		return Reply{}, err
	}
	// The park keeps the epoch, so a queued goal turn of an older decision stays valid for
	// the queue and must go here.
	if c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
	// A goal on an idle key gets no after-turn step, so the waker must start here. Arm also
	// stops the retry timer of the session.
	if c.Hooks.ArmWake != nil {
		c.Hooks.ArmWake(sess.ID, st)
	}
	suffix := ""
	if st.WaitingReason != nil {
		suffix = " (" + EscapeMarkdown(*st.WaitingReason) + ")"
	}
	return Reply{Text: fmt.Sprintf("⏳ Goal parked on pid %d%s. Loop pauses until it exits.", cmd.PID, suffix)}, nil
}

// goalUnwait removes the barrier of the goal. An active goal then takes its next turn.
func goalUnwait(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	st, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || (st.WaitingOnPID == nil && st.WaitingOnSession == nil && st.WaitingUntil <= 0) {
			return errNoBarrier
		}
		st.ClearWait()
		return nil
	})
	if errors.Is(err, errNoBarrier) {
		return Reply{Text: unwaitNoneReply}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	if st.Status == goals.StatusActive && c.Hooks.StartGoalTurn != nil {
		c.Hooks.StartGoalTurn(goals.Continuation{
			SessionID: sess.ID,
			Text:      goals.ContinuationPrompt(st, nil),
			Epoch:     st.Epoch,
			LineKind:  goals.LineLifted,
			Used:      st.TurnsUsed,
			Max:       st.MaxTurns,
		})
	}
	return Reply{Text: unwaitDoneReply}, nil
}

// cmdSubgoal lists, adds, removes, or clears the extra criteria of the goal. An edit keeps
// the epoch and starts no turn, so a queued continuation stays valid.
func cmdSubgoal(ctx context.Context, c *CommandCtx, args string) error {
	if c.Deps.Goals == nil {
		c.send(Reply{Text: "Goals unavailable on this session."})
		return nil
	}
	sc := goals.ParseSubgoal(args)
	if sc.Usage != "" {
		c.send(Reply{Text: EscapeMarkdown(sc.Usage)})
		return nil
	}
	sess, err := c.current(ctx)
	if err != nil {
		return err
	}
	rep, err := subgoalOp(ctx, c, sess, sc)
	if err != nil {
		return err
	}
	c.send(rep)
	return nil
}

// subgoalOp applies sc to the goal of sess and returns the reply.
func subgoalOp(ctx context.Context, c *CommandCtx, sess *sessions.Session, sc goals.SubgoalCommand) (Reply, error) {
	if sc.Op == "list" {
		st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
		if err != nil {
			return Reply{}, err
		}
		if !ok || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			return Reply{Text: goals.NoGoalText}, nil
		}
		var b strings.Builder
		b.WriteString(EscapeMarkdown(goals.StatusLine(st, nowFunc())))
		if len(st.Subgoals) == 0 {
			b.WriteString("\n")
			b.WriteString(noSubgoalsLine)
		}
		for i, s := range st.Subgoals {
			fmt.Fprintf(&b, "\n- %d. %s", i+1, EscapeMarkdown(s))
		}
		return Reply{Text: b.String()}, nil
	}
	var (
		index int
		text  string
		count int
		opErr error
	)
	_, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			return errNoActiveGoal
		}
		switch sc.Op {
		case "add":
			index, opErr = st.AddSubgoal(sc.Text)
			text = sc.Text
		case "remove":
			index = sc.Index
			text, opErr = st.RemoveSubgoal(sc.Index)
		case "clear":
			count = len(st.Subgoals)
			if count == 0 {
				return errNoSubgoals
			}
			st.ClearSubgoals()
		}
		if opErr != nil {
			return errOpFailed
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoActiveGoal):
		return Reply{Text: goals.NoGoalText}, nil
	case errors.Is(err, errNoSubgoals):
		return Reply{Text: noSubgoalsToClear}, nil
	case errors.Is(err, errOpFailed):
		return Reply{Text: "/subgoal " + sc.Op + ": " + EscapeMarkdown(opErr.Error())}, nil
	case err != nil:
		return Reply{}, err
	}
	switch sc.Op {
	case "add":
		return Reply{Text: "✓ Added subgoal " + strconv.Itoa(index) + ": " + EscapeMarkdown(strings.TrimSpace(text))}, nil
	case "remove":
		return Reply{Text: "✓ Removed subgoal " + strconv.Itoa(index) + ": " + EscapeMarkdown(text)}, nil
	}
	return Reply{Text: "✓ Cleared " + countWord(count, "subgoal") + "."}, nil
}

// countWord returns "N word", with an "s" when n is not 1.
func countWord(n int, word string) string {
	if n != 1 {
		word += "s"
	}
	return strconv.Itoa(n) + " " + word
}

// gateConfirm is one dangerous gate that waits for the confirm of the owner.
type gateConfirm struct {
	sessionID, command string
	expires            time.Time
}

// gateConfirms holds the waiting confirms of each key by number. gateConfirmSeq holds the
// last number of each key. gateConfirmsMu guards both. The numbers count up from 1.
var (
	gateConfirmsMu sync.Mutex
	gateConfirms   = map[string]map[int]gateConfirm{}
	gateConfirmSeq = map[string]int{}
)

// putGateConfirm stores gc for key and returns its number.
func putGateConfirm(key string, gc gateConfirm) int {
	gateConfirmsMu.Lock()
	defer gateConfirmsMu.Unlock()
	gateConfirmSeq[key]++
	n := gateConfirmSeq[key]
	if gateConfirms[key] == nil {
		gateConfirms[key] = map[int]gateConfirm{}
	}
	gateConfirms[key][n] = gc
	return n
}

// takeGateConfirm removes the confirm n of key and returns it. It returns false for a
// missing or expired confirm. It also removes the expired confirms of key, so the store
// does not grow.
func takeGateConfirm(key string, n int, now time.Time) (gateConfirm, bool) {
	gateConfirmsMu.Lock()
	defer gateConfirmsMu.Unlock()
	m := gateConfirms[key]
	gc, ok := m[n]
	delete(m, n)
	for i, x := range m {
		if now.After(x.expires) {
			delete(m, i)
		}
	}
	if len(m) == 0 {
		delete(gateConfirms, key)
	}
	if !ok || now.After(gc.expires) {
		return gateConfirm{}, false
	}
	return gc, true
}

// approvalTimeout returns approvals.timeout_s, the time that a gate confirm stays valid.
func approvalTimeout(d *Deps) time.Duration {
	s := config.Default().Approvals.TimeoutS
	if d.Cfg != nil {
		s = d.Cfg.Approvals.TimeoutS
	}
	return time.Duration(s) * time.Second
}

// gateAddedLine returns the Hermes reply for an added gate.
func gateAddedLine(command string) string {
	return fmt.Sprintf("⚿ Gate added: $ %s (%d retries, %ds timeout). It must pass before the goal can complete.",
		EscapeMarkdown(command), goals.GateMaxRetries, int(goals.GateTimeout/time.Second))
}

// goalGate lists, adds, removes, or clears the quality gates of the goal, like Hermes _gate.
// A dangerous gate waits for the confirm of the owner. A gate edit keeps the epoch.
func goalGate(ctx context.Context, c *CommandCtx, sess *sessions.Session, cmd goals.Command) (Reply, error) {
	switch cmd.GateOp {
	case "list":
		st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
		if err != nil {
			return Reply{}, err
		}
		if !ok || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			return Reply{Text: gateNoGoalList}, nil
		}
		return Reply{Text: EscapeMarkdown(goals.RenderGates(st.Gates))}, nil
	case "add":
		return gateAdd(ctx, c, sess, cmd.GateArg)
	case "ok":
		gc, ok := takeGateConfirm(c.Key, cmd.GateIndex, nowFunc())
		if !ok {
			return Reply{Text: gateConfirmInvalid, Edit: true}, nil
		}
		rep, err := addGate(ctx, c.Deps, gc.sessionID, gc.command)
		rep.Edit = true
		return rep, err
	case "no":
		takeGateConfirm(c.Key, cmd.GateIndex, nowFunc())
		return Reply{Text: gateNotAddedReply, Edit: true}, nil
	}
	var (
		removed goals.Gate
		count   int
		opErr   error
	)
	_, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			opErr = errNoActiveGoal
			return errOpFailed
		}
		if cmd.GateOp == "remove" {
			removed, opErr = st.RemoveGate(cmd.GateIndex)
			if opErr != nil {
				return errOpFailed
			}
			return nil
		}
		count = len(st.Gates)
		st.ClearGates()
		return nil
	})
	if errors.Is(err, errOpFailed) {
		return Reply{Text: "/goal gate " + cmd.GateOp + ": " + EscapeMarkdown(opErr.Error())}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	if cmd.GateOp == "remove" {
		return Reply{Text: "✓ Gate removed: $ " + EscapeMarkdown(removed.Command)}, nil
	}
	return Reply{Text: "✓ Cleared " + countWord(count, "gate") + "."}, nil
}

// gateAdd checks command once and adds it as a gate. A gate runs before each judge call
// with no approval prompt, so a refused command is never added, and a dangerous command
// needs the confirm of the owner.
func gateAdd(ctx context.Context, c *CommandCtx, sess *sessions.Session, command string) (Reply, error) {
	st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
	if err != nil {
		return Reply{}, err
	}
	if !ok || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
		return Reply{Text: "/goal gate add: " + errNoActiveGoal.Error()}, nil
	}
	check := goals.CheckGateCommand(command, !strings.HasPrefix(c.Key, "cli:"), sudoPasswordSet(c, sess))
	if check.Refused {
		return Reply{Text: EscapeMarkdown(check.Reason)}, nil
	}
	if !check.Dangerous {
		return addGate(ctx, c.Deps, sess.ID, command)
	}
	text := "⚠️ This gate matches a dangerous pattern: " + EscapeMarkdown(check.Description) +
		". The gate runs before each judge call, with no approval prompt. Add it?"
	switch {
	case c.interactive():
		n := putGateConfirm(c.Key, gateConfirm{sessionID: sess.ID, command: command,
			expires: nowFunc().Add(approvalTimeout(c.Deps))})
		num := strconv.Itoa(n)
		return Reply{Text: text, Buttons: [][]Button{{
			{Text: "Add gate", Data: "/goal gate ok " + num},
			{Text: "Cancel", Data: "/goal gate no " + num},
		}}}, nil
	case c.Confirm != nil && c.Confirm(StripMarkdown(text)):
		return addGate(ctx, c.Deps, sess.ID, command)
	}
	return Reply{Text: gateNotAddedReply}, nil
}

// addGate adds command as a gate of the goal of the session and returns the added line.
// The goal must be active or paused.
func addGate(ctx context.Context, d *Deps, sessionID, command string) (Reply, error) {
	var added string
	_, err := d.Goals.Update(ctx, sessionID, func(st *goals.State, exists bool) error {
		if !exists || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			return errNoActiveGoal
		}
		if n := st.AddGate(command); n > 0 {
			added = st.Gates[n-1].Command
		}
		return nil
	})
	if errors.Is(err, errNoActiveGoal) {
		return Reply{Text: "/goal gate add: " + errNoActiveGoal.Error()}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	return Reply{Text: gateAddedLine(added)}, nil
}

// sudoPasswordSet reports whether the approval gate of the session knows a sudo password.
func sudoPasswordSet(c *CommandCtx, sess *sessions.Session) bool {
	if c.Deps.NewEnv == nil {
		return false
	}
	channel, _, _ := strings.Cut(c.Key, ":")
	env := c.Deps.NewEnv(sess, channel)
	if env == nil {
		return false
	}
	switch g := env.Gate.(type) {
	case tools.GateApprover:
		return g.Gate != nil && g.Gate.SudoPasswordSet
	case *tools.GateApprover:
		return g != nil && g.Gate != nil && g.Gate.SudoPasswordSet
	}
	return false
}
