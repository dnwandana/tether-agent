// Package acp runs external coding agents over the Agent Client Protocol (ACP). It starts one
// adapter process for each Tether session and agent, and it sends each permission request of
// the agent to the approval gate.
package acp

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// BuildEnv returns the environment of the adapter process: base with no secret names, the
// folder of an absolute command first in PATH, and then the env of the agent. A value of the
// agent env replaces a base value of the same name. A PATH in the agent env replaces the
// whole PATH. BuildEnv does not change base.
func BuildEnv(base []string, dotenv map[string]string, a config.ACPAgent) []string {
	env := security.ChildEnv(base, dotenv, nil)
	if len(a.Command) > 0 && filepath.IsAbs(a.Command[0]) {
		// The folder comes from the path as written. An nvm shim is a symlink into
		// lib/node_modules, and node is in the folder of the symlink, not of its target.
		dir := filepath.Dir(a.Command[0])
		old := lastValue(env, "PATH")
		env = withoutNames(env, map[string]bool{"PATH": true})
		if old == "" {
			env = append(env, "PATH="+dir)
		} else {
			env = append(env, "PATH="+dir+string(os.PathListSeparator)+old)
		}
	}
	if len(a.Env) > 0 {
		names := make(map[string]bool, len(a.Env))
		for k := range a.Env {
			names[k] = true
		}
		env = withoutNames(env, names)
		for _, k := range slices.Sorted(maps.Keys(a.Env)) {
			env = append(env, k+"="+a.Env[k])
		}
	}
	return dedupLast(env)
}

// FindProgram returns the path of the program name. It looks up a bare name in the PATH of
// env, not in the PATH of this process. It checks a name with a slash as is. It does not
// resolve symlinks in the result.
func FindProgram(env []string, name string) (string, error) {
	if name == "" {
		return "", errors.New("acp: the command is empty")
	}
	if strings.ContainsRune(name, '/') {
		if err := checkExecutable(name); err != nil {
			return "", err
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(lastValue(env, "PATH")) {
		// A relative folder depends on the working directory, so it is not safe.
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		if checkExecutable(path) == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%q: %w", name, exec.ErrNotFound)
}

// SecretValues returns the values that an error text must not show: the .env values and the
// env values of the agent, with no empty value and no duplicate, in sorted order.
func SecretValues(dotenv map[string]string, a config.ACPAgent) []string {
	set := make(map[string]bool, len(dotenv)+len(a.Env))
	for _, m := range []map[string]string{dotenv, a.Env} {
		for _, v := range m {
			if v != "" {
				set[v] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// checkExecutable returns nil when path is a regular file with an execute bit. os.Stat
// follows a symlink, so a symlink to an executable file passes.
func checkExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}

// lastValue returns the value of the last entry of name in env, as exec.Cmd uses it.
func lastValue(env []string, name string) string {
	var v string
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return v
}

// withoutNames returns a copy of env with no entry whose name is in names.
func withoutNames(env []string, names map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !names[k] {
			out = append(out, kv)
		}
	}
	return out
}

// dedupLast keeps only the last entry of each name, in the order of those last entries. A
// duplicate name in base would otherwise reach the adapter twice.
func dedupLast(env []string) []string {
	last := make(map[string]int, len(env))
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		last[k] = i
	}
	out := make([]string, 0, len(last))
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if last[k] == i {
			out = append(out, kv)
		}
	}
	return out
}
