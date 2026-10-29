package gateway

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const resumeNote = "[System note: A gateway restart cut your previous turn in this session. The conversation history is intact. Do not run the old tool calls again. If the history has tool results that you did not answer, summarize them first. Then tell the user that the session continues, say where you stopped, and ask how to continue.]"

func TestResumeNoteText(t *testing.T) {
	if agent.ResumeNote != resumeNote {
		t.Fatal(agent.ResumeNote)
	}
	if ResumeLoopText != "⚠️ I stopped the automatic resume after 3 restarts in a row. Send a message to continue." {
		t.Fatal(ResumeLoopText)
	}
	if LostMediaLine != "[A photo or a file of this message was lost in a restart.]" {
		t.Fatal(LostMediaLine)
	}
}

// resumeGateway returns a gateway with a slow provider, a recChannel for "telegram", a
// captured log, and an active session for key "k".
func resumeGateway(t *testing.T, delay time.Duration) (*Gateway, *fakeProv, *recChannel, *syncBuffer, *sessions.Session) {
	t.Helper()
	g, fp := gatewayWithSlowProvider(t, delay)
	logs := &syncBuffer{}
	g.Deps.Log = slog.New(slog.NewTextHandler(logs, nil))
	ch := &recChannel{sent: make(chan Outbound, 16)}
	g.Channels = map[string]Channel{"telegram": ch}
	sess, err := g.Deps.Store.ActiveOrCreate(context.Background(), "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	return g, fp, ch, logs, sess
}

// putMarker writes a marker with the resume count and the time at.
func putMarker(t *testing.T, st *sessions.Store, key, sessionID string, queued []sessions.QueuedText, at time.Time, resumes int) {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertMarker(ctx, key, sessionID, queued, at); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMarkerResumes(ctx, key, resumes); err != nil {
		t.Fatal(err)
	}
}

// noMarkers fails the test when a marker is left.
func noMarkers(t *testing.T, st *sessions.Store) {
	t.Helper()
	ms, err := st.Markers(context.Background())
	if err != nil || len(ms) != 0 {
		t.Fatal(ms, err)
	}
}

// noTurn fails the test when the provider got a request.
func noTurn(t *testing.T, g *Gateway, fp *fakeProv) {
	t.Helper()
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatal("no turn must run:", got)
	}
}

func TestResumeRunsNoteThenQueuedTexts(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 100*time.Millisecond)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{
		{Text: "steer me"}, {Text: "look", Lost: 1}, {Text: "", Lost: 2},
	}, time.Now(), 0)
	if err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	if m := markerOf(t, g.Deps.Store, "k"); m == nil || m.Resumes != 1 {
		t.Fatalf("during the first turn the marker must have one resume: %+v", m)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 4 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	want := []string{resumeNote, "steer me", "look\n" + LostMediaLine, LostMediaLine}
	if got := fp.lastUserTexts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	flushMarkers(t, g)
	noMarkers(t, g.Deps.Store)
}

func TestResumeDropsOldMarkers(t *testing.T) {
	ctx := context.Background()
	g, fp, _, logs, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, time.Now(), 0)
	if err := g.ResumeInterrupted(ctx, time.Now().Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	noMarkers(t, g.Deps.Store)
	if out := logs.String(); !strings.Contains(out, "gateway: a turn marker is too old, so the turn does not resume") || !strings.Contains(out, "key=k") {
		t.Fatal(out)
	}
}

func TestResumeFallsBackToUpdatedAt(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, time.Now().Add(-10*time.Minute), 0)
	if err := g.ResumeInterrupted(ctx, time.Time{}, time.Hour); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); got[0] != resumeNote {
		t.Fatal(got)
	}
}

func TestResumeFallsBackToAnOldUpdatedAt(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, time.Now().Add(-2*time.Hour), 0)
	if err := g.ResumeInterrupted(ctx, time.Time{}, time.Hour); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	noMarkers(t, g.Deps.Store)
}

func TestResumeLoopGuard(t *testing.T) {
	ctx := context.Background()
	g, fp, ch, _, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "x"}}, time.Now(), 3)
	if err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	noMarkers(t, g.Deps.Store)
	select {
	case m := <-ch.sent:
		if m.Text != ResumeLoopText || ch.to.ChatID != "1" {
			t.Fatal(m, ch.to)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no loop message")
	}
	select {
	case m := <-ch.sent:
		t.Fatal("the loop message must be sent once:", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestResumeSkipsAChangedSession(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, s1 := resumeGateway(t, 0)
	s2, err := g.Deps.Store.Create(ctx, sessions.Session{Key: "k", Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Deps.Store.SetActive(ctx, "k", s2.ID); err != nil {
		t.Fatal(err)
	}
	putMarker(t, g.Deps.Store, "k", s1.ID, nil, time.Now(), 0)
	if err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	noMarkers(t, g.Deps.Store)
}

func TestResumeOffDeletesAll(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, time.Now(), 0)
	putMarker(t, g.Deps.Store, "k2", sess.ID, nil, time.Now(), 0)
	if err := g.ResumeInterrupted(ctx, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	noMarkers(t, g.Deps.Store)
}

func TestResumeEnqueueFailureDeletesTheMarker(t *testing.T) {
	ctx := context.Background()
	g, fp, _, logs, sess := resumeGateway(t, 0)
	// Key "a" has no session, so its Enqueue fails. It comes before "k" in the key order.
	putMarker(t, g.Deps.Store, "a", sess.ID, []sessions.QueuedText{{Text: "x"}}, time.Now(), 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, time.Now(), 0)
	if err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if markerOf(t, g.Deps.Store, "a") != nil {
		t.Fatal("the marker of a failed resume must be deleted")
	}
	out := logs.String()
	if !strings.Contains(out, "gateway: cannot resume the turn") || !strings.Contains(out, "key=a") || !strings.Contains(out, "err=") {
		t.Fatal(out)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); got[0] != resumeNote {
		t.Fatal("the other markers must still resume:", got)
	}
}

func TestResumeQueuedLine(t *testing.T) {
	for _, c := range []struct {
		in   sessions.QueuedText
		want string
	}{
		{sessions.QueuedText{Text: "hi"}, "hi"},
		{sessions.QueuedText{Text: "hi", Lost: 1}, "hi\n" + LostMediaLine},
		{sessions.QueuedText{Lost: 3}, LostMediaLine},
		{sessions.QueuedText{}, ""},
	} {
		if got := resumeText(c.in); got != c.want {
			t.Fatalf("%+v: %q", c.in, got)
		}
	}
}
