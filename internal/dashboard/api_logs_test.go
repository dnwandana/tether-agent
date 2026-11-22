package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/security"
)

func logsSetup(t *testing.T) (*http.ServeMux, *slog.Logger, *Hub) {
	t.Helper()
	ring := obs.NewRing(2000)
	log := obs.NewLoggerRing(io.Discard, "debug", security.NewRedactor(nil), ring)
	hub := NewHub(1 << 20)
	api := NewLogsAPI(ring, hub)
	t.Cleanup(hub.Close)
	t.Cleanup(api.Close)
	mux := http.NewServeMux()
	api.Register(mux)
	return mux, log, hub
}

func logsGet(t *testing.T, mux *http.ServeMux, path string) (int, LogView) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var v LogView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, v
}

func TestLogsRoute(t *testing.T) {
	mux, log, _ := logsSetup(t)
	for i := range 600 {
		log.Info(fmt.Sprintf("line %d", i))
	}
	log.Warn("cron failed", "job", "nightly")
	code, v := logsGet(t, mux, "/api/logs")
	last := v.Lines[len(v.Lines)-1]
	if code != http.StatusOK || len(v.Lines) != 500 || v.Lines[0].Msg != "line 101" ||
		last.Msg != "cron failed" || last.Level != "WARN" || last.Attrs != "job=nightly" {
		t.Fatalf("%d %d %+v %+v", code, len(v.Lines), v.Lines[0], last)
	}
	if _, v := logsGet(t, mux, "/api/logs?level=warn"); len(v.Lines) != 1 {
		t.Fatalf("%+v", v.Lines)
	}
	if _, v := logsGet(t, mux, "/api/logs?q=LINE%2059&limit=2"); len(v.Lines) != 2 ||
		v.Lines[0].Msg != "line 598" || v.Lines[1].Msg != "line 599" {
		t.Fatalf("%+v", v.Lines)
	}
	for _, bad := range []string{"0", "2001", "x"} {
		if code, _ := logsGet(t, mux, "/api/logs?limit="+bad); code != http.StatusBadRequest {
			t.Errorf("limit %s: %d", bad, code)
		}
	}
}

func TestLogsFollowEvent(t *testing.T) {
	_, log, hub := logsSetup(t)
	srv := httptest.NewServer(hub.Handler(func() Snapshot { return Snapshot{} }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events?follow=logs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for hub.Streams() == 0 {
		if ctx.Err() != nil {
			t.Fatal("the stream did not open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	log.Warn("follow me")
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() != "event: log" || !sc.Scan() {
			continue
		}
		var ev LogEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(sc.Text(), "data: ")), &ev); err != nil {
			t.Fatal(err)
		}
		for _, l := range ev.Lines {
			if l.Msg == "follow me" && l.Level == "WARN" {
				return
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal("the log stream failed:", err)
	}
	t.Fatal("no log event with the line")
}
