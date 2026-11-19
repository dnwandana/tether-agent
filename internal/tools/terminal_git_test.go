package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// gitRepo makes a git repository with the files and returns its folder. It skips the test
// when no system git exists.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	git, ok := systemGit()
	if !ok {
		t.Skip("no git in /usr/bin or /bin")
	}
	dir := t.TempDir()
	if out, err := exec.Command(git, "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func staged(t *testing.T, cmd, dir string) ([]string, bool) {
	t.Helper()
	return StagedCredentials(context.Background(), security.ReadCommand(cmd), dir)
}

func TestStagedCredentialsBroadForms(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n",
		"deploy/server.pem": "x\n", "sub/.env.local": "B=2\n", "sub/readme.md": "x\n"})
	all := []string{".env", "deploy/server.pem", "sub/.env.local"}
	for cmd, want := range map[string][]string{
		"git add -A":       all,
		"git add .":        all,
		"git add --all":    all,
		"git add deploy":   {"deploy/server.pem"},
		"git add '*.pem'":  {"deploy/server.pem"},
		"git -C sub add .": {"sub/.env.local"}, // git lists the paths of the index
		"git add main.go":  nil,
		"echo git add -A":  nil,
		"git status":       nil,
	} {
		got, unchecked := staged(t, cmd, dir)
		if unchecked || !slices.Equal(got, want) {
			t.Errorf("%q: got %v %v, want %v", cmd, got, unchecked, want)
		}
	}
	got, unchecked := staged(t, "cd sub && git add -A", dir)
	if unchecked || !slices.ContainsFunc(got, func(p string) bool { return strings.HasSuffix(p, ".env.local") }) {
		t.Fatal(got, unchecked)
	}
}

func TestStagedCredentialsIgnoreRules(t *testing.T) {
	dir := gitRepo(t, map[string]string{".gitignore": ".env\n", ".env": "A=1\n", "main.go": "package main\n"})
	if got, _ := staged(t, "git add .", dir); len(got) != 0 {
		t.Fatal("an ignored file is not staged:", got)
	}
	if got, _ := staged(t, "git add -f .", dir); !slices.Equal(got, []string{".env"}) {
		t.Fatal(got)
	}
}

// The check never runs core.fsmonitor of .git/config, which the agent can write.
func TestStagedCredentialsIgnoresFSMonitor(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n"})
	marker := filepath.Join(t.TempDir(), "pwned")
	cfg := filepath.Join(dir, ".git", "config")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[core]\n\tfsmonitor = touch " + marker + "\n")
	f.Close()
	if got, _ := staged(t, "git add -A", dir); !slices.Equal(got, []string{".env"}) {
		t.Fatal(got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the check ran core.fsmonitor")
	}
}

func TestStagedCredentialsUnchecked(t *testing.T) {
	if got, unchecked := staged(t, "git add -A", t.TempDir()); got != nil || unchecked {
		t.Fatal("not a repo adds nothing:", got, unchecked)
	}
	for _, cmd := range []string{"cd $HOME && git add -A", "popd && git add .", "cd - && git add ."} {
		if _, unchecked := staged(t, cmd, t.TempDir()); !unchecked {
			t.Errorf("%q: an unknown folder must ask", cmd)
		}
	}
	oldRun, oldTimeout := gitRun, stageTimeout
	t.Cleanup(func() { gitRun, stageTimeout = oldRun, oldTimeout })
	stageTimeout = 20 * time.Millisecond
	gitRun = func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, unchecked := staged(t, "git add -A", t.TempDir()); !unchecked {
		t.Fatal("a timeout must ask")
	}
	gitRun = func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("fatal: bad index file"), errors.New("exit status 128")
	}
	if _, unchecked := staged(t, "git add .", t.TempDir()); !unchecked {
		t.Fatal("another git error must ask")
	}
}

// checkRecorder is an Approver that records each check and blocks the command.
type checkRecorder struct{ checks []CommandCheck }

