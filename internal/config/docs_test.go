package config

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// walkKeys calls fn for each dotted yaml key under v. A map with struct values gives a
// "<name>" segment, and the walk goes into a sample entry: DefaultMCPServer for an MCP
// server, else the zero value. A map with other values is one key, and def is nil for it
// when the map is a placeholder map such as toolsets.<name>.
func walkKeys(v reflect.Value, prefix string, fn func(key string, def *string)) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		fv := v.Field(i)
		switch {
		case f.Type.Kind() == reflect.Struct:
			walkKeys(fv, key, fn)
		case f.Type.Kind() == reflect.Map && f.Type.Elem().Kind() == reflect.Struct:
			sample := reflect.New(f.Type.Elem()).Elem()
			if f.Type.Elem() == reflect.TypeOf(MCPServer{}) {
				sample = reflect.ValueOf(DefaultMCPServer())
			}
			walkKeys(sample, key+".<name>", fn)
		case f.Type.Kind() == reflect.Map && f.Type.Elem().Kind() == reflect.Slice:
			fn(key+".<name>", nil)
		default:
			d := docValue(fv)
			fn(key, &d)
		}
	}
}

// dottedKeys returns every dotted yaml key of the type t.
func dottedKeys(t reflect.Type, prefix string) []string {
	var keys []string
	walkKeys(reflect.New(t).Elem(), prefix, func(k string, _ *string) { keys = append(keys, k) })
	return keys
}

// docValue formats a default value the way CONFIG.md writes it.
func docValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if s == "" || s != strings.TrimSpace(s) {
			return strconv.Quote(s)
		}
		return s
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Slice:
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = docValue(v.Index(i))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Map:
		if v.Len() != 0 {
			panic("docValue: a map default must be empty")
		}
		return "{}"
	}
	panic("docValue: no format for kind " + v.Kind().String())
}

var docRowRE = regexp.MustCompile("^\\| `([^`]+)` \\| `([^`]*)` \\|")

// docRows returns the key and the default of each table row in CONFIG.md.
func docRows(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../../docs/CONFIG.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if m := docRowRE.FindStringSubmatch(line); m != nil {
			rows[m[1]] = m[2]
		}
	}
	return rows
}

func TestConfigDocListsEveryKey(t *testing.T) {
	b, err := os.ReadFile("../../docs/CONFIG.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range dottedKeys(reflect.TypeOf(Config{}), "") {
		if !strings.Contains(string(b), "`"+key+"`") {
			t.Fatal("CONFIG.md misses", key)
		}
	}
}

// TestConfigDocDefaultsMatchCode keeps the default column equal to Default(), so the doc
// cannot drift from the code.
func TestConfigDocDefaultsMatchCode(t *testing.T) {
	rows := docRows(t)
	want := map[string]bool{}
	walkKeys(reflect.ValueOf(Default()), "", func(key string, def *string) {
		want[key] = true
		got, ok := rows[key]
		if !ok {
			t.Errorf("CONFIG.md has no table row for %s", key)
			return
		}
		if def != nil && got != *def {
			t.Errorf("CONFIG.md default of %s is %q, the code has %q", key, got, *def)
		}
	})
	var extra []string
	for k := range rows {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("CONFIG.md has rows for keys that the code does not have: %v", extra)
	}
}

func TestSecurityDocCoversTopics(t *testing.T) {
	b, err := os.ReadFile("../../docs/SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"manual", "smart", "hardline", "600", "cron_mode", ".env", "0600", "pairing", "121",
		"scripts/", "MCP", "protected", "subprocess", "container", "env_passthrough", "block_private_networks"} {
		if !strings.Contains(string(b), s) {
			t.Fatal("SECURITY.md misses", s)
		}
	}
}

func TestLicenseIsMIT(t *testing.T) {
	b, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"MIT License", "Copyright (c) 2026 dnwandana", "WITHOUT WARRANTY OF ANY KIND"} {
		if !strings.Contains(string(b), s) {
			t.Fatal("LICENSE misses", s)
		}
	}
}
