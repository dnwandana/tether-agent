package goals

import (
	"context"
	"errors"
	"fmt"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// JudgeLog records each judge call and each draft call of a goal in one hidden session of kind
// goal_judge, so /usage shows the cost. Hermes does not record this cost.
type JudgeLog struct {
	Sessions *sessions.Store
	// Meta gives the meta of the assistant message. Nil uses the stamp of the result with no
	// price.
	Meta func(prov provider.Provider, out provider.Result) sessions.MessageMeta
}

// Record appends the prompt and the answer of one call to the judge session of the goal. It
// creates the session when st.JudgeSessionID is "" or names a deleted session, and sets the new
// ID in st. The caller saves the state. The system prompt is not saved.
func (l *JudgeLog) Record(ctx context.Context, goalSession *sessions.Session, st *State, prompt string, prov provider.Provider, out provider.Result) error {
	// The provider already billed the call, so a cancel must not stop the record.
	ctx = context.WithoutCancel(ctx)
	if st.JudgeSessionID != "" {
		// Retention deletes an old goal_judge session, for example while the goal stays paused.
		// Append ignores a missing session, so the messages would have no session.
		_, err := l.Sessions.Get(ctx, st.JudgeSessionID)
		switch {
		case errors.Is(err, sessions.ErrNotFound):
			st.JudgeSessionID = ""
		case err != nil:
			return fmt.Errorf("goals: record the judge call of session %s: %w", goalSession.ID, err)
		}
	}
	if st.JudgeSessionID == "" {
		// The session has no key, so no channel makes it active and /sessions does not show it.
		sess, err := l.Sessions.Create(ctx, sessions.Session{
			Source:   goalSession.Source,
			ChatID:   goalSession.ChatID,
			ThreadID: goalSession.ThreadID,
			UserID:   goalSession.UserID,
			Title:    "goal judge",
			ParentID: goalSession.ID,
			Kind:     sessions.KindGoalJudge,
		})
		if err != nil {
			return fmt.Errorf("goals: record the judge call of session %s: %w", goalSession.ID, err)
		}
		st.JudgeSessionID = sess.ID
	}
	if _, err := l.Sessions.Append(ctx, st.JudgeSessionID, provider.Message{Role: provider.RoleUser, Content: prompt}, sessions.MessageMeta{}); err != nil {
		return fmt.Errorf("goals: record the judge call of session %s: %w", goalSession.ID, err)
	}
	if _, err := l.Sessions.Append(ctx, st.JudgeSessionID, provider.Message{Role: provider.RoleAssistant, Content: out.Text}, l.meta(prov, out)); err != nil {
		return fmt.Errorf("goals: record the judge call of session %s: %w", goalSession.ID, err)
	}
	return nil
}

// meta returns the meta of the assistant message. The fallback is sessions.ResultMeta, the rule
// of Agent.meta with no price.
func (l *JudgeLog) meta(prov provider.Provider, out provider.Result) sessions.MessageMeta {
	if l.Meta != nil {
		return l.Meta(prov, out)
	}
	return sessions.ResultMeta(prov, out)
}
