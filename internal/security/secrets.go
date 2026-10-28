// Package security holds the secret handling, the redaction, and the command
// approval rules of Tether Agent.
package security

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
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
// _SECRET, or if it contains PASSWORD. The check ignores case.
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

// ChildEnv returns the environment for a child process, as NAME=value
// entries. It removes each secret name from base, except the names in
// passthrough. A passthrough name that is not in base but is in dotenv gets
// its dotenv value. ChildEnv does not change base.
func ChildEnv(base []string, dotenv map[string]string, passthrough []string) []string {
	pass := make(map[string]bool, len(passthrough))
	for _, n := range passthrough {
		pass[n] = true
	}
	seen := make(map[string]bool, len(passthrough))
	out := make([]string, 0, len(base)+len(passthrough))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
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
		if v, ok := dotenv[n]; ok {
			seen[n] = true
			out = append(out, n+"="+v)
		}
	}
	return out
}
