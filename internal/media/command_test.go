package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/hostos"
)

// ctx is the context for the media calls in the tests.
var ctx = context.Background()

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
}

func TestNoShellInjection(t *testing.T) {
	argv, _ := SplitTemplate("echo {text}")
	got := Expand(argv, map[string]string{"text": "hi; rm -rf ~"})
	if len(got) != 2 || got[1] != "hi; rm -rf ~" {
		t.Fatal(got)
	}
}

func TestQuotedTemplate(t *testing.T) {
	argv, err := SplitTemplate(`edge-tts --text "{text}" --write-media {out}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"edge-tts", "--text", "{text}", "--write-media", "{out}"}
	if !slices.Equal(argv, want) {
		t.Fatal(argv)
	}
	got := Expand(argv, map[string]string{"text": `say "a b" $HOME`, "out": "/tmp/o.mp3"})
	if got[2] != `say "a b" $HOME` || got[4] != "/tmp/o.mp3" {
		t.Fatal(got)
	}
}

func TestSplitTemplateQuoting(t *testing.T) {
	for tpl, want := range map[string][]string{
		`a 'b c' d`:              {"a", "b c", "d"},
		`a "b \"c\" \\ \x" d`:    {"a", `b "c" \ \x`, "d"},
		`a\ b c`:                 {"a b", "c"},
		`  a   b  `:              {"a", "b"},
		`a''b ""`:                {"ab", ""},
		`x 'it''s'`:              {"x", "its"},
		"a\tb\nc":                {"a", "b", "c"},
		`out={out_base}.txt`:     {"out={out_base}.txt"},
		`'$HOME' "$HOME" ;|&`:    {"$HOME", "$HOME", ";|&"},
		`whisper -of {out_base}`: {"whisper", "-of", "{out_base}"},
	} {
		got, err := SplitTemplate(tpl)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%q: got %q, %v", tpl, got, err)
		}
	}
	for _, bad := range []string{"", "   ", `a 'b`, `a "b`, `a \`} {
		if _, err := SplitTemplate(bad); err == nil {
			t.Fatalf("%q: no error", bad)
		}
	}
}

func TestTildeExpands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	argv, err := SplitTemplate("whisper-cli -m ~/models/x.bin -f {in} '~/q' a~/b ~")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"whisper-cli", "-m", filepath.Join(home, "models/x.bin"), "-f", "{in}", "~/q", "a~/b", home}
	if !slices.Equal(argv, want) {
		t.Fatal(argv)
	}
}

func TestExpandAllPlaceholdersOnce(t *testing.T) {
	argv := []string{"p", "{text}", "{in}", "{out}", "{out_base}.txt", "{prompt}", "--size={size}", "{other}", "{out}"}
	vals := map[string]string{"text": "{in}", "in": "i", "out": "o", "out_base": "b", "prompt": "a cat", "size": "512x512"}
	got := Expand(argv, vals)
	want := []string{"p", "{in}", "i", "o", "b.txt", "a cat", "--size=512x512", "{other}", "o"}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
	if argv[1] != "{text}" {
		t.Fatal("Expand changed its input")
	}
}

func TestRunWritesOutput(t *testing.T) {
	needSh(t)
	b := &CommandBackend{Template: "sh -c 'printf %s \"$0\" > \"$1\"' {text} {out}", WorkDir: t.TempDir(), Timeout: 5 * time.Second}
	out := filepath.Join(b.WorkDir, "o.txt")
	if err := b.Run(ctx, map[string]string{"text": "x y", "out": out}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "x y" {
		t.Fatal(string(b))
	}
}

func TestRunWorkDirAndEnv(t *testing.T) {
	needSh(t)
	t.Setenv("OPENAI_API_KEY", "sk-leak")
	dir := filepath.Join(t.TempDir(), "cache", "media")
	b := &CommandBackend{Template: "sh -c 'pwd > out.txt; env >> out.txt'", WorkDir: dir, Timeout: 5 * time.Second}
	if err := b.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "sk-leak") {
		t.Fatal("a secret reached the media command")
	}
	real, _ := filepath.EvalSymlinks(dir)
	if first, _, _ := strings.Cut(string(got), "\n"); first != dir && first != real {
		t.Fatal("work dir", first)
	}
}

