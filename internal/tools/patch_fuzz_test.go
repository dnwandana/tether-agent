package tools

import (
	"strings"
	"testing"
)

// FuzzFuzzyFindSpans checks the spans of FuzzyFind. FuzzFuzzyFind in
// patch_test.go checks the order of the spans. This target also checks that
// an exact match gives spans that hold old, and that a nil error always comes
// with at least one span.
func FuzzFuzzyFindSpans(f *testing.F) {
	f.Add("a\nfoo()\nb\n", "foo()")
	f.Add("  x = 1\n\ty  =  2\n", "x = 1\ny = 2\n")
	f.Add("aaaa", "aa")
	f.Add("\n\n\n", "\n")
	f.Add("😀 a\n😀 b\n", " a\n😀  b")
	f.Fuzz(func(t *testing.T, content, old string) {
		spans, err := FuzzyFind(content, old)
		if err != nil {
			if len(spans) != 0 {
				t.Fatalf("error %v with %d spans", err, len(spans))
			}
			if strings.Contains(content, old) && old != "" {
				t.Fatalf("content holds old, but FuzzyFind returned %v", err)
			}
			return
		}
		if len(spans) == 0 {
			t.Fatal("no error and no span")
		}
		last := 0
		for _, s := range spans {
			if s.Start < last || s.Start > s.End || s.End > len(content) {
				t.Fatalf("bad span %v (previous end %d, content length %d)", s, last, len(content))
			}
			last = s.End
		}
		if strings.Contains(content, old) {
			// strings.Count counts only matches that do not overlap, as FuzzyFind does.
			if len(spans) != strings.Count(content, old) {
				t.Fatalf("exact match: %d spans, want %d", len(spans), strings.Count(content, old))
			}
			for _, s := range spans {
				if content[s.Start:s.End] != old {
					t.Fatalf("exact match: span %v holds %q, want %q", s, content[s.Start:s.End], old)
				}
			}
		}
	})
}
