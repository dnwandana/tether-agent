package tools

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// recMarks records each mark and each clear.
type recMarks struct {
	mu              sync.Mutex
	marked, cleared []string
}

func (m *recMarks) Mark(_ context.Context, name string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marked = append(m.marked, name)
	return nil
}

func (m *recMarks) Clear(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleared = append(m.cleared, name)
	return nil
}

func (m *recMarks) get() (marked, cleared []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.marked), slices.Clone(m.cleared)
}

// fileScript prints the file <program>.out when it exists and exits with the number in
// <program>.code, or 0.
const fileScript = "[ -f \"$0.out\" ] && cat \"$0.out\"\nexit \"$(cat \"$0.code\" 2>/dev/null || echo 0)\"\n"

const markLine = "[PYPI_TOKEN is marked for a check: the command got an auth failure. " +
	"Do not run it again with this value. Ask the owner for a new value with request_secret.]"

func TestAuthFailureMarks(t *testing.T) {
	tool, twine := boundFixture(t, fileScript)
	marks := &recMarks{}
	tool.Marks = marks
	g, _, _ := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	set := func(out, code string) {
		t.Helper()
		if err := os.WriteFile(twine+".out", []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(twine+".code", []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set("HTTPError: 401 Unauthorized\n", "1")
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != "HTTPError: 401 Unauthorized\n[exit code: 1]\n"+markLine {
		t.Fatalf("got %q", out)
	}
	if m, c := marks.get(); !slices.Equal(m, []string{"PYPI_TOKEN"}) || len(c) != 0 {
		t.Fatalf("marked %q cleared %q", m, c)
	}
	// A transient failure changes nothing.
	set("HTTP 429 Too Many Requests: invalid request rate\n", "1")
	runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env)
	if m, c := marks.get(); len(m) != 1 || len(c) != 0 {
		t.Fatalf("marked %q cleared %q", m, c)
	}
	// A success clears the mark.
	set("uploaded\n", "0")
	if out := runToolEnv(t, tool, `{"command":"twine upload dist/x"}`, env); out != "uploaded\n" {
		t.Fatalf("got %q", out)
	}
	if _, c := marks.get(); !slices.Equal(c, []string{"PYPI_TOKEN"}) {
		t.Fatalf("cleared %q", c)
	}
	// A command that got no bound secret gets no mark.
	set("HTTP 401\n", "1")
	runToolEnv(t, tool, `{"command":"cat `+twine+`.out; exit 1"}`, env)
	if m, _ := marks.get(); len(m) != 1 {
		t.Fatalf("marked %q", m)
	}
}

func TestAuthFailureMarksBackground(t *testing.T) {
	tool, twine := boundFixture(t, fileScript)
	marks := &recMarks{}
	tool.Marks = marks
	g, _, _ := newPinGate(true, nil)
	env := newEnv(t)
	env.Gate = g
	if err := os.WriteFile(twine+".out", []byte("HTTP 401 Unauthorized\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(twine+".code", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	runToolEnv(t, tool, `{"command":"twine upload dist/x","background":true}`, env)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m, _ := marks.get(); slices.Equal(m, []string{"PYPI_TOKEN"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the exit of the background process must mark the secret")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
