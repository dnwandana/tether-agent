package main

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

// golden returns the content of testdata/help/<name>.txt.
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "help", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMainHelpGolden(t *testing.T) {
	if got := mainHelp("/home/you/.tether"); got != golden(t, "main") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestCommandHelpGolden(t *testing.T) {
	for _, h := range commandHelps {
		want := golden(t, h.Name)
		if h.Name == "chat" {
			want = strings.Replace(want, chatCommandsMarker, chatCommandsHelp(gateway.Commands()), 1)
		}
		if got := commandHelp(h.Name); got != want {
			t.Errorf("%s:\n%s", h.Name, got)
		}
	}
}

func TestHelpLineWidth(t *testing.T) {
	texts := map[string]string{"main": mainHelp("/home/you/.tether")}
	for _, h := range commandHelps {
		texts[h.Name] = commandHelp(h.Name)
	}
	for name, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s: line has %d columns: %q", name, n, line)
			}
		}
	}
}

func TestChatCommandsHelp(t *testing.T) {
	cmds := gateway.Commands()
	got := chatCommandsHelp(cmds)
	for name := range cmds {
		if !strings.Contains(got, "\n  /"+name) && !strings.HasPrefix(got, "  /"+name) {
			t.Errorf("missing /%s", name)
		}
	}
	if !strings.HasSuffix(got, "  /exit, /quit      End the chat\n") {
		t.Fatal(got)
	}
	if strings.Contains(got, "prompt cache") {
		t.Fatal("only the first sentence of a Help text")
	}
	if !strings.Contains(got, "  /resume <id>      Switch to another session\n") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "  /model ...        Pin the model of this session: <provider>[:<model>] | clear\n") {
		t.Fatal(got)
	}
}

