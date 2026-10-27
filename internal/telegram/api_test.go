package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var ctx = context.Background()

// tgServer starts a fake Bot API. The handler gets the method name and the
// decoded JSON body, and returns the "result" value of an ok reply.
func tgServer(t *testing.T, h func(method string, body map[string]any) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s, want POST", r.Method)
		}
		parts := strings.Split(r.URL.Path, "/")
		method := parts[len(parts)-1]
		body := map[string]any{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": h(method, body)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCallPathAndJSON(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type %q", ct)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["chat_id"].(float64) != 42 || body["text"] != "hi" {
			t.Errorf("body %v", body)
		}
		if _, ok := body["message_thread_id"]; ok {
			t.Errorf("zero thread id must be left out: %v", body)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":42}}}`)
	}))
	defer srv.Close()
	m, err := (&Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}).SendMessage(ctx, SendMessageParams{ChatID: 42, Text: "hi"})
	if err != nil || m.MessageID != 7 || m.Chat.ID != 42 {
		t.Fatal(m, err)
	}
	if path != "/botT/sendMessage" {
		t.Fatalf("path %q", path)
	}
}

func TestTypedHelpers(t *testing.T) {
	var seen []string
	srv := tgServer(t, func(method string, body map[string]any) any {
		seen = append(seen, method)
		switch method {
		case "getMe":
			return map[string]any{"id": 99, "is_bot": true, "username": "tether_bot"}
		case "getUpdates":
			if body["offset"].(float64) != 5 || body["timeout"].(float64) != 0 {
				t.Errorf("getUpdates body %v", body)
			}
			return []any{map[string]any{"update_id": 5, "message": map[string]any{
				"message_id": 1, "message_thread_id": 17585, "is_topic_message": true,
				"chat": map[string]any{"id": -100, "type": "supergroup", "is_forum": true},
				"from": map[string]any{"id": 7, "first_name": "Ana"},
				"text": "@tether_bot hi", "entities": []any{map[string]any{"type": "mention", "offset": 0, "length": 11}},
				"reply_to_message": map[string]any{"message_id": 0, "chat": map[string]any{"id": -100}},
			}}}
		case "sendChatAction":
			if body["message_thread_id"].(float64) != 1 || body["action"] != "typing" {
				t.Errorf("sendChatAction body %v", body)
			}
			return true
		case "getFile":
			return map[string]any{"file_id": "F", "file_path": "photos/a.jpg"}
		case "editMessageText":
			return map[string]any{"message_id": 2, "chat": map[string]any{"id": 1}}
		}
		return true
	})
	c := &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
	me, err := c.GetMe(ctx)
	if err != nil || me.Username != "tether_bot" || me.ID != 99 {
		t.Fatal(me, err)
	}
	ups, err := c.GetUpdates(ctx, 5, 0)
	if err != nil || len(ups) != 1 {
		t.Fatal(ups, err)
	}
	m := ups[0].Message
	if m.MessageThreadID != 17585 || !m.IsTopicMessage || !m.Chat.IsForum || m.ReplyToMessage == nil || m.Entities[0].Type != "mention" || m.From.FirstName != "Ana" {
		t.Fatalf("%+v", m)
	}
	if err := c.SendChatAction(ctx, -100, 1, "typing"); err != nil {
		t.Fatal(err)
	}
	f, err := c.GetFile(ctx, "F")
	if err != nil || f.FilePath != "photos/a.jpg" {
		t.Fatal(f, err)
	}
	if err := c.SetMyCommands(ctx, []BotCommand{{Command: "new", Description: "Start a new session."}}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteMessage(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := c.AnswerCallbackQuery(ctx, "cb", "ok", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SendMessageDraft(ctx, SendMessageDraftParams{ChatID: 1, DraftID: 3, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EditMessageText(ctx, EditMessageTextParams{ChatID: 1, MessageID: 2, Text: "y"}); err != nil {
		t.Fatal(err)
	}
	want := "getMe getUpdates sendChatAction getFile setMyCommands deleteMessage answerCallbackQuery sendMessageDraft editMessageText"
	if strings.Join(seen, " ") != want {
		t.Fatal(seen)
	}
}

func TestRetryAfter429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":8,"chat":{"id":1}}}`)
	}))
	defer srv.Close()
	start := time.Now()
	m, err := (&Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}).SendMessage(ctx, SendMessageParams{ChatID: 1, Text: "x"})
	if err != nil || m.MessageID != 8 {
		t.Fatal(m, err)
	}
	if time.Since(start) < time.Second {
		t.Fatal("did not wait retry_after")
	}
	if n.Load() != 2 {
		t.Fatal("calls", n.Load())
	}
}

