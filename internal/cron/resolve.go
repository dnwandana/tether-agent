package cron

import (
	"context"
	"errors"
	"fmt"
)

// The errors of ResolveHeld.
var (
	// ErrHeldGone means that no row has the id: another answer or the sweep ended it.
	ErrHeldGone = errors.New("cron: the held send has an answer already")
	// ErrHeldNotSent means that the send failed. The row is back, so the owner can try again.
	ErrHeldNotSent = errors.New("cron: the held send was not sent")
)

// ResolveHeld posts or drops the held send id for Telegram and the dashboard. It claims the
// row, so only one answer wins. A failed send puts the row back. After a good answer, it marks
// the run of a hold row and calls editCard with the result ("Sent.", "Posted.", or "Dropped.").
func ResolveHeld(ctx context.Context, st *Store, id string, action string, /* post, drop */
	send func(ctx context.Context, h HeldSend) error, editCard func(h HeldSend, result string)) (result string, err error) {
	held := &HeldStore{DB: st.DB}
	h, ok, err := held.Claim(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrHeldGone
	}
	restore := func(cause error) error {
		if rerr := held.Restore(ctx, h); rerr != nil {
			return errors.Join(cause, rerr)
		}
		return cause
	}
	var delivery string
	switch action {
	case "post":
		if send == nil {
			send = func(context.Context, HeldSend) error { return errors.New("no send function is set") }
		}
		if serr := send(ctx, h); serr != nil {
			return "Not sent: " + serr.Error() + ".",
				restore(fmt.Errorf("%w: %w", ErrHeldNotSent, serr))
		}
		delivery = DeliverySent
		result = "Sent."
		if h.Kind == "original" {
			result = "Posted."
		}
	case "drop":
		delivery = DeliveryDropped
		result = "Dropped."
	default:
		return "", restore(fmt.Errorf("cron: held send action %q is not valid. Use post or drop", action))
	}
	var markErr error
	if h.Kind == "hold" && h.RunID > 0 {
		if merr := st.UpdateRunDelivery(ctx, h.RunID, delivery); merr != nil {
			markErr = fmt.Errorf("cron: mark the run of held send %s: %w", h.ID, merr)
		}
	}
	if editCard != nil {
		editCard(h, result)
	}
	return result, markErr
}
