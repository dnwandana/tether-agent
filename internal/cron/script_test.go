package cron

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// script writes body to a new file name in a temporary folder and returns its absolute path.
func script(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStdoutVerbatim(t *testing.T) {
	p := script(t, "a.sh", "echo 'allsaints: 4120/9000'")
	txt, send := ScriptJobMessage(Job{Name: "rl"}, RunScriptSpill(ctx, p, "", nil, 120*time.Second, ""))
	if !send || txt != "allsaints: 4120/9000\n" {
		t.Fatalf("%q", txt)
	}
}

func TestEmptyStdoutSilent(t *testing.T) {
	p := script(t, "q.sh", "true")
	if _, send := ScriptJobMessage(Job{Name: "q"}, RunScriptSpill(ctx, p, "", nil, time.Minute, "")); send {
		t.Fatal("empty → nothing sent")
	}
	p = script(t, "ws.sh", "echo '   '")
	if txt, send := ScriptJobMessage(Job{Name: "q"}, RunScriptSpill(ctx, p, "", nil, time.Minute, "")); send {
		t.Fatalf("white space only → nothing sent, got %q", txt)
	}
}

func TestFailureAlerts(t *testing.T) {
	p := script(t, "f.sh", "echo boom 1>&2; exit 3")
	txt, send := ScriptJobMessage(Job{Name: "f"}, RunScriptSpill(ctx, p, "", nil, time.Minute, ""))
	if !send || !strings.HasPrefix(txt, "⚠ Cron job f failed (exit 3)") || !strings.Contains(txt, "boom") {
		t.Fatal(txt)
	}
}

func TestFailureAlertKeepsLast20StderrLines(t *testing.T) {
	p := script(t, "many.sh", "for i in $(seq 1 30); do echo line$i 1>&2; done; exit 1")
	r := RunScriptSpill(ctx, p, "", nil, time.Minute, "")
	txt, send := ScriptJobMessage(Job{Name: "many"}, r)
	if !send || strings.Contains(txt, "line10\n") || !strings.Contains(txt, "line11\n") || !strings.Contains(txt, "line30") {
		t.Fatal(txt)
	}
	if n := strings.Count(r.StderrTail, "\n"); n > 20 {
		t.Fatal("tail has too many lines", n)
	}
}

func TestFailureWithOutputStillAlerts(t *testing.T) {
	p := script(t, "fo.sh", "echo partial; exit 2")
	txt, send := ScriptJobMessage(Job{Name: "fo"}, RunScriptSpill(ctx, p, "", nil, time.Minute, ""))
	if !send || !strings.HasPrefix(txt, "⚠ Cron job fo failed (exit 2)") || !strings.Contains(txt, "partial") {
		t.Fatal(txt)
	}
}

func TestTimeoutAlerts(t *testing.T) {
	p := script(t, "slow.sh", "sleep 5")
	start := time.Now()
	r := RunScriptSpill(ctx, p, "", nil, time.Second, "")
	if time.Since(start) > 4*time.Second {
		t.Fatal("timeout did not stop the script", time.Since(start))
	}
	txt, send := ScriptJobMessage(Job{Name: "slow"}, r)
	if !r.TimedOut || !send || !strings.HasPrefix(txt, "⚠ Cron job slow failed (timed out after 1s)") {
		t.Fatal(r, txt)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	// The background child keeps stdout open. Only a kill of the whole group ends the run.
	p := script(t, "grp.sh", "sleep 30 &\nsleep 30")
	start := time.Now()
	r := RunScriptSpill(ctx, p, "", nil, time.Second, "")
	if !r.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatal(r, time.Since(start))
	}
}

func TestPythonByExtension(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	p := script(t, "p.py", "import sys\nprint('py', sys.version_info[0])")
	txt, send := ScriptJobMessage(Job{Name: "py"}, RunScriptSpill(ctx, p, "", nil, time.Minute, ""))
	if !send || txt != "py 3\n" {
		t.Fatalf("%q", txt)
	}
}

func TestBashExtension(t *testing.T) {
	// The ${BASH_VERSION} variable is set only by bash, not by sh.
	p := script(t, "b.bash", "[ -n \"${BASH_VERSION}\" ] && echo bash")
	if txt, _ := ScriptJobMessage(Job{Name: "b"}, RunScriptSpill(ctx, p, "", nil, time.Minute, "")); txt != "bash\n" {
		t.Fatalf("%q", txt)
	}
}

func TestWorkdirIsCwd(t *testing.T) {
	dir := t.TempDir()
	p := script(t, "pwd.sh", "pwd")
	r := RunScriptSpill(ctx, p, dir, nil, time.Minute, "")
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(r.Stdout)); got != want {
		t.Fatal(got, want)
	}
	// With no workdir, the script runs in its own folder (Hermes).
	r = RunScriptSpill(ctx, p, "", nil, time.Minute, "")
	want, _ = filepath.EvalSymlinks(filepath.Dir(p))
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(r.Stdout)); got != want {
		t.Fatal(got, want)
	}
}

