package tools

import (
	"reflect"
	"strings"
	"testing"
)

func TestEditSink(t *testing.T) {
	env := newEnv(t)
	AddEdit(env, "memory", 5)
	AddEdit(env, "skill", 7)
	AddEdit(env, "held", 9)
	want := []EditRef{{Kind: "memory", ID: 5}, {Kind: "skill", ID: 7}, {Kind: "held", ID: 9}}
	got := Edits(env)
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	got[0].ID = 99
	if Edits(env)[0].ID != 5 {
		t.Fatal("Edits must return a copy")
	}
	ResetEdits(env)
	if len(Edits(env)) != 0 {
		t.Fatal("ResetEdits must empty the sink")
	}
	AddEdit(nil, "memory", 1)
	ResetEdits(nil)
	if Edits(nil) != nil {
		t.Fatal("a nil env has no edits")
	}
	bare := &Env{}
	AddEdit(bare, "memory", 3)
	if got := Edits(bare); len(got) != 1 || got[0].ID != 3 {
		t.Fatal("AddEdit must make Env.Values", got)
	}
}

func TestMemorySource(t *testing.T) {
	for kind, want := range map[string]string{"chat": "chat", "review": "review", "tidy": "tidy", "cron": "cron"} {
		if got, err := MemorySource(kind); err != nil || got != want {
			t.Errorf("MemorySource(%q) = %q, %v", kind, got, err)
		}
	}
	for _, kind := range []string{"", "subagent", "goal_judge", "aux", "owner"} {
		if _, err := MemorySource(kind); err == nil || !strings.Contains(err.Error(), "memory: this run kind cannot write memory") {
			t.Errorf("MemorySource(%q) must be refused: %v", kind, err)
		}
	}
}
