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

// planStart is the start list of planItems.
const planStart = "📋 Plan 1/3\n[x] Find the flaky test\n[>] Replace the real clock with a fake clock\n" +
	"[ ] Run the cron tests 20 times"

// planChange is the change message from planItems to planStep("completed", "in_progress").
const planChange = "✅ 2/3 Replace the real clock with a fake clock"

// planDone is the done list from planStep("completed", "in_progress") to
// planStep("completed", "completed").
const planDone = "📋 Plan done 3/3\n[x] Find the flaky test\n[x] Replace the real clock with a fake clock\n" +
	"[x] Run the cron tests 20 times"

func planItems() []tools.TodoItem {
	return []tools.TodoItem{
		{ID: "1", Content: "Find the flaky test", Status: "completed"},
		{ID: "2", Content: "Replace the real clock with a fake clock", Status: "in_progress"},
		{ID: "3", Content: "Run the cron tests 20 times", Status: "pending"},
	}
}

// planStep returns planItems with new statuses for the items 2 and 3.
func planStep(second, third string) []tools.TodoItem {
	items := planItems()
	items[1].Status, items[2].Status = second, third
	return items
}

// runPlan sends the start list, the change message, and the done list of planItems.
func runPlan(s *Status) {
	s.SetPlan(ctx, nil, planItems())
	s.SetPlan(ctx, planItems(), planStep("completed", "in_progress"))
	s.SetPlan(ctx, planStep("completed", "in_progress"), planStep("completed", "completed"))
}

// sendOf returns the first sendMessage call with text. ok is false when no call has it.
func sendOf(api *fakeBot, text string) (apiCall, bool) {
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); c.Method == "sendMessage" && txt == text {
			return c, true
		}
	}
	return apiCall{}, false
}

// isSilent reports whether the call has disable_notification set.
func isSilent(c apiCall) bool {
	silent, _ := c.Body["disable_notification"].(bool)
	return silent
}

// deletedIDs returns the message IDs of the deleteMessage calls, in order.
func deletedIDs(api *fakeBot) []int {
	var out []int
	for _, c := range api.all() {
		if c.Method == "deleteMessage" {
			id, _ := c.Body["message_id"].(float64)
			out = append(out, int(id))
		}
	}
	return out
}

func TestToolEmojiTodoList(t *testing.T) {
	if got := ToolLine("all", "todo_list", "planning 3 task(s)", "{}"); got != `📋 todo_list: "planning 3 task(s)"` {
		t.Fatal(got)
	}
}

func TestGoalLineIsItsOwnMessage(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.SetGoalLine(ctx, goalLine)
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{goalLine}) || len(api.all()) != 1 {
		t.Fatal("no bubble must go out before a tool line:", api.methods(), got)
	}
	if !isSilent(api.lastCall("sendMessage")) {
		t.Fatal("the goal message must be silent")
	}
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	want := []string{goalLine, `📖 read_file: "a"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if isSilent(api.lastCall("sendMessage")) {
		t.Fatal("the bubble must not be silent")
	}
}

func TestGoalLineAndPlanStayFirst(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.SetGoalLine(ctx, goalLine)
	clk.Advance(2 * time.Second)
	s.SetPlan(ctx, nil, planItems())
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Reset()
	s.Add(ctx, Source{}, "web_fetch", "b", "{}")
	want := []string{goalLine, planStart, `📖 read_file: "a"`, `📄 web_fetch: "b"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, txt := range api.textsOf("editMessageText") {
		if strings.Contains(txt, "📋") || strings.Contains(txt, goalLine) {
			t.Fatalf("a bubble must show tool lines only: %q", txt)
		}
	}
}

func TestNoSplitBubbleStartsWithTheGoalLine(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "verbose")
	s.SetGoalLine(ctx, goalLine)
	s.SetPlan(ctx, nil, planItems())
	long := `{"code":"` + strings.Repeat("x", 900) + `"}`
	for i := range 6 {
		clk.Advance(2 * time.Second)
		s.Add(ctx, Source{}, "execute_code", "", strings.Replace(long, "x", fmt.Sprint(i), 1))
	}
	s.Close(ctx, true)
	sends := api.textsOf("sendMessage")
	if len(sends) < 4 || sends[0] != goalLine || sends[1] != planStart {
		t.Fatalf("%.80q", sends)
	}
	for _, txt := range sends[2:] {
		if !strings.HasPrefix(txt, "🐍 execute_code(code)\n") {
			t.Fatalf("a split bubble must start with a tool line: %.80q", txt)
		}
	}
	for _, c := range api.all() {
		txt, _ := c.Body["text"].(string)
		if UTF16Len(txt) > statusTextLimit {
			t.Fatal("bubble too long:", UTF16Len(txt))
		}
		if strings.Contains(txt, goalLine) && txt != goalLine {
			t.Fatalf("a bubble must not show the goal line: %.80q", txt)
		}
	}
}

func TestOffModeGoalLineIsOneSilentMessage(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "off")
	s.Cleanup = true
	s.SetGoalLine(ctx, goalLine)
	s.SetPlan(ctx, nil, planItems())
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Close(ctx, true)
	if len(api.all()) != 1 || api.count("sendMessage") != 1 || api.lastText() != goalLine {
		t.Fatal("off mode must send only the goal message, and the cleanup must not delete it:", api.methods(), api.lastText())
	}
	if !isSilent(api.lastCall("sendMessage")) {
		t.Fatal("the goal line must be silent in off mode")
	}
}

