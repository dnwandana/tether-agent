package security

import (
	"strings"
	"testing"
)

func TestStageCredentialParsed(t *testing.T) {
	if HumanOnlyStageCredential != "human_only_stage_credential" {
		t.Fatal(HumanOnlyStageCredential)
	}
	for cmd, want := range map[string]string{
		"git add .env":                        HumanOnlyStageCredential,
		"git stage deploy/server.pem":         HumanOnlyStageCredential,
		"git -C repo add -- id_ed25519":       HumanOnlyStageCredential,
		"git -c core.x=1 add main.go .netrc":  HumanOnlyStageCredential,
		"sh -c 'git add .env'":                HumanOnlyStageCredential,
		"cd app && git add config/.env.local": HumanOnlyStageCredential,
		"git add .env.example":                "",
		"git add main.go":                     "",
		"git add -A":                          "", // a broad form is checked by the stage step
		"git commit -m 'add .env to ignore'":  "",
		"echo git add .env":                   "",
	} {
		if got := humanOnly(cmd); got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
}

func TestStageCredentialRawAndScript(t *testing.T) {
	for cmd, want := range map[string]string{
		`git add -A; echo "x`:      HumanOnlyStageCredential,
		`git add .; echo "x`:       HumanOnlyStageCredential,
		`git add .env; echo "x`:    HumanOnlyStageCredential,
		`git add src/; echo "x`:    HumanOnlyStageCredential,
		`git add main.go; echo "x`: "",
	} {
		if ReadCommand(cmd).Read {
			t.Fatal("the test command must be unreadable:", cmd)
		}
		if got := humanOnly(cmd); got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
	for code, want := range map[string]string{
		`subprocess.run("git add -A", shell=True)`: HumanOnlyStageCredential,
		`subprocess.run(["git", "add", "--all"])`:  "",
		`os.system("git add .env")`:                HumanOnlyStageCredential,
		`os.system("git add main.go")`:             "",
	} {
		got := ""
		if p, ok := MatchHumanOnlyScript(code); ok {
			got = p.ID
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", code, got, want)
		}
	}
}

func TestStageStepAsksEveryTime(t *testing.T) {
	g := newGate(t, "off", "approve")
	p := &countingPrompter{answer: ScopeSession}
	req := ApprovalRequest{SessionID: "s", Scope: "cli", Command: "git add -A", StagedCredentials: []string{".env", "deploy/server.pem"}}
	for want := 1; want <= 2; want++ {
		if err := g.Check(ctx, req, p); err != nil || p.n != want {
			t.Fatal(err, p.n)
		}
		r := p.last
		if !r.HumanOnly || !r.NoSession || !r.NoAlways || r.PatternID != HumanOnlyStageCredential ||
			r.Description != "staging of credential files: .env, deploy/server.pem" {
			t.Fatalf("%+v", r)
		}
	}
	unchecked := ApprovalRequest{SessionID: "s", Scope: "cli", Command: "git add -A", StageUnchecked: true}
	if err := g.Check(ctx, unchecked, p); err != nil || !strings.Contains(p.last.Description, "Tether Agent could not check which files this command stages") {
		t.Fatal(err, p.last.Description)
	}
	// No staged credential and no unchecked flag: mode off runs the command with no question.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: "cli", Command: "git add -A"}, p); err != nil || p.n != 3 {
		t.Fatal(err, p.n)
	}
}

func TestStageStepBlocksWithNoOwner(t *testing.T) {
	g := newGate(t, "off", "approve")
	p := &countingPrompter{answer: ScopeOnce}
	staged := []string{".env"}
	for name, req := range map[string]ApprovalRequest{
		"cron":   {SessionID: "c", Kind: "cron", Command: "git add -A", StagedCredentials: staged},
		"member": {SessionID: "m", Command: "git add -A", Member: true, StagedCredentials: staged},
	} {
		if reason := blockedReason(t, g.Check(ctx, req, p)); !strings.HasPrefix(reason, "BLOCKED (human-only): staging of credential files") || p.n != 0 {
			t.Fatal(name, reason, p.n)
		}
	}
	// A coding agent asks for the same command.
	acp := acpReq("acp:claude-code:a1:execute")
	acp.Command, acp.StagedCredentials = "git add -A", staged
	if scope, err := g.AskACP(ctx, acp, p); err != nil || scope != ScopeOnce || !p.last.HumanOnly || !p.last.NoSession {
		t.Fatal(scope, err, p.last)
	}
	acp.Kind, acp.RootKind = "cron", "cron"
	n := p.n
	if scope, err := g.AskACP(ctx, acp, p); err != nil || scope != ScopeDeny || p.n != n {
		t.Fatal(scope, err, p.n)
	}
}

// The matchers skip the value of each git global option that takes a value, and read the
// unique prefix of a long option of git add, as git does.
func TestStageCredentialGitOptions(t *testing.T) {
	for cmd, want := range map[string]string{
		"git --namespace x add .env":                 HumanOnlyStageCredential,
		"git --config-env core.x=HOME add .env":      HumanOnlyStageCredential,
		"git --attr-source HEAD add .env":            HumanOnlyStageCredential,
		"git --super-prefix p/ add .env":             HumanOnlyStageCredential,
		"git --exec-path=/usr/lib/git-core add .env": HumanOnlyStageCredential,
		"git --namespace add main.go":                "", // "add" is the value of --namespace
		`git --namespace x add .env; echo "x`:        HumanOnlyStageCredential,
		`git --attr-source HEAD add -A; echo "x`:     HumanOnlyStageCredential,
		`git add --al; echo "x`:                      HumanOnlyStageCredential,
		`git add --up; echo "x`:                      HumanOnlyStageCredential,
		`git add --no-ignore-removal x; echo "x`:     HumanOnlyStageCredential,
		`git add --renorm; echo "x`:                  HumanOnlyStageCredential,
		`git add --pathspec-from-f=l.txt; echo "x`:   HumanOnlyStageCredential,
		`git add :/; echo "x`:                        HumanOnlyStageCredential,
		`git add :(top)main.go; echo "x`:             HumanOnlyStageCredential,
		`git add --i main.go; echo "x`:               "", // ambiguous: git refuses it
		`git add --no-all main.go; echo "x`:          "",
		`git --namespace x add main.go; echo "x`:     "",
	} {
		if got := humanOnly(cmd); got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
	for code, want := range map[string]string{
		`os.system("git --namespace x add -A")`:      HumanOnlyStageCredential,
		`os.system("git --super-prefix p/ add .")`:   HumanOnlyStageCredential,
		`os.system("git -C repo --git-dir g add .")`: HumanOnlyStageCredential,
		`os.system("git add --up")`:                  HumanOnlyStageCredential,
		`os.system("git add :/")`:                    HumanOnlyStageCredential,
		`os.system("git --namespace x add main.go")`: "",
	} {
		got := ""
		if p, ok := MatchHumanOnlyScript(code); ok {
			got = p.ID
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", code, got, want)
		}
	}
}

func TestGitAddLongOption(t *testing.T) {
	for w, want := range map[string]string{
		"all": "all", "al": "all", "up": "update", "no-ignore-rem": "-ignore-removal",
		"i": "", "no-": "", "verbos": "verbose", "x": "",
	} {
		name, neg, ok := GitAddLongOption(w)
		got := name
		if neg {
			got = "-" + name
		}
		if !ok {
			got = ""
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", w, got, want)
		}
	}
}
