package skills

import (
	"path"
	"strings"
	"testing"
)

func TestIndexFileIsValid(t *testing.T) {
	entries, err := LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the index has no entries")
	}
	seen := map[string]bool{}
	for i, e := range entries {
		if !validNameRe.MatchString(e.Name) || len(e.Name) > maxNameLen {
			t.Errorf("entry %d: name %q is not a valid skill name", i, e.Name)
		}
		if seen[e.Name] {
			t.Errorf("entry %d: name %q is not unique", i, e.Name)
		}
		seen[e.Name] = true
		if !strings.HasPrefix(e.URL, "https://") {
			t.Errorf("%s: url %q does not start with https://", e.Name, e.URL)
		}
		if e.Path == "" || path.IsAbs(e.Path) || path.Clean(e.Path) != e.Path {
			t.Errorf("%s: path %q is not a clean relative path", e.Name, e.Path)
		}
		for _, seg := range strings.Split(e.Path, "/") {
			if seg == ".." {
				t.Errorf("%s: path %q has a .. segment", e.Name, e.Path)
			}
		}
		if strings.TrimSpace(e.Description) == "" || strings.Contains(e.Description, "\n") {
			t.Errorf("%s: description %q is empty or has more than one line", e.Name, e.Description)
		}
		if i > 0 && entries[i-1].Name >= e.Name {
			t.Errorf("entries are not sorted: %q comes before %q", entries[i-1].Name, e.Name)
		}
	}
}

func TestLookupIndex(t *testing.T) {
	e, ok, err := LookupIndex("pdf")
	if err != nil || !ok {
		t.Fatalf("LookupIndex(pdf) = ok %v, err %v", ok, err)
	}
	if e.URL != "https://github.com/anthropics/skills" || e.Path != "skills/pdf" {
		t.Errorf("LookupIndex(pdf) = %+v", e)
	}
	if _, ok, _ := LookupIndex("no-such-skill"); ok {
		t.Error("LookupIndex(no-such-skill) found an entry")
	}
}

func TestFilterIndex(t *testing.T) {
	in := []IndexEntry{
		{Name: "jupyter-notebook", Description: "Edit notebooks"},
		{Name: "pdf", Description: "Read a PDF"},
	}
	names := func(es []IndexEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return strings.Join(out, ",")
	}
	cases := []struct{ word, want string }{
		{"NOTEBOOK", "jupyter-notebook"},
		{"pdf", "pdf"},
		{"read a", "pdf"},
		{"", "jupyter-notebook,pdf"},
		{"zzz", ""},
	}
	for _, c := range cases {
		if got := names(FilterIndex(in, c.word)); got != c.want {
			t.Errorf("FilterIndex(%q) = %q, want %q", c.word, got, c.want)
		}
	}
}
