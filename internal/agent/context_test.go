package agent

import (
	"errors"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// With no provider count, the estimate is the character estimate of the history.
func TestContextEstimateCountsHistory(t *testing.T) {
	a, sess := compactingAgent(t, scripted())
	tokens, window, threshold, err := a.ContextEstimate(ctx, sess.ID)
	// 80 messages of 58 bytes, plus 16 for each message, divided by 4.
	if err != nil || tokens != 1480 || window != 2000 || threshold != 0.50 {
		t.Fatal(tokens, window, threshold, err)
	}
	if _, _, _, err := a.ContextEstimate(ctx, "no-such-session"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatal(err)
	}
}

// The prompt count of the last response counts, plus the estimate of the newer messages.
func TestContextEstimateUsesProviderCount(t *testing.T) {
	a, sess := newAgent(t, scripted())
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 8000}
	seedHistory(t, a, sess.ID, 4)
	if _, err := a.Store.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "answer"},
		sessions.MessageMeta{Usage: provider.Usage{InputTokens: 900, CacheReadTokens: 100}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	tokens, window, threshold, err := a.ContextEstimate(ctx, sess.ID)
	// 1000 from the provider, plus ("answer" + 16 + "hi" + 16) / 4 = 10.
	if err != nil || tokens != 1010 || window != 8000 || threshold != 0.50 {
		t.Fatal(tokens, window, threshold, err)
	}
}

// The estimate is the number that the compaction check uses, and it drops after a compaction.
func TestContextEstimateMatchesCompactionCheck(t *testing.T) {
	a, sess := compactingAgent(t, scripted(text("SUM")))
	before, _, _, err := a.ContextEstimate(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.CompactNow(ctx, sess)
	if err != nil || res.Replaced == 0 || res.TokensBefore != before {
		t.Fatal(res, before, err)
	}
	after, _, _, err := a.ContextEstimate(ctx, sess.ID)
	if err != nil || after >= before {
		t.Fatal("the estimate after a compaction must be smaller:", after, before, err)
	}
}

// The ratio of ContextEstimate is the trigger of the compaction check: the cap when it is
// lower than threshold × window. The check compacts just above it and not at it.
func TestContextEstimateGivesTheCompactionTrigger(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := compactingAgent(t, fp)
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 4000, Vision: true}
	a.Compactor.Cfg.CompactAtTokens = 1500 // below 0.5 × 4000
	tokens, window, threshold, err := a.ContextEstimate(ctx, sess.ID)
	if err != nil || tokens != 1480 || window != 4000 {
		t.Fatal(tokens, window, err)
	}
	trigger := int(threshold * float64(window))
	if trigger != 1500 || trigger != ctxmgr.Trigger(4000, 0.5, 1500) {
		t.Fatalf("threshold × window = %d, want the trigger 1500", trigger)
	}
	stored := messages(t, a, sess.ID)
	sink := &noticeSink{}
	// With no provider count, the check size is fixed + 1480.
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, trigger-tokens); did {
		t.Fatal("a view at the trigger must not compact")
	}
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, trigger-tokens+1); !did {
		t.Fatal("a view just above the trigger must compact")
	}
	// With no compactor, the config gives the ratio.
	a.Compactor = nil
	a.Cfg.Context.CompactAtTokens = 1500
	if _, _, threshold, err := a.ContextEstimate(ctx, sess.ID); err != nil || threshold != 0.375 {
		t.Fatal("no compactor, cap 1500:", threshold, err)
	}
	a.Cfg.Context.CompactAtTokens = 0
	if _, _, threshold, err := a.ContextEstimate(ctx, sess.ID); err != nil || threshold != 0.5 {
		t.Fatal("no compactor, no cap:", threshold, err)
	}
}
