package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ErrCardClosed is the error of an answer to a card that has an answer, that timed out, or
// that does not exist. The command route sends 409 "This card is already answered."
var ErrCardClosed = errors.New("dashboard: the card is already answered")

// errBadAnswer is the error of an answer that the card does not permit.
var errBadAnswer = errors.New("dashboard: the answer is not valid for this card")

// The texts of the cards. The reply texts of Local are the Telegram texts.
const (
	headApproval  = "🛡 Approval needed"
	headHumanOnly = "🔒 Human-only action"
	headSend      = "📤 Send on your behalf?"
	headQuestion  = "❓ Question"
	headSecret    = "🔑 Secret needed: "
	headSignIn    = "🔑 New dashboard sign-in"
	noAnswerAsk   = "⏱ No answer."
	signInAllowed = "✅ Sign-in allowed"
	signInDenied  = "❌ Sign-in denied"
	signInClosed  = "⌛ Sign-in request closed"
	noPending     = "💤 **No command waits for approval**"
)

// Cards shows the prompts of the dashboard turns as cards on every open page.
type Cards struct {
	h          *Hub
	saveSecret func(name, value string) error
	now        func() time.Time
	mu         sync.Mutex
	open       map[string]*openCard
	seq        int64
	redact     func(string) string
	// afterSaveFail runs after a failed save of a secret card. Tests set it.
	afterSaveFail func()
}

// openCard is one card that waits for an answer.
type openCard struct {
	view                Card
	seq                 int64
	opened              time.Time
	noAlways, noSession bool
	reply               chan cardReply // capacity 1
	signIn              func(allow bool)
	signInState         func() string
	sessionID           string
	// done ends when the waiter stops waiting, for example when its turn ends.
	done <-chan struct{}
}

// cardReply is the answer that a waiter gets.
type cardReply struct {
	scope  security.Scope
	answer tools.Answer
	ok     bool
}

var (
	_ security.Prompter = (*Cards)(nil)
	_ tools.Asker       = (*Cards)(nil)
	_ tools.SecretAsker = (*Cards)(nil)
	_ SignInNotifier    = (*Cards)(nil)
)

// NewCards returns cards that publish to h. saveSecret stores the value of a secret card. now
// gives the open time of a card.
func NewCards(h *Hub, saveSecret func(name, value string) error, now func() time.Time) *Cards {
	if now == nil {
		now = time.Now
	}
	return &Cards{h: h, saveSecret: saveSecret, now: now, open: map[string]*openCard{}}
}

// SetRedact sets the function that cleans the text fields of each card before the card event.
func (c *Cards) SetRedact(f func(string) string) {
	c.mu.Lock()
	c.redact = f
	c.mu.Unlock()
}

// add makes a card with a random id, puts it in the open list, and sends "card", in one
// publishWith.
func (c *Cards) add(ctx context.Context, view Card, setup func(*openCard)) *openCard {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	view.ID = hex.EncodeToString(raw[:])
	if dl, ok := ctx.Deadline(); ok {
		view.Deadline = dl
	}
	c.mu.Lock()
	redact := c.redact
	c.mu.Unlock()
	if redact != nil {
		view.Summary = redact(view.Summary)
		view.Command = redact(view.Command)
		view.Description = redact(view.Description)
		view.Diff = redact(view.Diff)
		view.Reason = redact(view.Reason)
	}
	// A page expects arrays, never null.
	for _, p := range []*[]string{&view.Secrets, &view.Findings, &view.Answers, &view.Choices} {
		if *p == nil {
			*p = []string{}
		}
	}
	oc := &openCard{view: view, opened: c.now(), reply: make(chan cardReply, 1), done: ctx.Done()}
	if setup != nil {
		setup(oc)
	}
	c.h.publishWith(Event{Name: "card", Data: view}, func() {
		c.mu.Lock()
		c.seq++
		oc.seq = c.seq
		c.open[view.ID] = oc
		c.mu.Unlock()
	})
	return oc
}

// takeLocked removes the card id from the open list and returns it. It returns nil when the
// card is not open. The caller holds c.mu.
func (c *Cards) takeLocked(id string) *openCard {
	oc := c.open[id]
	delete(c.open, id)
	return oc
}

// take removes the card id from the open list under the lock.
func (c *Cards) take(id string) *openCard {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.takeLocked(id)
}

// answeredMaxRunes is the cut of the header and the preview of an answered card.
const answeredMaxRunes = 300

