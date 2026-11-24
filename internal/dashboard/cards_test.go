package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// cardsFixture returns cards over a new hub, and the events of one page.
func cardsFixture(t *testing.T, save func(name, value string) error, now func() time.Time) (*Cards, *Hub, <-chan sseEvent) {
	t.Helper()
	h := NewHub(1 << 20)
	c := NewCards(h, save, now)
	ev, _ := sseClient(t, h, func() Snapshot { return Snapshot{Cards: c.Open()} })
	nextEvent(t, ev)
	waitStreams(t, h, 1)
	return c, h, ev
}

func cardOf(t *testing.T, e sseEvent, name string) Card {
	t.Helper()
	var c Card
	if e.Name != name || json.Unmarshal(e.Data, &c) != nil {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	return c
}

// scopeResult is the result of one PromptApproval.
type scopeResult struct {
	s   security.Scope
	err error
}

func promptCard(c *Cards, ctx context.Context, req security.ApprovalRequest) chan scopeResult {
	out := make(chan scopeResult, 1)
	go func() {
		s, err := c.PromptApproval(ctx, req)
		out <- scopeResult{s, err}
	}()
	return out
}

func TestCardFirstAnswerWins(t *testing.T) {
	h := NewHub(1 << 20)
	c := NewCards(h, nil, time.Now)
	pageA, _ := sseClient(t, h, func() Snapshot { return Snapshot{Cards: c.Open()} })
	pageB, _ := sseClient(t, h, func() Snapshot { return Snapshot{Cards: c.Open()} })
	nextEvent(t, pageA)
	nextEvent(t, pageB)
	waitStreams(t, h, 2)
	out := promptCard(c, context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
	card := cardOf(t, nextEvent(t, pageA), "card")
	if cardOf(t, nextEvent(t, pageB), "card").ID != card.ID || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(card.ID) {
		t.Fatalf("%+v", card)
	}
	// Two pages answer at the same time.
	type res struct {
		answer string
		err    error
	}
	errs := make(chan res, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, a := range []string{"once", "deny"} {
		wg.Go(func() {
			<-start
			errs <- res{a, c.Answer(context.Background(), card.ID, CardAnswer{Answer: a})}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	winner := ""
	for r := range errs {
		switch {
		case r.err == nil && winner == "":
			winner = r.answer
		case errors.Is(r.err, ErrCardClosed):
		default:
			t.Fatalf("%s: %v", r.answer, r.err)
		}
	}
	if winner == "" {
		t.Fatal("one answer must win")
	}
	got := <-out
	wantScope, wantHeader := security.ScopeOnce, "✅ Approval allowed · once"
	if winner == "deny" {
		wantScope, wantHeader = security.ScopeDeny, "❌ Approval denied"
	}
	if got.s != wantScope || got.err != nil {
		t.Fatalf("%+v", got)
	}
	// Both pages close the card, once.
	for _, page := range []<-chan sseEvent{pageA, pageB} {
		done := cardOf(t, nextEvent(t, page), "card.done")
		if done.ID != card.ID || done.Header != wantHeader {
			t.Fatalf("%+v", done)
		}
		expectQuiet(t, page, 100*time.Millisecond)
	}
	if err := c.Answer(context.Background(), card.ID, CardAnswer{Answer: "once"}); !errors.Is(err, ErrCardClosed) {
		t.Fatal("a late answer must get ErrCardClosed:", err)
	}
	if len(c.Open()) != 0 {
		t.Fatal(c.Open())
	}
}

func TestApprovalCardAnswerSets(t *testing.T) {
	findings := []security.Finding{{Kind: "card", Label: "[card ••••1111]"}}
	hash := "abcd1234567890abcdef1234567890abcdef1234567890abcdef12345678cdef"
	for _, c := range []struct {
		name       string
		req        security.ApprovalRequest
		kind       string
		header     string
		answers    []string
		answer     string
		wantScope  security.Scope
		wantResult string
	}{
		{"normal", security.ApprovalRequest{Command: "rm -rf /tmp/x"}, "approval", "🛡 Approval needed",
			[]string{"deny", "once", "session", "always"}, "always", security.ScopeAlways, "✅ Approval allowed · always"},
		{"no session", security.ApprovalRequest{Command: "x", NoSession: true}, "approval", "🛡 Approval needed",
			[]string{"deny", "once"}, "once", security.ScopeOnce, "✅ Approval allowed · once"},
		{"acp", security.ApprovalRequest{Command: "edit a.go", Tool: "acp:claude", NoAlways: true}, "approval", "🛡 Approval needed",
			[]string{"deny", "once", "session"}, "session", security.ScopeSession, "✅ Approval allowed · this session"},
		{"human-only", security.ApprovalRequest{Command: "git push --force", HumanOnly: true}, "approval", "🔒 Human-only action",
			[]string{"deny", "once", "session", "always"}, "deny", security.ScopeDeny, "❌ Approval denied"},
		{"mcp send", security.ApprovalRequest{Command: "{}", Class: "send", Server: "gmail", ToolName: "send", Findings: findings, NoSession: true},
			"approval", "📤 Send on your behalf?", []string{"deny", "redacted", "once"}, "redacted", security.ScopeRedacted, "✅ Sent redacted"},
		{"pin", security.ApprovalRequest{Command: "gh pr list", PinName: "GH_TOKEN", PinPath: "/usr/bin/gh", PinHash: hash}, "pin",
			"🔑 Give GH_TOKEN to this program?", []string{"deny", "allow"}, "allow", security.ScopeOnce, "✅ Program allowed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cards, _, ev := cardsFixture(t, nil, time.Now)
			out := promptCard(cards, context.Background(), c.req)
			card := cardOf(t, nextEvent(t, ev), "card")
			if card.Kind != c.kind || card.Header != c.header || !reflect.DeepEqual(card.Answers, c.answers) ||
				card.HumanOnly != c.req.HumanOnly || !card.Deadline.IsZero() {
				t.Fatalf("%+v", card)
			}
			if c.kind == "pin" && (card.SecretName != "GH_TOKEN" || card.Description != "/usr/bin/gh" || card.Rule != "sha256 abcd…cdef") {
				t.Fatalf("%+v", card)
			}
			if c.name == "mcp send" && !reflect.DeepEqual(card.Findings, []string{"card ••••1111"}) {
				t.Fatalf("%+v", card.Findings)
			}
			if c.name == "no session" {
				if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "always"}); err == nil || errors.Is(err, ErrCardClosed) {
					t.Fatal("an answer that the card does not permit must fail and keep the card:", err)
				}
			}
			if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: c.answer}); err != nil {
				t.Fatal(err)
			}
			if got := <-out; got.s != c.wantScope || got.err != nil {
				t.Fatalf("%+v", got)
			}
			if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != c.wantResult {
				t.Fatalf("%q", done.Header)
			}
		})
	}
}

func TestCardDeadline(t *testing.T) {
	opened := time.Now().Add(-2 * time.Minute)
	cards, _, ev := cardsFixture(t, nil, func() time.Time { return opened })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := promptCard(cards, ctx, security.ApprovalRequest{Command: "x"})
	card := cardOf(t, nextEvent(t, ev), "card")
	if dl, _ := ctx.Deadline(); !card.Deadline.Equal(dl) {
		t.Fatalf("%v", card.Deadline)
	}
	got := <-out
	if got.s != security.ScopeTimeout || !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("%+v", got)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "⌛ Approval denied · no answer in 2 min" {
		t.Fatalf("%q", done.Header)
	}
	// A stopped turn has its own text. It is not a Deny tap.
	sctx, stop := context.WithCancel(context.Background())
	out = promptCard(cards, sctx, security.ApprovalRequest{Command: "y"})
	nextEvent(t, ev)
	stop()
	<-out
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "⏹ No answer · the turn stopped" {
		t.Fatalf("%q", done.Header)
	}
}

// card.done carries the record. The header and the preview are redacted and cut at 300 runes.
func TestCardDoneRecord(t *testing.T) {
	at := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	cards, h, ev := cardsFixture(t, nil, func() time.Time { return at })
	cards.SetRedact(func(s string) string { return strings.ReplaceAll(s, "sk-live-123", "***") })
	out := promptCard(cards, context.Background(), security.ApprovalRequest{SessionID: "s1",
		Command: "deploy --token sk-live-123", Description: "deploy"})
	card := cardOf(t, nextEvent(t, ev), "card")
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "once"}); err != nil {
		t.Fatal(err)
	}
	<-out
	e := nextEvent(t, ev)
	var rec AnsweredCard
	if e.Name != "card.done" || json.Unmarshal(e.Data, &rec) != nil || !rec.At.Equal(at) {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	rec.At = at
	want := AnsweredCard{ID: card.ID, Kind: "approval", Header: "✅ Approval allowed · once",
		Preview: "deploy --token ***", SessionID: "s1", At: at}
	if rec != want {
		t.Fatalf("got %+v, want %+v", rec, want)
	}

	// A clarify header holds the typed answer, so it is redacted and cut too.
	ans := make(chan tools.Answer, 1)
	go func() {
		a, _ := cards.Ask(context.Background(), tools.Question{Text: "Which branch?"})
		ans <- a
	}()
	q := cardOf(t, nextEvent(t, ev), "card")
	if err := cards.Answer(context.Background(), q.ID, CardAnswer{Text: "sk-live-123 " + strings.Repeat("a", 400)}); err != nil {
		t.Fatal(err)
	}
	<-ans
	var rq AnsweredCard
	if e := nextEvent(t, ev); json.Unmarshal(e.Data, &rq) != nil {
		t.Fatal(string(e.Data))
	}
	wantHeader := string([]rune("✅ *** " + strings.Repeat("a", 400))[:300])
	if rq.Kind != "clarify" || rq.Header != wantHeader || rq.Preview != "Which branch?" || rq.SessionID != "" {
		t.Fatalf("%+v", rq)
	}
	if got := h.Answered(); len(got) != 2 || got[0].ID != q.ID || got[1].ID != card.ID {
		t.Fatalf("the ring is newest first: %+v", got)
	}
}

