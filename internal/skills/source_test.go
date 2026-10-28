package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestParseSourceForms(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		arg  string
		opts SourceOptions
		want Source
	}{
		{"./my skills", SourceOptions{}, Source{Kind: SourceLocal, Identifier: "./my skills", URL: mustAbs(t, "./my skills")}},
		{".", SourceOptions{Skill: "pdf"}, Source{Kind: SourceLocal, Identifier: ".", URL: mustAbs(t, ".")}},
		{"~/src/skills", SourceOptions{}, Source{Kind: SourceLocal, Identifier: "~/src/skills", URL: filepath.Join(home, "src/skills")}},
		{"/abs/path", SourceOptions{}, Source{Kind: SourceLocal, Identifier: "/abs/path", URL: "/abs/path"}},
		{"https://gitlab.com/org/repo", SourceOptions{Ref: "v1"}, Source{Kind: SourceGit, Identifier: "https://gitlab.com/org/repo", URL: "https://gitlab.com/org/repo", Ref: "v1"}},
		{"git@github.com:o/r.git", SourceOptions{}, Source{Kind: SourceGit, Identifier: "git@github.com:o/r.git", URL: "git@github.com:o/r.git"}},
		{"https://github.com/openai/skills.git", SourceOptions{}, Source{Kind: SourceGit, Identifier: "https://github.com/openai/skills.git", URL: "https://github.com/openai/skills.git"}},
		{"https://github.com/anthropics/skills/tree/main/skills/pdf", SourceOptions{}, Source{Kind: SourceGit, Identifier: "https://github.com/anthropics/skills/tree/main/skills/pdf", URL: "https://github.com/anthropics/skills", Ref: "main", Path: "skills/pdf"}},
		{"https://GitHub.com/o/r/tree/v2", SourceOptions{Skill: "x"}, Source{Kind: SourceGit, Identifier: "https://GitHub.com/o/r/tree/v2", URL: "https://github.com/o/r", Ref: "v2"}},
		{"openai/skills", SourceOptions{Skill: "pdf"}, Source{Kind: SourceGit, Identifier: "openai/skills", URL: "https://github.com/openai/skills"}},
		{"o/r", SourceOptions{Ref: "dev"}, Source{Kind: SourceGit, Identifier: "o/r", URL: "https://github.com/o/r", Ref: "dev"}},
		// A commit SHA goes to lower case; a branch name keeps its case.
		{"o/r", SourceOptions{Ref: "0123456789ABCDEF0123456789ABCDEF01234567"}, Source{Kind: SourceGit, Identifier: "o/r", URL: "https://github.com/o/r", Ref: "0123456789abcdef0123456789abcdef01234567"}},
		{"o/r", SourceOptions{Ref: "Main"}, Source{Kind: SourceGit, Identifier: "o/r", URL: "https://github.com/o/r", Ref: "Main"}},
		{"https://github.com/o/r/tree/0123456789ABCDEF0123456789ABCDEF01234567/x", SourceOptions{}, Source{Kind: SourceGit, Identifier: "https://github.com/o/r/tree/0123456789ABCDEF0123456789ABCDEF01234567/x", URL: "https://github.com/o/r", Ref: "0123456789abcdef0123456789abcdef01234567", Path: "x"}},
		{"jupyter-notebook", SourceOptions{}, Source{Kind: SourceIndex, Identifier: "jupyter-notebook", IndexName: "jupyter-notebook"}},
	}
	for _, c := range cases {
		got, err := ParseSource(c.arg, c.opts)
		if err != nil {
			t.Errorf("ParseSource(%q, %+v): unexpected error: %v", c.arg, c.opts, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSource(%q, %+v) = %+v, want %+v", c.arg, c.opts, got, c.want)
		}
	}
}

