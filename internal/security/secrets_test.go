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

func TestBoundSecretNames(t *testing.T) {
	t.Cleanup(func() { SetBoundSecretNames(nil) })
	names := []string{"PYPI_TOKEN", "GITHUB_PAT"}
	SetBoundSecretNames(names)
	names[0] = "CHANGED" // the setter keeps a copy
	if got := BoundSecretNames(); !slices.Equal(got, []string{"GITHUB_PAT", "PYPI_TOKEN"}) {
		t.Fatalf("got %v", got)
	}
	for _, n := range []string{"GITHUB_PAT", "github_pat"} {
		if !IsSecretName(n, nil) {
			t.Fatalf("%s must be a secret name", n)
		}
	}
	if IsSecretName("CHANGED", nil) {
		t.Fatal("the caller's slice must not change the list")
	}
	// ChildEnv strips a bound name that the heuristic misses.
	if out := ChildEnv([]string{"GITHUB_PAT=x", "HOME=/h"}, nil, nil); !slices.Equal(out, []string{"HOME=/h"}) {
		t.Fatalf("got %v", out)
	}
	SetBoundSecretNames(nil)
	if IsSecretName("GITHUB_PAT", nil) || len(BoundSecretNames()) != 0 {
		t.Fatal("the reset must clear the list")
	}
}

func TestChildEnvBound(t *testing.T) {
	base := []string{"PATH=/bin", "PYPI_TOKEN=process-value", "GH_TOKEN=gh-process"}
	dotenv := map[string]string{"PYPI_TOKEN": "dotenv-value", "GH_TOKEN": "gh-dotenv"}
	// A bound name gets the .env value, also when the process has a value. A name that this
	// call does not give stays out.
	out := ChildEnvBound(base, dotenv, nil, map[string]string{"PYPI_TOKEN": "dotenv-value"})
	if want := []string{"PATH=/bin", "PYPI_TOKEN=dotenv-value"}; !slices.Equal(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	// A name in both lists comes one time, with the bound value.
	out = ChildEnvBound(base, dotenv, []string{"PYPI_TOKEN"}, map[string]string{"PYPI_TOKEN": "dotenv-value"})
	if want := []string{"PATH=/bin", "PYPI_TOKEN=dotenv-value"}; !slices.Equal(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	// The bound names come last, in sorted order.
	out = ChildEnvBound([]string{"PATH=/bin"}, nil, nil, map[string]string{"B_TOKEN": "b", "A_TOKEN": "a"})
	if want := []string{"PATH=/bin", "A_TOKEN=a", "B_TOKEN=b"}; !slices.Equal(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	// A nil bound map gives the ChildEnv result.
	if a, b := ChildEnvBound(base, dotenv, []string{"GH_TOKEN"}, nil), ChildEnv(base, dotenv, []string{"GH_TOKEN"}); !slices.Equal(a, b) {
		t.Fatalf("%v != %v", a, b)
	}
	// base does not change.
	if !slices.Equal(base, []string{"PATH=/bin", "PYPI_TOKEN=process-value", "GH_TOKEN=gh-process"}) {
		t.Fatalf("base changed: %v", base)
	}
}
