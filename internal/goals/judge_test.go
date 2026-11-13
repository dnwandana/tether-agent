package goals

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// judgeFakeProv answers each call with reply, or fails with err. With block set, it waits for
// the end of the context. It keeps the last request and the count of calls.
type judgeFakeProv struct {
	reply string
	err   error
	block bool

	mu    sync.Mutex
	calls int
	last  provider.Request
	dl    bool
}

func (p *judgeFakeProv) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.mu.Lock()
	p.calls++
	p.last = req
	_, p.dl = ctx.Deadline()
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan provider.Event, 2)
	if p.block {
		go func() {
			<-ctx.Done()
			ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
			close(ch)
		}()
		return ch, nil
	}
	ch <- provider.Event{Type: provider.EventText, Delta: p.reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop",
		Usage: &provider.Usage{InputTokens: 900, OutputTokens: 40}}
	close(ch)
	return ch, nil
}

func (p *judgeFakeProv) Name() string  { return "fake" }
func (p *judgeFakeProv) Model() string { return "fake-model" }

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		in   string
		want Verdict
	}{
		{`{"verdict": "done", "reason": "20 runs pass"}`, Verdict{Kind: VerdictDone, Reason: "20 runs pass"}},
		{"```json\n{\"verdict\": \"BLOCKED\", \"reason\": \"needs a key\"}\n```", Verdict{Kind: VerdictBlocked, Reason: "needs a key"}},
		{`Sure. {"verdict": "continue", "reason": "not verified"} ok`, Verdict{Kind: VerdictContinue, Reason: "not verified"}},
		{`{"done": true, "reason": "ok"}`, Verdict{Kind: VerdictDone, Reason: "ok"}},
		{`{"done": "yes"}`, Verdict{Kind: VerdictDone, Reason: "no reason provided"}},
		{`{"done": false, "reason": "more"}`, Verdict{Kind: VerdictContinue, Reason: "more"}},
		{`{"verdict": "maybe", "reason": "x"}`, Verdict{Kind: VerdictContinue, Reason: "x"}},
		{`{"verdict": "wait", "wait_on_session": " 4242 ", "reason": "ci"}`, Verdict{Kind: VerdictWait, Reason: "ci", WaitSession: "4242"}},
		{`{"verdict": "wait", "session_id": "4242", "wait_on_pid": 7, "reason": "ci"}`, Verdict{Kind: VerdictWait, Reason: "ci", WaitSession: "4242"}},
		{`{"verdict": "wait", "pid": 4242, "reason": "ci"}`, Verdict{Kind: VerdictWait, Reason: "ci", WaitPID: 4242}},
		{`{"verdict": "wait", "wait_pid": "4242", "wait_for_seconds": 60, "reason": "ci"}`, Verdict{Kind: VerdictWait, Reason: "ci", WaitPID: 4242}},
		{`{"verdict": "wait", "wait_on_pid": 0, "wait_seconds": "900", "reason": "batches"}`, Verdict{Kind: VerdictWait, Reason: "batches", WaitSeconds: 900}},
		{`{"verdict": "wait", "reason": "idle"}`, Verdict{Kind: VerdictContinue, Reason: "idle (wait verdict had no target — continuing)"}},
		{"", Verdict{Kind: VerdictContinue, Reason: "judge returned empty response", ParseFailed: true}},
	}
	for _, tc := range cases {
		if got := ParseVerdict(tc.in); got != tc.want {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"I think it is done.", "[1, 2]", "{not json}"} {
		got := ParseVerdict(bad)
		if got.Kind != VerdictContinue || !got.ParseFailed || !strings.HasPrefix(got.Reason, "judge reply was not JSON: ") {
			t.Errorf("%q: %+v", bad, got)
		}
	}
}

func TestApplyCounters(t *testing.T) {
	var st State
	parse := Verdict{Kind: VerdictContinue, ParseFailed: true}
	api := Verdict{Kind: VerdictContinue, TransportFailed: true}
	good := Verdict{Kind: VerdictContinue, Reason: "next"}
	for i, want := range []string{"", ""} {
		if got := ApplyCounters(&st, parse); got != want || st.ConsecutiveParseFailures != i+1 {
			t.Fatal(i, got, st.ConsecutiveParseFailures)
		}
	}
	// Each failure kind sets the other counter to 0.
	if got := ApplyCounters(&st, api); got != "" || st.ConsecutiveParseFailures != 0 || st.ConsecutiveTransportFailures != 1 {
		t.Fatalf("%q %+v", got, st)
	}
	ApplyCounters(&st, parse)
	if st.ConsecutiveTransportFailures != 0 {
		t.Fatal("a parse failure must set the API counter to 0")
	}
	ApplyCounters(&st, parse)
	if got := ApplyCounters(&st, parse); got != "parse_fail" || st.ConsecutiveParseFailures != 3 {
		t.Fatalf("the third parse failure must pause: %q %+v", got, st)
	}
	ApplyCounters(&st, good)
	if st.ConsecutiveParseFailures != 0 || st.ConsecutiveTransportFailures != 0 {
		t.Fatalf("a good verdict must set both counters to 0: %+v", st)
	}
	for i := 1; i <= 5; i++ {
		got := ApplyCounters(&st, api)
		if (i < 5 && got != "") || (i == 5 && got != "transport_fail") {
			t.Fatalf("API error %d: %q", i, got)
		}
	}
	if st.Status != "" {
		t.Fatal("ApplyCounters must not change the status")
	}
}