// record builds the answered-card record of card. It redacts the header and the preview, and
// then cuts each one.
func (c *Cards) record(card *openCard, header string) AnsweredCard {
	v := card.view
	var preview string
	switch v.Kind {
	case "approval":
		preview = v.Command
		if preview == "" {
			preview = v.Summary
		}
	case "pin":
		preview = v.SecretName + " " + v.Description
	case "clarify":
		preview = v.Summary
	case "secret":
		preview = v.SecretName
	case "signin":
		preview = v.Device
	}
	c.mu.Lock()
	redact := c.redact
	c.mu.Unlock()
	if redact != nil {
		header, preview = redact(header), redact(preview)
	}
	return AnsweredCard{ID: v.ID, Kind: v.Kind, Header: cutRunes(header, answeredMaxRunes),
		Preview: cutRunes(preview, answeredMaxRunes), SessionID: card.sessionID, At: c.now()}
}

// done sends "card.done" with the answered-card record. The hub keeps the record in its ring
// in the same step, so a page that connects at that moment gets the record once.
func (c *Cards) done(card *openCard, header string) {
	rec := c.record(card, header)
	c.h.publishWith(Event{Name: "card.done", Data: rec}, func() { c.h.addAnswered(rec) })
}

// finish ends a card that the caller took: it sends the reply to the waiter and closes the card.
func (c *Cards) finish(oc *openCard, r cardReply, header string) {
	oc.reply <- r
	c.done(oc, header)
}

// wait blocks until the card has a reply or ctx ends. On a context end it takes the card. It
// returns the reply and true, or false when the context ended with the card still open.
func (c *Cards) wait(ctx context.Context, oc *openCard) (cardReply, bool) {
	select {
	case r := <-oc.reply:
		return r, true
	case <-ctx.Done():
	}
	if c.take(oc.view.ID) == nil {
		// An answer took the card first. Its reply is on the way.
		return <-oc.reply, true
	}
	return cardReply{}, false
}

// cardHeader returns the header after the answer s of an approval, pin, or send card.
func cardHeader(kind string, s security.Scope) string {
	return gateway.CardResultHeader(gateway.CardResult{Kind: kind, Scope: string(s)})
}

// approvalHeaderOf returns the header text of the scope for the Local replies.
func approvalHeaderOf(s security.Scope) (emoji, title, detail string) {
	switch s {
	case security.ScopeSession:
		return "✅", "Approval allowed", "this session"
	case security.ScopeAlways:
		return "✅", "Approval allowed", "always"
	case security.ScopeOnce:
		return "✅", "Approval allowed", "once"
	}
	return "❌", "Approval denied", ""
}

func markdownHeader(emoji, title, detail string) string {
	s := emoji + " **" + title + "**"
	if detail != "" {
		s += " · " + detail
	}
	return s
}

// PromptApproval shows an approval card or a pin card and waits for the answer.
func (c *Cards) PromptApproval(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
	view := Card{
		Summary:     req.Summary,
		Command:     security.ShowHidden(req.Command),
		Description: req.Description,
		Workdir:     req.Workdir,
		Via:         req.Via,
		Child:       req.ChildName,
		HumanOnly:   req.HumanOnly,
	}
	pin := req.PinName != ""
	kind := "approval"
	switch {
	case pin:
		kind = "pin"
	case req.Class == "send":
		kind = "send"
	}
	if pin {
		view.Kind = "pin"
		view.Header = gateway.PinQuestion(req)
		view.Answers = []string{"deny", "allow"}
		view.SecretName = req.PinName
		view.Description = req.PinPath
		view.Rule = "sha256 " + gateway.ShortHash(req.PinHash)
	} else {
		view.Kind = "approval"
		switch {
		case req.HumanOnly:
			view.Header = headHumanOnly
		case req.Class == "send":
			view.Header = headSend
		default:
			view.Header = headApproval
		}
		view.Rule = gateway.ApprovalFlagged(req)
		view.Secrets = gateway.ApprovalSecretLines(req)
		for _, f := range req.Findings {
			view.Findings = append(view.Findings, strings.TrimSuffix(strings.TrimPrefix(f.Label, "["), "]"))
		}
		view.Answers = []string{"deny"}
		if len(req.Findings) > 0 {
			view.Answers = append(view.Answers, "redacted")
		}
		view.Answers = append(view.Answers, "once")
		if !req.NoSession {
			view.Answers = append(view.Answers, "session")
			if !req.NoAlways {
				view.Answers = append(view.Answers, "always")
			}
		}
	}
	oc := c.add(ctx, view, func(oc *openCard) {
		oc.noAlways, oc.noSession = req.NoAlways, req.NoSession
		oc.sessionID = req.SessionID
		if pin {
			oc.noAlways, oc.noSession = true, true
		}
	})
	r, ok := c.wait(ctx, oc)
	if ok {
		return r.scope, nil
	}
	header := gateway.CardResultHeader(gateway.CardResult{Kind: kind, Stopped: true})
	if dl, has := ctx.Deadline(); has && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		header = gateway.CardResultHeader(gateway.CardResult{Kind: kind, Scope: string(security.ScopeTimeout), Wait: dl.Sub(oc.opened)})
	}
	c.done(oc, header)
	return security.ScopeTimeout, ctx.Err()
}

