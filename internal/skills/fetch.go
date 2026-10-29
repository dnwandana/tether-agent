package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/hostos"
)

// ErrNoGit tells that the git program is not on PATH. FindGit gives it with the
// install hint for the current system.
var ErrNoGit = errors.New("the git program is not on PATH")

// noGitError returns ErrNoGit with the install hint for the current system. The
// CLI prints the text as two full sentences, so it ends with a period.
func noGitError() error {
	//lint:ignore ST1005 the text is a fixed user message with two sentences.
	return fmt.Errorf("%w. %s, or give a local folder.", ErrNoGit, installGit())
}

// installGit returns the install hint for git as the start of a sentence.
func installGit() string {
	return "Install" + strings.TrimPrefix(hostos.Hint("git"), "install")
}

// gitTimeout is the time limit for each git call.
const gitTimeout = 120 * time.Second

// gitWaitDelay is the time that a stopped git call gets to close its output.
// A child of git can keep the pipes open after git stops.
const gitWaitDelay = 500 * time.Millisecond

// defaultProtocols are the git transports that production code allows. The
// other transports, for example "ext" and "file", stay blocked, because an
// "ext::" URL runs a shell command.
var defaultProtocols = []string{"https", "ssh", "git"}

// Git runs the external git program with fixed hardening flags. The hardening
// comes from vercel-labs/skills src/git.ts.
type Git struct {
	Path      string
	Timeout   time.Duration
	Protocols []string
}

// FindGit returns a Git for the git program on PATH, with the production
// timeout and protocols. When git is not on PATH, it returns an error that
// wraps ErrNoGit.
func FindGit() (Git, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return Git{}, noGitError()
	}
	return Git{Path: p, Timeout: gitTimeout, Protocols: defaultProtocols}, nil
}

// IsCommitSHA reports whether ref is a full commit SHA: exactly 40 lower-case
// hex characters.
func IsCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// GitError is a failed git call. Op is the git command, for example "clone".
// Stderr is the error output of git.
type GitError struct {
	Op     string
	Stderr string

	// after is the time limit when the call stopped on the timeout.
	after time.Duration
	// hint is an extra next action for the owner.
	hint string
}

// Error returns a one-line text with a short cause and the last three lines
// of the git output. The text goes through Sanitize, because the output comes
// from a remote.
func (e *GitError) Error() string {
	var b strings.Builder
	if e.after > 0 {
		fmt.Fprintf(&b, "git %s stopped after %s", e.Op, formatLimit(e.after))
	} else {
		fmt.Fprintf(&b, "git %s failed: %s", e.Op, e.cause())
		if tail := lastLines(e.Stderr, 3); tail != "" {
			b.WriteString(": ")
			b.WriteString(tail)
		}
	}
	if e.hint != "" {
		b.WriteString("; ")
		b.WriteString(e.hint)
	}
	return Sanitize(b.String())
}

// cause returns a short cause from the git output.
func (e *GitError) cause() string {
	for _, s := range []string{"not found", "Authentication failed", "Could not read from remote"} {
		if strings.Contains(e.Stderr, s) {
			return "repository not found or no access"
		}
	}
	return "see the git output"
}

// formatLimit gives "120s" for a whole number of seconds, else the Go
// duration text.
func formatLimit(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// lastLines returns the last n lines of s that are not blank, joined with " / ".
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

// flags returns the -c flags that go before each git command. Only the
// transports in g.Protocols are allowed. Git LFS and hooks are off.
func (g Git) flags() []string {
	f := []string{"-c", "protocol.allow=never"}
	for _, p := range g.Protocols {
		f = append(f, "-c", "protocol."+p+".allow=always")
	}
	return append(f,
		"-c", "filter.lfs.required=false",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
		"-c", "filter.lfs.process=",
		"-c", "core.hooksPath=/dev/null",
	)
}

// run runs one git command with the hardening flags and the time limit, and
// returns its stdout. When dir is not empty, git runs with -C dir. It never
// uses a shell. A failure is a *GitError.
func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = gitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := g.flags()
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, g.Path, full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	cmd.WaitDelay = gitWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	op := ""
	if len(args) > 0 {
		op = args[0]
	}
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", &GitError{Op: op, Stderr: stderr.String(), after: timeout}
	}
	if err != nil {
		stderrText := stderr.String()
		if stderrText == "" {
			stderrText = err.Error()
		}
		return "", &GitError{Op: op, Stderr: stderrText}
	}
	return stdout.String(), nil
}

// Clone fetches url into dst and returns the commit of the work tree. An
// empty ref gives the default branch of the remote. A branch or a tag gives
// one shallow clone. A full commit SHA cannot go to clone --branch, so it
// gives init, fetch of that SHA, and checkout of FETCH_HEAD.
func (g Git) Clone(ctx context.Context, url, ref, dst string) (string, error) {
	if IsCommitSHA(ref) {
		if _, err := g.run(ctx, "", "init", "--quiet", "--", dst); err != nil {
			return "", err
		}
		if _, err := g.run(ctx, dst, "fetch", "--quiet", "--depth", "1", "--", url, ref); err != nil {
			var ge *GitError
			if errors.As(err, &ge) && ge.after == 0 && ge.cause() != "repository not found or no access" {
				ge.hint = "the server can refuse a fetch by commit SHA, use a branch or a tag with --ref"
			}
			return "", err
		}
		if _, err := g.run(ctx, dst, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
			return "", err
		}
	} else {
		args := []string{"clone", "--quiet", "--depth", "1", "--single-branch", "--no-recurse-submodules"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, "--", url, dst)
		if _, err := g.run(ctx, "", args...); err != nil {
			return "", err
		}
	}
	return g.HeadCommit(ctx, dst)
}

// RemoteCommit returns the commit that ref points to on the remote, without
// a fetch. An empty ref means HEAD, the default branch. For an annotated tag
// it returns the commit of the tag, not the tag object.
func (g Git) RemoteCommit(ctx context.Context, url, ref string) (string, error) {
	pattern := ref
	if pattern == "" {
		pattern = "HEAD"
	}
	args := []string{"ls-remote", "--", url, pattern}
	if ref != "" {
		// The pattern "v1" does not match the peeled line "refs/tags/v1^{}".
		args = append(args, ref+"^{}")
	}
	out, err := g.run(ctx, "", args...)
	if err != nil {
		return "", err
	}
	// ls-remote matches a pattern against the end of each ref name, so "main"
	// also matches refs/heads/feature/main. Keep only the exact names, in the
	// order that clone --branch uses: a branch before a tag.
	byName := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		hash, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && hash != "" {
			byName[name] = hash
		}
	}
	for _, name := range []string{"refs/heads/" + pattern, "refs/tags/" + pattern + "^{}", "refs/tags/" + pattern, pattern + "^{}", pattern} {
		if hash, ok := byName[name]; ok {
			return hash, nil
		}
	}
	return "", fmt.Errorf("ref %q not found", Sanitize(pattern))
}

// HeadCommit returns the commit of HEAD in the git work tree at dir.
func (g Git) HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := g.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
