// Package security holds the secret handling, the redaction, and the command
// approval rules of Tether Agent.
package security

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
)

// LoadDotEnv reads the .env file at path and returns its NAME=value pairs.
//
// A missing file returns an empty map and no error. A file that the group or
// other users can read or write is an error, because the file holds secrets.
// The parser accepts "#" comments, an "export " prefix, and values in single
// or double quotes.
func LoadDotEnv(path string) (map[string]string, error) {
	m := map[string]string{}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf(".env: cannot read %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf(".env has mode %04o; run chmod 600 %s", perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(".env: cannot read %s: %w", path, err)
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf(".env: %s line %d is not NAME=value. Correct the line and try again", path, n)
		}
		m[name] = parseDotEnvValue(strings.TrimSpace(value))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf(".env: cannot read %s: %w", path, err)
	}
	return m, nil
}

// parseDotEnvValue removes the quotes from one value. A double-quoted value
// accepts the escapes \n, \t, \", and \\. A single-quoted value is literal.
// In an unquoted value, " #" starts a comment.
func parseDotEnvValue(v string) string {
	if len(v) >= 2 {
		switch q := v[0]; {
		case q == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1]
		case q == '"' && v[len(v)-1] == '"':
			return unescapeDouble(v[1 : len(v)-1])
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '"', '\\':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// EnvLookup returns a lookup function over the process environment and
// dotenv. The process environment wins over dotenv.
func EnvLookup(dotenv map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		v, ok := dotenv[name]
		return v, ok
	}
}

// IsSecretName reports whether the environment variable name holds a secret.
//
// A name is a secret if it is a key in dotenv, if it ends in _KEY, _TOKEN, or
// _SECRET, or if it contains PASSWORD. A name of exec_secrets is also a secret name. The
// check ignores case.
func IsSecretName(name string, dotenv map[string]string) bool {
	if _, ok := dotenv[name]; ok {
		return true
	}
	for k := range dotenv {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	u := strings.ToUpper(name)
	boundMu.RLock()
	_, bound := boundNames[u]
	boundMu.RUnlock()
	if bound {
		return true
	}
	return strings.HasSuffix(u, "_KEY") ||
		strings.HasSuffix(u, "_TOKEN") ||
		strings.HasSuffix(u, "_SECRET") ||
		strings.Contains(u, "PASSWORD")
}

// KnownSecretValues returns the values that the redactor and the backup mask
// hide: each dotenv value, and the value of each secret name in environ.
// environ has the NAME=value format of os.Environ.
func KnownSecretValues(dotenv map[string]string, environ []string) []string {
	vals := make([]string, 0, len(dotenv))
	for _, v := range dotenv {
		vals = append(vals, v)
	}
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && IsSecretName(name, dotenv) {
			vals = append(vals, v)
		}
	}
	return vals
}

// KnownSecretNames maps each value of KnownSecretValues to the name of its variable, so a
// backup can write ${NAME} in place of the value. A .env name wins over an environ name, and
// the first name in sorted order wins between two .env names. An empty value is not in the map.
func KnownSecretNames(dotenv map[string]string, environ []string) map[string]string {
	names := make(map[string]string, len(dotenv))
	keys := make([]string, 0, len(dotenv))
	for k := range dotenv {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if v := dotenv[k]; v != "" {
			if _, ok := names[v]; !ok {
				names[v] = k
			}
		}
	}
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && v != "" && IsSecretName(name, dotenv) {
			if _, ok := names[v]; !ok {
				names[v] = name
			}
		}
	}
	return names
}

// KnownSecretPairs returns the name and value of each secret that the named redactor hides:
// each .env pair, and each secret name in environ. The .env value wins for a name. An empty
// value is not in the map. environ has the NAME=value format of os.Environ.
func KnownSecretPairs(dotenv map[string]string, environ []string) map[string]string {
	pairs := make(map[string]string, len(dotenv))
	for k, v := range dotenv {
		if v != "" {
			pairs[k] = v
		}
	}
	for _, kv := range environ {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" || !IsSecretName(name, dotenv) {
			continue
		}
		if _, ok := pairs[name]; !ok {
			pairs[name] = v
		}
	}
	return pairs
}

var (
	boundMu    sync.RWMutex
	boundNames map[string]string // the upper-case name of exec_secrets to its original form
)

// SetBoundSecretNames sets the names of exec_secrets. IsSecretName returns true for each of
// them, so ChildEnv strips them from each child. app.New, tether doctor, and tether backup
// call it after the config loads. Tests reset it with SetBoundSecretNames(nil).
func SetBoundSecretNames(names []string) {
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[strings.ToUpper(n)] = n
	}
	boundMu.Lock()
	boundNames = m
	boundMu.Unlock()
}

// BoundSecretNames returns the names that SetBoundSecretNames set, sorted.
func BoundSecretNames() []string {
	boundMu.RLock()
	defer boundMu.RUnlock()
	if len(boundNames) == 0 {
		return nil
	}
	return slices.Sorted(maps.Values(boundNames))
}

// ChildEnv returns the environment for a child process, as NAME=value
// entries. It removes each secret name from base, except the names in
// passthrough. A passthrough name that is not in base but is in dotenv gets
// its dotenv value. ChildEnv does not change base.
func ChildEnv(base []string, dotenv map[string]string, passthrough []string) []string {
	return ChildEnvBound(base, dotenv, passthrough, nil)
}

// ChildEnvBound returns ChildEnv(base, dotenv, passthrough), and gives each name of bound its
// value from bound. It removes each entry of a bound name from base, also a passthrough
// entry, and adds the bound entries at the end in sorted name order. It does not change base.
func ChildEnvBound(base []string, dotenv map[string]string, passthrough []string, bound map[string]string) []string {
	pass := make(map[string]bool, len(passthrough))
	for _, n := range passthrough {
		pass[n] = true
	}
	seen := make(map[string]bool, len(passthrough))
	out := make([]string, 0, len(base)+len(passthrough)+len(bound))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := bound[name]; ok {
			continue
		}
		if pass[name] {
			seen[name] = true
			out = append(out, kv)
			continue
		}
		if IsSecretName(name, dotenv) {
			continue
		}
		out = append(out, kv)
	}
	for _, n := range passthrough {
		if seen[n] {
			continue
		}
		if _, ok := bound[n]; ok {
			continue
		}
		if v, ok := dotenv[n]; ok {
			seen[n] = true
			out = append(out, n+"="+v)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(bound)) {
		out = append(out, n+"="+bound[n])
	}
	return out
}
