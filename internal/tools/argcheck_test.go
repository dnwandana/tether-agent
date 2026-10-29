package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// abSchema is a tool schema with the two properties a and b.
const abSchema = `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`

func TestUnknownArgs(t *testing.T) {
	cases := []struct {
		name, args, schema string
		unknown            []string
		ok                 bool
	}{
		{"clean", `{"a":"x"}`, abSchema, nil, true},
		{"extra key", `{"a":"x","x":2}`, abSchema, []string{"x"}, true},
		{"wrapper", `{"args":{"a":"x"}}`, abSchema, []string{"args"}, true},
		{"letter case", `{"A":"x"}`, abSchema, nil, true},
		{"sorted", `{"z":1,"y":2}`, abSchema, []string{"y", "z"}, true},
		{"empty object", `{}`, abSchema, nil, true},
		{"not an object", `[]`, abSchema, nil, false},
		{"no properties", `{"x":1}`, `{"type":"object"}`, nil, false},
		{"additional true", `{"x":1}`, `{"type":"object","properties":{"a":{}},"additionalProperties":true}`, nil, false},
		{"additional object", `{"x":1}`, `{"type":"object","properties":{"a":{}},"additionalProperties":{}}`, nil, false},
		{"additional false", `{"x":1}`, `{"type":"object","properties":{"a":{}},"additionalProperties":false}`, []string{"x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unknown, known, ok := unknownArgs(json.RawMessage(c.args), json.RawMessage(c.schema))
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !slices.Equal(unknown, c.unknown) {
				t.Fatalf("unknown = %q, want %q", unknown, c.unknown)
			}
			if ok && c.schema == abSchema && !slices.Equal(known, []string{"a", "b"}) {
				t.Fatalf("known = %q, want [a b]", known)
			}
		})
	}
}

func TestUnknownArgsKeepsSchemaOrder(t *testing.T) {
	schema := `{"type":"object","properties":{"b":{"type":"object","properties":{"z":{}}},"a":{"type":"string"}}}`
	unknown, known, ok := unknownArgs(json.RawMessage(`{"z":1}`), json.RawMessage(schema))
	if !ok {
		t.Fatal("check did not apply")
	}
	if !slices.Equal(known, []string{"b", "a"}) {
		t.Fatalf("known = %q, want [b a]", known)
	}
	if !slices.Equal(unknown, []string{"z"}) {
		t.Fatalf("unknown = %q, want [z]", unknown)
	}
}

func TestUnknownArgsNote(t *testing.T) {
	got := unknownArgsNote([]string{"args"}, []string{"action", "job_id"})
	want := "Tether ignored these unknown arguments: args. Put each argument at the top level of the call, and use only the known names. The known arguments are: action, job_id."
	if got != want {
		t.Fatalf("note = %q, want %q", got, want)
	}

	var many []string
	for i := 1; i <= 31; i++ {
		many = append(many, fmt.Sprintf("p%d", i))
	}
	got = unknownArgsNote([]string{"x", "y"}, many)
	if !strings.HasPrefix(got, "Tether ignored these unknown arguments: x, y. ") {
		t.Fatalf("note = %q", got)
	}
	if !strings.HasSuffix(got, "p29, p30, ….") || strings.Contains(got, "p31") {
		t.Fatalf("note = %q", got)
	}
}
