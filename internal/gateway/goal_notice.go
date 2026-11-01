package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goOnText ends each notice of a pause that the owner can resume or clear.
const goOnText = " Use /goal resume to keep going, or /goal clear to stop."

// StopButtons returns the buttons of a notice that stops the goal loop. Each action is a
// typed command, so a tap runs it through Gateway.Submit.
func StopButtons() [][]Button {
	return [][]Button{{{Text: "Resume", Data: "/goal resume"}, {Text: "Clear", Data: "/goal clear"}}}
}

// FormatNotice returns the reply of a goal notice. The texts come from Hermes. The judge
// failure texts name goals.judge_provider, because the Hermes config path does not exist
// here. Each user or model string goes through EscapeMarkdown. An unknown kind gives an
// empty Reply.
func FormatNotice(n goals.Notice) Reply {
	reason := EscapeMarkdown(n.Reason)
	var text string
	switch n.Kind {
	case goals.NoticeDone:
		text = "✓ Goal achieved: " + reason
	case goals.NoticeBlocked:
		text = "🚫 Goal judged unachievable — paused: " + reason + " Re-scope with /goal set, or override with /goal resume."
	case goals.NoticeBudget:
		text = fmt.Sprintf("⏸ Goal paused — %d/%d turns used.", n.Used, n.Max) + goOnText
	case goals.NoticeBudgetGate:
		text = fmt.Sprintf("⏸ Goal paused — %d/%d turns used (a quality gate is still failing).", n.Used, n.Max) + goOnText
	case goals.NoticeParseFail:
		text = fmt.Sprintf("⏸ Goal paused — the judge reply could not be parsed %d times. Check the model of goals.judge_provider, then /goal resume.", n.Count)
	case goals.NoticeTransportFail:
		text = fmt.Sprintf("⏸ Goal paused — the judge call failed %d times. Check goals.judge_provider and its key, then /goal resume.", n.Count)
	case goals.NoticeParkedJudge:
		text = "⏳ Goal parked (judge) — waiting on " + EscapeMarkdown(n.Target) + ": " + reason
	case goals.NoticeStillParked:
		text = "⏳ Goal parked — waiting on " + EscapeMarkdown(n.Target) + ": " + reason
	case goals.NoticeGateFailed:
		text = fmt.Sprintf("✗ Quality gate failed (%d/%d turns, attempt %d/%d): $ ", n.Used, n.Max, n.Attempt, n.MaxRetries) + EscapeMarkdown(n.Command)
	case goals.NoticeGateExhausted:
		text = fmt.Sprintf("⏸ Goal paused — quality gate still failing after %d retries: $ ", n.MaxRetries) + EscapeMarkdown(n.Command) +
			fmt.Sprintf(" (exit %d). Fix it manually or /goal gate remove it, then /goal resume.", n.ExitCode)
	case goals.NoticeTurnFailed:
		text = "⏸ Goal paused — the last turn failed: " + EscapeMarkdown(n.Err) + "." + goOnText
	case goals.NoticeRestartPause:
		text = "⏸ Goal paused — the gateway was down for " + formatDown(n.Down) + "." + goOnText
	case goals.NoticeLoopPause:
		text = "⏸ Goal paused — " + reason + "." + goOnText
	default:
		return Reply{}
	}
	rep := Reply{Text: text}
	if n.StopButtons {
		rep.Buttons = StopButtons()
	}
	return rep
}

// formatDown returns d in whole minutes, rounded down: "N min" under 1 hour (at least
// "1 min"), else "H h" or "H h M min".
func formatDown(d time.Duration) string {
	mins := max(int(d/time.Minute), 1)
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// sendGoalNotice sends a goal notice to the owner of key. A turn renderer r shows it in the
// turn. With no turn, Deps.SendNotice sends it with the buttons. Else the channel of sess
// gets the text only, because Channel.Send puts the raw data into a button.
func (g *Gateway) sendGoalNotice(ctx context.Context, key string, sess *sessions.Session, r Renderer, n goals.Notice) {
	rep := FormatNotice(n)
	if rep.Text == "" {
		return
	}
	if r != nil {
		// The CLI renderer has no buttons, and the texts name the commands.
		if rr, ok := r.(ReplyRenderer); ok {
			rr.AckReply(rep)
		} else {
			r.Ack(rep.Text)
		}
		return
	}
	log := g.Deps.logger()
	if g.Deps.SendNotice != nil {
		if err := g.Deps.SendNotice(ctx, key, sess, rep); err != nil {
			log.Warn("cannot send the goal notice", "key", key, "kind", n.Kind, "err", err.Error())
		}
		return
	}
	if sess == nil {
		log.Warn("cannot send the goal notice: no session", "key", key, "kind", n.Kind)
		return
	}
	ch := g.Channels[sess.Source]
	if ch == nil {
		log.Warn("cannot send the goal notice: no channel for the source", "key", key, "kind", n.Kind, "source", sess.Source)
		return
	}
	to := Target{Source: sess.Source, ChatID: sess.ChatID, ThreadID: sess.ThreadID}
	if err := ch.Send(ctx, to, Outbound{Text: rep.Text}); err != nil {
		log.Warn("cannot send the goal notice", "key", key, "kind", n.Kind, "err", err.Error())
	}
}
