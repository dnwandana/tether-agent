package telegram

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const goalLine = "↻ Goal 2/20: the fix is not verified."

// planBlock is the plan block of planItems.
const planBlock = "📋 Plan 1/3\n[x] Find the flaky test\n[>] Replace the real clock with a fake clock\n" +
	"[ ] Run the cron tests 20 times\n───"

func planItems() []tools.TodoItem {
	return []tools.TodoItem{
		{ID: "1", Content: "Find the flaky test", Status: "completed"},
		{ID: "2", Content: "Replace the real clock with a fake clock", Status: "in_progress"},
		{ID: "3", Content: "Run the cron tests 20 times", Status: "pending"},
	}
}

func TestToolEmojiTodoList(t *testing.T) {
	if got := ToolLine("all", "todo_list", "planning 3 task(s)", "{}"); got != `📋 todo_list: "planning 3 task(s)"` {
		t.Fatal(got)
	}
}

func TestGoalLineOpensTheBubble(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.SetGoalLine(ctx, goalLine)
	if api.count("sendMessage") != 1 || api.lastText() != goalLine {
		t.Fatal(api.methods(), api.lastText())
	}
	if silent, _ := api.lastCall("sendMessage").Body["disable_notification"].(bool); silent {
		t.Fatal("the bubble must not be silent")
	}
}

func TestGoalLineAndPlanStayFirst(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.SetGoalLine(ctx, goalLine)
	clk.Advance(2 * time.Second)
	s.SetPlan(ctx, planItems())
	if got := api.lastText(); got != goalLine+"\n"+planBlock {
		t.Fatalf("%q", got)
	}
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	if got := api.lastText(); got != goalLine+"\n"+planBlock+"\n📖 read_file: \"a\"" {
		t.Fatalf("%q", got)
	}
	s.Reset()
	s.Add(ctx, Source{}, "web_fetch", "b", "{}")
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || sends[1] != goalLine+"\n"+planBlock+"\n📄 web_fetch: \"b\"" {
		t.Fatalf("a bubble after a Reset must start with the goal line and the plan: %q", sends)
	}
}

func TestGoalLineInEachSplitBubble(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "verbose")
	s.SetGoalLine(ctx, goalLine)
	s.SetPlan(ctx, planItems())
	long := `{"code":"` + strings.Repeat("x", 900) + `"}`
	for i := range 6 {
		clk.Advance(2 * time.Second)
		s.Add(ctx, Source{}, "execute_code", "", strings.Replace(long, "x", fmt.Sprint(i), 1))
	}
	s.Close(ctx, true)
	sends := api.textsOf("sendMessage")
	if len(sends) < 2 {
		t.Fatal(api.methods())
	}
	for _, txt := range sends[1:] {
		if !strings.HasPrefix(txt, goalLine+"\n"+planBlock+"\n") {
			t.Fatalf("a split bubble must start with the goal line and the plan: %q", txt[:80])
		}
	}
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); UTF16Len(txt) > statusTextLimit {
			t.Fatal("bubble too long:", UTF16Len(txt))
		}
	}
}

func TestPlanBlockShowsEightItems(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	var items []tools.TodoItem
	for i := 1; i <= 10; i++ {
		status := "pending"
		if i <= 3 {
			status = "completed"
		}
		items = append(items, tools.TodoItem{ID: fmt.Sprint(i), Content: fmt.Sprintf("task %d", i), Status: status})
	}
	s.SetPlan(ctx, items)
	want := "📋 Plan 3/10\n[ ] task 4\n[ ] task 5\n[ ] task 6\n[ ] task 7\n[ ] task 8\n[ ] task 9\n[ ] task 10\n" +
		"[x] task 1\n+2 more\n───"
	if got := api.lastText(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlanBlockLeavesOutTheCancelledItemsFromTheCount(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.SetPlan(ctx, []tools.TodoItem{
		{ID: "1", Content: "a", Status: "completed"},
		{ID: "2", Content: "b", Status: "cancelled"},
		{ID: "3", Content: "c", Status: "pending"},
	})
	if got := api.lastText(); got != "📋 Plan 1/2\n[x] a\n[~] b\n[ ] c\n───" {
		t.Fatalf("%q", got)
	}
}

func TestPlanGivesUpItsLastLinesFirst(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	var items []tools.TodoItem
	for i := 1; i <= 8; i++ {
		items = append(items, tools.TodoItem{ID: fmt.Sprint(i), Content: strings.Repeat(fmt.Sprint(i), 1000), Status: "pending"})
	}
	s.SetGoalLine(ctx, goalLine)
	clk.Advance(2 * time.Second)
	s.SetPlan(ctx, items)
	got := api.lastText()
	if UTF16Len(got) > statusTextLimit || !strings.HasPrefix(got, goalLine+"\n📋 Plan 0/8\n[ ] "+strings.Repeat("1", 1000)) ||
		!strings.Contains(got, "\n+5 more\n───") || strings.Contains(got, "4444") {
		t.Fatalf("len %d: %q", UTF16Len(got), got[:120])
	}
}

func TestOffModeGoalLineIsOneSilentMessage(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "off")
	s.SetGoalLine(ctx, goalLine)
	s.SetPlan(ctx, planItems())
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Close(ctx, true)
	if len(api.all()) != 1 || api.count("sendMessage") != 1 || api.lastText() != goalLine {
		t.Fatal(api.methods(), api.lastText())
	}
	if silent, _ := api.lastCall("sendMessage").Body["disable_notification"].(bool); !silent {
		t.Fatal("the goal line must be silent in off mode")
	}
}

func TestCleanupDeletesTheGoalBubble(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.SetGoalLine(ctx, goalLine)
	s.Close(ctx, true)
	if api.count("deleteMessage") != 1 {
		t.Fatal(api.methods())
	}
}

func TestRendererGoalLineAndPlan(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), Cfg: config.Default().Telegram, typingEvery: 20 * time.Millisecond}
	r := ch.newRenderer(ctx, 42, "", true)
	var _ gateway.GoalLineRenderer = r
	var _ agent.PlanSink = r
	r.GoalLine(goalLine)
	r.Plan(planItems())
	r.Done("ok", nil)
	sends := api.textsOf("sendMessage")
	if len(sends) == 0 || sends[0] != goalLine {
		t.Fatalf("%q", sends)
	}
	if !slices.Contains(api.textsOf("editMessageText"), goalLine+"\n"+planBlock) {
		t.Fatalf("%q", api.textsOf("editMessageText"))
	}
}
