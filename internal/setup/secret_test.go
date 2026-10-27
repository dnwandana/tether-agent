package setup

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeTerm is a terminal seam that records the echo changes.
type fakeTerm struct {
	echoOffs, restores, stops int
	failEchoOff               bool
	sigs                      chan os.Signal
}

func (f *fakeTerm) ops() *termOps {
	f.sigs = make(chan os.Signal, 1)
	return &termOps{
		isTerminal: func(int) bool { return true },
		echoOff: func(int) (func() error, error) {
			if f.failEchoOff {
				return nil, errors.New("no termios")
			}
			f.echoOffs++
			return func() error { f.restores++; return nil }, nil
		},
		interrupts: func() (<-chan os.Signal, func()) { return f.sigs, func() { f.stops++ } },
	}
}

// pipe returns the two ends of an OS pipe. A pipe is not a terminal.
func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

// secretWizard returns a wizard with a fake terminal over in.
func secretWizard(t *testing.T, in *bufio.Reader) (*Wizard, *fakeTerm, *strings.Builder) {
	t.Helper()
	r, _ := pipe(t)
	f := &fakeTerm{}
	out := &strings.Builder{}
	return &Wizard{In: in, Out: out, Term: r, term: f.ops()}, f, out
}

func TestAskSecretNotTerminalReadsAsBefore(t *testing.T) {
	r, _ := pipe(t)
	out := &strings.Builder{}
	w := &Wizard{In: answers("sk-1"), Out: out, Term: r}
	got, err := w.askSecret(ctx, "API key")
	if err != nil || got != "sk-1" {
		t.Fatal(got, err)
	}
	if out.String() != "API key: " {
		t.Fatalf("%q", out.String())
	}
	// Without Term, the prompt is the same.
	w = &Wizard{In: answers("sk-2"), Out: out}
	if got, err := w.askSecret(ctx, "API key"); err != nil || got != "sk-2" {
		t.Fatal(got, err)
	}
}

func TestAskSecretHidesAndRestores(t *testing.T) {
	w, f, out := secretWizard(t, answers("  sk-1  "))
	got, err := w.askSecret(ctx, "API key")
	if err != nil || got != "sk-1" {
		t.Fatal(got, err)
	}
	if f.echoOffs != 1 || f.restores != 1 || f.stops != 1 {
		t.Fatalf("%+v", f)
	}
	if out.String() != "API key (the input is hidden): \n" {
		t.Fatalf("%q", out.String())
	}
}

func TestAskSecretRestoresAtEndOfInput(t *testing.T) {
	w, f, out := secretWizard(t, answers())
	got, err := w.askSecret(ctx, "Bot token")
	if err != nil || got != "" || !w.eof {
		t.Fatal(got, err, w.eof)
	}
	if f.restores != 1 || !strings.HasSuffix(out.String(), ": \n") {
		t.Fatalf("%+v %q", f, out.String())
	}
	// After the end of the input, the next prompt does not touch the terminal.
	if _, err := w.askSecret(ctx, "Bot token"); err != nil || f.echoOffs != 1 {
		t.Fatal(err, f)
	}
}

func TestAskSecretRestoresOnInterrupt(t *testing.T) {
	blocked, _ := pipe(t)
	w, f, out := secretWizard(t, bufio.NewReader(blocked))
	f.sigs <- os.Interrupt
	_, err := w.askSecret(ctx, "API key")
	if !errors.Is(err, errInterrupted) {
		t.Fatal(err)
	}
	if f.restores != 1 || f.stops != 1 || !strings.HasSuffix(out.String(), ": \n") {
		t.Fatalf("%+v %q", f, out.String())
	}
}

func TestAskSecretRestoresOnCancel(t *testing.T) {
	blocked, _ := pipe(t)
	w, f, _ := secretWizard(t, bufio.NewReader(blocked))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.askSecret(cctx, "API key"); !errors.Is(err, errInterrupted) {
		t.Fatal(err)
	}
	if f.restores != 1 {
		t.Fatalf("%+v", f)
	}
}

func TestAskSecretEchoOffFailsReadsVisible(t *testing.T) {
	w, f, _ := secretWizard(t, answers("sk-1"))
	f.failEchoOff = true
	if got, err := w.askSecret(ctx, "API key"); err != nil || got != "sk-1" {
		t.Fatal(got, err)
	}
	if f.restores != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestEchoOffOnPipeFails(t *testing.T) {
	r, _ := pipe(t)
	if isTerminal(int(r.Fd())) {
		t.Fatal("a pipe is a terminal")
	}
	if _, err := echoOff(int(r.Fd())); err == nil {
		t.Fatal("echoOff on a pipe works")
	}
}

// TestWizardHidesKeyAndToken checks that the wizard hides the same prompts as Hermes: the
// API key and the bot token. The other answers stay visible.
func TestWizardHidesKeyAndToken(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL+"/v1", "sk-test-123", "glm-4.7", "n", "n"))
	r, _ := pipe(t)
	f := &fakeTerm{}
	w.Term, w.term = r, f.ops()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if f.echoOffs != 1 || f.restores != 1 {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(out.String(), "API key (or ${NAME} for a variable in .env) (the input is hidden): \n") ||
		strings.Contains(out.String(), "Model (the input is hidden)") {
		t.Fatal(out.String())
	}
	if loadCfg(t, dir).Providers["primary"].APIKey != "sk-test-123" {
		t.Fatal("the key is not saved")
	}
}
