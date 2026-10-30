package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// ResumeLoopText goes to the chat of a key whose turn a restart cut too many times in a row.
const ResumeLoopText = "⚠️ I stopped the automatic resume after 3 restarts in a row. Send a message to continue."

// LostMediaLine replaces the images and the files of a queued message, because a turn
// marker keeps the text only.
const LostMediaLine = "[A photo or a file of this message was lost in a restart.]"

// maxResumes is the number of resumes in a row after which a key does not resume again. A
// turn that crashes the gateway each time must not run forever. Hermes uses a window of 1 h
// and a limit of 3.
const maxResumes = 3

// ResumeInterrupted continues the turns that a restart cut. For each fresh marker, it
// enqueues agent.ResumeNote and then each queued text of the marker. It never runs a tool
// call again: the only new turns are the enqueued texts.
//
// cut is the time when the last gateway stopped. A zero cut uses the UpdatedAt of each
// marker. A marker older than freshness is deleted. A zero freshness deletes every marker
// and runs no turn. Only an error of the marker read is returned. The other errors are
// logged, and the next marker continues.
func (g *Gateway) ResumeInterrupted(ctx context.Context, cut time.Time, freshness time.Duration) error {
	if g.Deps == nil || g.Deps.Store == nil {
		return nil
	}
	// Markers closes its rows before it returns, so the loop below can query the database.
	markers, err := g.Deps.Store.Markers(ctx)
	if err != nil {
		return fmt.Errorf("gateway: resume the cut turns: %w", err)
	}
	log := g.logger()
	now := time.Now()
	for _, m := range markers {
		at := cut
		if at.IsZero() {
			at = m.UpdatedAt
		}
		switch {
		case freshness <= 0:
			log.Info("gateway: the resume is off, so the turn does not resume", "key", m.SessionKey)
			g.deleteMarker(ctx, m.SessionKey)
		case now.Sub(at) > freshness:
			log.Info("gateway: a turn marker is too old, so the turn does not resume",
				"key", m.SessionKey, "age", now.Sub(at).Round(time.Second).String())
			g.deleteMarker(ctx, m.SessionKey)
		case m.Resumes >= maxResumes:
			log.Warn("gateway: the turn resumed too many times in a row, so it does not resume",
				"key", m.SessionKey, "resumes", m.Resumes)
			g.deleteMarker(ctx, m.SessionKey)
			g.sendResumeLoop(ctx, m.SessionKey)
		default:
			g.resumeMarker(ctx, m)
		}
	}
	return nil
}

// resumeMarker enqueues the note turn and the queued texts of m. A key with a different
// active session does not resume, because the user started a new session after the cut.
// A key with no session goes to Enqueue, which fails, so the marker is deleted.
func (g *Gateway) resumeMarker(ctx context.Context, m sessions.TurnMarker) {
	log := g.logger()
	sess, err := g.Deps.Store.Active(ctx, m.SessionKey)
	if err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return
	}
	if sess != nil && sess.ID != m.SessionID {
		log.Info("gateway: the active session of the key changed, so the turn does not resume",
			"key", m.SessionKey, "session", m.SessionID, "active", sess.ID)
		g.deleteMarker(ctx, m.SessionKey)
		return
	}
	// The count goes up before the turn starts, so a crash in the resumed turn counts too.
	// The marker write of the turn keeps the count.
	if err := g.Deps.Store.SetMarkerResumes(ctx, m.SessionKey, m.Resumes+1); err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return
	}
	if err := g.Enqueue(ctx, m.SessionKey, agent.ResumeNote); err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return
	}
	log.Info("gateway: resume the cut turn", "key", m.SessionKey, "session", m.SessionID,
		"resumes", m.Resumes+1, "queued", len(m.Queued))
	for _, q := range m.Queued {
		text := resumeText(q)
		if text == "" {
			continue
		}
		// The note turn runs now and writes the marker of the key, so the marker is not
		// deleted here. The text is a message of the owner, so /now counts it.
		if err := g.enqueue(ctx, m.SessionKey, text, false); err != nil {
			log.Warn("gateway: cannot resume a queued message", "key", m.SessionKey, "err", err.Error())
			return
		}
	}
}

// resumeText returns the turn text of a queued text. A message whose images or files were
// lost gets LostMediaLine. It returns "" when nothing is left to send.
func resumeText(q sessions.QueuedText) string {
	switch {
	case q.Lost > 0 && q.Text != "":
		return q.Text + "\n" + LostMediaLine
	case q.Lost > 0:
		return LostMediaLine
	default:
		return q.Text
	}
}

// sendResumeLoop sends ResumeLoopText to the chat of the active session of key.
func (g *Gateway) sendResumeLoop(ctx context.Context, key string) {
	in, _, err := g.sendRenderer(ctx, key)
	if err == nil {
		err = g.Channels[in.Source].Send(ctx, in.Target, Outbound{Text: ResumeLoopText})
	}
	if err != nil {
		g.logger().Warn("gateway: cannot send the resume loop message", "key", key, "err", err.Error())
	}
}

// deleteMarker deletes the marker of key directly. At boot no turn writes the marker of
// key, so the write does not need the marker writer.
func (g *Gateway) deleteMarker(ctx context.Context, key string) {
	if err := g.Deps.Store.DeleteMarker(ctx, key); err != nil {
		g.logger().Warn("gateway: cannot delete the turn marker", "key", key, "err", err.Error())
	}
}