func (r *checkRecorder) CheckCommand(_ context.Context, _ *Env, c CommandCheck) error {
	r.checks = append(r.checks, c)
	return &security.BlockedError{Reason: "stop"}
}

func (r *checkRecorder) CheckScript(context.Context, *Env, string, string) error { return nil }

func TestTerminalFillsStagedCredentials(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n"})
	rec := &checkRecorder{}
	env := &Env{WorkDir: dir, Gate: rec, Values: map[string]any{}}
	args, _ := json.Marshal(map[string]string{"command": "git add -A"})
	_, _ = (&TerminalTool{}).Run(context.Background(), args, env)
	if len(rec.checks) != 1 || !slices.Equal(rec.checks[0].StagedCredentials, []string{".env"}) {
		t.Fatalf("%+v", rec.checks)
	}
	// The coding agent asks to run the same command.
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	req := security.ApprovalRequest{Command: "git add -A", PatternID: "acp:claude-code:a1:execute"}
	if _, err := g.AskPermission(context.Background(), &Env{WorkDir: dir}, req); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 || !slices.Equal(p.reqs[0].StagedCredentials, []string{".env"}) || !p.reqs[0].HumanOnly {
		t.Fatalf("%+v", p.reqs)
	}
}

// gitExec runs the system git in dir with no hooks, for the set-up of a test.
func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	git, _ := systemGit()
	all := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)
	if out, err := exec.Command(git, all...).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The check never runs a filter driver of .gitattributes and .git/config. The agent can
// write both files with no approval, and the check runs before the gate.
func TestStagedCredentialsRunsNoFilter(t *testing.T) {
	for _, key := range []string{"clean", "process"} {
		t.Run(key, func(t *testing.T) {
			dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n", "keep.pem": "x\n"})
			gitExec(t, dir, "add", "--", "main.go", "keep.pem")
			// The same size and a new content: git must read the file to see the change.
			writeFiles(t, dir, map[string]string{"main.go": "package mian\n", "keep.pem": "y\n",
				".gitattributes": "* filter=evil\n"})
			marker := filepath.Join(t.TempDir(), "pwned")
			f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("[filter \"evil\"]\n\t" + key + " = touch '" + marker + "'; cat\n")
			f.Close()
			for _, cmd := range []string{"git add -A", "git add .", "git add -u", "git add '*.pem'", "git add -f ."} {
				staged(t, cmd, dir)
				if _, err := os.Stat(marker); err == nil {
					t.Fatalf("%q: the check ran filter.evil.%s", cmd, key)
				}
			}
			if got, unchecked := staged(t, "git add -A", dir); unchecked || !slices.Equal(got, []string{".env", "keep.pem"}) {
				t.Fatal(got, unchecked)
			}
		})
	}
}

func TestStagedCredentialsUpdate(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "a.pem": "1\n", "b.pem": "2\n", "gone.pem": "3\n",
		"main.go": "package main\n"})
	gitExec(t, dir, "add", "--", "a.pem", "b.pem", "gone.pem", "main.go")
	writeFiles(t, dir, map[string]string{"a.pem": "11\n"})
	if err := os.Remove(filepath.Join(dir, "gone.pem")); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string][]string{
		"git add -u":          {"a.pem"},
		"git add --update .":  {"a.pem"},
		"git add -vu":         {"a.pem"},
		"git add -A":          {".env", "a.pem"},
		"git add .":           {".env", "a.pem"},
		"git add --al":        {".env", "a.pem"}, // git takes a unique prefix of a long option
		"git add -u '*.go'":   nil,
		"git add -A -- b.pem": nil,
	} {
		got, unchecked := staged(t, cmd, dir)
		if unchecked || !slices.Equal(got, want) {
			t.Errorf("%q: got %v %v, want %v", cmd, got, unchecked, want)
		}
	}
}

