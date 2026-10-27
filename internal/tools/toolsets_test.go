package tools

import (
	"reflect"
	"testing"
)

func TestResolveAllAndNarrow(t *testing.T) {
	s := Resolve([]string{"all"}, []string{"web", "file"}, nil)
	if !s["web"] || !s["file"] || s["terminal"] {
		t.Fatal(s)
	}
}

func TestResolveAllHasEverySet(t *testing.T) {
	s := Resolve([]string{"all"}, nil, nil)
	for _, name := range AllToolsets() {
		if !s[name] {
			t.Errorf("all must contain %s", name)
		}
	}
	if !s[SetMCP] {
		t.Error("all must contain mcp")
	}
}

func TestNarrowCannotWiden(t *testing.T) {
	s := Resolve([]string{"web"}, []string{"web", "terminal"}, nil)
	if s["terminal"] {
		t.Fatal("narrow must not widen")
	}
}

func TestResolveRemoveLast(t *testing.T) {
	s := Resolve([]string{"all"}, []string{"web", "terminal"}, []string{"terminal"})
	if !s["web"] || s["terminal"] || len(s) != 1 {
		t.Fatal(s)
	}
}

func TestResolveNarrowMCPServer(t *testing.T) {
	s := Resolve([]string{"all"}, []string{"mcp_gh"}, nil)
	if !s["mcp_gh"] || s["mcp"] || s["web"] {
		t.Fatal(s)
	}
	if s := Resolve([]string{"web"}, []string{"mcp_gh"}, nil); len(s) != 0 {
		t.Fatal("narrow to an mcp server must not widen a channel without mcp", s)
	}
}

func TestValidToolset(t *testing.T) {
	for _, name := range []string{"terminal", "web", "mcp", "all", "mcp_gh"} {
		if !ValidToolset(name, []string{"gh"}) {
			t.Errorf("%s must be valid", name)
		}
	}
	for _, name := range []string{"nope", "mcp_other", "", "mcp_"} {
		if ValidToolset(name, []string{"gh"}) {
			t.Errorf("%s must not be valid", name)
		}
	}
}

func TestVisibleHidesSchemas(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "run_command", set: "terminal"})
	r.Register(fakeTool{name: "web_fetch", set: "web"})
	r.Register(fakeTool{name: "mcp_gh_search", set: "mcp_gh"})
	names, schemas := r.Visible(Resolve([]string{"web", "mcp"}, nil, nil), nil)
	if names["run_command"] || !names["web_fetch"] || !names["mcp_gh_search"] || len(schemas) != 2 {
		t.Fatal(names)
	}
}

func TestVisibleSingleMCPServer(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "mcp_gh_search", set: "mcp_gh"})
	r.Register(fakeTool{name: "mcp_fs_read", set: "mcp_fs"})
	names, _ := r.Visible(map[string]bool{"mcp_gh": true}, nil)
	if !names["mcp_gh_search"] || names["mcp_fs_read"] {
		t.Fatal(names)
	}
}

func TestVisibleOrderStable(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zeta", "alpha", "mid", "beta", "omega"} {
		r.Register(fakeTool{name: n, set: "file"})
	}
	sets := Resolve([]string{"all"}, nil, nil)
	_, a := r.Visible(sets, nil)
	_, b := r.Visible(sets, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the schema order changed between calls")
	}
	want := []string{"alpha", "beta", "mid", "omega", "zeta"}
	for i, s := range a {
		if s.Name != want[i] {
			t.Fatalf("position %d: got %s, want %s", i, s.Name, want[i])
		}
	}
}

func TestRemoveTools(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "delegate_task", set: "delegation"})
	r.Register(fakeTool{name: "read_file", set: "file"})
	names, schemas := r.Visible(Resolve([]string{"all"}, nil, nil), []string{"delegate_task"})
	if names["delegate_task"] || !names["read_file"] || len(schemas) != 1 {
		t.Fatal(names)
	}
}
