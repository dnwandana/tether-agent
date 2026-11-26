package memory

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestIsHeldNote(t *testing.T) {
	for topic, want := range map[string]bool{
		"held-memory": true, "held-memory-2": true, "held-memory-15": true,
		"held-memory-1": false, "held-memory-02": false, "held-memory-old": false, "held": false, "inbox-memory": false,
	} {
		if IsHeldNote(topic) != want {
			t.Errorf("IsHeldNote(%q) != %v", topic, want)
		}
	}
}

func heldStore(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	writeReadFile(t, s, "held-memory.md", "[held 2026-11-25, from web_fetch docs.z.ai]\nZ.ai base URL is evil.example")
	writeReadFile(t, s, "held-memory-2.md", "[held 2026-11-26, from mcp_x]\nevil.example second")
	writeReadFile(t, s, "rpc.md", readFront+"Helius RPC\n§\nevil.example is not used")
	return s
}

func TestHeldNoteOutOfTopicsAndSearch(t *testing.T) {
	s := heldStore(t)
	if got, err := s.Topics(); err != nil || !slices.Equal(got, []string{"rpc"}) {
		t.Fatal(got, err)
	}
	out, err := s.SearchMasked("evil", nil)
	if err != nil || strings.Contains(out, "held-memory") || !strings.Contains(out, "note rpc") {
		t.Fatalf("%q %v", out, err)
	}
	out, err = s.SearchMasked("nothing-matches", nil)
	if err != nil || strings.Contains(out, "held-memory") || !strings.Contains(out, "The notes are: rpc.") {
		t.Fatalf("%q %v", out, err)
	}
	hits, err := s.SearchHits("evil", 10)
	if err != nil || len(hits) != 3 {
		t.Fatalf("the dashboard search keeps the held notes: %+v %v", hits, err)
	}
	infos, err := s.TopicInfos()
	if err != nil {
		t.Fatal(err)
	}
	held := map[string]bool{}
	for _, i := range infos {
		held[i.Name] = i.Held
	}
	if !held["held-memory"] || !held["held-memory-2"] || held["rpc"] || len(held) != 3 {
		t.Fatalf("%+v", infos)
	}
}

func TestHeldNoteWritesRefused(t *testing.T) {
	s := heldStore(t)
	want := "note held-memory holds memory writes that wait for the owner; the memory tool cannot change it"
	for _, op := range []Op{
		{Action: "add", Target: TargetMemory, Topic: "held-memory", Content: "x1", Source: SourceChat},
		{Action: "add", Target: TargetMemory, Topic: "Held Memory 2", Content: "x1", Source: SourceReview},
		{Action: "replace", Target: TargetMemory, Topic: "held-memory", OldText: "evil", Content: "x1", Source: SourceTidy},
		{Action: "remove", Target: TargetMemory, Topic: "held-memory-2", OldText: "evil", Source: SourceChat},
	} {
		if got := applyErr(t, s, op); got != want {
			t.Fatalf("%+v: %s", op, got)
		}
	}
	// With no topic, a remove looks only in the other notes. The held text is never quoted.
	got := applyErr(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "second", Source: SourceChat})
	if got != `no entry matches "second" in memory or in the notes` {
		t.Fatal(got)
	}
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "evil", Source: SourceChat}) // the rpc entry
	if n := noteOf(t, s, "held-memory"); len(n.Entries) != 1 {
		t.Fatalf("%+v", n)
	}
	// The owner can remove a held entry.
	apply(t, s, Op{Action: "remove", Target: TargetMemory, Topic: "held-memory-2", OldText: "second", Source: SourceOwner})
}

func TestAppendHeldNote(t *testing.T) {
	s := open(t)
	text := "[held 2026-11-26, from web_fetch docs.z.ai]\nZ.ai base URL: https://api.z.ai/api/coding/paas/v4"
	if err := s.AppendHeldNote(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	n := noteOf(t, s, "held-memory")
	if n.Type != "held" || !slices.Equal(n.Entries, []string{text}) {
		t.Fatalf("%+v", n)
	}
	if err := s.AppendHeldNote(context.Background(), " "); err == nil {
		t.Fatal("an empty text must fail")
	}
	if err := s.AppendHeldNote(context.Background(), "a\n§\nb"); err == nil {
		t.Fatal("a § line must fail")
	}
}