func TestLocalApproveDeny(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	a := promptCard(cards, context.Background(), security.ApprovalRequest{Command: "a"})
	nextEvent(t, ev)
	b := promptCard(cards, context.Background(), security.ApprovalRequest{Command: "b", HumanOnly: true})
	nextEvent(t, ev)
	c := promptCard(cards, context.Background(), security.ApprovalRequest{Command: "c"})
	nextEvent(t, ev)
	if _, ok := cards.Local("/help"); ok {
		t.Fatal("/help is not a local command")
	}
	if _, ok := cards.Local("approve all"); ok {
		t.Fatal("a text with no slash is not a command")
	}
	reply, ok := cards.Local("/approve all")
	if !ok || reply != "✅ **Approval allowed** · 2 commands\nThe agent continues.\n1 human-only request still waits for a tap." {
		t.Fatalf("%q", reply)
	}
	if (<-a).s != security.ScopeOnce || (<-c).s != security.ScopeOnce {
		t.Fatal("/approve all answers the cards that are not human-only")
	}
	nextEvent(t, ev) // card.done of a
	nextEvent(t, ev) // card.done of c
	if reply, _ = cards.Local("/approve all"); reply != "🔒 **No command was approved**\n1 human-only request still waits for a tap." {
		t.Fatalf("%q", reply)
	}
	if reply, _ = cards.Local("/deny"); reply != "❌ **Approval denied**\nThe agent continues without the command." {
		t.Fatalf("%q", reply)
	}
	if (<-b).s != security.ScopeDeny {
		t.Fatal("/deny answers a human-only card")
	}
	nextEvent(t, ev) // card.done of b
	if reply, _ = cards.Local("/approve"); reply != "💤 **No command waits for approval**" {
		t.Fatalf("%q", reply)
	}
	d := promptCard(cards, context.Background(), security.ApprovalRequest{Command: "d", NoAlways: true})
	cardOf(t, nextEvent(t, ev), "card")
	if reply, _ = cards.Local("/approve always"); reply != "✅ **Approval allowed** · this session\nThe agent continues." {
		t.Fatalf("%q", reply)
	}
	if (<-d).s != security.ScopeSession {
		t.Fatal("FitScope applies to /approve")
	}
}

