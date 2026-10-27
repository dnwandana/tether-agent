package security

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the umask. Chmod sets the exact mode for the test.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDotEnvModeMustBe0600(t *testing.T) {
	p := writeEnv(t, "A=1\n", 0o644)
	_, err := LoadDotEnv(p)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "mode 0644") || !strings.Contains(err.Error(), p) {
		t.Fatalf("error must name the mode and the path: %v", err)
	}
}

func TestDotEnvStricterModeIsAccepted(t *testing.T) {
	p := writeEnv(t, "A=1\n", 0o400)
	if _, err := LoadDotEnv(p); err != nil {
		t.Fatal(err)
	}
}

func TestDotEnvMissingFileIsEmpty(t *testing.T) {
	m, err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil || m == nil || len(m) != 0 {
		t.Fatalf("got %v, %v", m, err)
	}
}

func TestDotEnvParses(t *testing.T) {
	p := writeEnv(t, "# c\nZAI_API_KEY=abc\nQUOTED=\"x y\"\nexport B=2\n", 0o600)
	m, err := LoadDotEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["ZAI_API_KEY"] != "abc" || m["QUOTED"] != "x y" || m["B"] != "2" {
		t.Fatal(m)
	}
}

func TestDotEnvQuotesAndComments(t *testing.T) {
	body := strings.Join([]string{
		"",
		"   # indented comment",
		"SINGLE='a \"b\" $c'",
		`DOUBLE="line1\nline2 \"q\""`,
		"INLINE=value # a comment",
		"HASH=abc#def",
		"QHASH=\"abc # not a comment\"",
		"  SPACED  =  padded  ",
		"EMPTY=",
		"URL=https://x.io/a?b=c",
	}, "\n")
	m, err := LoadDotEnv(writeEnv(t, body, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SINGLE": `a "b" $c`,
		"DOUBLE": "line1\nline2 \"q\"",
		"INLINE": "value",
		"HASH":   "abc#def",
		"QHASH":  "abc # not a comment",
		"SPACED": "padded",
		"EMPTY":  "",
		"URL":    "https://x.io/a?b=c",
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d keys: %v", len(m), m)
	}
}

func TestDotEnvBadLineIsError(t *testing.T) {
	_, err := LoadDotEnv(writeEnv(t, "A=1\nnot a pair\n", 0o600))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("got %v", err)
	}
}

func TestProcessEnvWins(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "env")
	v, ok := EnvLookup(map[string]string{"ZAI_API_KEY": "file"})("ZAI_API_KEY")
	if !ok || v != "env" {
		t.Fatal(v)
	}
}

func TestEnvLookupFallsBackToDotEnv(t *testing.T) {
	look := EnvLookup(map[string]string{"TETHER_TEST_ONLY_IN_FILE": "file"})
	if v, ok := look("TETHER_TEST_ONLY_IN_FILE"); !ok || v != "file" {
		t.Fatal(v, ok)
	}
	if _, ok := look("TETHER_TEST_NOT_SET_ANYWHERE"); ok {
		t.Fatal("unset name must not be found")
	}
	if _, ok := EnvLookup(nil)("TETHER_TEST_NOT_SET_ANYWHERE"); ok {
		t.Fatal("nil map must work")
	}
}

func TestIsSecretName(t *testing.T) {
	d := map[string]string{"HELIUS": "x"}
	for _, n := range []string{"HELIUS", "OPENAI_API_KEY", "TELEGRAM_BOT_TOKEN", "CLIENT_SECRET", "DB_PASSWORD_X"} {
		if !IsSecretName(n, d) {
			t.Fatal(n)
		}
	}
	if IsSecretName("PATH", d) || IsSecretName("HOME", d) {
		t.Fatal("not secret")
	}
}

func TestIsSecretNameCaseInsensitive(t *testing.T) {
	d := map[string]string{"Helius": "x"}
	for _, n := range []string{"helius", "openai_api_key", "My_Token", "client_secret", "dbpassword"} {
		if !IsSecretName(n, d) {
			t.Fatal(n)
		}
	}
	for _, n := range []string{"KEYBOARD", "TOKENS_USED", "SECRETARY", "LANG", "TERM"} {
		if IsSecretName(n, nil) {
			t.Fatal(n)
		}
	}
}

func TestChildEnvStripsAndPassesThrough(t *testing.T) {
	out := ChildEnv([]string{"PATH=/bin", "OPENAI_API_KEY=s", "HELIUS_API_KEY=h"}, nil, []string{"HELIUS_API_KEY"})
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "OPENAI_API_KEY") || !strings.Contains(joined, "HELIUS_API_KEY=h") || !strings.Contains(joined, "PATH=/bin") {
		t.Fatal(joined)
	}
}

func TestChildEnvDoesNotChangeBase(t *testing.T) {
	base := []string{"OPENAI_API_KEY=s", "PATH=/bin", "HOME=/h"}
	keep := slices.Clone(base)
	out := ChildEnv(base, map[string]string{"HOME": "x"}, nil)
	if !slices.Equal(base, keep) {
		t.Fatalf("base changed: %v", base)
	}
	if !slices.Equal(out, []string{"PATH=/bin"}) {
		t.Fatalf("got %v", out)
	}
}

func TestChildEnvStripsDotEnvNames(t *testing.T) {
	out := ChildEnv([]string{"HELIUS=1", "PATH=/bin"}, map[string]string{"HELIUS": "1"}, nil)
	if slices.Contains(out, "HELIUS=1") {
		t.Fatal(out)
	}
}

func TestChildEnvAddsPassthroughFromDotEnv(t *testing.T) {
	// A passthrough value that lives only in .env reaches the child.
	out := ChildEnv([]string{"PATH=/bin"}, map[string]string{"HELIUS_API_KEY": "h", "OTHER_KEY": "o"}, []string{"HELIUS_API_KEY"})
	if !slices.Contains(out, "HELIUS_API_KEY=h") || slices.Contains(out, "OTHER_KEY=o") {
		t.Fatal(out)
	}
	// The process value wins over the .env value, and it is not duplicated.
	out = ChildEnv([]string{"HELIUS_API_KEY=proc"}, map[string]string{"HELIUS_API_KEY": "file"}, []string{"HELIUS_API_KEY"})
	if !slices.Equal(out, []string{"HELIUS_API_KEY=proc"}) {
		t.Fatal(out)
	}
}
