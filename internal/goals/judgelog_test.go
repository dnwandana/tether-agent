package goals

import (
	"context"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

func TestJudgeLogRecord(t *testing.T) {
	ctx := context.Background()
	d := openGoalsDB(t)
	store := sessions.New(d)
	goal, err := store.Create(ctx, sessions.Session{Key: "telegram:42", Source: "telegram", ChatID: "42", UserID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	l := &JudgeLog{Sessions: store, Meta: func(p provider.Provider, out provider.Result) sessions.MessageMeta {
		return sessions.MessageMeta{Usage: out.Usage, Model: p.Model(), Provider: p.Name(), FinishReason: out.FinishReason, CostUSD: 0.002}
	}}
	st := State{Goal: "Fix the flaky cron test", Status: StatusActive}
	out := provider.Result{Text: `{"verdict": "continue", "reason": "not verified"}`, FinishReason: "stop",
		Usage: provider.Usage{InputTokens: 900, OutputTokens: 40}}
	if err := l.Record(ctx, goal, &st, "JUDGE PROMPT 1", &judgeFakeProv{}, out); err != nil {
		t.Fatal(err)
	}
	if st.JudgeSessionID == "" {
		t.Fatal("Record must keep the judge session id in the state")
	}
	judge, err := store.Get(ctx, st.JudgeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if judge.Kind != sessions.KindGoalJudge || judge.Key != "" || judge.ParentID != goal.ID ||
		judge.Source != "telegram" || judge.UserID != "42" {
		t.Fatalf("%+v", judge)
	}
	first := st.JudgeSessionID
	if err := l.Record(ctx, goal, &st, "JUDGE PROMPT 2", &judgeFakeProv{}, out); err != nil || st.JudgeSessionID != first {
		t.Fatalf("a second call must reuse the session: %v %q", err, st.JudgeSessionID)
	}
	ms, err := store.Messages(ctx, first)
	if err != nil || len(ms) != 4 {
		t.Fatal(len(ms), err)
	}
	if ms[0].Msg.Role != provider.RoleUser || ms[0].Msg.Content != "JUDGE PROMPT 1" || ms[0].Meta.Model != "" {
		t.Fatalf("%+v", ms[0])
	}
	a := ms[1]
	if a.Msg.Role != provider.RoleAssistant || a.Msg.Content != out.Text || a.Meta.Model != "fake-model" ||
		a.Meta.Provider != "fake" || a.Meta.Usage.OutputTokens != 40 || a.Meta.CostUSD != 0.002 {
		t.Fatalf("%+v", a)
	}
	rows, _, err := obs.UsageReport(ctx, d, time.Time{}, goal.ID)
	if err != nil || len(rows) != 1 || rows[0].Calls != 2 || !rows[0].CostKnown {
		t.Fatalf("/usage session must count the judge calls: %+v %v", rows, err)
	}
}

func TestJudgeLogDefaultMeta(t *testing.T) {
	ctx := context.Background()
	store := sessions.New(openGoalsDB(t))
	goal, _ := store.Create(ctx, sessions.Session{Key: "cli:user", Source: "cli"})
	st := State{Goal: "g", Status: StatusActive}
	l := &JudgeLog{Sessions: store}
	out := provider.Result{Text: "x", Provider: "judge", Model: "small-model", Usage: provider.Usage{OutputTokens: 3}}
	if err := l.Record(ctx, goal, &st, "p", &judgeFakeProv{}, out); err != nil {
		t.Fatal(err)
	}
	ms, _ := store.Messages(ctx, st.JudgeSessionID)
	if len(ms) != 2 || ms[1].Meta.Provider != "judge" || ms[1].Meta.Model != "small-model" || ms[1].Meta.Usage.OutputTokens != 3 {
		t.Fatalf("a nil Meta must use the stamp of the result: %+v", ms)
	}
}

func TestJudgeLogRecordsAfterCancel(t *testing.T) {
	store := sessions.New(openGoalsDB(t))
	goal, _ := store.Create(context.Background(), sessions.Session{Key: "cli:user", Source: "cli"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := State{Goal: "g", Status: StatusActive}
	if err := (&JudgeLog{Sessions: store}).Record(ctx, goal, &st, "p", &judgeFakeProv{}, provider.Result{Text: "x"}); err != nil {
		t.Fatalf("a paid call must be recorded after a cancel: %v", err)
	}
}

func TestJudgeLogRecreatesDeletedSession(t *testing.T) {
	ctx := context.Background()
	store := sessions.New(openGoalsDB(t))
	goal, _ := store.Create(ctx, sessions.Session{Key: "cli:user", Source: "cli"})
	st := State{Goal: "g", Status: StatusPaused}
	l := &JudgeLog{Sessions: store}
	if err := l.Record(ctx, goal, &st, "p1", &judgeFakeProv{}, provider.Result{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	old := st.JudgeSessionID
	// Retention deletes an old goal_judge session while the goal stays paused.
	if err := store.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, goal, &st, "p2", &judgeFakeProv{}, provider.Result{Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if st.JudgeSessionID == "" || st.JudgeSessionID == old {
		t.Fatalf("Record must create a new judge session, got %q", st.JudgeSessionID)
	}
	judge, err := store.Get(ctx, st.JudgeSessionID)
	if err != nil || judge.Kind != sessions.KindGoalJudge || judge.ParentID != goal.ID {
		t.Fatalf("%+v %v", judge, err)
	}
	ms, err := store.Messages(ctx, st.JudgeSessionID)
	if err != nil || len(ms) != 2 || ms[0].Msg.Content != "p2" {
		t.Fatalf("%+v %v", ms, err)
	}
	if orphan, _ := store.Messages(ctx, old); len(orphan) != 0 {
		t.Fatalf("Record must not append to a deleted session: %+v", orphan)
	}
}
