package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestCLIPromptScopes(t *testing.T) {
	for in, want := range map[string]security.Scope{
		"o\n": security.ScopeOnce, "s\n": security.ScopeSession, "a\n": security.ScopeAlways, "d\n": security.ScopeDeny,
		"x\nd\n": security.ScopeDeny, "once\n": security.ScopeOnce, " S \n": security.ScopeSession, "": security.ScopeDeny,
	} {
		var out bytes.Buffer
		c := &CLIAsk{In: bufio.NewReader(strings.NewReader(in)), Out: &out}
		got, _ := c.PromptApproval(context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
		if got != want || !strings.Contains(out.String(), "[o]nce / [s]ession / [a]lways / [d]eny") {
			t.Fatal(in, got, out.String())
		}
	}
}

func TestCLIPromptText(t *testing.T) {
	for _, c := range []struct {
		req  security.ApprovalRequest
		want string
	}{
		{security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete", Summary: "Delete the temp folder."},
			"⚠ Approval needed\n  Delete the temp folder.\n  Flagged: recursive delete\n  rm -rf /tmp/x\n"},
		{security.ApprovalRequest{Command: "cd /x\nrm -rf /\necho done", Description: "recursive delete of root filesystem"},
			"⚠ Approval needed\n  recursive delete of root filesystem: rm -rf / (+2 lines)\n  Flagged: recursive delete of root filesystem\n  cd /x\n  rm -rf /\n  echo done\n"},
		{security.ApprovalRequest{Command: "rm -rf /tmp/x"},
			"⚠ Approval needed\n  dangerous command: rm -rf /tmp/x\n  Flagged: dangerous command\n  rm -rf /tmp/x\n"},
		// The ESC bytes and U+202E do not reach the terminal.
		{security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete", Summary: "\x1b[31mDelete\x1b[0m the \u202etemp folder."},
			"⚠ Approval needed\n  Delete the temp folder.\n  Flagged: recursive delete\n  rm -rf /tmp/x\n"},
	} {
		var out bytes.Buffer
		ask := &CLIAsk{In: bufio.NewReader(strings.NewReader("o\n")), Out: &out}
		if _, err := ask.PromptApproval(context.Background(), c.req); err != nil {
			t.Fatal(err)
		}
		if want := c.want + ApprovalPrompt + "✅ Approval allowed · once\n"; out.String() != want {
			t.Fatalf("got %q, want %q", out.String(), want)
		}
	}
}

func TestCLIPromptResultLines(t *testing.T) {
	for in, want := range map[string]string{
		"o\n": "✅ Approval allowed · once\n",
		"s\n": "✅ Approval allowed · this session\n",
		"a\n": "✅ Approval allowed · always\n",
		"d\n": "❌ Approval denied\n",
		"":    "\n❌ Approval denied\n",
	} {
		var out bytes.Buffer
		c := &CLIAsk{In: bufio.NewReader(strings.NewReader(in)), Out: &out}
		_, _ = c.PromptApproval(context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
		if !strings.HasSuffix(out.String(), ApprovalPrompt+want) {
			t.Errorf("%q: got %q", in, out.String())
		}
	}
}

func TestCLIPromptNamesChild(t *testing.T) {
	var out bytes.Buffer
	c := &CLIAsk{In: bufio.NewReader(strings.NewReader("d\n")), Out: &out}
	_, _ = c.PromptApproval(context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete", ChildName: "2/3"})
	if !strings.Contains(out.String(), "subagent 2/3") {
		t.Fatal(out.String())
	}
}

func TestCLIPromptTimeoutReturnsScopeTimeout(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	var out bytes.Buffer
	c := &CLIAsk{In: bufio.NewReader(r), Out: &out}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := c.PromptApproval(ctx, security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
	if got != security.ScopeTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(got, err)
	}
	if !strings.Contains(out.String(), ApprovalPrompt+"\n⌛ Approval denied · no answer\n") {
		t.Fatal(out.String())
	}
	// The line that the user types later goes to the next question, not to a lost read.
	go func() { _, _ = io.WriteString(w, "o\n") }()
	got, err = c.PromptApproval(context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
	if got != security.ScopeOnce || err != nil {
		t.Fatal(got, err)
	}
}

func TestCLIAskChoices(t *testing.T) {
	var out bytes.Buffer
	c := &CLIAsk{In: bufio.NewReader(strings.NewReader("2\n")), Out: &out}
	ans, err := c.Ask(context.Background(), tools.Question{Text: "Pick", Choices: []string{"a", "b"}})
	if err != nil || !reflect.DeepEqual(ans.Choices, []string{"b"}) || ans.Text != "" {
		t.Fatal(ans, err)
	}
	if !strings.Contains(out.String(), "Pick") || !strings.Contains(out.String(), "1. a") || !strings.Contains(out.String(), "2. b") {
		t.Fatal(out.String())
	}
}

func TestCLIAskMultiAndFreeText(t *testing.T) {
	c := &CLIAsk{In: bufio.NewReader(strings.NewReader("1, 3\n")), Out: io.Discard}
	ans, _ := c.Ask(context.Background(), tools.Question{Text: "Pick", Choices: []string{"a", "b", "c"}, Multi: true})
	if !reflect.DeepEqual(ans.Choices, []string{"a", "c"}) {
		t.Fatal(ans)
	}
	c = &CLIAsk{In: bufio.NewReader(strings.NewReader("\nsomething else\n")), Out: io.Discard}
	ans, _ = c.Ask(context.Background(), tools.Question{Text: "Pick", Choices: []string{"a", "b"}})
	if ans.Text != "something else" || len(ans.Choices) != 0 {
		t.Fatal(ans)
	}
	c = &CLIAsk{In: bufio.NewReader(strings.NewReader("blue\n")), Out: io.Discard}
	ans, _ = c.Ask(context.Background(), tools.Question{Text: "Color?"})
	if ans.Text != "blue" {
		t.Fatal(ans)
	}
	// A number out of range is a free text answer.
	c = &CLIAsk{In: bufio.NewReader(strings.NewReader("7\n")), Out: io.Discard}
	ans, _ = c.Ask(context.Background(), tools.Question{Text: "Pick", Choices: []string{"a", "b"}})
	if ans.Text != "7" || len(ans.Choices) != 0 {
		t.Fatal(ans)
	}
}

func TestCLIAskTimeoutAndEOF(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	c := &CLIAsk{In: bufio.NewReader(r), Out: io.Discard}
	if _, err := c.Ask(context.Background(), tools.Question{Text: "x", Timeout: 30 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	c = &CLIAsk{In: bufio.NewReader(strings.NewReader("")), Out: io.Discard}
	if _, err := c.Ask(context.Background(), tools.Question{Text: "x"}); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestCLIAskOneQuestionAtATime(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	c := &CLIAsk{In: bufio.NewReader(r), Out: io.Discard}
	var wg sync.WaitGroup
	answers := make(chan string, 2)
	for _, q := range []string{"q1", "q2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _ := c.Ask(context.Background(), tools.Question{Text: q})
			answers <- q + "=" + a.Text
		}()
	}
	_, _ = io.WriteString(w, "first\n")
	_, _ = io.WriteString(w, "second\n")
	wg.Wait()
	close(answers)
	var got []string
	for a := range answers {
		got = append(got, a)
	}
	// Each question gets one whole line.
	if len(got) != 2 || strings.Contains(got[0], "=first") == strings.Contains(got[1], "=first") {
		t.Fatal(got)
	}
}

// approvalDeps returns test deps with the terminal tool behind a gate that asks the CLI.
func approvalDeps(t *testing.T, fp *fakeProv) *Deps {
	t.Helper()
	d := testDeps(t, fp)
	d.Agent.Tools.Register(&tools.TerminalTool{DefaultTimeout: time.Minute})
	ask := &CLIAsk{}
	gate := &security.Gate{Mode: "manual"}
	d.CLIAsk = ask
	d.NewEnv = func(_ *sessions.Session, channel string) *tools.Env {
		return &tools.Env{Channel: channel, Gate: tools.GateApprover{Gate: gate, Prompter: ask}, Ask: ask, Values: map[string]any{}}
	}
	return d
}

func TestCLIApprovalPromptSharesInputWithREPL(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": "rm -rf " + marker})
	fp := &fakeProv{script: []step{
		{calls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: string(args)}}},
		{text: "removed"},
		{text: "second turn"},
	}}
	d := approvalDeps(t, fp)
	out := runCLI(t, d, "clean up\no\nnext question\n/exit\n")
	if !strings.Contains(out, "⚠ Approval needed\n  ") || !strings.Contains(out, "\n  Flagged: ") ||
		!strings.Contains(out, "[o]nce / [s]ession / [a]lways / [d]eny: ") || !strings.Contains(out, "✅ Approval allowed · once\n") {
		t.Fatal(out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the approved command did not run", out)
	}
	// The answer "o" went to the prompt, and the next line went to the REPL.
	if got := fp.lastUserTexts(); len(got) != 3 || got[2] != "next question" {
		t.Fatal(got)
	}
}

func TestCLIApprovalDenyBlocksCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"command": "rm -rf " + marker})
	fp := &fakeProv{script: []step{
		{calls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: string(args)}}},
		{text: "ok, I stop"},
	}}
	d := approvalDeps(t, fp)
	out := runCLI(t, d, "clean up\nd\n/exit\n")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the denied command ran", out)
	}
	fp.mu.Lock()
	last := fp.reqs[len(fp.reqs)-1].Messages
	fp.mu.Unlock()
	if tool := last[len(last)-1]; tool.Role != provider.RoleTool || !strings.Contains(tool.Content, "BLOCKED by user") {
		t.Fatalf("%+v", tool)
	}
}

func TestCLICtrlCDuringApprovalStopsTurn(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "rm -rf /tmp/tether-never"})
	fp := &fakeProv{script: []step{{calls: []provider.ToolCall{{ID: "c1", Name: "terminal", Args: string(args)}}}}}
	d := approvalDeps(t, fp)
	r, w := io.Pipe()
	ints := make(chan os.Signal, 1)
	var out syncBuffer
	c := &CLI{In: r, Out: &out, Key: "cli:test", Deps: d, Interrupts: ints}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	_, _ = io.WriteString(w, "clean up\n")
	waitFor(t, func() bool { return strings.Contains(out.String(), "[d]eny: ") })
	ints <- os.Interrupt
	waitFor(t, func() bool { return strings.Contains(out.String(), StoppedReply) })
	_, _ = io.WriteString(w, "/exit\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.Close()
}

