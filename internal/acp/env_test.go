package acp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// envValue returns the value of name in env and the count of its entries.
func envValue(env []string, name string) (string, int) {
	var v string
	n := 0
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v, n = val, n+1
		}
	}
	return v, n
}

// writeExec writes an executable script at path and creates its folder.
func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestBuildEnvDropsSecrets(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "HOME=/home/user", "ANTHROPIC_API_KEY=sk-ant-0123456789",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-0123456789", "DB_PASSWORD=pw-0123456789", "TELEGRAM_CHAT=12345"}
	keep := slices.Clone(base)
	env := BuildEnv(base, map[string]string{"TELEGRAM_CHAT": "12345"}, config.ACPAgent{Command: []string{"claude-agent-acp"}})
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "DB_PASSWORD", "TELEGRAM_CHAT"} {
		if _, n := envValue(env, name); n != 0 {
			t.Errorf("%s must not reach the adapter", name)
		}
	}
	if v, _ := envValue(env, "HOME"); v != "/home/user" {
		t.Errorf("HOME = %q", v)
	}
	if !slices.Equal(base, keep) {
		t.Fatal("BuildEnv changed base")
	}
}

func TestBuildEnvPathRule(t *testing.T) {
	tests := []struct {
		name    string
		base    []string
		command []string
		env     map[string]string
		want    string
	}{
		{"absolute command", []string{"PATH=/usr/bin:/bin"}, []string{"/opt/node/bin/claude-agent-acp"}, nil, "/opt/node/bin:/usr/bin:/bin"},
		{"bare command", []string{"PATH=/usr/bin:/bin"}, []string{"claude-agent-acp"}, nil, "/usr/bin:/bin"},
		{"no command", []string{"PATH=/usr/bin:/bin"}, nil, nil, "/usr/bin:/bin"},
		{"no base PATH", []string{"HOME=/home/user"}, []string{"/opt/node/bin/claude-agent-acp"}, nil, "/opt/node/bin"},
		{"env PATH wins", []string{"PATH=/usr/bin:/bin"}, []string{"/opt/node/bin/claude-agent-acp"},
			map[string]string{"PATH": "/custom/bin"}, "/custom/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := BuildEnv(tt.base, nil, config.ACPAgent{Command: tt.command, Env: tt.env})
			got, n := envValue(env, "PATH")
			if n != 1 || got != tt.want {
				t.Fatalf("PATH = %q (%d entries), want %q", got, n, tt.want)
			}
		})
	}
}

func TestBuildEnvAgentEnvReplacesBase(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/user", "CLAUDE_CONFIG_DIR=/home/user/.claude"}
	env := BuildEnv(base, nil, config.ACPAgent{
		Command: []string{"claude-agent-acp"},
		Env:     map[string]string{"CLAUDE_CONFIG_DIR": "/home/user/.claude-work", "B_OPT": "2", "A_OPT": "1"},
	})
	if v, n := envValue(env, "CLAUDE_CONFIG_DIR"); n != 1 || v != "/home/user/.claude-work" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q (%d entries)", v, n)
	}
	a, b := slices.Index(env, "A_OPT=1"), slices.Index(env, "B_OPT=2")
	if a < 0 || b < 0 || a > b {
		t.Fatalf("the agent env must come in sorted order: %v", env)
	}
}

func TestBuildEnvSymlinkCommand(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lib", "x", "shim.js")
	writeExec(t, target)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "shim")
	if err := os.Symlink(target, shim); err != nil {
		t.Fatal(err)
	}
	env := BuildEnv([]string{"PATH=/usr/bin:/bin"}, nil, config.ACPAgent{Command: []string{shim}})
	if got, _ := envValue(env, "PATH"); got != bin+":/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want the folder of the symlink first", got)
	}
	for _, name := range []string{"shim", shim} {
		got, err := FindProgram(env, name)
		if err != nil || got != shim {
			t.Fatalf("FindProgram(%q) = %q, %v; want %q", name, got, err, shim)
		}
	}
}

func TestFindProgram(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	writeExec(t, filepath.Join(dir1, "tool"))
	writeExec(t, filepath.Join(dir2, "tool"))
	writeExec(t, filepath.Join(dir2, "only2"))
	if err := os.WriteFile(filepath.Join(dir1, "noexec"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir1, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=/home/user", "PATH=" + dir1 + ":" + dir2}
	if got, err := FindProgram(env, "tool"); err != nil || got != filepath.Join(dir1, "tool") {
		t.Fatalf("the first PATH folder must win: %q, %v", got, err)
	}
	if got, err := FindProgram(env, "only2"); err != nil || got != filepath.Join(dir2, "only2") {
		t.Fatal(got, err)
	}
	for _, name := range []string{"noexec", "adir", "missing", ""} {
		if _, err := FindProgram(env, name); err == nil {
			t.Errorf("FindProgram(%q) must fail", name)
		}
	}
	if _, err := FindProgram(env, "missing"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("a missing program must wrap exec.ErrNotFound: %v", err)
	}
	if _, err := FindProgram([]string{"HOME=/home/user"}, "tool"); err == nil {
		t.Error("an env with no PATH must fail for a bare name")
	}
	// An absolute name is checked as is, with no PATH.
	if got, err := FindProgram(nil, filepath.Join(dir2, "tool")); err != nil || got != filepath.Join(dir2, "tool") {
		t.Fatal(got, err)
	}
	if _, err := FindProgram(env, filepath.Join(dir1, "noexec")); err == nil {
		t.Error("an absolute file with no execute bit must fail")
	}
}

func TestFindProgramUsesAgentEnvPath(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, filepath.Join(dir, "codex-acp"))
	// The base PATH does not have dir. The PATH of the agent env has it.
	env := BuildEnv([]string{"PATH=/usr/bin:/bin"}, nil, config.ACPAgent{
		Command: []string{"codex-acp"},
		Env:     map[string]string{"PATH": dir},
	})
	if got, err := FindProgram(env, "codex-acp"); err != nil || got != filepath.Join(dir, "codex-acp") {
		t.Fatal(got, err)
	}
}

func TestSecretValues(t *testing.T) {
	dotenv := map[string]string{"OPENAI_API_KEY": "sk-0123456789", "EMPTY": ""}
	a := config.ACPAgent{Env: map[string]string{"CLAUDE_CONFIG_DIR": "/home/user/.claude-work", "SAME": "sk-0123456789"}}
	got := SecretValues(dotenv, a)
	want := []string{"/home/user/.claude-work", "sk-0123456789"}
	if !slices.Equal(got, want) {
		t.Fatalf("SecretValues = %q, want %q", got, want)
	}
}
