package textmatch

import (
	"maps"
	"slices"
	"testing"
)

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

func TestWords(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Tether update procedure: fast-forward ~/projects/tether-agent, make build",
			[]string{"agent", "build", "fast", "forward", "make", "procedure", "projects", "tether", "update"}},
		// The stop list and the words under 4 runes go out.
		{"this is what they have with that from when", nil},
		{"VPS = ovh", nil},
		// Letters of any script count, in lower case. Digits count.
		{"Ünïcode ÄBCD 2026", []string{"2026", "äbcd", "ünïcode"}},
	} {
		got := slices.Sorted(maps.Keys(Words(c.in)))
		if !slices.Equal(got, c.want) {
			t.Errorf("Words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSimilar(t *testing.T) {
	four := set("alpha", "bravo", "charlie", "delta")
	ten := set("alpha", "bravo", "charlie", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo")
	eleven := set("alpha", "bravo", "charlie", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima")
	for _, c := range []struct {
		name   string
		a, b   map[string]bool
		shared int
		ok     bool
	}{
		{"3 of the smaller 4", four, ten, 3, true},
		{"3 of 10 is 30%", ten, set("alpha", "bravo", "charlie", "x1x1", "x2x2", "x3x3", "x4x4", "x5x5", "x6x6", "x7x7"), 3, true},
		{"3 of 11 is under 30%", eleven, set("alpha", "bravo", "charlie", "x1x1", "x2x2", "x3x3", "x4x4", "x5x5", "x6x6", "x7x7", "x8x8"), 3, false},
		{"2 shared words", set("alpha", "bravo"), set("alpha", "bravo"), 2, false},
		{"empty", set(), four, 0, false},
	} {
		shared, ok := Similar(c.a, c.b)
		if shared != c.shared || ok != c.ok {
			t.Errorf("%s: got %d %v, want %d %v", c.name, shared, ok, c.shared, c.ok)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Do NOT send—a file, unless I ask!! ": "do not send a file unless i ask",
		"`make build`": "make build",
		"a\t\n b":      "a b",
		"VPS = ovh":    "vps ovh",
		"":             "",
		" ... ":        "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainsOnWords(t *testing.T) {
	long := "do not send a file unless i ask"
	for _, c := range []struct {
		long, short string
		want        bool
	}{
		{long, "send a file", true},
		{long, long, true},
		{long, "do not", true},
		{long, "i ask", true},
		{long, "send a fil", false},
		{"resend a file", "send a file", false},
		{"do not send a files", "send a file", false},
		{long, "", false},
		{"", "", false},
	} {
		if got := ContainsOnWords(c.long, c.short); got != c.want {
			t.Errorf("ContainsOnWords(%q, %q) = %v, want %v", c.long, c.short, got, c.want)
		}
	}
}
