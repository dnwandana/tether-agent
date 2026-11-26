package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/memory"
)

// fakeQuotes is a QuoteSource. It records each call as "<session>|<quote>".
type fakeQuotes struct {
	ok    bool
	err   error
	calls []string
}

func (q *fakeQuotes) HasQuote(_ context.Context, sessionID, quote string) (bool, error) {
	q.calls = append(q.calls, sessionID+"|"+quote)
	return q.ok, q.err
}

func TestCheckQuote(t *testing.T) {
	review := kindEnv(t, "review", "r1")
	review.RootSessionID = "c1"
	cases := []struct {
		name      string
		env       *Env
		op        memory.Op
		quotes    *fakeQuotes
		wantErr   string
		wantOK    bool
		wantCalls []string
	}{
		{"review user hit", review, memory.Op{Action: "add", Target: "user", Source: "review", Quote: "I live in Jakarta now"},
			&fakeQuotes{ok: true}, "", true, []string{"c1|I live in Jakarta now"}},
		{"review user miss", review, memory.Op{Action: "replace", Target: "user", Source: "review", Quote: "I live in Jakarta now"},
			&fakeQuotes{}, "A write to the user profile needs a quote of the owner's exact words from this session. No owner message has: I live in Jakarta now.", false, []string{"c1|I live in Jakarta now"}},
		{"review user no quote", review, memory.Op{Action: "add", Target: "user", Source: "review"},
			&fakeQuotes{ok: true}, "A write to the user profile needs a quote of the owner's exact words from this session, with 3 or more words.", false, nil},
		{"under 3 words", review, memory.Op{Action: "add", Target: "user", Source: "review", Quote: "Jakarta now"},
			&fakeQuotes{ok: true}, "with 3 or more words", false, nil},
		{"review memory needs no quote", review, memory.Op{Action: "add", Target: "memory", Source: "review"},
			&fakeQuotes{}, "", false, nil},
		{"review remove needs no quote", review, memory.Op{Action: "remove", Target: "user", Source: "review"},
			&fakeQuotes{}, "", false, nil},
		{"chat rule hit", kindEnv(t, "chat", "c2"), memory.Op{Action: "add", Target: "rule", Source: "chat", Quote: "never send a file unless I ask"},
			&fakeQuotes{ok: true}, "", true, []string{"c2|never send a file unless I ask"}},
		{"chat rule miss", kindEnv(t, "chat", "c2"), memory.Op{Action: "add", Target: "rule", Source: "chat", Quote: "never send a file unless I ask"},
			&fakeQuotes{}, "A rule needs a quote of the owner's exact words from this session. No owner message has: never send a file unless I ask.", false, []string{"c2|never send a file unless I ask"}},
		{"chat user optional miss", kindEnv(t, "chat", "c2"), memory.Op{Action: "add", Target: "user", Source: "chat", Quote: "I prefer short answers"},
			&fakeQuotes{}, "", false, []string{"c2|I prefer short answers"}},
		{"chat user no quote", kindEnv(t, "chat", "c2"), memory.Op{Action: "add", Target: "user", Source: "chat"},
			&fakeQuotes{}, "", false, nil},
		{"tidy move into rule", kindEnv(t, "tidy", "t1"), memory.Op{Action: "move", To: "file", Target: "rule", Source: "tidy", Quote: "never send a file unless I ask"},
			&fakeQuotes{ok: true}, "", true, []string{"|never send a file unless I ask"}},
		{"tidy user", kindEnv(t, "tidy", "t1"), memory.Op{Action: "replace", Target: "user", Source: "tidy", Quote: "my name is Dana"},
			&fakeQuotes{ok: true}, "", true, []string{"|my name is Dana"}},
		{"lookup error", review, memory.Op{Action: "add", Target: "user", Source: "review", Quote: "I live in Jakarta now"},
			&fakeQuotes{err: errors.New("db closed")}, "the quote check failed: db closed", false, []string{"c1|I live in Jakarta now"}},
	}
	for _, c := range cases {
		op := c.op
		err := MemoryTool{Quotes: c.quotes}.checkQuote(ctx, c.env, &op)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
		}
		if op.QuoteOK != c.wantOK || strings.Join(c.quotes.calls, ",") != strings.Join(c.wantCalls, ",") {
			t.Errorf("%s: QuoteOK %v, calls %v", c.name, op.QuoteOK, c.quotes.calls)
		}
	}
	op := memory.Op{Action: "add", Target: "user", Source: "review", Quote: "I live in Jakarta now"}
	if err := (MemoryTool{}).checkQuote(ctx, review, &op); err == nil || !strings.Contains(err.Error(), "the quote check is not available in this session") {
		t.Fatal("a required check with no QuoteSource must refuse:", err)
	}
	op = memory.Op{Action: "add", Target: "user", Source: "chat", Quote: "I prefer short answers"}
	if err := (MemoryTool{}).checkQuote(ctx, kindEnv(t, "chat", "c2"), &op); err != nil || op.QuoteOK {
		t.Fatal("an optional check with no QuoteSource must pass with no verdict:", err)
	}
}

func TestMemoryToolRunsQuoteCheck(t *testing.T) {
	s := openStore(t)
	s.History = &fakeHistory{}
	env := kindEnv(t, "review", "r1")
	env.RootSessionID = "c1"
	args := `{"action":"add","target":"user","content":"Lives in Jakarta","quote":"I live in Jakarta now"}`
	out := runToolEnvErr(t, MemoryTool{Store: s, Quotes: &fakeQuotes{}}, args, env)
	if !strings.Contains(out, "No owner message has: I live in Jakarta now.") || len(s.Entries(memory.TargetUser)) != 0 {
		t.Fatal(out)
	}
	runToolEnv(t, MemoryTool{Store: s, Quotes: &fakeQuotes{ok: true}}, args, env)
	if got := s.Entries(memory.TargetUser); len(got) != 1 || got[0] != "Lives in Jakarta" {
		t.Fatal(got)
	}
}

// A review or a tidy cannot put a note entry into the user profile with no quote, because a
// note write to target memory needs no quote.
func TestMoveIntoUserFileNeedsQuote(t *testing.T) {
	for _, kind := range []string{"review", "tidy"} {
		op := memory.Op{Action: "move", To: "file", Target: "user", Topic: "inbox-memory", Source: kind}
		err := (MemoryTool{Quotes: &fakeQuotes{ok: true}}).checkQuote(ctx, kindEnv(t, kind, "s1"), &op)
		if err == nil || !strings.Contains(err.Error(), "A write to the user profile needs a quote") {
			t.Fatalf("%s: %v", kind, err)
		}
		op.Quote = "I live in Jakarta now"
		if err := (MemoryTool{Quotes: &fakeQuotes{ok: true}}).checkQuote(ctx, kindEnv(t, kind, "s1"), &op); err != nil || !op.QuoteOK {
			t.Fatalf("%s with quote: %v", kind, err)
		}
	}
	// A move into a note takes nothing into the prompt, so it needs no quote.
	op := memory.Op{Action: "move", To: "note", Target: "user", Topic: "old", Source: "review"}
	if err := (MemoryTool{}).checkQuote(ctx, kindEnv(t, "review", "s1"), &op); err != nil {
		t.Fatal(err)
	}
}