func TestCleanupDeletesTheGoalMessage(t *testing.T) {
	for _, mode := range []string{"all", "new", "verbose"} {
		t.Run(mode, func(t *testing.T) {
			s, api, _ := statusWithFakeAPI(t, mode)
			s.Cleanup = true
			s.SetGoalLine(ctx, goalLine)
			s.Close(ctx, true)
			if got := deletedIDs(api); !slices.Equal(got, []int{firstBubble}) || api.count("sendMessage") != 1 {
				t.Fatal("the cleanup must delete the goal message:", api.methods(), got)
			}
		})
	}
}

func TestRendererGoalLineAndPlan(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), Cfg: config.Default().Telegram, typingEvery: 20 * time.Millisecond}
	r := ch.newRenderer(ctx, 42, "", true)
	var _ gateway.GoalLineRenderer = r
	var _ agent.PlanSink = r
	r.GoalLine(goalLine)
	r.Plan(nil, planItems())
	r.Done("ok", nil)
	sends := api.textsOf("sendMessage")
	if len(sends) < 2 || sends[0] != goalLine || sends[1] != planStart {
		t.Fatalf("%q", sends)
	}
	if c, _ := sendOf(api, planStart); !isSilent(c) {
		t.Fatal("the plan message must be silent")
	}
	for _, txt := range api.textsOf("editMessageText") {
		if strings.Contains(txt, "📋") {
			t.Fatalf("a bubble must not show the plan: %q", txt)
		}
	}
}

func TestPlanMessageClosesTheBubble(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Add(ctx, Source{}, "terminal", "c", "{}") // waits for the interval
	s.SetPlan(ctx, nil, planItems())
	if got := lastEditOf(api, firstBubble); got != "📖 read_file: \"a\"\n💻 terminal: \"c\"" {
		t.Fatalf("the closed bubble must get its last line: %q", got)
	}
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "web_fetch", "b", "{}")
	want := []string{`📖 read_file: "a"`, planStart, `📄 web_fetch: "b"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := lastEditOf(api, firstBubble); strings.Contains(got, "web_fetch") {
		t.Fatalf("a tool line after a plan message must open a new bubble: %q", got)
	}
}

func TestPlanWithNoTextKeepsTheBubble(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.SetPlan(ctx, planItems(), planItems()) // a read: no plan message
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "web_fetch", "b", "{}")
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{`📖 read_file: "a"`}) {
		t.Fatalf("%q", got)
	}
	if got := lastEditOf(api, firstBubble); got != "📖 read_file: \"a\"\n📄 web_fetch: \"b\"" {
		t.Fatalf("%q", got)
	}
}

func TestEachPlanMessageIsSilent(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	runPlan(s)
	want := []string{planStart, planChange, planDone}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, c := range api.all() {
		if c.Method == "sendMessage" && !isSilent(c) {
			t.Fatalf("a plan message must be silent: %q", c.Body["text"])
		}
	}
}

func TestOffModeSendsNoPlanMessage(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "off")
	runPlan(s)
	s.Close(ctx, true)
	if len(api.all()) != 0 {
		t.Fatal("off mode must send no plan message:", api.methods())
	}
}

func TestMainTodoListLineIsHidden(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "todo_list", "planning 3 task(s)", "{}")
	if len(api.all()) != 0 {
		t.Fatal("the todo_list line of the main agent must not show:", api.methods())
	}
	s.Add(ctx, Source{Batch: 1, Child: "a"}, "todo_list", "planning 3 task(s)", "")
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{`🔀 [a] todo_list "planning 3 task(s)"`}) {
		t.Fatalf("the todo_list line of a child must stay: %q", got)
	}
}

func TestHiddenTodoListKeepsTheLastKey(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "new")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	s.Add(ctx, Source{}, "todo_list", "planning 3 task(s)", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	s.Close(ctx, false)
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{`📖 read_file: "a"`}) || api.count("editMessageText") != 0 {
		t.Fatal("a hidden todo_list call must not give the next call of the same tool a line:", api.methods(), got)
	}
}

func TestNoEditSameToolAfterPlanMessage(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "terminal", "go test", "{}")
	failEdits(api)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the edit fails for good: noEdit
	s.SetPlan(ctx, nil, planItems())
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	want := []string{`💻 terminal: "go test"`, `📖 read_file: "a"`, planStart, `📖 read_file: "a"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCleanupKeepsTheDoneList(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.Add(ctx, Source{}, "read_file", "a", "{}") // firstBubble
	runPlan(s)                                   // the start list, the change message, the done list
	s.Close(ctx, true)
	want := []int{firstBubble, firstBubble + 1, firstBubble + 2}
	if got := deletedIDs(api); !slices.Equal(got, want) {
		t.Fatalf("the cleanup must delete the bubble, the start list, and the change message, but not the done list: got %v, want %v", got, want)
	}
}

func TestFloodWaitGoalMessageGoesFirst(t *testing.T) {
	for _, mode := range []string{"all", "off"} {
		t.Run(mode, func(t *testing.T) {
			s, api, clk := statusWithFakeAPI(t, mode)
			api.setReply(func(method string, _ map[string]any) *APIError {
				if method == "sendMessage" {
					return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
				}
				return nil
			})
			s.SetGoalLine(ctx, goalLine)
			api.setReply(nil)
			s.Add(ctx, Source{}, "read_file", "a", "{}")
			if api.count("sendMessage") != 1 {
				t.Fatal("the bubble must wait behind the goal message:", api.methods())
			}
			clk.Advance(11 * time.Second)
			if !fireTimer(s) {
				t.Fatal("no timer sends the queued goal message")
			}
			want := []string{goalLine, goalLine} // the failed send, and then the send that works
			if mode != "off" {
				want = append(want, `📖 read_file: "a"`)
			}
			if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
			for _, c := range api.all() {
				if txt, _ := c.Body["text"].(string); c.Method == "sendMessage" && txt == goalLine && !isSilent(c) {
					t.Fatal("the goal message must be silent")
				}
			}
		})
	}
}
