package tools

import (
	"context"
	"strings"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// QuoteSource reports whether an owner message holds a quote. The tools package cannot
// import agent, ctxmgr, or goals, so app gives the tool the cleaned owner texts
// through this interface. An empty sessionID searches all owner sessions.
type QuoteSource interface {
	HasQuote(ctx context.Context, sessionID, quote string) (bool, error)
}

// quoteMinWords is the smallest number of words in a quote.
const quoteMinWords = 3

// checkQuote checks the quote of a write. A required check refuses the write on a
// miss. An optional check never refuses: it only sets op.QuoteOK.
func (t MemoryTool) checkQuote(ctx context.Context, env *Env, op *memory.Op) error {
	toFile := op.Action == memory.ActionMove && op.To == "file"
	required := (op.Source == memory.SourceReview || op.Source == memory.SourceTidy) &&
		op.Target == memory.TargetUser && (op.Action == "add" || op.Action == "replace" || toFile)
	if op.Source == memory.SourceChat && op.Target == memory.TargetRule &&
		(op.Action == "add" || op.Action == "replace" || toFile) {
		required = true
	}
	if op.Source == memory.SourceTidy && op.Target == memory.TargetRule && toFile {
		required = true
	}
	optional := op.Source == memory.SourceChat && op.Quote != "" && op.Action != "remove"
	if !required && !optional {
		return nil
	}
	subject := "A write to the user profile"
	if op.Target == memory.TargetRule {
		subject = "A rule"
	}
	if len(strings.Fields(textmatch.Normalize(op.Quote))) < quoteMinWords {
		if required {
			return toolErr("%s needs a quote of the owner's exact words from this session, with 3 or more words.", subject)
		}
		return nil
	}
	if t.Quotes == nil {
		if required {
			return toolErr("the quote check is not available in this session")
		}
		return nil
	}
	session := ""
	switch {
	case env == nil || op.Source == memory.SourceTidy:
	case op.Source == memory.SourceReview:
		session = env.RunRootSession()
	default:
		session = env.SessionID
	}
	ok, err := t.Quotes.HasQuote(ctx, session, op.Quote)
	if err != nil {
		if required {
			return toolErr("the quote check failed: %v", err)
		}
		return nil
	}
	if ok {
		op.QuoteOK = true
		return nil
	}
	if required {
		return toolErr("%s needs a quote of the owner's exact words from this session. No owner message has: %s.", subject, op.Quote)
	}
	return nil
}
