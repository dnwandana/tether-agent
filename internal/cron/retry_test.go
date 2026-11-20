package cron_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const retryLine = "An earlier start of this run failed after it used tools. Check what it did before you act again."

// connErr is a turn error after a transport failure, wrapped as agent.RunTurn wraps it.
func connErr() error {
	return fmt.Errorf("agent: the model call to primary failed: %w",
		fmt.Errorf("provider primary: %w: %w: dial tcp: connection refused", provider.ErrBeforeFirstByte, provider.ErrConnection))
}

// statusErr is a turn error after an HTTP status that triggers the fallback.
func statusErr(code int) error {
	return fmt.Errorf("agent: the model call to primary failed: %w",
		fmt.Errorf("provider primary: %w: %w: body", provider.ErrBeforeFirstByte, &provider.HTTPStatusError{Code: code, Body: "body"}))
}

// turnLog records the starts of a run.
type turnLog struct {
	texts, sessions []string
	deadlines       []time.Time
	waits           []time.Duration
}

// retryRunner returns a fixture with a stored job "digest", a Sleep hook that records each wait,
// and a Turn that returns errs in order. A start after the errs answers "done".
func retryRunner(t *testing.T, errs ...error) (*fixture, cron.Job, *turnLog) {
	t.Helper()
	f := newFixture(t)
	j, err := f.jobs.Create(bg, cron.Job{Name: "digest", Schedule: "30m", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	l := &turnLog{}
	f.r.Sleep = func(_ context.Context, d time.Duration) error { l.waits = append(l.waits, d); return nil }
	f.r.Turn = func(ctx context.Context, ct cron.CronTurn) (cron.TurnOutput, error) {
		n := len(l.texts)
		dl, _ := ctx.Deadline()
		l.texts, l.sessions, l.deadlines = append(l.texts, ct.Text), append(l.sessions, ct.Session.ID), append(l.deadlines, dl)
		if n < len(errs) && errs[n] != nil {
			return cron.TurnOutput{}, errs[n]
		}
		return cron.TurnOutput{Final: "done"}, nil
	}
	return f, j, l
}

func TestRetryAfterTemporaryFailure(t *testing.T) {
	for name, e := range map[string]error{"connection": connErr(), "408": statusErr(408), "429": statusErr(429), "503": statusErr(503)} {
		f, j, l := retryRunner(t, e)
		o := f.r.Run(bg, j, false)
		if len(l.texts) != 2 || !reflect.DeepEqual(l.waits, []time.Duration{60 * time.Second}) ||
			o.Status != "ok" || o.Text != "done" || o.ErrKey != "" {
			t.Fatalf("%s: %+v, %d starts, waits %v", name, o, len(l.texts), l.waits)
		}
		if l.texts[0] != l.texts[1] || l.sessions[0] == l.sessions[1] ||
			!reflect.DeepEqual(o.SessionIDs, l.sessions) || o.SessionID != l.sessions[1] {
			t.Fatalf("%s: %+v, %v", name, o, l.sessions)
		}
	}
}

func TestNoRetryForLastingFailure(t *testing.T) {
	for name, e := range map[string]error{
		"401":      statusErr(401),
		"402":      statusErr(402),
		"403":      statusErr(403),
		"400":      fmt.Errorf("provider primary: %w: bad request", &provider.HTTPStatusError{Code: 400}),
		"unusable": fmt.Errorf("provider x: %w: needs an API key", provider.ErrBeforeFirstByte),
		"vision":   fmt.Errorf("provider x: %w: no entry of the chain has vision", provider.ErrBeforeFirstByte),
		"other":    errors.New("boom"),
	} {
		f, j, l := retryRunner(t, e)
		o := f.r.Run(bg, j, false)
		if len(l.texts) != 1 || len(l.waits) != 0 || o.Status != "error" || len(o.SessionIDs) != 1 {
			t.Fatalf("%s: %+v, %d starts", name, o, len(l.texts))
		}
	}
}

func TestNoRetryAfterStopTimeoutOrEmpty(t *testing.T) {
	// A stop of the gateway.
	f, j, l := retryRunner(t)
	sctx, cancel := context.WithCancel(bg)
	f.r.Turn = func(context.Context, cron.CronTurn) (cron.TurnOutput, error) {
		l.texts = append(l.texts, "x")
		cancel()
		return cron.TurnOutput{}, connErr()
	}
	f.r.Run(sctx, j, false)
	if len(l.texts) != 1 || len(l.waits) != 0 {
		t.Fatal("stop", len(l.texts), l.waits)
	}
	// AgentTimeout.
	f, j, l = retryRunner(t)
	f.r.AgentTimeout = 50 * time.Millisecond
	f.r.Turn = func(ctx context.Context, _ cron.CronTurn) (cron.TurnOutput, error) {
		l.texts = append(l.texts, "x")
		<-ctx.Done()
		return cron.TurnOutput{}, connErr()
	}
	if o := f.r.Run(bg, j, false); len(l.texts) != 1 || o.ErrKey != "timeout" {
		t.Fatal("timeout", o, len(l.texts))
	}
	// An empty answer.
	f, j, l = retryRunner(t)
	f.r.Turn = func(context.Context, cron.CronTurn) (cron.TurnOutput, error) {
		l.texts = append(l.texts, "x")
		return cron.TurnOutput{Final: " "}, nil
	}
	if o := f.r.Run(bg, j, false); len(l.texts) != 1 || o.ErrKey != "empty" {
		t.Fatal("empty", o, len(l.texts))
	}
}

// After the wait, a paused or deleted job does not start again. The run records the first failure.
func TestNoRetryWhenJobStoppedDuringWait(t *testing.T) {
	for name, act := range map[string]func(f *fixture, id string) error{
		"paused":  func(f *fixture, id string) error { return f.jobs.SetEnabled(bg, id, false) },
		"deleted": func(f *fixture, id string) error { return f.jobs.Remove(bg, id) },
	} {
		f, j, l := retryRunner(t, connErr())
		f.r.Sleep = func(context.Context, time.Duration) error { return act(f, j.ID) }
		o := f.r.Run(bg, j, false)
		if len(l.texts) != 1 || o.Status != "error" || o.ErrKey != "connection" ||
			!strings.HasPrefix(o.Text, "⚠ Cron job digest failed:") {
			t.Fatalf("%s: %+v", name, o)
		}
	}
}

// The retry uses the text of the first start. The data script does not run again.
func TestRetryKeepsFirstText(t *testing.T) {
	f, j, l := retryRunner(t, statusErr(503))
	writeScript(t, f.r, "count.sh", "echo run >> runs.txt; echo data")
	j.Script = "count.sh"
	f.r.Run(bg, j, false)
	b, err := os.ReadFile(filepath.Join(f.r.ScriptsDir, "runs.txt"))
	if err != nil || string(b) != "run\n" || len(l.texts) != 2 || l.texts[0] != l.texts[1] || !strings.Contains(l.texts[0], "data") {
		t.Fatalf("%q %v %q", b, err, l.texts)
	}
}

// After a tool call of the first start, the retry text starts with the retry line.
func TestRetryLineAfterToolCall(t *testing.T) {
	f, j, l := retryRunner(t)
	f.r.Turn = func(_ context.Context, ct cron.CronTurn) (cron.TurnOutput, error) {
		l.texts = append(l.texts, ct.Text)
		if len(l.texts) == 1 {
			if _, err := f.store.Append(bg, ct.Session.ID, provider.Message{Role: provider.RoleTool, ToolCallID: "c1",
				ToolName: "web_search", Content: "result"}, sessions.MessageMeta{}); err != nil {
				t.Fatal(err)
			}
			return cron.TurnOutput{}, connErr()
		}
		return cron.TurnOutput{Final: "done"}, nil
	}
	f.r.Run(bg, j, false)
	if len(l.texts) != 2 || l.texts[1] != retryLine+"\n\n"+l.texts[0] {
		t.Fatalf("%q", l.texts)
	}
}

// One AgentTimeout covers the two starts and the wait. With less than 2 minutes left, no retry.
func TestRetryTimeBudget(t *testing.T) {
	f, j, l := retryRunner(t, connErr())
	f.r.AgentTimeout = 10 * time.Minute
	f.r.Run(bg, j, false)
	if len(l.deadlines) != 2 || l.deadlines[0].IsZero() || !l.deadlines[0].Equal(l.deadlines[1]) {
		t.Fatal(l.deadlines)
	}
	f, j, l = retryRunner(t, connErr())
	f.r.AgentTimeout = 90 * time.Second
	if o := f.r.Run(bg, j, false); len(l.texts) != 1 || o.Status != "error" {
		t.Fatal(o, len(l.texts))
	}
}

// A second failure is a normal failed run.
func TestRetryOnlyOnce(t *testing.T) {
	f, j, l := retryRunner(t, connErr(), statusErr(503))
	o := f.r.Run(bg, j, false)
	if len(l.texts) != 2 || o.Status != "error" || o.ErrKey != "http 503" || len(o.SessionIDs) != 2 {
		t.Fatalf("%+v", o)
	}
}

// The cost covers each session of the run. A nil Cost and a script job leave it nil.
func TestRunCost(t *testing.T) {
	f, j, _ := retryRunner(t, connErr())
	var got []string
	f.r.Cost = func(_ context.Context, ids []string) (*float64, error) {
		got = ids
		v := 0.42
		return &v, nil
	}
	o := f.r.Run(bg, j, false)
	if o.CostUSD == nil || *o.CostUSD != 0.42 || !reflect.DeepEqual(got, o.SessionIDs) || len(got) != 2 {
		t.Fatal(o.CostUSD, got)
	}
	got = nil
	writeScript(t, f.r, "s.sh", "echo hi")
	if o := f.r.Run(bg, cron.Job{ID: "s1", Name: "s", NoAgent: true, Script: "s.sh"}, false); o.CostUSD != nil || got != nil {
		t.Fatal(o.CostUSD, got)
	}
	f.r.Cost = nil
	if o := f.r.Run(bg, j, false); o.CostUSD != nil {
		t.Fatal(o.CostUSD)
	}
}

func TestErrKeys(t *testing.T) {
	f, j, _ := retryRunner(t, errors.New("boom 42 at 7"))
	if o := f.r.Run(bg, j, false); o.ErrKey != "⚠ Cron job digest failed: boom  at " {
		t.Fatalf("%q", o.ErrKey)
	}
	f, j, _ = retryRunner(t, statusErr(401))
	if o := f.r.Run(bg, j, false); o.ErrKey != "http 401" {
		t.Fatalf("%q", o.ErrKey)
	}
	f, j, _ = retryRunner(t)
	if o := f.r.Run(bg, j, false); o.ErrKey != "" {
		t.Fatalf("an ok run has no key: %q", o.ErrKey)
	}
	writeScript(t, f.r, "bad.sh", "exit 3")
	if o := f.r.Run(bg, cron.Job{ID: "s1", Name: "s", NoAgent: true, Script: "bad.sh"}, false); o.ErrKey != "script exit 3" {
		t.Fatalf("%q", o.ErrKey)
	}
	w := watchJob(sha("v1\n"), "v1\n")
	w.Script = "bad.sh"
	if o := f.r.Run(bg, w, false); o.ErrKey != "script exit 3" {
		t.Fatalf("watch: %q", o.ErrKey)
	}
	o := f.r.Run(bg, cron.Job{ID: "s1", Name: "s", NoAgent: true, Script: "gone.sh"}, false)
	if !strings.HasPrefix(o.ErrKey, "⚠ Cron job s failed: cron: script ") || strings.ContainsAny(o.ErrKey, "0123456789") {
		t.Fatalf("%q", o.ErrKey)
	}
	f.r.Script = cron.ScriptLimits{Timeout: time.Second}
	writeScript(t, f.r, "slow.sh", "sleep 5")
	if o := f.r.Run(bg, cron.Job{ID: "s1", Name: "s", NoAgent: true, Script: "slow.sh"}, false); o.ErrKey != "script timeout" {
		t.Fatalf("%q", o.ErrKey)
	}
}
