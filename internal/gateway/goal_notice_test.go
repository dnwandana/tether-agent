package gateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const goOn = " Use /goal resume to keep going, or /goal clear to stop."

func TestFormatNoticeTexts(t *testing.T) {
	for _, c := range []struct {
		n    goals.Notice
		want string
	}{
		{goals.Notice{Kind: goals.NoticeDone, Reason: "20 test runs pass"}, "✓ Goal achieved: 20 test runs pass"},
		{goals.Notice{Kind: goals.NoticeBlocked, Reason: "needs the *prod* key", StopButtons: true},
			"🚫 Goal judged unachievable — paused: needs the \\*prod\\* key Re-scope with /goal set, or override with /goal resume."},
		{goals.Notice{Kind: goals.NoticeBudget, Used: 20, Max: 20, StopButtons: true}, "⏸ Goal paused — 20/20 turns used." + goOn},
		{goals.Notice{Kind: goals.NoticeBudgetGate, Used: 20, Max: 20, StopButtons: true},
			"⏸ Goal paused — 20/20 turns used (a quality gate is still failing)." + goOn},
		{goals.Notice{Kind: goals.NoticeParseFail, Count: 3, StopButtons: true},
			"⏸ Goal paused — the judge reply could not be parsed 3 times. Check the model of goals.judge_provider, then /goal resume."},
		{goals.Notice{Kind: goals.NoticeTransportFail, Count: 5, StopButtons: true},
			"⏸ Goal paused — the judge call failed 5 times. Check goals.judge_provider and its key, then /goal resume."},
		{goals.Notice{Kind: goals.NoticeParkedJudge, Target: "pid 4242", Reason: "the build_all job"},
			"⏳ Goal parked (judge) — waiting on pid 4242: the build\\_all job"},
		{goals.Notice{Kind: goals.NoticeStillParked, Target: "120s remaining", Reason: "deploy"},
			"⏳ Goal parked — waiting on 120s remaining: deploy"},
		{goals.Notice{Kind: goals.NoticeGateFailed, Used: 3, Max: 20, Attempt: 1, MaxRetries: 3, Command: "rg `TODO` [x]"},
			"✗ Quality gate failed (3/20 turns, attempt 1/3): $ rg \\`TODO\\` \\[x\\]"},
		{goals.Notice{Kind: goals.NoticeGateExhausted, MaxRetries: 3, Command: "make lint", ExitCode: 2, StopButtons: true},
			"⏸ Goal paused — quality gate still failing after 3 retries: $ make lint (exit 2). Fix it manually or /goal gate remove <N>, then /goal resume."},
		{goals.Notice{Kind: goals.NoticeGateRefused, Command: "rm -rf /", StopButtons: true,
			Reason: "BLOCKED (hardline): recursive delete of root filesystem. This command cannot be a gate."},
			"⏸ Goal paused — a quality gate cannot run: $ rm -rf /. BLOCKED (hardline): recursive delete of root filesystem. This command cannot be a gate. Remove it with /goal gate remove <N>, then /goal resume."},
		{goals.Notice{Kind: goals.NoticeTurnFailed, Err: "the turn gave no answer", StopButtons: true},
			"⏸ Goal paused — the last turn failed: the turn gave no answer." + goOn},
		{goals.Notice{Kind: goals.NoticeRestartPause, Down: 2*time.Hour + 5*time.Minute + 40*time.Second, StopButtons: true},
			"⏸ Goal paused — the gateway was down for 2 h 5 min." + goOn},
		{goals.Notice{Kind: goals.NoticeLoopPause, Reason: "the turn crashed the gateway 3 times", StopButtons: true},
			"⏸ Goal paused — the turn crashed the gateway 3 times." + goOn},
	} {
		rep := FormatNotice(c.n)
		if rep.Text != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.n.Kind, rep.Text, c.want)
		}
		if wantButtons := c.n.StopButtons; (rep.Buttons != nil) != wantButtons || wantButtons && !reflect.DeepEqual(rep.Buttons, StopButtons()) {
			t.Errorf("%s: buttons %v", c.n.Kind, rep.Buttons)
		}
	}
	if rep := FormatNotice(goals.Notice{Kind: "nope"}); rep.Text != "" {
		t.Fatal(rep)
	}
}

func TestFormatDown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "1 min", 45 * time.Minute: "45 min", 90 * time.Minute: "1 h 30 min", 3 * time.Hour: "3 h",
	} {
		if got := formatDown(d); got != want {
			t.Errorf("formatDown(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStopButtons(t *testing.T) {
	want := [][]Button{{{Text: "Resume", Data: "/goal resume"}, {Text: "Clear", Data: "/goal clear"}}}
	if got := StopButtons(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	for _, b := range StopButtons()[0] {
		if !strings.HasPrefix(b.Data, "/") || len(b.Data) >= 64 {
			t.Fatal(b)
		}
	}
}

func TestSendGoalNoticePaths(t *testing.T) {
	ctx := context.Background()
	n := goals.Notice{Kind: goals.NoticeBudget, Used: 20, Max: 20, StopButtons: true}
	ch := &recChannel{sent: make(chan Outbound, 4)}
	g := &Gateway{Deps: testDeps(t, &fakeProv{}), Channels: map[string]Channel{"telegram": ch}}
	sess := &sessions.Session{ID: "s1", Source: "telegram", ChatID: "1", UserID: "7"}

	rr := &replyRenderer{}
	g.sendGoalNotice(ctx, "k1", sess, rr, n)
	if len(rr.replies) != 1 || !reflect.DeepEqual(rr.replies[0], FormatNotice(n)) {
		t.Fatalf("a ReplyRenderer gets the buttons: %+v", rr.replies)
	}
	pr := &recRenderer{}
	g.sendGoalNotice(ctx, "k1", sess, pr, n)
	if len(pr.acks) != 1 || pr.acks[0] != FormatNotice(n).Text {
		t.Fatalf("a plain renderer gets the text: %q", pr.acks)
	}

	var got []Reply
	g.Deps.SendNotice = func(_ context.Context, key string, s *sessions.Session, r Reply) error {
		if key != "k1" || s != sess {
			t.Errorf("%s %+v", key, s)
		}
		got = append(got, r)
		return errors.New("telegram is down") // logged, no panic
	}
	g.sendGoalNotice(ctx, "k1", sess, nil, n)
	if len(got) != 1 || !reflect.DeepEqual(got[0], FormatNotice(n)) {
		t.Fatalf("no turn goes through SendNotice: %+v", got)
	}

	g.Deps.SendNotice = nil
	g.sendGoalNotice(ctx, "k1", sess, nil, n)
	if m := <-ch.sent; m.Text != FormatNotice(n).Text || m.Keyboard != nil || ch.to != (Target{Source: "telegram", ChatID: "1"}) {
		t.Fatalf("the fallback sends the text only: %+v %+v", m, ch.to)
	}
	g.sendGoalNotice(ctx, "k1", sess, nil, goals.Notice{Kind: "nope"})
	select {
	case m := <-ch.sent:
		t.Fatal("an empty notice must send nothing:", m)
	default:
	}
}