// Ask shows a clarify card and waits for the answer.
func (c *Cards) Ask(ctx context.Context, q tools.Question) (tools.Answer, error) {
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}
	oc := c.add(ctx, Card{
		Kind:    "clarify",
		Header:  headQuestion,
		Summary: q.Text,
		Choices: slices.Clone(q.Choices),
		Multi:   q.Multi,
	}, nil)
	r, ok := c.wait(ctx, oc)
	if ok {
		return r.answer, nil
	}
	c.done(oc, noAnswerAsk)
	return tools.Answer{}, ctx.Err()
}

// AskSecret shows a secret card. The value goes to saveSecret only.
func (c *Cards) AskSecret(ctx context.Context, q tools.SecretQuestion) (bool, error) {
	if c.saveSecret == nil {
		return false, errors.New("dashboard: no secret store")
	}
	oc := c.add(ctx, Card{
		Kind:       "secret",
		Header:     headSecret + q.Name,
		SecretName: q.Name,
		Reason:     q.Reason,
		Command:    strings.Join(q.Commands, "\n"),
		Answers:    []string{"deny"},
	}, nil)
	r, ok := c.wait(ctx, oc)
	if ok {
		return r.ok, nil
	}
	c.done(oc, "❌ No value given: "+q.Name)
	return false, ctx.Err()
}

// signInHeader returns the result header of a sign-in card from the state of the request.
// When state is nil, the header shows tapped: the answer of this card, or "" when no answer.
func signInHeader(state func() string, tapped string) string {
	st := tapped
	if state != nil {
		st = state()
	}
	switch st {
	case "allowed":
		return signInAllowed
	case "denied":
		return signInDenied
	}
	return signInClosed
}

// NotifySignIn shows a sign-in card and returns at once. An answer calls answer(allow). When
// ctx ends first, the card closes and answer is not called. The result header shows the
// final state of req, because another notifier can answer first.
func (c *Cards) NotifySignIn(ctx context.Context, req SignInRequest, answer func(allow bool)) {
	oc := c.add(context.Background(), Card{
		Kind:      "signin",
		Header:    headSignIn,
		Device:    req.Label,
		IP:        req.IP,
		Code:      req.Code,
		HumanOnly: true,
		Answers:   []string{"deny", "allow"},
		Deadline:  req.Expires,
	}, func(oc *openCard) { oc.signIn, oc.signInState = answer, req.State })
	go func() {
		if _, ok := c.wait(ctx, oc); !ok {
			c.done(oc, signInHeader(req.State, ""))
		}
	}()
}