// TestRunCallerEnvKeepsPassthrough checks that Run uses a caller Env as given. The caller
// filtered it with security.ChildEnv already, so a passthrough secret name stays.
func TestRunCallerEnvKeepsPassthrough(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "ELEVENLABS_API_KEY=pass-through"}
	b := &CommandBackend{Template: "sh -c 'env > out.txt'", WorkDir: dir, Timeout: 5 * time.Second, Env: env}
	if err := b.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ELEVENLABS_API_KEY=pass-through") {
		t.Fatalf("the passthrough name did not reach the media command:\n%s", got)
	}
}

func TestRunFailureShowsStderrTail(t *testing.T) {
	needSh(t)
	b := &CommandBackend{Template: `sh -c 'i=0; while [ $i -lt 30 ]; do echo line$i >&2; i=$((i+1)); done; exit 3'`, WorkDir: t.TempDir(), Timeout: 5 * time.Second}
	err := b.Run(ctx, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "sh failed (exit 3): ") {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), "line9\n") || !strings.Contains(err.Error(), "line10\n") || !strings.HasSuffix(err.Error(), "line29") {
		t.Fatal("want the last 20 stderr lines:", err)
	}
}

func TestMissingProgramHint(t *testing.T) {
	b := &CommandBackend{Template: "no-such-prog-tether {in}", WorkDir: t.TempDir(), Timeout: time.Second}
	err := b.Run(ctx, map[string]string{"in": "x"})
	if err == nil || !strings.Contains(err.Error(), "no-such-prog-tether not found") || !strings.Contains(err.Error(), "media.*.command") {
		t.Fatal(err)
	}
	b.Template = "edge-tts-missing-tether --text {text}"
	if err := b.Run(ctx, nil); err == nil || !strings.Contains(err.Error(), "install it") {
		t.Fatal(err)
	}

	// The hint gives the command of the host system only.
	t.Cleanup(hostos.Set(hostos.Ubuntu))
	cases := []struct {
		sys      hostos.System
		template string
		want     string
	}{
		{hostos.Ubuntu, "/nonexistent-tether/ffmpeg -i {in}", "ffmpeg not found; install it (for example: sudo apt install ffmpeg) or change media.*.command"},
		{hostos.Ubuntu, "/nonexistent-tether/whisper-cli {in}", "whisper-cli not found; build whisper-cli from whisper.cpp and put it on PATH or change media.*.command"},
		{hostos.MacOS, "/nonexistent-tether/ffmpeg -i {in}", "ffmpeg not found; install it (for example: brew install ffmpeg) or change media.*.command"},
	}
	for _, tc := range cases {
		hostos.Set(tc.sys) // The first Cleanup restores the system.
		b.Template = tc.template
		if err := b.Run(ctx, map[string]string{"in": "x"}); err == nil || err.Error() != tc.want {
			t.Errorf("%s, %s: got %v, want %q", tc.sys, tc.template, err, tc.want)
		}
	}
}

func TestTimeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not on PATH")
	}
	b := &CommandBackend{Template: "sleep 5", WorkDir: t.TempDir(), Timeout: time.Second}
	start := time.Now()
	err := b.Run(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("the timeout did not stop the command", time.Since(start))
	}
}

func TestTimeoutKillsChildren(t *testing.T) {
	needSh(t)
	// The child sleep keeps stderr open. Run must still return at the timeout.
	b := &CommandBackend{Template: "sh -c 'sleep 30 & sleep 30'", WorkDir: t.TempDir(), Timeout: 500 * time.Millisecond}
	start := time.Now()
	if err := b.Run(ctx, nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("Run waited for the children", time.Since(start))
	}
}

func TestDefaultTimeout(t *testing.T) {
	if (&CommandBackend{}).timeout() != 120*time.Second {
		t.Fatal()
	}
}