func TestSuggestCommand(t *testing.T) {
	for in, want := range map[string]string{"doctr": "doctor", "updat": "update", "chta": "chat", "zzz": "", "gen-tools-doc": ""} {
		if got := suggestCommand(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestHelpTableOrder(t *testing.T) {
	want := []string{"setup", "doctor", "chat", "gateway", "sessions", "skills", "pairing", "usage", "update", "version", "help"}
	if len(commandHelps) != len(want) {
		t.Fatalf("got %d entries, want %d", len(commandHelps), len(want))
	}
	for i, h := range commandHelps {
		if h.Name != want[i] {
			t.Errorf("entry %d: got %q want %q", i, h.Name, want[i])
		}
	}
	if _, ok := findHelp("gen-tools-doc"); ok {
		t.Fatal("gen-tools-doc must stay out of the help table")
	}
}

func chatFlags() *flag.FlagSet {
	fl := flag.NewFlagSet("chat", flag.ContinueOnError)
	fl.String("data-dir", "", "")
	return fl
}

func TestUsageError(t *testing.T) {
	var errb bytes.Buffer
	if code := usageError(&errb, "sessions", "export needs a session id"); code != 2 {
		t.Fatal(code)
	}
	want := "sessions: export needs a session id\nusage: tether sessions <command> [flags]\nRun \"tether sessions help\" for the details.\n"
	if errb.String() != want {
		t.Fatalf("%q", errb.String())
	}
}

func TestParseCommandUnknownFlag(t *testing.T) {
	var out, errb bytes.Buffer
	_, code, ok := parseCommand(chatFlags(), []string{"--verbose"}, &out, &errb)
	want := "chat: unknown flag --verbose\nusage: tether chat [flags]\nRun \"tether chat help\" for the details.\n"
	if ok || code != 2 || errb.String() != want || out.Len() != 0 {
		t.Fatalf("%v %d %q", ok, code, errb.String())
	}
}

func TestParseCommandMissingValue(t *testing.T) {
	var out, errb bytes.Buffer
	_, code, ok := parseCommand(chatFlags(), []string{"--data-dir"}, &out, &errb)
	first, _, _ := strings.Cut(errb.String(), "\n")
	if ok || code != 2 || first != "chat: flag --data-dir needs a value" || out.Len() != 0 {
		t.Fatalf("%v %d %q", ok, code, errb.String())
	}
}

func TestParseCommandHelpAfterFlags(t *testing.T) {
	for _, h := range []string{"-h", "--help", "-help"} {
		var out, errb bytes.Buffer
		_, code, ok := parseCommand(chatFlags(), []string{"--data-dir", "d", h}, &out, &errb)
		if ok || code != 0 || out.String() != commandHelp("chat") || errb.Len() != 0 {
			t.Fatalf("%s: %v %d %q", h, ok, code, errb.String())
		}
	}
}

func TestParseCommandInterleaved(t *testing.T) {
	fl := flag.NewFlagSet("sessions", flag.ContinueOnError)
	outFlag := fl.String("out", "", "")
	var out, errb bytes.Buffer
	rest, code, ok := parseCommand(fl, []string{"export", "id1", "--out", "f"}, &out, &errb)
	if !ok || code != 0 || out.Len() != 0 || errb.Len() != 0 {
		t.Fatalf("%v %d %q %q", ok, code, out.String(), errb.String())
	}
	if strings.Join(rest, " ") != "export id1" || *outFlag != "f" {
		t.Fatalf("rest %q, out %q", rest, *outFlag)
	}
}

func TestMainHelpForms(t *testing.T) {
	t.Setenv("TETHER_HOME", "/tmp/th")
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"-help"}, {"--help"}} {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		if code != 0 || out.String() != mainHelp("/tmp/th") || errb.Len() != 0 {
			t.Fatalf("%v: exit %d, stderr %q, stdout %q", args, code, errb.String(), out.String())
		}
	}
}

func TestCommandHelpForms(t *testing.T) {
	for _, h := range commandHelps {
		for _, args := range [][]string{{"help", h.Name}, {h.Name, "help"}, {h.Name, "-h"}, {h.Name, "-help"}, {h.Name, "--help"}} {
			var out, errb bytes.Buffer
			code := run(args, &out, &errb)
			if code != 0 || out.String() != commandHelp(h.Name) || errb.Len() != 0 {
				t.Errorf("%v: exit %d, stderr %q", args, code, errb.String())
			}
		}
	}
}

func TestHelpUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"help", "nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	want := "help: unknown command: nope\nRun \"tether help\" for the commands.\n"
	if errb.String() != want || out.Len() != 0 {
		t.Fatalf("stderr %q, stdout %q", errb.String(), out.String())
	}
}

func TestHelpTooManyArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"help", "chat", "extra"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	want := "help: unexpected argument: extra\nusage: tether help [command]\nRun \"tether help help\" for the details.\n"
	if errb.String() != want || out.Len() != 0 {
		t.Fatalf("stderr %q, stdout %q", errb.String(), out.String())
	}
}

func TestFlagsWithoutCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--data-dir", "d"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	want := "tether: a command is needed\nRun \"tether help\" for the commands.\n"
	if errb.String() != want || out.Len() != 0 {
		t.Fatalf("stderr %q, stdout %q", errb.String(), out.String())
	}
}

func TestSkillsResetHelpIsReset(t *testing.T) {
	t.Setenv("TETHER_HOME", t.TempDir())
	var out, errb bytes.Buffer
	code := run([]string{"skills", "reset", "help"}, &out, &errb)
	if code == 0 || out.String() == commandHelp("skills") {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
	if !strings.Contains(errb.String(), "help") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestHelpTableMatchesSwitch(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					cases[v] = true
				}
			}
			return true
		})
	}
	if !cases["gen-tools-doc"] {
		t.Fatal("the switch of run has no case gen-tools-doc; the parse found the wrong switch")
	}
	delete(cases, "gen-tools-doc")
	for _, h := range commandHelps {
		if !cases[h.Name] {
			t.Errorf("help entry %q has no case in the switch of run", h.Name)
		}
		delete(cases, h.Name)
	}
	for name := range cases {
		t.Errorf("case %q of run has no help entry", name)
	}
}
