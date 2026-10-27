package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixedAsker gives the same answer to each question and records the questions.
type fixedAsker struct {
	ans  Answer
	seen *[]Question
}

func (f fixedAsker) Ask(_ context.Context, q Question) (Answer, error) {
	if f.seen != nil {
		*f.seen = append(*f.seen, q)
	}
	return f.ans, nil
}

// blockingAsker never answers. With obey false it also ignores the end of its context.
type blockingAsker struct{ obey bool }

func (b blockingAsker) Ask(ctx context.Context, _ Question) (Answer, error) {
	if b.obey {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}
	select {}
}

func TestAskReturnsAnswerText(t *testing.T) {
	var seen []Question
	env := &Env{Ask: fixedAsker{ans: Answer{Text: "blue"}, seen: &seen}}
	if out := runToolEnv(t, AskUserTool{Timeout: time.Minute}, `{"question":"Color?"}`, env); out != "blue" {
		t.Fatal(out)
	}
	if len(seen) != 1 || seen[0].Text != "Color?" || seen[0].Timeout != time.Minute || seen[0].Multi || len(seen[0].Choices) != 0 {
		t.Fatalf("%+v", seen)
	}
}

func TestAskChoicesJoined(t *testing.T) {
	var seen []Question
	env := &Env{Ask: fixedAsker{ans: Answer{Choices: []string{"a", "c"}}, seen: &seen}}
	out := runToolEnv(t, AskUserTool{Timeout: time.Minute}, `{"question":"Pick","choices":["a"," b ","","c"],"multi_select":true}`, env)
	if out != "a, c" {
		t.Fatal(out)
	}
	if !seen[0].Multi || !reflect.DeepEqual(seen[0].Choices, []string{"a", "b", "c"}) {
		t.Fatalf("%+v", seen[0])
	}
}

func TestAskTimeout(t *testing.T) {
	for _, obey := range []bool{true, false} {
		start := time.Now()
		out := runToolEnv(t, AskUserTool{Timeout: 50 * time.Millisecond}, `{"question":"x"}`, &Env{Ask: blockingAsker{obey: obey}})
		if out != "no answer within 50ms; continue without it" || time.Since(start) > 2*time.Second {
			t.Fatal(obey, out)
		}
	}
}

func TestAskTimeoutTextUsesDurationString(t *testing.T) {
	if got := askTimeoutText(10 * time.Minute); got != "no answer within 10m0s; continue without it" {
		t.Fatal(got)
	}
}

func TestAskUnavailableInCronAndChildren(t *testing.T) {
	for _, kind := range []string{"cron", "subagent", "review"} {
		out := runToolEnvErr(t, AskUserTool{Timeout: time.Second}, `{"question":"x"}`, &Env{Kind: kind, Ask: fixedAsker{}})
		if !strings.Contains(out, "no user is present") || !strings.Contains(out, kind) {
			t.Fatal(kind, out)
		}
	}
}

func TestAskNoAskerIsError(t *testing.T) {
	out := runToolEnvErr(t, AskUserTool{Timeout: time.Second}, `{"question":"x"}`, &Env{Kind: "chat"})
	if !strings.Contains(out, "no user is present") {
		t.Fatal(out)
	}
}

func TestAskRejectsBadArgs(t *testing.T) {
	env := &Env{Ask: fixedAsker{ans: Answer{Text: "x"}}}
	if out := runToolEnvErr(t, AskUserTool{}, `{"question":"  "}`, env); !strings.Contains(out, "question is required") {
		t.Fatal(out)
	}
	many := `{"question":"x","choices":["1","2","3","4","5","6","7","8","9","10","11"]}`
	if out := runToolEnvErr(t, AskUserTool{}, many, env); !strings.Contains(out, "at most 10 choices") {
		t.Fatal(out)
	}
}

func TestAskEmptyAnswer(t *testing.T) {
	out := runToolEnv(t, AskUserTool{Timeout: time.Second}, `{"question":"x"}`, &Env{Ask: fixedAsker{}})
	if out != "(the user gave an empty answer)" {
		t.Fatal(out)
	}
}

func TestAskToolMeta(t *testing.T) {
	tool := AskUserTool{}
	if tool.Name() != "ask_user" || tool.Toolset() != SetAsk || tool.ParallelSafe() || tool.Schema().Name != "ask_user" {
		t.Fatal("meta")
	}
	if tool.timeout() != 600*time.Second {
		t.Fatal(tool.timeout())
	}
}
