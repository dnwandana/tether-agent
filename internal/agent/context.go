package agent

import (
	"context"
	"fmt"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
)

// ContextEstimate returns the size of the model view of the session in tokens, the context
// window of the entry that serves its calls, and the compaction trigger as a part of that
// window (trigger / window). These are the size and the trigger that the compaction check
// uses. It makes no model call.
func (a *Agent) ContextEstimate(ctx context.Context, sessionID string) (tokens, window int, threshold float64, err error) {
	s, err := a.Store.Get(ctx, sessionID)
	if err != nil {
		return 0, 0, 0, err
	}
	// The first read closes its rows before the second read starts.
	stored, err := a.Store.Messages(ctx, sessionID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("agent: read the history: %w", err)
	}
	comps, err := ctxmgr.LatestCompaction(ctx, a.DB, sessionID)
	if err != nil {
		return 0, 0, 0, err
	}
	// With no shape, a provider count in the history already holds the prompt and the tools.
	fixed := 0
	if sh := a.Reviews.shape(sessionID); sh.haveShape {
		fixed = ctxmgr.EstimateFixed(sh.system, sh.schemas)
	}
	tokens = ctxmgr.StoredSize(stored, comps, fixed)
	window = a.servedWindow(a.providerFor(s))
	var trigger int
	if a.Compactor != nil {
		trigger = a.Compactor.TriggerFor(sessionID, window)
	} else {
		cfg := a.config().Context
		trigger = ctxmgr.Trigger(window, cfg.CompactThreshold, cfg.CompactAtTokens)
	}
	if window > 0 {
		threshold = float64(trigger) / float64(window)
	}
	return tokens, window, threshold, nil
}
