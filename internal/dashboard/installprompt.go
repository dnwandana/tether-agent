package dashboard

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// installConfirmTimeout is the time that the owner has to answer a confirm card.
const installConfirmTimeout = 5 * time.Minute

// installPrompter is the skills.Prompter of a skill action from the dashboard. Its Confirm
// opens a clarify card with the installer report. Its Ask refuses, so a skill with a dangerous
// verdict installs only from a terminal.
type installPrompter struct {
	ctx    context.Context
	ask    tools.Asker
	report *bytes.Buffer // the installer output so far
	name   string
}

func newInstallPrompter(ctx context.Context, ask tools.Asker, report *bytes.Buffer, name string) *installPrompter {
	return &installPrompter{ctx: ctx, ask: ask, report: report, name: name}
}

// Terminal returns true, so the installer asks this prompter and does not refuse at once.
func (p *installPrompter) Terminal() bool { return true }

// Confirm opens a card with the report and the question. Only the answer Yes is a yes.
func (p *installPrompter) Confirm(question string) (bool, error) {
	ans, err := p.ask.Ask(p.ctx, tools.Question{
		Text:    skills.Sanitize(p.report.String()) + "\n" + question,
		Choices: []string{"No", "Yes"},
		Timeout: installConfirmTimeout,
	})
	if err != nil {
		return false, err
	}
	return len(ans.Choices) == 1 && ans.Choices[0] == "Yes", nil
}

// Ask refuses. It sends no card.
func (p *installPrompter) Ask(string) (string, error) {
	return "", errors.New("The guard rates this skill dangerous, so the dashboard does not install it. " +
		"Run this command in a terminal on the host: tether skills install " + p.name)
}
