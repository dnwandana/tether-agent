package dashboard

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// skillsAsker is a tools.Asker that records each question. A non-nil block holds each answer
// until the channel closes.
type skillsAsker struct {
	mu     sync.Mutex
	qs     []tools.Question
	answer tools.Answer
	block  chan struct{}
}

func (a *skillsAsker) Ask(ctx context.Context, q tools.Question) (tools.Answer, error) {
	a.mu.Lock()
	a.qs = append(a.qs, q)
	a.mu.Unlock()
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return tools.Answer{}, ctx.Err()
		}
	}
	return a.answer, nil
}

func (a *skillsAsker) questions() []tools.Question {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.qs)
}

// taskRecorder is a TaskHandle that keeps the waiting changes and the last code.
type taskRecorder struct {
	mu    sync.Mutex
	waits []bool
	code  string
}

func (r *taskRecorder) Progress(string)     {}
func (r *taskRecorder) Backup(BackupReport) {}
func (r *taskRecorder) Waiting(on bool)     { r.mu.Lock(); r.waits = append(r.waits, on); r.mu.Unlock() }
func (r *taskRecorder) Code(code string)    { r.mu.Lock(); r.code = code; r.mu.Unlock() }

func TestInstallPrompterConfirmAndAsk(t *testing.T) {
	var report bytes.Buffer
	report.WriteString("Skill: pdf — Read PDF files.\n")
	ask := &skillsAsker{answer: tools.Answer{Choices: []string{"Yes"}}}
	h := &taskRecorder{}
	p := newInstallPrompter(context.Background(), ask, &report, "pdf", h)
	if !p.Terminal() {
		t.Fatal("the prompter must say Terminal, so the installer asks it")
	}
	ok, err := p.Confirm(`Install "pdf"?`)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	q := ask.questions()[0]
	if q.Text != "Skill: pdf — Read PDF files.\n\nInstall \"pdf\"?" || !slices.Equal(q.Choices, []string{"No", "Yes"}) ||
		q.Multi || q.Timeout != 5*time.Minute {
		t.Fatalf("%+v", q)
	}
	if !slices.Equal(h.waits, []bool{true, false}) {
		t.Fatalf("Confirm waits for the card and then stops: %v", h.waits)
	}
	ask.answer = tools.Answer{Choices: []string{"No"}}
	if ok, err := p.Confirm(`Install "pdf"?`); err != nil || ok {
		t.Fatalf("No must be no: %v %v", ok, err)
	}
	_, err = p.Ask("Type the skill name to install it anyway:")
	want := "The guard rates this skill dangerous, so the dashboard does not install it. " +
		"Run this command in a terminal on the host: tether skills install pdf"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if h.code != "need_override" {
		t.Fatalf("a dangerous refusal sets need_override: %q", h.code)
	}
	if len(ask.questions()) != 2 {
		t.Fatal("Ask must send no card")
	}
}