func TestJudgeCallRequestAndRecord(t *testing.T) {
	prov := &judgeFakeProv{reply: `{"verdict": "done", "reason": "ok"}`}
	var recPrompt string
	var recOut provider.Result
	j := &Judge{Timeout: 30 * time.Second, Record: func(_ context.Context, prompt string, _ provider.Provider, out provider.Result) {
		recPrompt, recOut = prompt, out
	}}
	res, err := j.Call(context.Background(), prov, "SYS", "USER")
	if err != nil || res.Text != `{"verdict": "done", "reason": "ok"}` {
		t.Fatal(res, err)
	}
	r := prov.last
	if r.System != "SYS" || len(r.Messages) != 1 || r.Messages[0].Role != provider.RoleUser || r.Messages[0].Content != "USER" {
		t.Fatalf("%+v", r)
	}
	if r.Effort != "low" || r.MaxTokens != 4096 || !prov.dl {
		t.Fatalf("effort %q, max tokens %d, deadline %v", r.Effort, r.MaxTokens, prov.dl)
	}
	if recPrompt != "USER" || recOut.Text != res.Text || recOut.Usage.OutputTokens != 40 {
		t.Fatalf("Record got %q %+v", recPrompt, recOut)
	}
}

func TestJudgeCallTimeoutAndError(t *testing.T) {
	recorded := false
	j := &Judge{Timeout: 50 * time.Millisecond, Record: func(context.Context, string, provider.Provider, provider.Result) { recorded = true }}
	start := time.Now()
	if _, err := j.Call(context.Background(), &judgeFakeProv{block: true}, "s", "u"); err == nil {
		t.Fatal("a timeout must be an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the timeout must end the call")
	}
	if _, err := j.Call(context.Background(), &judgeFakeProv{err: errors.New("401 unauthorized")}, "s", "u"); err == nil {
		t.Fatal("a provider error must be an error")
	}
	if recorded {
		t.Fatal("a failed call must not be recorded")
	}
}

func TestJudgeEvaluate(t *testing.T) {
	in := JudgeInput{Goal: "Fix the flaky cron test", Response: "20 runs pass: ok cron 1.2s", Now: time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)}
	prov := &judgeFakeProv{reply: `{"verdict": "done", "reason": "20 runs pass"}`}
	j := &Judge{Timeout: time.Second}
	if v := j.Evaluate(context.Background(), prov, in); v != (Verdict{Kind: VerdictDone, Reason: "20 runs pass"}) {
		t.Fatalf("%+v", v)
	}
	if prov.last.System != JudgeSystemPrompt || prov.last.Messages[0].Content != JudgeUserPrompt(in) {
		t.Fatal("Evaluate must send the judge system prompt and the user prompt")
	}
	v := j.Evaluate(context.Background(), &judgeFakeProv{err: errors.New("dial tcp: no route")}, in)
	if v.Kind != VerdictContinue || !v.TransportFailed || v.ParseFailed || !strings.HasPrefix(v.Reason, "judge error: ") {
		t.Fatalf("an API error must fail open: %+v", v)
	}
	empty := &judgeFakeProv{reply: "x"}
	in.Response = "  \n"
	v = j.Evaluate(context.Background(), empty, in)
	if v != (Verdict{Kind: VerdictContinue, Reason: "empty response (nothing to evaluate)"}) || empty.calls != 0 {
		t.Fatalf("an empty reply must not call the judge: %+v, %d calls", v, empty.calls)
	}
}

func TestJudgeLimitFields(t *testing.T) {
	ctx := context.Background()
	prov := &judgeFakeProv{reply: `{"verdict": "done", "reason": "ok"}`}
	if _, err := (&Judge{Timeout: time.Second, MaxTokens: 1024}).Call(ctx, prov, "s", "u"); err != nil || prov.last.MaxTokens != 1024 {
		t.Fatal(prov.last.MaxTokens, err)
	}
	prov = &judgeFakeProv{reply: `{"verdict": "done", "reason": "ok"}`}
	j := &Judge{Timeout: time.Second, Chars: config.GoalsJudgeChars{Goal: 10}}
	j.Evaluate(ctx, prov, JudgeInput{Goal: strings.Repeat("g", 11), Response: "r", Now: time.Unix(0, 0)})
	if !strings.Contains(prov.last.Messages[0].Content, "Goal:\n"+strings.Repeat("g", 10)+"… [truncated]") {
		t.Fatal("Evaluate must use the cuts of the judge")
	}
	p := &draftProv{reply: `{"outcome": "x"}`}
	DraftContract(ctx, &Judge{Timeout: time.Second, DraftChars: 100}, p, strings.Repeat("é", 101))
	if got, want := p.reqs[0].Messages[0].Content, "Objective:\n"+strings.Repeat("é", 100)+"… [truncated]"; got != want {
		t.Fatalf("the objective must be cut at 100 chars, got %d chars", len([]rune(got)))
	}
	var st State
	parse := Verdict{Kind: VerdictContinue, ParseFailed: true}
	if got := applyCounters(&st, parse, 2, 2); got != "" {
		t.Fatal("the first parse failure must not pause:", got)
	}
	if got := applyCounters(&st, parse, 2, 2); got != NoticeParseFail {
		t.Fatal("the second parse failure must pause with a limit of 2:", got)
	}
}
