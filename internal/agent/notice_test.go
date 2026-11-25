package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

func TestNoticeText(t *testing.T) {
	wib := time.FixedZone("WIB", 7*3600)
	zai := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, wib)
	quota := provider.Notice{Kind: provider.NoticeQuota, Entry: "primary", Model: "glm-5.3",
		NextEntry: "openrouter", NextModel: "deepseek/deepseek-v4.1-flash-1017"}
	today, otherDay, unknown, noNext := quota, quota, quota, quota
	// 19:40 in UTC+8 is 18:40 WIB. The text uses the zone of now.
	today.ResetAt = time.Date(2026, 11, 25, 19, 40, 0, 0, zai)
	otherDay.ResetAt = time.Date(2026, 11, 26, 18, 40, 0, 0, wib)
	noNext.NextEntry, noNext.NextModel, noNext.ResetAt = "", "", today.ResetAt
	wait := provider.Notice{Kind: provider.NoticeWait, Entry: "primary", Model: "glm-5.3", Wait: 30 * time.Second, Cause: "HTTP 429"}
	waitRound := wait
	waitRound.Wait = 12600 * time.Millisecond
	cases := []struct {
		name string
		n    provider.Notice
		want string
	}{
		{"wait", wait, "⏳ `primary` is busy (HTTP 429). The next try is in 30 s."},
		{"wait rounds to a second", waitRound, "⏳ `primary` is busy (HTTP 429). The next try is in 13 s."},
		{"reset today", today, "⚠️ The quota of `primary` is used up. It resets at 18:40 WIB. Until then, the calls go to `openrouter` (`deepseek/deepseek-v4.1-flash-1017`)."},
		{"reset another day", otherDay, "⚠️ The quota of `primary` is used up. It resets at Thu 26 Nov 18:40 WIB. Until then, the calls go to `openrouter` (`deepseek/deepseek-v4.1-flash-1017`)."},
		{"reset not known", unknown, "⚠️ The quota of `primary` is used up. The reset time is not known. The calls go to `openrouter` (`deepseek/deepseek-v4.1-flash-1017`) until `primary` works again."},
		{"no next entry", noNext, ""},
		{"unknown kind", provider.Notice{Entry: "primary"}, ""},
	}
	for _, c := range cases {
		if got := noticeText(c.n, now); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}
	if got := fmt.Sprintf(NoteProviderBusy, "a", "b", 3); got != "⏳ `a` is busy (b). The next try is in 3 s." {
		t.Fatal(got)
	}
}

// quotaPrimary fails each call before the first byte with the Z.AI quota code 1308.
type quotaPrimary struct{}

func (quotaPrimary) Name() string  { return "primary" }
func (quotaPrimary) Model() string { return "glm-5.3" }
func (quotaPrimary) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, fmt.Errorf("provider primary: %w: %w", provider.ErrBeforeFirstByte,
		&provider.HTTPStatusError{Code: 429, ProviderCode: "1308", ResetAt: time.Now().Add(2 * time.Hour)})
}

// quotaAgent returns an Agent whose Z.AI primary has a used-up quota, so the fallback
// answers n calls.
func quotaAgent(t *testing.T, n int) (*Agent, *sessions.Session) {
	t.Helper()
	steps := make([]step, n)
	for i := range steps {
		steps[i] = text("hi")
	}
	sec := &fakeProv{name: "fallback", model: "deepseek/deepseek-v4.1-flash-1017", stamp: true, script: steps}
	a, sess := newAgent(t, scripted())
	h := provider.NewHealth(nil)
	h.Sleep = func(context.Context, time.Duration) error { return nil }
	a.Prov = provider.NewChain([]provider.ChainEntry{{Name: "primary", P: quotaPrimary{}, ZAI: true}, {Name: "fallback", P: sec}},
		h, slog.New(slog.DiscardHandler))
	a.Providers = map[string]provider.Provider{"primary": a.Prov, "fallback": sec}
	a.Cfg.Providers = map[string]config.Provider{
		"primary":  {Model: "glm-5.3"},
		"fallback": {Model: "deepseek/deepseek-v4.1-flash-1017"},
	}
	return a, sess
}

// quotaNotices returns the quota notices of sink.
func quotaNotices(sink *noticeSink) []string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []string
	for _, s := range sink.notices {
		if strings.HasPrefix(s, "⚠️ The quota of ") {
			out = append(out, s)
		}
	}
	return out
}

// One quota episode gives one notice, also over two chat turns.
func TestQuotaNoticeReachesChatSinkOnce(t *testing.T) {
	a, sess := quotaAgent(t, 2)
	sink := &noticeSink{}
	for range 2 {
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: sink}); err != nil {
			t.Fatal(err)
		}
	}
	got := quotaNotices(sink)
	if len(got) != 1 || !strings.HasPrefix(got[0], "⚠️ The quota of `primary` is used up. It resets at ") ||
		!strings.HasSuffix(got[0], " Until then, the calls go to `fallback` (`deepseek/deepseek-v4.1-flash-1017`).") {
		t.Fatalf("one quota notice for the episode: %q", sink.notices)
	}
}

// A cron turn and a turn with no sink show no notice. The notice stays pending, and the next
// chat turn with a sink delivers it.
func TestQuotaNoticeWaitsForChatTurn(t *testing.T) {
	a, sess := quotaAgent(t, 3)
	sink := &noticeSink{}
	sess.Kind = "cron"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "report", Sink: sink}); err != nil {
		t.Fatal(err)
	}
	if len(sink.notices) != 0 {
		t.Fatalf("a cron turn shows no notice: %q", sink.notices)
	}
	sess.Kind = "chat"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: sink}); err != nil {
		t.Fatal(err)
	}
	if got := quotaNotices(sink); len(got) != 1 {
		t.Fatalf("the chat turn with a sink delivers the pending notice: %q", sink.notices)
	}
}

// A turn of a group member shows no notice. The notice stays pending for the next turn of
// the owner.
func TestQuotaNoticeSkipsMemberTurn(t *testing.T) {
	a, sess := quotaAgent(t, 2)
	member := &noticeSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: member, Member: true}); err != nil {
		t.Fatal(err)
	}
	if got := quotaNotices(member); len(got) != 0 {
		t.Fatalf("a member turn shows no quota notice: %q", got)
	}
	owner := &noticeSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: owner}); err != nil {
		t.Fatal(err)
	}
	if got := quotaNotices(owner); len(got) != 1 {
		t.Fatalf("the turn of the owner delivers the pending notice: %q", owner.notices)
	}
}

// RunTurn clears the notify function of its context. A child turn or a review turn must not
// send a notice through the function of the parent turn.
func TestRunTurnClearsParentNotify(t *testing.T) {
	a, sess := quotaAgent(t, 2)
	var parent []provider.Notice
	pctx := provider.WithNotify(ctx, func(n provider.Notice) { parent = append(parent, n) })
	if _, err := a.RunTurn(pctx, Turn{Session: sess, UserText: "q"}); err != nil {
		t.Fatal(err)
	}
	if len(parent) != 0 {
		t.Fatalf("the notify function of the parent got notices: %+v", parent)
	}
	sink := &noticeSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: sink}); err != nil {
		t.Fatal(err)
	}
	if got := quotaNotices(sink); len(got) != 1 {
		t.Fatalf("the notice stays pending for the next chat turn: %q", sink.notices)
	}
}
