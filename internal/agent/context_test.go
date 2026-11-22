package agent

import (
	"errors"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
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
