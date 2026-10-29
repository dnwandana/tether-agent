package sessions

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func markers(t *testing.T, s *Store) []TurnMarker {
	t.Helper()
	m, err := s.Markers(context.Background())
	must(t, err)
	return m
}

func TestMarkerUpsertKeepsResumes(t *testing.T) {
	s := New(openTestDB(t))
	ctx, now := context.Background(), time.UnixMilli(1_000_000)
	q := []QueuedText{{Text: "hi"}, {Text: "", Lost: 1}}
	must(t, s.UpsertMarker(ctx, "telegram:1", "s1", q, now))
	must(t, s.SetMarkerResumes(ctx, "telegram:1", 2))
	must(t, s.UpsertMarker(ctx, "telegram:1", "s2", nil, now.Add(time.Second)))
	m := markers(t, s)
	if len(m) != 1 || m[0].Resumes != 2 || len(m[0].Queued) != 0 || m[0].SessionID != "s2" ||
		!m[0].UpdatedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("%+v", m)
	}
}

func TestMarkerQueuedRoundTrip(t *testing.T) {
	s := New(openTestDB(t))
	ctx, now := context.Background(), time.UnixMilli(1_000_000)
	q := []QueuedText{{Text: "hi"}, {Text: "", Lost: 1}}
	must(t, s.UpsertMarker(ctx, "b", "s1", q, now))
	must(t, s.UpsertMarker(ctx, "a", "s2", nil, now))
	m := markers(t, s)
	if len(m) != 2 || m[0].SessionKey != "a" || m[1].SessionKey != "b" {
		t.Fatalf("order: %+v", m)
	}
	if !reflect.DeepEqual(m[1].Queued, q) {
		t.Fatalf("queued = %+v, want %+v", m[1].Queued, q)
	}
}

func TestMarkerTouchAndDelete(t *testing.T) {
	s := New(openTestDB(t))
	ctx, now := context.Background(), time.UnixMilli(1_000_000)
	q := []QueuedText{{Text: "x"}}
	must(t, s.UpsertMarker(ctx, "k", "s1", q, now))
	must(t, s.SetMarkerResumes(ctx, "k", 1))
	later := now.Add(time.Minute)
	must(t, s.TouchMarker(ctx, "k", later))
	m := markers(t, s)
	if len(m) != 1 || !m[0].UpdatedAt.Equal(later) || m[0].Resumes != 1 || m[0].SessionID != "s1" ||
		!reflect.DeepEqual(m[0].Queued, q) {
		t.Fatalf("%+v", m)
	}
	must(t, s.DeleteMarker(ctx, "k"))
	if m := markers(t, s); len(m) != 0 {
		t.Fatalf("%+v", m)
	}
	must(t, s.TouchMarker(ctx, "missing", later))
	must(t, s.DeleteMarker(ctx, "missing"))
}