func TestBulkAnswersOnlyPostedIDs(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	open := func(req security.ApprovalRequest) (string, chan scopeResult) {
		out := promptCard(cards, context.Background(), req)
		return cardOf(t, nextEvent(t, ev), "card").ID, out
	}
	a, aOut := open(security.ApprovalRequest{Command: "a"})
	b, bOut := open(security.ApprovalRequest{Command: "b", HumanOnly: true})
	cID, cOut := open(security.ApprovalRequest{Command: "c"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cards.NotifySignIn(ctx, SignInRequest{ID: "r1", Code: "4821", Label: "Phone", IP: "10.8.0.5", Expires: time.Now().Add(time.Minute)}, func(bool) {})
	s := cardOf(t, nextEvent(t, ev), "card").ID
	if _, err := cards.Bulk(context.Background(), []string{a}, "always"); err == nil {
		t.Fatal("bulk takes deny or once only")
	}
	if n, err := cards.Bulk(context.Background(), []string{a, b, s, "unknown"}, "once"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if (<-aOut).s != security.ScopeOnce {
		t.Fatal("a")
	}
	if n, err := cards.Bulk(context.Background(), []string{b, cID, s}, "deny"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if (<-bOut).s != security.ScopeDeny || (<-cOut).s != security.ScopeDeny {
		t.Fatal("b and c")
	}
	if open := cards.Open(); len(open) != 1 || open[0].Kind != "signin" {
		t.Fatalf("bulk answers never close a sign-in card: %+v", open)
	}
}

func TestClarifyCards(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	ask := func(q tools.Question) (chan tools.Answer, chan error) {
		ans, errc := make(chan tools.Answer, 1), make(chan error, 1)
		go func() {
			a, err := cards.Ask(context.Background(), q)
			ans <- a
			errc <- err
		}()
		return ans, errc
	}
	ans, errc := ask(tools.Question{Text: "Which?", Choices: []string{"a", "b"}})
	card := cardOf(t, nextEvent(t, ev), "card")
	if card.Kind != "clarify" || card.Header != "❓ Question" || card.Summary != "Which?" || !reflect.DeepEqual(card.Choices, []string{"a", "b"}) {
		t.Fatalf("%+v", card)
	}
	for _, bad := range []CardAnswer{{Choices: []string{"c"}}, {Choices: []string{"a", "b"}}, {Text: "a"}} {
		if err := cards.Answer(context.Background(), card.ID, bad); err == nil || errors.Is(err, ErrCardClosed) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Choices: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if a, err := <-ans, <-errc; err != nil || !reflect.DeepEqual(a.Choices, []string{"b"}) {
		t.Fatal(a, err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "✅ b" {
		t.Fatalf("%q", done.Header)
	}
	ans, errc = ask(tools.Question{Text: "Name?"})
	card = cardOf(t, nextEvent(t, ev), "card")
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Text: "  "}); err == nil {
		t.Fatal("an empty text must fail")
	}
	_ = cards.Answer(context.Background(), card.ID, CardAnswer{Text: "Bob"})
	if a, err := <-ans, <-errc; err != nil || a.Text != "Bob" {
		t.Fatal(a, err)
	}
	nextEvent(t, ev)
	ans, errc = ask(tools.Question{Text: "Pick", Choices: []string{"a", "b", "c"}, Multi: true})
	card = cardOf(t, nextEvent(t, ev), "card")
	_ = cards.Answer(context.Background(), card.ID, CardAnswer{Choices: []string{"a", "c"}})
	if a, err := <-ans, <-errc; err != nil || !reflect.DeepEqual(a.Choices, []string{"a", "c"}) {
		t.Fatal(a, err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "✅ a, c" {
		t.Fatalf("%q", done.Header)
	}
	ans, errc = ask(tools.Question{Text: "Late", Timeout: 30 * time.Millisecond})
	nextEvent(t, ev)
	if _, err := <-ans, <-errc; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "⏱ No answer." {
		t.Fatalf("%q", done.Header)
	}
}

// A second answer to a secret card saves nothing and gets ErrCardClosed.
func TestSecretCardTakenBeforeSave(t *testing.T) {
	var mu sync.Mutex
	saves := 0
	var cards *Cards
	var id string
	var second error
	save := func(name, value string) error {
		mu.Lock()
		defer mu.Unlock()
		saves++
		if saves == 1 {
			second = cards.Answer(context.Background(), id, CardAnswer{Value: "other"})
		}
		return nil
	}
	var ev <-chan sseEvent
	cards, _, ev = cardsFixture(t, save, time.Now)
	go func() { _, _ = cards.AskSecret(context.Background(), tools.SecretQuestion{Name: "GH_TOKEN"}) }()
	card := cardOf(t, nextEvent(t, ev), "card")
	id = card.ID
	if err := cards.Answer(context.Background(), id, CardAnswer{Value: "first"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(second, ErrCardClosed) || saves != 1 {
		t.Fatal(second, saves)
	}
}

func TestSecretCards(t *testing.T) {
	var mu sync.Mutex
	saved := map[string]string{}
	fail := true
	save := func(name, value string) error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			fail = false
			return errors.New("cannot write .env")
		}
		saved[name] = value
		return nil
	}
	cards, _, ev := cardsFixture(t, save, time.Now)
	type result struct {
		ok  bool
		err error
	}
	askSecret := func() chan result {
		out := make(chan result, 1)
		go func() {
			ok, err := cards.AskSecret(context.Background(), tools.SecretQuestion{Name: "GH_TOKEN", Commands: []string{"gh", "git push"}, Reason: "push the fix"})
			out <- result{ok, err}
		}()
		return out
	}
	out := askSecret()
	card := cardOf(t, nextEvent(t, ev), "card")
	if card.Kind != "secret" || card.Header != "🔑 Secret needed: GH_TOKEN" || card.SecretName != "GH_TOKEN" ||
		card.Reason != "push the fix" || card.Command != "gh\ngit push" || !reflect.DeepEqual(card.Answers, []string{"deny"}) {
		t.Fatalf("%+v", card)
	}
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Value: "ghp_secretvalue123"}); err == nil ||
		strings.Contains(err.Error(), "ghp_secretvalue123") {
		t.Fatal("a failed save must return an error with no value:", err)
	}
	if len(cards.Open()) != 1 {
		t.Fatal("a failed save keeps the card open")
	}
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Value: "ghp_secretvalue123"}); err != nil {
		t.Fatal(err)
	}
	if r := <-out; !r.ok || r.err != nil || saved["GH_TOKEN"] != "ghp_secretvalue123" {
		t.Fatal(r, saved)
	}
	done := nextEvent(t, ev)
	if strings.Contains(string(done.Data), "ghp_") || cardOf(t, done, "card.done").Header != "✅ Value saved: GH_TOKEN" {
		t.Fatalf("%s", done.Data)
	}
	out = askSecret()
	card = cardOf(t, nextEvent(t, ev), "card")
	_ = cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "deny"})
	if r := <-out; r.ok || r.err != nil {
		t.Fatal(r)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "❌ No value given: GH_TOKEN" {
		t.Fatalf("%q", done.Header)
	}
	none := NewCards(NewHub(1<<10), nil, time.Now)
	if ok, err := none.AskSecret(context.Background(), tools.SecretQuestion{Name: "X"}); ok || err == nil {
		t.Fatal("no secret store must fail")
	}
}

func TestSignInCards(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	exp := time.Now().Add(5 * time.Minute)
	got := make(chan bool, 2)
	cards.NotifySignIn(context.Background(), SignInRequest{ID: "r1", Code: "4821", Label: "Chrome on Android", IP: "10.8.0.5", Expires: exp},
		func(allow bool) { got <- allow })
	card := cardOf(t, nextEvent(t, ev), "card")
	if card.Kind != "signin" || card.Header != "🔑 New dashboard sign-in" || card.Device != "Chrome on Android" || card.IP != "10.8.0.5" ||
		card.Code != "4821" || !card.HumanOnly || !reflect.DeepEqual(card.Answers, []string{"deny", "allow"}) || !card.Deadline.Equal(exp) {
		t.Fatalf("%+v", card)
	}
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Answer: "allow"}); err != nil || !<-got {
		t.Fatal(err)
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "✅ Sign-in allowed" {
		t.Fatalf("%q", done.Header)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cards.NotifySignIn(ctx, SignInRequest{ID: "r2", Code: "1111", Label: "x", IP: "1.2.3.4", Expires: exp}, func(allow bool) { got <- allow })
	nextEvent(t, ev)
	cancel()
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "⌛ Sign-in request closed" {
		t.Fatalf("%q", done.Header)
	}
	select {
	case v := <-got:
		t.Fatal("a closed card must not answer:", v)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCardRedact(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	cards.SetRedact(func(s string) string { return strings.ReplaceAll(s, "sk-123", "«KEY»") })
	promptCard(cards, context.Background(), security.ApprovalRequest{Command: "curl -H sk-123", Summary: "uses sk-123"})
	card := cardOf(t, nextEvent(t, ev), "card")
	if card.Command != "curl -H «KEY»" || card.Summary != "uses «KEY»" {
		t.Fatalf("%+v", card)
	}
	if open := cards.Open(); len(open) != 1 || open[0].Command != "curl -H «KEY»" {
		t.Fatalf("%+v", open)
	}
}

// When the turn ends while a failed save runs, the card closes, so the waiter does not hang.
func TestSecretCardSaveFailsAfterTurnEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	save := func(name, value string) error {
		cancel()
		return errors.New("cannot write .env")
	}
	cards, _, ev := cardsFixture(t, save, time.Now)
	type res struct {
		ok  bool
		err error
	}
	out := make(chan res, 1)
	go func() {
		ok, err := cards.AskSecret(ctx, tools.SecretQuestion{Name: "GH_TOKEN"})
		out <- res{ok, err}
	}()
	card := cardOf(t, nextEvent(t, ev), "card")
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Value: "v"}); err == nil {
		t.Fatal("a failed save must return an error")
	}
	select {
	case r := <-out:
		if r.ok {
			t.Fatal("no value was saved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter still waits after its turn ended")
	}
	if len(cards.Open()) != 0 {
		t.Fatal("the card must close")
	}
}

// When the turn ends after a failed save and before the card goes back, the waiter does not
// hang and the card closes.
func TestSecretCardSaveFailsWhileTurnEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cards, _, ev := cardsFixture(t, func(string, string) error { return errors.New("cannot write .env") }, time.Now)
	out := make(chan bool, 1)
	go func() {
		ok, _ := cards.AskSecret(ctx, tools.SecretQuestion{Name: "GH_TOKEN"})
		out <- ok
	}()
	card := cardOf(t, nextEvent(t, ev), "card")
	cards.afterSaveFail = func() {
		cancel()
		// Give the waiter the time to look for the card, which is not in the open list.
		time.Sleep(50 * time.Millisecond)
	}
	if err := cards.Answer(context.Background(), card.ID, CardAnswer{Value: "v"}); err == nil {
		t.Fatal("a failed save must return an error")
	}
	select {
	case ok := <-out:
		if ok {
			t.Fatal("no value was saved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter still waits after its turn ended")
	}
	if done := cardOf(t, nextEvent(t, ev), "card.done"); done.Header != "❌ The value was not saved: GH_TOKEN" {
		t.Fatalf("%q", done.Header)
	}
	if len(cards.Open()) != 0 {
		t.Fatal("the card must close")
	}
}

// "Approve all once" skips a send card with findings, from the tray and from a typed /approve all.
func TestBulkSkipsSendWithFindings(t *testing.T) {
	cards, _, ev := cardsFixture(t, nil, time.Now)
	open := func(req security.ApprovalRequest) (string, chan scopeResult) {
		out := promptCard(cards, context.Background(), req)
		return cardOf(t, nextEvent(t, ev), "card").ID, out
	}
	a, aOut := open(security.ApprovalRequest{Command: "a"})
	s, sOut := open(security.ApprovalRequest{Command: "post the report", Class: "send",
		Findings: []security.Finding{{Label: "[email]"}}})
	if n, err := cards.Bulk(context.Background(), []string{a, s}, "once"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if (<-aOut).s != security.ScopeOnce {
		t.Fatal("a")
	}
	nextEvent(t, ev) // card.done of a
	if open := cards.Open(); len(open) != 1 || open[0].ID != s {
		t.Fatalf("a send with findings stays open: %+v", open)
	}
	reply, ok := cards.Local("/approve all")
	if !ok || reply != "🔒 **No command was approved**\n1 send with findings still waits for a tap." {
		t.Fatalf("%q", reply)
	}
	if len(cards.Open()) != 1 {
		t.Fatal("a typed /approve all must skip the send with findings")
	}
	if n, err := cards.Bulk(context.Background(), []string{s}, "deny"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if (<-sOut).s != security.ScopeDeny {
		t.Fatal("deny all still answers a send with findings")
	}
}