func TestStagedCredentialsPathspecs(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n",
		"deploy/server.pem": "x\n", "sub/.env.local": "B=2\n", "sub/readme.md": "x\n",
		"list.txt": "main.go\n.env\n", "nul.txt": "main.go\x00deploy\x00"})
	all := []string{".env", "deploy/server.pem", "sub/.env.local"}
	for cmd, want := range map[string][]string{
		"git add --pathspec-from-file=list.txt":                    {".env"},
		"git add --pathspec-from-file list.txt":                    {".env"},
		"git add --pathspec-from-file=nul.txt --pathspec-file-nul": {"deploy/server.pem"},
		"git add -A -- ':!deploy'":                                 {".env", "sub/.env.local"},
		"git add :/":                                               all,
		"cd sub && git add -A":                                     all,
		"cd sub && git add .":                                      {"sub/.env.local"},
		"git --namespace x add .":                                  all,
		"git --exec-path=/usr/lib/git-core add .":                  all,
		"git --attr-source HEAD add .":                             all,
	} {
		got, unchecked := staged(t, cmd, dir)
		if unchecked || !slices.Equal(got, want) {
			t.Errorf("%q: got %v %v, want %v", cmd, got, unchecked, want)
		}
	}
	for _, cmd := range []string{
		"git add --pathspec-from-file=- < list.txt", // the paths come from stdin
		"git -c core.excludesFile=x add .",          // the config can change the ignore rules
		"git --config-env core.excludesFile=HOME add .",
		"mkdir d && cp .env d/ && git add d", // d does not exist yet
		"cd $X && git add main.go",
		"popd && git add main.go",
		"cd - && git add main.go",
	} {
		if _, unchecked := staged(t, cmd, dir); !unchecked {
			t.Errorf("%q: must be unchecked", cmd)
		}
	}
	// An absolute -C folder is known after popd.
	if got, unchecked := staged(t, "popd && git -C '"+dir+"' add deploy", dir); unchecked || !slices.Equal(got, []string{"deploy/server.pem"}) {
		t.Fatal(got, unchecked)
	}
}

// A FIFO in the place of a tracked credential file does not block the check. git does not
// stage a FIFO as a file.
func TestStagedCredentialsSkipsFIFO(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n"})
	gitExec(t, dir, "add", "--", ".env")
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	type result struct {
		got       []string
		unchecked bool
	}
	done := make(chan result, 1)
	go func() {
		got, unchecked := staged(t, "git add -A", dir)
		done <- result{got, unchecked}
	}()
	select {
	case r := <-done:
		if r.unchecked || len(r.got) != 0 {
			t.Fatal(r.got, r.unchecked)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check blocks on a FIFO")
	}
}

// A file that is larger than the hash limit counts as changed. A file with a new stat and the
// same content does not.
func TestStagedCredentialsHashLimit(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\nB=2\n", "main.go": "package main\n"})
	gitExec(t, dir, "add", "--", ".env", "main.go")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, ".env"), later, later); err != nil {
		t.Fatal(err)
	}
	if got, unchecked := staged(t, "git add -A", dir); unchecked || len(got) != 0 {
		t.Fatal("the same content is not a change:", got, unchecked)
	}
	old := maxHashBytes
	t.Cleanup(func() { maxHashBytes = old })
	maxHashBytes = 4
	if got, unchecked := staged(t, "git add -A", dir); unchecked || !slices.Equal(got, []string{".env"}) {
		t.Fatal(got, unchecked)
	}
}

// With core.autocrlf, the index blob is not the raw file. The check compares the stat data of
// the index entry first, as git does, so an unchanged file is not reported.
func TestStagedCredentialsStatClean(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\r\nB=2\r\n", "main.go": "package main\n"})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, ".env"), past, past); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "config", "core.autocrlf", "true")
	gitExec(t, dir, "add", "--", ".env", "main.go")
	for _, cmd := range []string{"git add -A", "git add -u", "git add ."} {
		if got, unchecked := staged(t, cmd, dir); unchecked || len(got) != 0 {
			t.Errorf("%q: got %v %v, want nothing", cmd, got, unchecked)
		}
	}
	// A new content with the same size and the same mtime: the change time and the inode
	// still tell the change.
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("A=9\r\nB=2\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	if got, unchecked := staged(t, "git add -A", dir); unchecked || !slices.Equal(got, []string{".env"}) {
		t.Fatal(got, unchecked)
	}
}

