package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
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