func TestRetryAfterGivesUpAfterThree(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":0}}`)
	}))
	defer srv.Close()
	c := &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	err := c.Call(ctx, "sendMessage", map[string]any{"chat_id": 1}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 429 {
		t.Fatal(err)
	}
	if n.Load() != 4 {
		t.Fatal("want 1 call + 3 retries, got", n.Load())
	}
}

func TestAPIErrorDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`)
	}))
	defer srv.Close()
	_, err := (&Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}).SendMessage(ctx, SendMessageParams{ChatID: 1, Text: "*"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 400 || ae.Description != "Bad Request: can't parse entities" {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "sendMessage") {
		t.Fatal("error must name the method:", err)
	}
}

func TestThreadParamGeneralTopic(t *testing.T) {
	if _, ok := ThreadParam("1", false); ok {
		t.Fatal("omit for sends")
	}
	if v, ok := ThreadParam("1", true); !ok || v != 1 {
		t.Fatal("keep for chat action")
	}
	if v, ok := ThreadParam("17585", false); !ok || v != 17585 {
		t.Fatal(v, ok)
	}
	if _, ok := ThreadParam("", true); ok {
		t.Fatal("empty thread id")
	}
	if _, ok := ThreadParam("abc", false); ok {
		t.Fatal("bad thread id")
	}
}

func TestTokenNeverLogged(t *testing.T) {
	const token = "123:T0KEN"
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The transport fails every request. net/http then wraps the error in a
	// *url.Error that holds the full URL with the token.
	hc := &http.Client{Transport: failTransport{}}
	c := &Client{Token: token, BaseURL: "http://127.0.0.1:1", HC: hc, Log: log}
	_, err := c.GetMe(ctx)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "T0KEN") {
		t.Fatal("token in error:", err)
	}
	if !strings.Contains(err.Error(), "getMe") {
		t.Fatal("error must name the method:", err)
	}
	if buf.Len() == 0 {
		t.Fatal("want a log line for the request error")
	}
	if strings.Contains(buf.String(), "T0KEN") {
		t.Fatal("token in log:", buf.String())
	}
	err = c.DownloadFile(ctx, "a/b.jpg", filepath.Join(t.TempDir(), "x"))
	if err == nil || strings.Contains(err.Error(), "T0KEN") {
		t.Fatal("download error:", err)
	}
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestUploadMultipart(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(p, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse: %v", err)
		}
		if r.FormValue("chat_id") != "42" || r.FormValue("caption") != "c" {
			t.Errorf("fields %v", r.MultipartForm.Value)
		}
		f, fh, err := r.FormFile("photo")
		if err != nil {
			t.Errorf("file: %v", err)
		} else {
			b, _ := io.ReadAll(f)
			if string(b) != "PNGDATA" || fh.Filename != "pic.png" {
				t.Errorf("file %q %q", b, fh.Filename)
			}
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9,"chat":{"id":42}}}`)
	}))
	defer srv.Close()
	var m Message
	err := (&Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}).Upload(ctx, "sendPhoto", map[string]string{"chat_id": "42", "caption": "c"}, "photo", p, &m)
	if err != nil || m.MessageID != 9 {
		t.Fatal(m, err)
	}
}

func TestDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/botT/photos/a.jpg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "JPEG")
	}))
	defer srv.Close()
	c := &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
	dst := filepath.Join(t.TempDir(), "sub", "a.jpg")
	if err := c.DownloadFile(ctx, "photos/a.jpg", dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "JPEG" {
		t.Fatal(string(b), err)
	}
	if err := c.DownloadFile(ctx, "missing", filepath.Join(t.TempDir(), "m")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal(err)
	}
}

func TestGetUpdatesAsksForEditedMessages(t *testing.T) {
	var allowed []any
	srv := tgServer(t, func(method string, body map[string]any) any {
		allowed, _ = body["allowed_updates"].([]any)
		return []any{map[string]any{
			"update_id": 9,
			"edited_message": map[string]any{
				"message_id": 4, "chat": map[string]any{"id": 6, "type": "private"}, "text": "fixed text",
			},
		}}
	})
	c := &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
	ups, err := c.GetUpdates(ctx, 9, 0)
	if err != nil || len(ups) != 1 {
		t.Fatal(ups, err)
	}
	if !reflect.DeepEqual(allowed, []any{"message", "edited_message", "callback_query"}) {
		t.Fatal(allowed)
	}
	if e := ups[0].EditedMessage; e == nil || e.MessageID != 4 || e.Text != "fixed text" || ups[0].Message != nil {
		t.Fatalf("%+v", ups[0])
	}
}
