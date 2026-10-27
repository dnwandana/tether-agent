// Package media holds the two backends of the media tools: a command
// template that runs a program on the host, and an OpenAI-format HTTP
// endpoint.
package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// DefaultCommandTimeout is the timeout of a media command.
const DefaultCommandTimeout = 120 * time.Second

const (
	// stderrLines is the number of stderr lines in a failure message.
	stderrLines = 20
	// stderrKeep bounds the stderr bytes that Run keeps in memory.
	stderrKeep = 64 << 10
	// killWait is the time that Run waits for the pipes after a kill.
	killWait = 2 * time.Second
)

// placeholders are the names that Expand replaces, as {name}.
var placeholders = []string{"text", "in", "out", "out_base", "prompt", "size"}

// installHints gives an install example for the default programs.
var installHints = map[string]string{
	"edge-tts":    "pipx install edge-tts",
	"whisper-cli": "build whisper.cpp, or brew install whisper-cpp",
	"ffmpeg":      "apt install ffmpeg, or brew install ffmpeg",
}

// SplitTemplate splits a command template into argv. It reads the quoting
// like a POSIX shell: single quotes, double quotes, and backslash. It does
// not expand variables, globs, or other shell syntax. An unquoted "~" or
// "~/" at the start of a word becomes the home folder.
func SplitTemplate(tpl string) ([]string, error) {
	var (
		argv  []string
		word  strings.Builder
		in    bool // a word has started
		tilde bool // the word starts with an unquoted "~"
		quote rune // 0, '\'', or '"'
		rs    = []rune(tpl)
	)
	end := func() {
		if in {
			argv = append(argv, expandTilde(word.String(), tilde))
		}
		word.Reset()
		in, tilde = false, false
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`\n", rs[i+1]):
				i++
				word.WriteRune(rs[i])
			default:
				word.WriteRune(r)
			}
		case r == ' ' || r == '\t' || r == '\n':
			end()
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("media command: the template ends with a backslash")
			}
			i++
			word.WriteRune(rs[i])
			in = true
		default:
			if !in && r == '~' {
				tilde = true
			}
			word.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("media command: the template has no closing %c quote", quote)
	}
	end()
	if len(argv) == 0 {
		return nil, errors.New("media command: the template is empty")
	}
	return argv, nil
}

// expandTilde replaces a leading "~" or "~/" with the home folder.
func expandTilde(w string, ok bool) string {
	if !ok || (w != "~" && !strings.HasPrefix(w, "~/")) {
		return w
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return w
	}
	if w == "~" {
		return home
	}
	return filepath.Join(home, w[2:])
}

// Expand returns a copy of argv with the placeholders {text}, {in}, {out},
// {out_base}, {prompt}, and {size} replaced by the values in vals. Each
// value stays inside its own word, and a value is never expanded again. A
// placeholder with no value in vals stays as it is.
func Expand(argv []string, vals map[string]string) []string {
	var pairs []string
	for _, name := range placeholders {
		if v, ok := vals[name]; ok {
			pairs = append(pairs, "{"+name+"}", v)
		}
	}
	out := make([]string, len(argv))
	if len(pairs) == 0 {
		copy(out, argv)
		return out
	}
	r := strings.NewReplacer(pairs...)
	for i, w := range argv {
		out[i] = r.Replace(w)
	}
	return out
}

// CommandBackend runs a media command template. WorkDir is cache/media/.
// A zero Timeout uses DefaultCommandTimeout. Env is the environment of the
// program. The caller filters it with security.ChildEnv, so Run uses it as
// given and the passthrough names stay. A nil Env gives os.Environ() without
// the secret names.
type CommandBackend struct {
	Template string
	WorkDir  string
	Timeout  time.Duration
	Env      []string
}

// Run expands the template with vals and runs the program directly, with no
// shell. It returns an error with the tail of stderr when the program fails.
func (b *CommandBackend) Run(ctx context.Context, vals map[string]string) error {
	argv, err := SplitTemplate(b.Template)
	if err != nil {
		return err
	}
	argv = Expand(argv, vals)
	prog := filepath.Base(argv[0])
	path, err := exec.LookPath(argv[0])
	if err != nil {
		hint := "install it"
		if h, ok := installHints[prog]; ok {
			hint = "install it (for example: " + h + ")"
		}
		return fmt.Errorf("%s not found; %s or change media.*.command", prog, hint)
	}
	if b.WorkDir != "" {
		if err := os.MkdirAll(b.WorkDir, 0o700); err != nil {
			return fmt.Errorf("media command: create work folder %s: %w", b.WorkDir, err)
		}
	}
	// A second ChildEnv would remove the passthrough names that the caller kept.
	env := b.Env
	if env == nil {
		env = security.ChildEnv(os.Environ(), nil, nil)
	}

	tctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	stderr := &tailWriter{}
	cmd := exec.CommandContext(tctx, path, argv[1:]...)
	cmd.Dir = b.WorkDir
	cmd.Env = env
	cmd.Stderr = stderr
	// A new process group lets a timeout stop the children of the program too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = killWait
	err = cmd.Run()
	if err == nil {
		return nil
	}
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%s stopped: %w", prog, ctx.Err())
	case tctx.Err() != nil:
		return fmt.Errorf("%s timed out after %s", prog, b.timeout())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s failed (exit %d): %s", prog, exit.ExitCode(), stderr.lastLines(stderrLines))
	}
	return fmt.Errorf("%s failed: %w", prog, err)
}

func (b *CommandBackend) timeout() time.Duration {
	if b.Timeout <= 0 {
		return DefaultCommandTimeout
	}
	return b.Timeout
}

// tailWriter keeps the last stderrKeep bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > stderrKeep {
		w.buf = w.buf[len(w.buf)-stderrKeep:]
	}
	return len(p), nil
}

// lastLines returns the last n lines, with no final newline.
func (w *tailWriter) lastLines(n int) string {
	w.mu.Lock()
	s := strings.TrimRight(string(w.buf), "\n")
	w.mu.Unlock()
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
