package memory

import (
	"strings"
	"testing"
)

const (
	procedure = "Tether update procedure: fast-forward ~/projects/tether-agent, make build, install"
	released  = "Dana released and installed Tether himself"
	newUpdate = "Tether update: install the release from GitHub, never make build"
)

func TestSimilarEntries(t *testing.T) {
	got := SimilarEntries([]string{procedure, released, "Owner likes green tea"}, newUpdate, 2)
	if len(got) != 1 || got[0].ID != EntryID(procedure) || got[0].Text != procedure {
		t.Fatalf("%+v", got)
	}
	if got := SimilarEntries([]string{newUpdate}, newUpdate, 2); len(got) != 0 {
		t.Fatalf("the same entry is not similar: %+v", got)
	}
	if got := SimilarEntries([]string{procedure}, newUpdate, 0); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSimilarEntriesOrderAndCut(t *testing.T) {
	three := "alpha bravo charlie delta"
	four := "alpha bravo charlie echo " + strings.Repeat("x", 120)
	got := SimilarEntries([]string{three, four, "alpha bravo charlie golf"}, "alpha bravo charlie echo", 2)
	if len(got) != 2 || got[0].ID != EntryID(four) || got[1].ID != EntryID(three) {
		t.Fatalf("%+v", got)
	}
	if want := string([]rune(four)[:100]) + "…"; got[0].Text != want {
		t.Fatalf("%q", got[0].Text)
	}
}

func TestAddResultListsSimilar(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: procedure})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: released})
	res := apply(t, s, Op{Action: "add", Target: TargetMemory, Content: newUpdate})
	want := "\nSimilar entries (check that they are still true; replace or remove the old one if not):\n" +
		"- " + EntryID(procedure) + ` "` + procedure + `"`
	if !strings.HasSuffix(res.Text, want) {
		t.Fatalf("%q", res.Text)
	}
	// An add to a note and an add with no similar entry get no list.
	res = apply(t, s, Op{Action: "add", Target: TargetMemory, Topic: "rpc", Content: newUpdate + " again"})
	if strings.Contains(res.Text, "Similar entries") {
		t.Fatalf("%q", res.Text)
	}
	res = apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Owner likes green tea"})
	if strings.Contains(res.Text, "Similar entries") {
		t.Fatalf("%q", res.Text)
	}
}