// Answer gives the answer a to the card id. The first valid answer wins. A later answer gets
// ErrCardClosed. An answer that the card does not permit returns another error.
func (c *Cards) Answer(ctx context.Context, id string, a CardAnswer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	oc := c.open[id]
	if oc == nil {
		c.mu.Unlock()
		return ErrCardClosed
	}
	kind, view := oc.view.Kind, oc.view
	switch kind {
	case "approval", "pin":
		scope, ok := answerScope(view.Answers, a.Answer)
		if !ok {
			c.mu.Unlock()
			return errBadAnswer
		}
		c.takeLocked(id)
		c.mu.Unlock()
		scope = security.FitScope(scope, oc.noAlways, oc.noSession)
		c.finish(oc, cardReply{scope: scope}, cardHeader(kind, scope))
		return nil
	case "clarify":
		ans, summary, ok := clarifyAnswer(view, a)
		if !ok {
			c.mu.Unlock()
			return errBadAnswer
		}
		c.takeLocked(id)
		c.mu.Unlock()
		c.finish(oc, cardReply{answer: ans}, "✅ "+summary)
		return nil
	case "secret":
		c.mu.Unlock()
		return c.answerSecret(id, oc, a)
	case "signin":
		if a.Answer != "allow" && a.Answer != "deny" {
			c.mu.Unlock()
			return errBadAnswer
		}
		c.takeLocked(id)
		c.mu.Unlock()
		allow := a.Answer == "allow"
		if oc.signIn != nil {
			oc.signIn(allow)
		}
		tapped := "denied"
		if allow {
			tapped = "allowed"
		}
		header := signInHeader(oc.signInState, tapped)
		c.finish(oc, cardReply{ok: header == signInAllowed}, header)
		return nil
	}
	c.mu.Unlock()
	return errBadAnswer
}

// answerSecret saves the value outside the lock. A failed save keeps the card open. The
// errors never hold the value.
func (c *Cards) answerSecret(id string, oc *openCard, a CardAnswer) error {
	name := oc.view.SecretName
	if a.Value == "" {
		if a.Answer != "deny" {
			return errBadAnswer
		}
		if c.take(id) == nil {
			return ErrCardClosed
		}
		c.finish(oc, cardReply{}, "❌ No value given: "+name)
		return nil
	}
	if strings.TrimSpace(a.Value) == "" {
		return errBadAnswer
	}
	if c.saveSecret == nil {
		return errors.New("dashboard: no secret store")
	}
	// Take the card first, so a second answer saves nothing.
	if c.take(id) == nil {
		return ErrCardClosed
	}
	if err := c.saveSecret(name, a.Value); err != nil {
		if c.afterSaveFail != nil {
			c.afterSaveFail()
		}
		// Check the waiter and put the card back in one lock. Else the waiter can stop between
		// the two steps, find no card, and wait for a reply that never comes.
		stopped := false
		c.mu.Lock()
		select {
		case <-oc.done:
			stopped = true
		default:
			// Put the card back, so the user can try again.
			c.open[id] = oc
		}
		c.mu.Unlock()
		if stopped {
			// The waiter stopped while the save ran. It waits for this reply, so close the card.
			c.finish(oc, cardReply{}, "❌ The value was not saved: "+name)
		}
		return fmt.Errorf("dashboard: cannot save the value of %s", name)
	}
	c.finish(oc, cardReply{ok: true}, "✅ Value saved: "+name)
	return nil
}

// answerScope maps an answer word to a scope when the card permits the word.
func answerScope(allowed []string, word string) (security.Scope, bool) {
	if !slices.Contains(allowed, word) {
		return "", false
	}
	switch word {
	case "deny":
		return security.ScopeDeny, true
	case "redacted":
		return security.ScopeRedacted, true
	case "once", "allow":
		return security.ScopeOnce, true
	case "session":
		return security.ScopeSession, true
	case "always":
		return security.ScopeAlways, true
	}
	return "", false
}

// clarifyAnswer checks the answer of a clarify card. It returns the answer and the summary
// line of the result header.
func clarifyAnswer(view Card, a CardAnswer) (tools.Answer, string, bool) {
	if len(view.Choices) > 0 {
		if len(a.Choices) == 0 || (!view.Multi && len(a.Choices) != 1) {
			return tools.Answer{}, "", false
		}
		for _, ch := range a.Choices {
			if !slices.Contains(view.Choices, ch) {
				return tools.Answer{}, "", false
			}
		}
		return tools.Answer{Choices: slices.Clone(a.Choices)}, strings.Join(a.Choices, ", "), true
	}
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return tools.Answer{}, "", false
	}
	return tools.Answer{Text: text}, text, true
}

