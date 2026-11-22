package agent

import (
	"context"
	"fmt"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
)

// ContextEstimate returns the size of the model view of the session in tokens, the context
// window of its model, and the compaction threshold. It is the size that the compaction
// check uses. It makes no model call.
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
	window = a.contextWindow(a.providerFor(s))
	if a.Compactor != nil {
		threshold = a.Compactor.Cfg.CompactThreshold
	} else {
		threshold = a.config().Context.CompactThreshold
	}
	return tokens, window, threshold, nil
}
