package skills

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// buildFakeGit puts a git script on PATH and returns its path.
func buildFakeGit(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	p := filepath.Join(bin, "git")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return p
}

func TestNewInstallerFromConfig(t *testing.T) {
	git := buildFakeGit(t)
	data := t.TempDir()
	cfg := config.Default()
	cfg.Skills.ExternalDirs = []string{"/opt/skills"}
	cfg.Skills.GitTimeoutS = 45
	p := &fakePrompt{term: true}
	var out bytes.Buffer
	users := func(context.Context) (map[string][]string, error) { return nil, nil }
	in, err := NewInstaller(data, &cfg, p, &out, users)
	if err != nil {
		t.Fatal(err)
	}
	if in.SkillsDir != filepath.Join(data, "skills") || !slices.Equal(in.External, []string{"/opt/skills"}) ||
		in.Prompt != Prompter(p) || in.Out != &out || in.CronUsers == nil {
		t.Fatalf("%+v", in)
	}
	g, err := in.FindGit()
	if err != nil || g.Path != git || g.Timeout != 45*time.Second || len(g.Protocols) == 0 {
		t.Fatalf("%+v %v", g, err)
	}
	cfg.Skills.GitTimeoutS = 0
	in, _ = NewInstaller(data, &cfg, p, &out, nil)
	if g, _ := in.FindGit(); g.Timeout != 120*time.Second {
		t.Fatalf("zero key: %s", g.Timeout)
	}
	in, err = NewInstaller(data, nil, nil, &out, nil)
	if err != nil || in.External != nil {
		t.Fatalf("nil config: %+v %v", in, err)
	}
	if g, _ := in.FindGit(); g.Timeout != 120*time.Second {
		t.Fatalf("nil config: %s", g.Timeout)
	}
	if _, err := NewInstaller("", &cfg, p, &out, nil); err == nil || err.Error() != "skills: the data folder is not set" {
		t.Fatalf("got %v", err)
	}
}