// syncBuffer is a bytes.Buffer that is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A bidi rune or an ESC byte in the command must not reach the terminal.
func TestCLIPromptShowsHiddenRunesInCommand(t *testing.T) {
	var out bytes.Buffer
	ask := &CLIAsk{In: bufio.NewReader(strings.NewReader("d\n")), Out: &out}
	req := security.ApprovalRequest{Command: "rm -rf /tmp/\u202ex\x1b[2K", Description: "recursive delete"}
	if _, err := ask.PromptApproval(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.ContainsAny(s, "\u202e\x1b") || !strings.Contains(s, "  rm -rf /tmp/<U+202E>x<U+001B>[2K\n") {
		t.Fatalf("%q", s)
	}
}

func TestCLIPromptNoAlways(t *testing.T) {
	req := security.ApprovalRequest{Command: "go test ./...", Description: "claude-code wants to run a command",
		Summary: "Run the tests", NoAlways: true}
	const head = "⚠ Approval needed\n  Run the tests\n  Flagged: claude-code wants to run a command\n  go test ./...\n"
	const p = "[o]nce / [s]ession / [d]eny: "
	for _, c := range []struct {
		in   string
		want security.Scope
		out  string
	}{
		{"o\n", security.ScopeOnce, head + p + "✅ Approval allowed · once\n"},
		{"s\n", security.ScopeSession, head + p + "✅ Approval allowed · this session\n"},
		{"d\n", security.ScopeDeny, head + p + "❌ Approval denied\n"},
		{"a\ns\n", security.ScopeSession, head + p + "Type o, s, or d.\n" + p + "✅ Approval allowed · this session\n"},
		{"always\nd\n", security.ScopeDeny, head + p + "Type o, s, or d.\n" + p + "❌ Approval denied\n"},
		{"", security.ScopeDeny, head + p + "\n❌ Approval denied\n"},
	} {
		var out bytes.Buffer
		ask := &CLIAsk{In: bufio.NewReader(strings.NewReader(c.in)), Out: &out}
		got, err := ask.PromptApproval(context.Background(), req)
		if got != c.want || err != nil {
			t.Fatalf("%q: got %v %v, want %v", c.in, got, err, c.want)
		}
		if out.String() != c.out {
			t.Fatalf("%q: got %q, want %q", c.in, out.String(), c.out)
		}
	}
}

func TestCLIPromptNoSession(t *testing.T) {
	req := security.ApprovalRequest{Command: "1. Add the cache.\n2. Run the tests.",
		Description: "claude-code wants to leave plan mode and start to code", Summary: "Ready to code?",
		NoAlways: true, NoSession: true}
	const head = "⚠ Approval needed\n  Ready to code?\n  Flagged: claude-code wants to leave plan mode and start to code\n" +
		"  1. Add the cache.\n  2. Run the tests.\n"
	const p = "[o]nce / [d]eny: "
	for _, c := range []struct {
		in   string
		want security.Scope
		out  string
	}{
		{"o\n", security.ScopeOnce, head + p + "✅ Approval allowed · once\n"},
		{"d\n", security.ScopeDeny, head + p + "❌ Approval denied\n"},
		{"s\na\no\n", security.ScopeOnce, head + p + "Type o or d.\n" + p + "Type o or d.\n" + p + "✅ Approval allowed · once\n"},
	} {
		var out bytes.Buffer
		ask := &CLIAsk{In: bufio.NewReader(strings.NewReader(c.in)), Out: &out}
		got, err := ask.PromptApproval(context.Background(), req)
		if got != c.want || err != nil {
			t.Fatalf("%q: got %v %v, want %v", c.in, got, err, c.want)
		}
		if out.String() != c.out {
			t.Fatalf("%q: got %q, want %q", c.in, out.String(), c.out)
		}
	}
	// NoSession wins also when NoAlways is false.
	req.NoAlways = false
	var out bytes.Buffer
	ask := &CLIAsk{In: bufio.NewReader(strings.NewReader("a\nd\n")), Out: &out}
	if got, _ := ask.PromptApproval(context.Background(), req); got != security.ScopeDeny ||
		!strings.HasSuffix(out.String(), p+"Type o or d.\n"+p+"❌ Approval denied\n") {
		t.Fatalf("got %v, %q", got, out.String())
	}
}

func TestCLIPromptDefaultRetryLine(t *testing.T) {
	var out bytes.Buffer
	ask := &CLIAsk{In: bufio.NewReader(strings.NewReader("x\na\n")), Out: &out}
	got, _ := ask.PromptApproval(context.Background(), security.ApprovalRequest{Command: "rm -rf /tmp/x", Description: "recursive delete"})
	if got != security.ScopeAlways || !strings.Contains(out.String(), ApprovalPrompt+"Type o, s, a, or d.\n"+ApprovalPrompt) {
		t.Fatalf("got %v, %q", got, out.String())
	}
}