func TestParseSourceUsageErrors(t *testing.T) {
	bad := []struct {
		arg  string
		opts SourceOptions
	}{
		{"https://github.com/o/r/tree/main/x", SourceOptions{Ref: "dev"}},
		{"jupyter-notebook", SourceOptions{Skill: "x"}},
		{"jupyter-notebook", SourceOptions{Ref: "v1"}},
		{"-upload-pack=evil", SourceOptions{}},
		{"https://github.com/o/r", SourceOptions{Ref: "--help"}},
		{"https://github.com/o/r/tree/-x/y", SourceOptions{}},
		{"https://github.com/o/r/tree/main/../../etc", SourceOptions{}},
		{"ext::sh -c id", SourceOptions{}},
		{"file:///etc", SourceOptions{}},
		{"http://github.com/openai/skills", SourceOptions{}},
		{"Not A Name", SourceOptions{}},
		{"", SourceOptions{}},
		{"./local", SourceOptions{Ref: "v1"}},
	}
	for _, c := range bad {
		_, err := ParseSource(c.arg, c.opts)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("ParseSource(%q, %+v): err = %v, want *UsageError", c.arg, c.opts, err)
		}
	}
}

func TestParseSourceUsageErrorTexts(t *testing.T) {
	_, err := ParseSource("https://github.com/o/r/tree/main/x", SourceOptions{Ref: "dev"})
	if err == nil || err.Error() != "--ref cannot be used with a tree URL, because the URL gives the ref" {
		t.Errorf("tree URL with --ref: err = %v", err)
	}
	_, err = ParseSource("jupyter-notebook", SourceOptions{Skill: "x"})
	if err == nil || err.Error() != "--ref and --skill cannot be used with an index name" {
		t.Errorf("index name with --skill: err = %v", err)
	}
	_, err = ParseSource("http://github.com/openai/skills", SourceOptions{})
	if err == nil || err.Error() != `the URL scheme "http" is not supported. Use https, ssh, or git.` {
		t.Errorf("http URL: err = %v", err)
	}
}

func TestTrustForURL(t *testing.T) {
	want := map[string]Trust{
		"https://github.com/openai/skills":                 TrustTrusted,
		"https://github.com/openai/skills.git":             TrustTrusted,
		"https://github.com/openai/skills/":                TrustTrusted,
		"https://github.com/Anthropics/Skills":             TrustTrusted,
		"https://github.com/huggingface/skills":            TrustTrusted,
		"git@github.com:NVIDIA/skills.git":                 TrustTrusted,
		"git@GitHub.com:openai/skills":                     TrustTrusted,
		"https://github.com/someone/skills":                TrustCommunity,
		"https://gitlab.com/openai/skills":                 TrustCommunity,
		"https://github.com.evil.io/openai/skills":         TrustCommunity,
		"https://evil.io/github.com/openai/skills":         TrustCommunity,
		"git@github.com.evil.io:openai/skills.git":         TrustCommunity,
		"http://github.com/openai/skills":                  TrustCommunity,
		"https://github.com/openai/skills/../../evil/repo": TrustCommunity,
		"/home/me/openai/skills":                           TrustCommunity,
		"openai/skills":                                    TrustCommunity,
		"":                                                 TrustCommunity,
	}
	for url, w := range want {
		if got := TrustForURL(url); got != w {
			t.Errorf("TrustForURL(%q) = %q, want %q", url, got, w)
		}
	}
}

func TestGitHubRepo(t *testing.T) {
	cases := []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://github.com/o/r", "o/r", true},
		{"https://github.com/o/r.git", "o/r", true},
		{"https://github.com/o/r/", "o/r", true},
		{"git@github.com:O/R.git", "O/R", true},
		// ParseSource turns a tree URL into the plain repo URL first.
		{"https://github.com/o/r/tree/main/x", "", false},
		{"https://github.com/openai/skills/../../evil/repo", "", false},
		{"https://github.com/../skills", "", false},
		{"https://github.com/openai/skills%2F..%2F..%2Fevil", "", false},
		{"https://github.com/openai/skills?x=1", "", false},
		{"https://github.com/openai/skills#x", "", false},
		{"https://u@github.com/openai/skills", "", false},
		{"git@github.com:../skills", "", false},
		{"https://github.com/o", "", false},
		{"https://gitlab.com/o/r", "", false},
		{"git@github.com:o", "", false},
		{"git@github.com:o/r/x", "", false},
	}
	for _, c := range cases {
		got, ok := GitHubRepo(c.url)
		if got != c.want || ok != c.ok {
			t.Errorf("GitHubRepo(%q) = %q, %v, want %q, %v", c.url, got, ok, c.want, c.ok)
		}
	}
}
