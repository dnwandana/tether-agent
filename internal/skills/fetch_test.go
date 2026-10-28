package skills

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// gitEnv isolates the helper git calls from the git config of the user, so the
// tests do not depend on a global name, email, or signing setting.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

// gitT runs a helper git command in dir and returns the trimmed stdout.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("git %v: %v\n%s", args, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// makeRemote makes a bare repo whose default branch is branch, with two
// commits and an annotated tag "v1" on the first commit. The first commit adds
// SKILL.md with the frontmatter name "pdf" at the repo root. The second commit
// adds a second file. It returns the bare repo path and the two commit SHAs.
func makeRemote(t *testing.T, branch string) (url string, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "remote.git")
	gitT(t, root, "init", "--quiet", "--initial-branch="+branch, work)
	skill := "---\nname: pdf\ndescription: Read PDF files.\n---\n# PDF\n"
	if err := os.WriteFile(filepath.Join(work, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, work, "add", "SKILL.md")
	gitT(t, work, "commit", "--quiet", "--no-gpg-sign", "-m", "first")
	first = gitT(t, work, "rev-parse", "HEAD")
	gitT(t, work, "tag", "-a", "-m", "version 1", "v1")
	if err := os.WriteFile(filepath.Join(work, "notes.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, work, "add", "notes.md")
	gitT(t, work, "commit", "--quiet", "--no-gpg-sign", "-m", "second")
	second = gitT(t, work, "rev-parse", "HEAD")
	gitT(t, root, "init", "--quiet", "--bare", "--initial-branch="+branch, bare)
	gitT(t, work, "push", "--quiet", bare, branch+":"+branch, "refs/tags/v1:refs/tags/v1")
	return bare, first, second
}

// testGit returns a Git that allows only the "file" transport, so the tests
// can use a local bare repo. It also isolates git from the config of the user.
func testGit(t *testing.T) Git {
	t.Helper()
	g, err := FindGit()
	if err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	g.Protocols = []string{"file"}
	return g
}

func TestCloneDefaultBranchNotMain(t *testing.T) {
	url, _, second := makeRemote(t, "trunk")
	dst := filepath.Join(t.TempDir(), "repo")
	commit, err := testGit(t).Clone(t.Context(), url, "", dst)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if commit != second {
		t.Fatalf("commit = %q, want %q", commit, second)
	}
	if _, err := os.Stat(filepath.Join(dst, "notes.md")); err != nil {
		t.Fatalf("notes.md is missing: %v", err)
	}
}

func TestCloneRefTagAndSHA(t *testing.T) {
	url, first, _ := makeRemote(t, "main")
	g := testGit(t)

	dst1 := filepath.Join(t.TempDir(), "repo")
	commit, err := g.Clone(t.Context(), url, "v1", dst1)
	if err != nil {
		t.Fatalf("Clone tag: %v", err)
	}
	if commit != first {
		t.Fatalf("tag commit = %q, want %q", commit, first)
	}

	dst2 := filepath.Join(t.TempDir(), "repo")
	commit, err = g.Clone(t.Context(), url, first, dst2)
	if err != nil {
		t.Fatalf("Clone SHA: %v", err)
	}
	if commit != first {
		t.Fatalf("SHA commit = %q, want %q", commit, first)
	}
	if _, err := os.Stat(filepath.Join(dst2, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md is missing after the SHA clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst2, "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("notes.md exists after the SHA clone of the first commit: %v", err)
	}

	head, err := g.HeadCommit(t.Context(), dst2)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if head != first {
		t.Fatalf("HeadCommit = %q, want %q", head, first)
	}
}

func TestCloneUnknownSHAHint(t *testing.T) {
	url, _, _ := makeRemote(t, "main")
	missing := strings.Repeat("ab", 20)
	_, err := testGit(t).Clone(t.Context(), url, missing, filepath.Join(t.TempDir(), "repo"))
	if err == nil {
		t.Fatal("Clone of an unknown SHA: want an error")
	}
	if !strings.Contains(err.Error(), "use a branch or a tag with --ref") {
		t.Fatalf("error = %q, want the --ref hint", err)
	}
}

func TestCloneRefusesBlockedProtocol(t *testing.T) {
	url, _, _ := makeRemote(t, "main")
	g := testGit(t)
	g.Protocols = defaultProtocols

	_, err := g.Clone(t.Context(), url, "", filepath.Join(t.TempDir(), "repo"))
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("Clone of a local path with the default protocols: err = %v, want *GitError", err)
	}
	if !strings.Contains(ge.Stderr, "not allowed") {
		t.Fatalf("Clone of a local path: stderr = %q, want a transport refusal", ge.Stderr)
	}

	marker := filepath.Join(t.TempDir(), "pwned")
	_, err = g.Clone(t.Context(), "ext::sh -c touch% "+marker, "", filepath.Join(t.TempDir(), "repo"))
	if !errors.As(err, &ge) || !strings.Contains(ge.Stderr, "not allowed") {
		t.Fatalf("Clone of an ext:: URL: err = %v, want a transport refusal", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the ext:: URL ran a command: %v", statErr)
	}
}

func TestRemoteCommit(t *testing.T) {
	url, first, second := makeRemote(t, "trunk")
	g := testGit(t)

	got, err := g.RemoteCommit(t.Context(), url, "")
	if err != nil {
		t.Fatalf("RemoteCommit HEAD: %v", err)
	}
	if got != second {
		t.Fatalf("RemoteCommit HEAD = %q, want %q", got, second)
	}

	got, err = g.RemoteCommit(t.Context(), url, "v1")
	if err != nil {
		t.Fatalf("RemoteCommit v1: %v", err)
	}
	if got != first {
		t.Fatalf("RemoteCommit v1 = %q, want %q (the peeled commit)", got, first)
	}

	_, err = g.RemoteCommit(t.Context(), url, "nope")
	if err == nil || !strings.Contains(err.Error(), `ref "nope" not found`) {
		t.Fatalf("RemoteCommit nope: err = %v, want ref not found", err)
	}

	// refs/heads/feature/trunk sorts before refs/heads/trunk and also ends
	// with "trunk", but it is not the branch trunk.
	gitT(t, url, "branch", "feature/trunk", first)
	got, err = g.RemoteCommit(t.Context(), url, "trunk")
	if err != nil {
		t.Fatalf("RemoteCommit trunk: %v", err)
	}
	if got != second {
		t.Fatalf("RemoteCommit trunk = %q, want %q", got, second)
	}
}

func TestGitTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	script := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := Git{Path: script, Timeout: 100 * time.Millisecond, Protocols: defaultProtocols}
	start := time.Now()
	_, err := g.Clone(t.Context(), "https://example.com/o/r", "", filepath.Join(t.TempDir(), "repo"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Clone took %v, want under 2s", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
}

func TestFindGitMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := FindGit()
	if !errors.Is(err, ErrNoGit) {
		t.Fatalf("err = %v, want ErrNoGit", err)
	}
}

func TestIsCommitSHA(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a1", 20):            true,
		strings.Repeat("A1", 20):            false,
		strings.Repeat("a", 39):             false,
		strings.Repeat("a", 41):             false,
		strings.Repeat("g", 40):             false,
		"main":                              false,
		"":                                  false,
		"v1.0.0":                            false,
		strings.Repeat("0123456789", 4):     true,
		strings.Repeat("0123456789", 4)[1:]: false,
	}
	for ref, want := range cases {
		if got := IsCommitSHA(ref); got != want {
			t.Errorf("IsCommitSHA(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestGitErrorText(t *testing.T) {
	cases := []struct {
		err  *GitError
		want string
	}{
		{
			&GitError{Op: "clone", Stderr: "Cloning into 'repo'...\nremote: Repository not found.\nfatal: repository 'https://github.com/o/r/' not found\n"},
			"git clone failed: repository not found or no access: Cloning into 'repo'... / remote: Repository not found. / fatal: repository 'https://github.com/o/r/' not found",
		},
		{
			&GitError{Op: "ls-remote", Stderr: "a\nb\nc\nfatal: Authentication failed for 'x'\n"},
			"git ls-remote failed: repository not found or no access: b / c / fatal: Authentication failed for 'x'",
		},
		{
			&GitError{Op: "fetch", Stderr: "fatal: bad object\x1b[31m red\n"},
			"git fetch failed: see the git output: fatal: bad object red",
		},
		{
			&GitError{Op: "init"},
			"git init failed: see the git output",
		},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() =\n  %q\nwant\n  %q", got, c.want)
		}
	}
}
