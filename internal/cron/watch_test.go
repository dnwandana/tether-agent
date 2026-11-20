package cron

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseWakeGate(t *testing.T) {
	for _, c := range []struct {
		line     string
		wake, ok bool
	}{
		{`{"wakeAgent": false}`, false, true},
		{`{"wakeAgent": true}`, true, true},
		{`{"wakeAgent":false,"reason":"no change"}`, false, true},
		{`  {"wakeAgent": true}  `, true, true},
		{`{"wakeAgent": "false"}`, false, false},
		{`{"wakeAgent": 0}`, false, false},
		{`{"wakeAgent": null}`, false, false},
		{`{"other": false}`, false, false},
		{`[false]`, false, false},
		{`wakeAgent: false`, false, false},
		{`{"wakeAgent": false} trailing`, false, false},
		{``, false, false},
	} {
		wake, ok := ParseWakeGate(c.line)
		if wake != c.wake || ok != c.ok {
			t.Errorf("%q: got %v, %v; want %v, %v", c.line, wake, ok, c.wake, c.ok)
		}
	}
}

func TestWatchDiff(t *testing.T) {
	if d := WatchDiff("a\nb\n", "a\nb"); d != "" {
		t.Fatalf("the same lines give no diff: %q", d)
	}
	if d := WatchDiff("a\nb\nc\n", "a\nB\nc\n"); d != "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n" {
		t.Fatalf("%q", d)
	}
	if d := WatchDiff("", "a\nb\n"); d != "@@ -0,0 +1,2 @@\n+a\n+b\n" {
		t.Fatalf("%q", d)
	}
	var oldL, newL []string
	for i := 1; i <= 20; i++ {
		oldL = append(oldL, fmt.Sprintf("l%d", i))
		newL = append(newL, fmt.Sprintf("l%d", i))
	}
	newL[1], newL[17] = "X2", "X18"
	want := "@@ -1,5 +1,5 @@\n l1\n-l2\n+X2\n l3\n l4\n l5\n" +
		"@@ -15,6 +15,6 @@\n l15\n l16\n l17\n-l18\n+X18\n l19\n l20\n"
	if d := WatchDiff(strings.Join(oldL, "\n"), strings.Join(newL, "\n")); d != want {
		t.Fatalf("%q", d)
	}
}

func TestWatchDiffTooLarge(t *testing.T) {
	// 2001 × 2001 lines is more than WatchDiffMaxCells.
	if d := WatchDiff(strings.Repeat("a\n", 2001), strings.Repeat("b\n", 2001)); d != "The output changed. It is too large to diff." {
		t.Fatalf("%q", d)
	}
	// 2000 × 2000 lines is at the limit, so the diff is made and then cut.
	d := WatchDiff(strings.Repeat("a\n", 2000), strings.Repeat("b\n", 2000))
	if !strings.HasPrefix(d, "@@ -1,2000 +1,2000 @@\n-a\n") || !strings.HasSuffix(d, "…") ||
		utf8.RuneCountInString(d) != WatchDiffMaxChars+1 {
		t.Fatalf("%d runes: %q", utf8.RuneCountInString(d), d[:40])
	}
}