// Bulk answers the open approval and pin cards in ids with deny or once. It skips a human-only
// card and a send card with findings for once, and it skips every clarify, secret, and sign-in
// card. It returns the count of answered cards.
func (c *Cards) Bulk(ctx context.Context, ids []string, answer string) (int, error) {
	if answer != "deny" && answer != "once" {
		return 0, errBadAnswer
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	scope := security.ScopeDeny
	if answer == "once" {
		scope = security.ScopeOnce
	}
	var taken []*openCard
	c.mu.Lock()
	for _, id := range ids {
		oc := c.open[id]
		if oc == nil || (oc.view.Kind != "approval" && oc.view.Kind != "pin") || (scope == security.ScopeOnce && (oc.view.HumanOnly || len(oc.view.Findings) > 0)) {
			continue
		}
		taken = append(taken, c.takeLocked(id))
	}
	c.mu.Unlock()
	for _, oc := range taken {
		c.finish(oc, cardReply{scope: scope}, cardHeader(oc.view.Kind, scope))
	}
	return len(taken), nil
}

// Local runs /approve [session|always] [all] and /deny [all] on the open approval and pin cards.
// For other text, ok is false.
func (c *Cards) Local(text string) (reply string, ok bool) {
	name, args, isCmd := gateway.IsCommand(text)
	if !isCmd || (name != "approve" && name != "deny") {
		return "", false
	}
	all := false
	scope := security.ScopeOnce
	for f := range strings.FieldsSeq(strings.ToLower(args)) {
		switch f {
		case "all":
			all = true
		case "session", "ses":
			scope = security.ScopeSession
		case "always", "permanent", "permanently":
			scope = security.ScopeAlways
		}
	}
	if name == "deny" {
		scope = security.ScopeDeny
	}
	var list []*openCard
	waiting, sends := 0, 0
	c.mu.Lock()
	for _, oc := range c.open {
		if oc.view.Kind == "approval" || oc.view.Kind == "pin" {
			list = append(list, oc)
		}
	}
	if all && scope != security.ScopeDeny {
		kept := list[:0]
		for _, oc := range list {
			if oc.view.HumanOnly {
				waiting++
				continue
			}
			if len(oc.view.Findings) > 0 {
				sends++
				continue
			}
			kept = append(kept, oc)
		}
		list = kept
	}
	slices.SortFunc(list, func(a, b *openCard) int { return int(a.seq - b.seq) })
	if !all && len(list) > 1 {
		list = list[:1]
	}
	for _, oc := range list {
		c.takeLocked(oc.view.ID)
	}
	c.mu.Unlock()
	applied := scope
	for _, oc := range list {
		applied = security.FitScope(scope, oc.noAlways, oc.noSession)
		c.finish(oc, cardReply{scope: applied}, cardHeader(oc.view.Kind, applied))
	}
	return resolveReply(name, applied, len(list), waiting, sends), true
}

// resolveReply returns the reply to /approve or /deny that answered n cards. These are the
// Telegram texts.
func resolveReply(name string, scope security.Scope, n, waiting, sends int) string {
	line := "1 human-only request still waits for a tap."
	if waiting > 1 {
		line = fmt.Sprintf("%d human-only requests still wait for a tap.", waiting)
	}
	sendLine := "1 send with findings still waits for a tap."
	if sends > 1 {
		sendLine = fmt.Sprintf("%d sends with findings still wait for a tap.", sends)
	}
	lines := ""
	if waiting > 0 {
		lines += "\n" + line
	}
	if sends > 0 {
		lines += "\n" + sendLine
	}
	if n == 0 && waiting+sends > 0 {
		return "🔒 **No command was approved**" + lines
	}
	var reply string
	switch {
	case n == 0:
		return noPending
	case name == "deny" && n == 1:
		reply = markdownHeader("❌", "Approval denied", "") + "\nThe agent continues without the command."
	case name == "deny":
		reply = markdownHeader("❌", "Approval denied", fmt.Sprintf("%d commands", n)) + "\nThe agent continues without them."
	case n > 1:
		reply = markdownHeader("✅", "Approval allowed", fmt.Sprintf("%d commands", n)) + "\nThe agent continues."
	default:
		e, t, d := approvalHeaderOf(scope)
		reply = markdownHeader(e, t, d) + "\nThe agent continues."
	}
	return reply + lines
}

// Open returns copies of the open cards, oldest first.
func (c *Cards) Open() []Card {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := make([]*openCard, 0, len(c.open))
	for _, oc := range c.open {
		list = append(list, oc)
	}
	slices.SortFunc(list, func(a, b *openCard) int { return int(a.seq - b.seq) })
	out := make([]Card, 0, len(list))
	for _, oc := range list {
		v := oc.view
		v.Secrets, v.Findings = slices.Clone(v.Secrets), slices.Clone(v.Findings)
		v.Answers, v.Choices = slices.Clone(v.Answers), slices.Clone(v.Choices)
		out = append(out, v)
	}
	return out
}