func TestStdoutCappedWithSpill(t *testing.T) {
	spill := t.TempDir()
	p := script(t, "big.sh", "head -c 60000 /dev/zero | tr '\\0' 'x'")
	r := RunScriptSpill(ctx, p, "", nil, time.Minute, spill)
	if r.ExitCode != 0 || len(r.Stdout) > 50000+500 || !strings.Contains(r.Stdout, "chars omitted") {
		t.Fatal(len(r.Stdout), r.ExitCode, r.StderrTail)
	}
	files, _ := os.ReadDir(spill)
	if len(files) != 1 {
		t.Fatal("spill files", len(files))
	}
}

func TestRelativePathUnderScriptsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveScript("x.sh", dir)
	if err != nil || got != filepath.Join(dir, "x.sh") {
		t.Fatal(got, err)
	}
	if got, err := ResolveScript(filepath.Join(dir, "x.sh"), dir); err != nil || got != filepath.Join(dir, "x.sh") {
		t.Fatal("absolute path in the scripts folder", got, err)
	}
	_, err = ResolveScript("missing.sh", dir)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "missing.sh")) {
		t.Fatal(err)
	}
}

func TestScriptOutsideScriptsDirRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "scripts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "evil.sh")
	if err := os.WriteFile(outside, []byte("true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.sh")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../evil.sh", outside, "link.sh"} {
		if _, err := ResolveScript(p, dir); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatal(p, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScript("sub", dir); err == nil {
		t.Fatal("a folder is not a script")
	}
}

func TestEnvHasNoSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-not-for-children")
	t.Setenv("TETHER_TEST_PLAIN", "visible")
	p := script(t, "env.sh", "env")
	r := RunScriptSpill(ctx, p, "", nil, time.Minute, "")
	if strings.Contains(r.Stdout, "sk-test-not-for-children") || !strings.Contains(r.Stdout, "TETHER_TEST_PLAIN=visible") {
		t.Fatal(r.Stdout)
	}
	// A caller env replaces the default env.
	r = RunScriptSpill(ctx, p, "", []string{"ONLY=1", "PATH=" + os.Getenv("PATH")}, time.Minute, "")
	if !strings.Contains(r.Stdout, "ONLY=1") || strings.Contains(r.Stdout, "TETHER_TEST_PLAIN") {
		t.Fatal(r.Stdout)
	}
}

func TestStartFailureAlerts(t *testing.T) {
	r := RunScriptSpill(ctx, filepath.Join(t.TempDir(), "gone.sh"), "", nil, time.Minute, "")
	txt, send := ScriptJobMessage(Job{Name: "gone"}, r)
	if !send || !strings.HasPrefix(txt, "⚠ Cron job gone failed") {
		t.Fatal(r, txt)
	}
}
