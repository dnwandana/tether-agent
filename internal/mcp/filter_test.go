package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// names returns the names of defs, joined with ",".
func names(defs []ToolDef) string {
	var out []string
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return strings.Join(out, ",")
}

func TestCheckFilter(t *testing.T) {
	defs := []ToolDef{{Name: "search_issues"}, {Name: "list_repos"}, {Name: "delete_repository"}}
	cfg := config.DefaultMCPServer()
	kept, hidden, warns := CheckFilter(cfg, defs)
	if names(kept) != "search_issues,list_repos,delete_repository" || hidden != 0 || warns != nil {
		t.Fatal("no filter keeps all tools:", names(kept), hidden, warns)
	}
	cfg.Tools.Include = []string{"*_issues", "list_*"}
	kept, hidden, warns = CheckFilter(cfg, defs)
	if names(kept) != "search_issues,list_repos" || hidden != 1 || warns != nil {
		t.Fatal("include:", names(kept), hidden, warns)
	}
	// Exclude wins over include.
	cfg.Tools.Exclude = []string{"list_*"}
	kept, hidden, _ = CheckFilter(cfg, defs)
	if names(kept) != "search_issues" || hidden != 2 {
		t.Fatal("exclude wins:", names(kept), hidden)
	}
	// An entry that matches no tool gives a warning. A bad pattern matches nothing.
	cfg.Tools.Include = []string{"nomatch_*", "search_*"}
	cfg.Tools.Exclude = []string{"delete_*", "x*", "["}
	cfg.Tools.Class = map[string]string{"zz_tool": "read", "list_repos": "read", "aa_tool": "write"}
	kept, hidden, warns = CheckFilter(cfg, defs)
	want := []string{
		`tools.include entry "nomatch_*" matches no tool`,
		`tools.exclude entry "x*" matches no tool`,
		`tools.exclude entry "[" matches no tool`,
		`tools.class entry "aa_tool" matches no tool`,
		`tools.class entry "zz_tool" matches no tool`,
	}
	if names(kept) != "search_issues" || hidden != 2 || fmt.Sprint(warns) != fmt.Sprint(want) {
		t.Fatalf("%s %d\n%q", names(kept), hidden, warns)
	}
	// The input slice does not change.
	if names(defs) != "search_issues,list_repos,delete_repository" {
		t.Fatal(names(defs))
	}
}

// sum returns the sha256 hex of s.
func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func TestConfigHash(t *testing.T) {
	cfg := config.DefaultMCPServer()
	cfg.Command = []string{"npx", "-y", "gh-mcp"}
	cfg.URL, cfg.Transport = "https://example.com/mcp", "sse"
	cfg.Env = map[string]string{"GITHUB_TOKEN": "ghp_secret", "A": "1"}
	cfg.Headers = map[string]string{"X-Key": "k", "Authorization": "Bearer t"}
	in := strings.Join([]string{
		"npx -y gh-mcp",
		"https://example.com/mcp",
		"sse",
		"A=" + sum("1") + "\n" + "GITHUB_TOKEN=" + sum("ghp_secret"),
		"Authorization=" + sum("Bearer t") + "\n" + "X-Key=" + sum("k"),
	}, "\x00")
	h := ConfigHash(cfg)
	if h != sum(in)[:16] || len(h) != 16 {
		t.Fatalf("got %s, want %s", h, sum(in)[:16])
	}
	// The tools block, the timeouts, and the other fields do not change the hash.
	same := cfg
	same.Tools = config.MCPTools{Exclude: []string{"x"}, Class: map[string]string{"a": "read"}}
	same.TimeoutS, same.ConnectTimeoutS, same.AlwaysOn, same.IdleStopMinutes = 5, 5, true, 1
	if ConfigHash(same) != h {
		t.Fatal("a tools entry or a timeout changed the hash")
	}
	// A new Env value, a new header value, a new command, and a new URL change it.
	env := cfg
	env.Env = map[string]string{"GITHUB_TOKEN": "ghp_other", "A": "1"}
	hdr := cfg
	hdr.Headers = map[string]string{"X-Key": "k2", "Authorization": "Bearer t"}
	cmd := cfg
	cmd.Command = []string{"npx", "-y", "gh-mcp@2"}
	url := cfg
	url.URL = "https://example.com/other"
	for name, c := range map[string]config.MCPServer{"env": env, "headers": hdr, "command": cmd, "url": url} {
		if ConfigHash(c) == h {
			t.Errorf("a changed %s keeps the hash", name)
		}
	}
}
