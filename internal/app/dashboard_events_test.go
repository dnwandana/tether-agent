package app

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/goals"
)

// dashEvent is one event of a dashboard stream.
type dashEvent struct{ name, data string }

// dashEvents opens a stream on the hub of a.Dashboard and returns its events. A test
// cleanup closes the stream.
func dashEvents(t *testing.T, a *App) <-chan dashEvent {
	t.Helper()
	srv := httptest.NewServer(a.Dashboard.Hub().Handler(func() dashboard.Snapshot { return dashboard.Snapshot{} }))
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		resp.Body.Close()
		srv.CloseClientConnections()
		srv.Close()
	})
	ch := make(chan dashEvent, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		name := ""
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ch <- dashEvent{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		// The cleanup closes the body, so a read error at the end is expected.
		_ = sc.Err()
	}()
	waitDashEvent(t, ch, "hello")
	return ch
}

// waitDashEvent returns the next event with the name. It skips other events and fails the
// test after 2 s.
func waitDashEvent(t *testing.T, ch <-chan dashEvent, name string) dashEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("the stream ended")
			}
			if e.name == name {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event in 2 s", name)
		}
	}
}

// The scheduler of the gateway sends job.run to the dashboard at the end of a run.
func TestJobRunEvent(t *testing.T) {
	a := dashApp(t)
	ev := dashEvents(t, a)
	if a.Sched == nil || a.Sched.RunEnd == nil {
		t.Fatal("the gateway scheduler has no RunEnd")
	}
	a.Sched.RunEnd("j1", "ok")
	if e := waitDashEvent(t, ev, "job.run"); e.data != `{"job_id":"j1","status":"ok"}` {
		t.Fatal(e.data)
	}
}

// With no dashboard, or after the hub closes, RunEnd does nothing.
func TestJobRunEventNoDashboard(t *testing.T) {
	a := gatewayApp(t, "", "")
	a.Sched.RunEnd("j1", "ok")
	d := dashApp(t)
	d.Dashboard.Hub().Close()
	d.Sched.RunEnd("j1", "ok")
}

// A goal write sends goal.state to the dashboard. The goal event of the chat line is a
// different event.
func TestGoalStateEvent(t *testing.T) {
	a := dashApp(t)
	sess := dashSession(t, a)
	ev := dashEvents(t, a)
	if _, err := a.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.State{Goal: "ship it", Status: goals.StatusActive, MaxTurns: 20, Epoch: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := `{"session_id":"` + sess.ID + `","status":"active"}`
	if e := waitDashEvent(t, ev, "goal.state"); e.data != want {
		t.Fatal(e.data)
	}
}

// With no dashboard, a goal write sends nothing and does not panic.
func TestGoalStateEventNoDashboard(t *testing.T) {
	a := gatewayApp(t, "", "")
	if _, err := a.Goals.Update(ctx, "s-none", func(st *goals.State, _ bool) error {
		*st = goals.State{Goal: "ship it", Status: goals.StatusActive, MaxTurns: 20, Epoch: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
