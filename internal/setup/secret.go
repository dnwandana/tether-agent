package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// errInterrupted is the error when the user stops the wizard during a hidden prompt.
var errInterrupted = errors.New("setup: the setup was stopped; run tether setup again")

// termOps is the terminal access of the hidden input. Tests replace it.
type termOps struct {
	isTerminal func(fd int) bool
	// echoOff turns off the echo and returns the function that restores the terminal.
	echoOff func(fd int) (func() error, error)
	// interrupts returns a channel that gets SIGINT and SIGTERM, and the function that
	// stops the channel.
	interrupts func() (<-chan os.Signal, func())
}

// osTerm is the real terminal access.
var osTerm = termOps{
	isTerminal: isTerminal,
	echoOff:    echoOff,
	interrupts: func() (<-chan os.Signal, func()) {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		return ch, func() { signal.Stop(ch) }
	},
}

func (w *Wizard) termOps() termOps {
	if w.term != nil {
		return *w.term
	}
	return osTerm
}

// askSecret asks for a secret such as an API key or a bot token. When Term is a terminal,
// the echo is off while the user types, and the wizard prints the newline after the input.
// Otherwise askSecret reads the answer like ask. An empty answer gives "".
func (w *Wizard) askSecret(ctx context.Context, question string) (string, error) {
	ops := w.termOps()
	if w.Term == nil || w.eof || !ops.isTerminal(int(w.Term.Fd())) {
		return w.ask(question, ""), nil
	}
	fmt.Fprintf(w.Out, "%s (the input is hidden): ", question)
	restore, err := ops.echoOff(int(w.Term.Fd()))
	if err != nil {
		// The echo stays on. The input is the same, only visible.
		return w.readAnswer(""), nil
	}
	// The restore and the newline also run on an error or a panic.
	defer func() {
		_ = restore()
		fmt.Fprintln(w.Out)
	}()

	sigs, stop := ops.interrupts()
	defer stop()
	type result struct {
		line string
		err  error
	}
	// The read is in a goroutine, so a signal or the end of ctx can stop the wait. After
	// that stop the goroutine stays blocked, but the wizard ends with the error.
	got := make(chan result, 1)
	go func() {
		line, err := w.In.ReadString('\n')
		got <- result{line, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			w.eof = true
		}
		return strings.TrimSpace(r.line), nil
	case <-sigs:
		return "", errInterrupted
	case <-ctx.Done():
		return "", errInterrupted
	}
}