// The check cannot see the repository or the folder that git uses.
func TestStagedCredentialsWrongTarget(t *testing.T) {
	dir := gitRepo(t, map[string]string{".env": "A=1\n", "main.go": "package main\n",
		"sub/.env.local": "B=2\n", "sub/readme.md": "x\n"})
	all := []string{".env", "sub/.env.local"}
	for _, cmd := range []string{
		"GIT_DIR=/x/.git GIT_WORK_TREE=/x git add -A",
		"GIT_WORK_TREE=/x git add main.go",
		"HOME=/tmp git add -A",
		"env GIT_DIR=/x/.git git add -A",
		"export GIT_DIR=/x/.git; git add -A",
		"export GIT_WORK_TREE=/x && git add main.go",
		"GIT_INDEX_FILE=/x/index; export GIT_INDEX_FILE; git add -A",
		"declare -x XDG_CONFIG_HOME=/x; git add -A",
		"ls | xargs git add",
		"sudo git add -A",
	} {
		if got, unchecked := staged(t, cmd, dir); !unchecked {
			t.Errorf("%q: got %v, want unchecked", cmd, got)
		}
	}
	// A cd in a subshell, a substitution, a pipeline, or a background job does not move the
	// shell of the later parts.
	for _, cmd := range []string{
		"(cd sub && ls); git add .",
		"true $(cd sub) && git add .",
		"cd sub | true; git add .",
		"cd sub & git add .",
		"f() { cd sub; }; git add .",
	} {
		if got, unchecked := staged(t, cmd, dir); !unchecked && !slices.Equal(got, all) {
			t.Errorf("%q: got %v %v, want %v or unchecked", cmd, got, unchecked, all)
		}
	}
	for cmd, want := range map[string][]string{
		"cd sub && git add .":       {"sub/.env.local"},
		"cd sub; git add -A":        all,
		"git status && git add -A":  all,
		"true && git add .":         all,
		"ls 2>/dev/null; git add .": all,
	} {
		if got, unchecked := staged(t, cmd, dir); unchecked || !slices.Equal(got, want) {
			t.Errorf("%q: got %v %v, want %v", cmd, got, unchecked, want)
		}
	}
}

// A broad add, or a named path that does not exist yet, is unchecked only after a part that
// can write files.
func TestStagedCredentialsEarlierWrites(t *testing.T) {
	dir := gitRepo(t, map[string]string{"main.go": "package main\n", ".env.prod.bak": "A=1\n"})
	gitExec(t, dir, "add", "--", "main.go")
	for _, cmd := range []string{
		"cp .env.prod.bak .env && git add -A",
		"echo A=1 > .env && git add .",
		"make && git add -u",
		"git diff --output=x && git add -A",
		"git -c core.fsmonitor=x status && git add -A",
		"./ls && git add -A",
		"mkdir d && cp .env.prod.bak d/.env && git add d",
		"sh ./x.sh && git add new.txt",
		"git add -A > .env",
	} {
		if got, unchecked := staged(t, cmd, dir); !unchecked {
			t.Errorf("%q: got %v, want unchecked", cmd, got)
		}
	}
	for _, cmd := range []string{
		"echo x > notes.md && git add notes.md",
		"git add gone.md",
		"git rm -q --cached main.go; git add main.go",
		"cat main.go && git add main.go",
		"git status && git log -1 && git add -A",
	} {
		if got, unchecked := staged(t, cmd, dir); unchecked {
			t.Errorf("%q: got %v unchecked, want checked", cmd, got)
		}
	}
}
