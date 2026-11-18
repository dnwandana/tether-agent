package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
)

// matchAny reports whether name matches one pattern of patterns. A bad pattern matches
// nothing.
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// CheckFilter applies tools.include and tools.exclude of cfg to defs. It matches each
// pattern against the raw tool name. An empty include list keeps all tools, and an exclude
// match wins over an include match. It returns the kept tools in a new slice, the count of
// hidden tools, and a warning for each entry that matches no tool. warnings is nil when
// there is no warning.
func CheckFilter(cfg config.MCPServer, defs []ToolDef) (kept []ToolDef, hidden int, warnings []string) {
	inc, exc := cfg.Tools.Include, cfg.Tools.Exclude
	kept = make([]ToolDef, 0, len(defs))
	for _, d := range defs {
		if len(inc) > 0 && !matchAny(inc, d.Name) {
			continue
		}
		if matchAny(exc, d.Name) {
			continue
		}
		kept = append(kept, d)
	}
	hidden = len(defs) - len(kept)
	unmatched := func(kind string, entries []string) {
		for _, e := range entries {
			if !slices.ContainsFunc(defs, func(d ToolDef) bool { return matchAny([]string{e}, d.Name) }) {
				warnings = append(warnings, fmt.Sprintf("tools.%s entry %q matches no tool", kind, e))
			}
		}
	}
	unmatched("include", inc)
	unmatched("exclude", exc)
	for _, k := range slices.Sorted(maps.Keys(cfg.Tools.Class)) {
		if !slices.ContainsFunc(defs, func(d ToolDef) bool { return d.Name == k }) {
			warnings = append(warnings, fmt.Sprintf("tools.class entry %q matches no tool", k))
		}
	}
	return kept, hidden, warnings
}

// hexSum returns the sha256 hex of s.
func hexSum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// hashEntries returns the entries of m as sorted key=<sha256 of value> lines.
func hashEntries(m map[string]string) string {
	lines := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		lines = append(lines, k+"="+hexSum(m[k]))
	}
	return strings.Join(lines, "\n")
}

// ConfigHash returns 16 hex characters that identify the connection part of cfg: the
// command, the URL, the transport, the Env, and the Headers. The input holds no secret
// value, because each Env and Headers value enters as its own hash. A new config field,
// the tools block, and a timeout do not change the hash.
func ConfigHash(cfg config.MCPServer) string {
	in := strings.Join([]string{
		strings.Join(cfg.Command, " "),
		cfg.URL,
		cfg.Transport,
		hashEntries(cfg.Env),
		hashEntries(cfg.Headers),
	}, "\x00")
	return hexSum(in)[:16]
}
