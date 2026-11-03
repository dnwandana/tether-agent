package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// queuedPlanText is a plan message for the queue tests.
const queuedPlanText = "📋 Plan 0/1\n[ ] Find the flaky test"

// queueSend puts a silent, deletable message into the send queue, as a plan event does.
func queueSend(s *Status, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushCtx = context.WithoutCancel(ctx)
	s.enqueue(ctx, text, true, true)
}

// queueOf returns a copy of the send queue.
func queueOf(s *Status) []queuedSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queue)
}

// idsOf returns a copy of the message IDs for the cleanup.
func idsOf(s *Status) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ids)
}

func TestQueuedSendWaitsForTheFloodWait(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	floodEdits(api)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}") // edit → 429, wait 10 s
	api.setReply(nil)
	queueSend(s, queuedPlanText)
	if api.count("sendMessage") != 1 || len(queueOf(s)) != 1 {
		t.Fatal("a send during the flood wait must wait in the queue:", api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || api.count("sendMessage") != 1 {
		t.Fatal("send during the flood wait:", api.methods())
	}
	clk.Advance(9 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	if sends := api.textsOf("sendMessage"); len(sends) != 2 || sends[1] != queuedPlanText || len(queueOf(s)) != 0 {
		t.Fatalf("%q", sends)
	}
	if silent, _ := api.lastCall("sendMessage").Body["disable_notification"].(bool); !silent {
		t.Fatal("the queued message must stay silent")
	}
	if ids := idsOf(s); !slices.Equal(ids, []int{firstBubble, firstBubble + 1}) {
		t.Fatal("a deletable entry must join the cleanup:", ids)
	}
	if got := lastEditOf(api, firstBubble); got != `📖 read_file: "b" (×2)` {
		t.Fatal("the open bubble must get its edit after the queue:", got)
	}
}

func TestHeldBubbleThatClosesKeepsItsLines(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	floodEdits(api)
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}") // edit → 429, wait 10 s
	api.setReply(nil)
	s.Reset()
	if api.count("editMessageText") != 1 {
		t.Fatal("a bubble that closes during the flood wait must not get a forced edit:", api.methods())
	}
	s.Add(ctx, Source{}, "web_fetch", "c", "{}")
	if api.count("sendMessage") != 1 {
		t.Fatal("a new bubble must wait for the flood wait:", api.methods())
	}
	s.Reset()
	want := []queuedSend{{text: `📄 web_fetch: "c"`, silent: false, deletable: true}}
	if got := queueOf(s); !slices.Equal(got, want) {
		t.Fatalf("the held bubble must go into the queue: %+v", got)
	}
	clk.Advance(11 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	if sends := api.textsOf("sendMessage"); len(sends) != 2 || sends[1] != `📄 web_fetch: "c"` {
		t.Fatalf("%q", sends)
	}
	if got := lastEditOf(api, firstBubble); got != `📖 read_file: "b" (×2)` {
		t.Fatal("the closed bubble must get its last lines:", got)
	}
}

func TestHeldBubbleThatSplitsKeepsItsLines(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "verbose")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
	queueSend(s, queuedPlanText) // send → 429, wait 10 s
	api.setReply(nil)
	a, b := strings.Repeat("a", 3000), strings.Repeat("b", 3000)
	s.Add(ctx, Source{}, "read_file", a, "")
	s.Add(ctx, Source{}, "read_file", b, "") // the held bubble is full, so it splits
	for range 3 {
		clk.Advance(11 * time.Second)
		fireTimer(s)
	}
	want := []string{queuedPlanText, queuedPlanText, `📖 read_file: "` + a + `"`, `📖 read_file: "` + b + `"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("the split must keep the lines of the held bubble: got %d sends %.80q", len(got), got)
	}
}

func TestNewBubbleWaitsBehindAQueuedEntry(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 502, Description: "Bad Gateway"}
		}
		return nil
	})
	queueSend(s, queuedPlanText)
	if len(queueOf(s)) != 1 {
		t.Fatal("a 5xx reply must keep the entry")
	}
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	if api.count("sendMessage") != 1 {
		t.Fatal("a new bubble must wait behind the queued entry:", api.methods())
	}
	api.setReply(nil)
	clk.Advance(2 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer for the queued entry")
	}
	want := []string{queuedPlanText, queuedPlanText, `📖 read_file: "a"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestQueueKeepsAnEntryAfterANetworkError(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	good := s.C
	s.C = &Client{Token: "T", BaseURL: "http://127.0.0.1:1", HC: &http.Client{Transport: failTransport{}}}
	queueSend(s, queuedPlanText)
	s.C = good
	if len(queueOf(s)) != 1 || api.count("sendMessage") != 0 {
		t.Fatal("a network error must keep the entry:", api.methods())
	}
	clk.Advance(2 * time.Second)
	if !fireTimer(s) || !slices.Equal(api.textsOf("sendMessage"), []string{queuedPlanText}) {
		t.Fatal(api.methods())
	}
}

func TestQueueDropsAnEntryOnAPermanentError(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 400, Description: "Bad Request: message text is empty"}
		}
		return nil
	})
	queueSend(s, queuedPlanText)
	if len(queueOf(s)) != 0 || len(idsOf(s)) != 0 {
		t.Fatal("a permanent error must drop the entry")
	}
	if !strings.Contains(logs.String(), "drop") {
		t.Fatal("no log line for the dropped entry:", logs.String())
	}
	api.setReply(nil)
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	if sends := api.textsOf("sendMessage"); len(sends) != 2 || sends[1] != `📖 read_file: "a"` {
		t.Fatalf("the next bubble must go out at once: %q", sends)
	}
}

func TestNoEditLineWaitsBehindAFloodedEntry(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	var floodSends atomic.Bool
	api.setReply(func(method string, _ map[string]any) *APIError {
		switch {
		case method == "editMessageText":
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		case method == "sendMessage" && floodSends.Load():
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
	s.Add(ctx, Source{}, "terminal", "a", "{}")
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "web_fetch", "b", "{}") // edit → 400: noEdit mode
	floodSends.Store(true)
	queueSend(s, queuedPlanText) // send → 429, wait 10 s
	floodSends.Store(false)
	s.Add(ctx, Source{}, "web_search", "c", "{}")
	if n := api.count("sendMessage"); n != 3 {
		t.Fatal("the tool line must wait behind the queued entry:", api.methods())
	}
	clk.Advance(11 * time.Second)
	if !fireTimer(s) {
		t.Fatal("no timer after the flood wait")
	}
	want := []string{`💻 terminal: "a"`, `📄 web_fetch: "b"`, queuedPlanText, queuedPlanText, `🔍 web_search: "c"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
