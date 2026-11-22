package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/media"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// upProv answers "ok" at once.
type upProv struct{}

func (upProv) Name() string  { return "primary" }
func (upProv) Model() string { return "fake-model" }
func (upProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	out <- provider.Event{Type: provider.EventText, Delta: "ok"}
	out <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	close(out)
	return out, nil
}

type upHarness struct {
	ch       *Channel
	gw       *gateway.Gateway
	st       *sessions.Store
	srv      *httptest.Server
	dir      string
	stopRecv context.CancelFunc
}

// newUpHarness returns a running dashboard channel with the message route on a test server.
// mut changes the upload deps.
func newUpHarness(t *testing.T, mut func(*UploadDeps)) *upHarness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := sessions.New(d)
	cfg := config.Default()
	cfg.Dashboard = config.DashboardConfig{Enabled: true, Listen: "127.0.0.1:0", SessionHours: 24, MaxUploadMB: 20}
	log := slog.New(slog.DiscardHandler)
	ch, err := NewChannel(Deps{Cfg: cfg.Dashboard, DB: d, Sessions: st, Redact: func(s string) string { return s },
		Partial: func(string) int { return 0 }, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var prov provider.Provider = upProv{}
	ag := &agent.Agent{Prov: prov, Providers: map[string]provider.Provider{"primary": prov},
		Tools: tools.NewRegistry(), Store: st, Cfg: &cfg, SpillDir: t.TempDir(), MediaInbox: dir}
	gw := &gateway.Gateway{Deps: &gateway.Deps{Cfg: &cfg, Store: st, Agent: ag, Providers: ag.Providers, Log: log},
		Channels: map[string]gateway.Channel{ChannelName: ch}, Log: log}
	workCtx, stopWork := context.WithCancel(context.Background())
	recvCtx, stopRecv := context.WithCancel(workCtx)
	ch.SetTurnContext(workCtx)
	ran := make(chan struct{})
	go func() { defer close(ran); _ = ch.Run(recvCtx, gw) }()
	upWait(t, func() bool { return ch.Gateway() != nil })
	ud := UploadDeps{MediaDir: dir, Limits: media.Limits{MaxBytes: 20 << 20, InlineTextMax: 100 * 1024},
		MaxRequestBytes: 20 << 20, Now: time.Now}
	if mut != nil {
		mut(&ud)
	}
	mux := http.NewServeMux()
	NewMessages(ch, ud).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		stopWork()
		<-ran
		_ = ch.Shutdown(context.Background())
	})
	return &upHarness{ch: ch, gw: gw, st: st, srv: srv, dir: dir, stopRecv: stopRecv}
}

func upWait(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the condition did not come true in 5 s")
		}
	}
}

type upFile struct {
	field, name, mime string
	body              []byte
}

// upForm returns the content type and the body of a message form, in the order of the page.
func upForm(clientID, text string, files ...upFile) (string, []byte) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if clientID != "" {
		_ = mw.WriteField("client_id", clientID)
	}
	if text != "" {
		_ = mw.WriteField("text", text)
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+f.field+`"; filename="`+f.name+`"`)
		h.Set("Content-Type", f.mime)
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(f.body)
	}
	_ = mw.Close()
	return mw.FormDataContentType(), b.Bytes()
}

// post sends a form. It does not call t.Fatal, so a goroutine can call it.
func (h *upHarness) post(ctype string, body io.Reader) (int, map[string]any, error) {
	resp, err := http.Post(h.srv.URL+"/api/messages", ctype, body)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

func (h *upHarness) send(t *testing.T, clientID, text string, files ...upFile) (int, map[string]any) {
	t.Helper()
	ct, body := upForm(clientID, text, files...)
	code, got, err := h.post(ct, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return code, got
}

// userTexts returns the saved user messages of the dashboard session.
func (h *upHarness) userTexts() []string {
	s, err := h.st.Active(context.Background(), SessionKey)
	if err != nil || s == nil {
		return nil
	}
	msgs, _ := h.st.Messages(context.Background(), s.ID)
	var out []string
	for _, m := range msgs {
		if m.Msg.Role == provider.RoleUser {
			out = append(out, m.Msg.Content)
		}
	}
	return out
}

// waitTexts waits until n user messages are saved and no turn runs.
func (h *upHarness) waitTexts(t *testing.T, n int) []string {
	t.Helper()
	upWait(t, func() bool {
		return len(h.userTexts()) == n && !slices.Contains(h.gw.BusyKeys(), SessionKey)
	})
	return h.userTexts()
}

// saved returns the names of the files in the media inbox.
func (h *upHarness) saved(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

var savedName = regexp.MustCompile(`^\d+-[0-9a-f]{16}\.[a-z0-9]+$`)

func TestUploadTextAndFile(t *testing.T) {
	h := newUpHarness(t, nil)
	code, got := h.send(t, "c-1", "summarize this", upFile{"files", "notes.md", "text/markdown", []byte("# Notes\nline\n")})
	if code != http.StatusOK || got["client_id"] != "c-1" || got["files"] != float64(1) {
		t.Fatal(code, got)
	}
	names := h.saved(t)
	if len(names) != 1 || !savedName.MatchString(names[0]) || !strings.HasSuffix(names[0], ".md") || strings.Contains(names[0], "notes") {
		t.Fatalf("the saved name is <ms>-<random><ext>: %v", names)
	}
	path := filepath.Join(h.dir, names[0])
	want := "[The user sent a text document: 'notes.md'. Its content has been included below. The file is also saved at: " +
		path + "]\n\n[Content of notes.md]:\n# Notes\nline\n\n\nsummarize this"
	if texts := h.waitTexts(t, 1); !strings.Contains(texts[0], want) {
		t.Fatalf("%q", texts[0])
	}
}

func TestUploadUnsupportedType(t *testing.T) {
	h := newUpHarness(t, nil)
	code, got := h.send(t, "", "is this safe?", upFile{"files", "setup.exe", "application/x-msdownload", []byte("MZ")})
	if code != http.StatusOK || got["files"] != float64(0) {
		t.Fatal(code, got)
	}
	if names := h.saved(t); len(names) != 0 {
		t.Fatal("a refused file is not saved:", names)
	}
	texts := h.waitTexts(t, 1)
	if !strings.Contains(texts[0], "Unsupported document type '.exe'. Supported types: ") || !strings.HasSuffix(texts[0], "\n\nis this safe?") {
		t.Fatalf("%q", texts[0])
	}
}

func TestUploadTooLarge(t *testing.T) {
	h := newUpHarness(t, func(d *UploadDeps) { d.MaxRequestBytes = 1 << 20 })
	code, got := h.send(t, "c-2", "", upFile{"files", "big.zip", "application/zip", bytes.Repeat([]byte("x"), 2<<20)})
	if code != http.StatusRequestEntityTooLarge || got["error"] != "The upload is larger than dashboard.max_upload_mb (1 MB)." {
		t.Fatal(code, got)
	}
	time.Sleep(200 * time.Millisecond)
	if names, texts := h.saved(t), h.userTexts(); len(names) != 0 || len(texts) != 0 {
		t.Fatal(names, texts)
	}
}

func TestUploadFileOverFileLimit(t *testing.T) {
	h := newUpHarness(t, func(d *UploadDeps) { d.Limits.MaxBytes = 1 << 20; d.MaxRequestBytes = 4 << 20 })
	code, got := h.send(t, "", "", upFile{"files", "a.pdf", "application/pdf", bytes.Repeat([]byte("x"), 1<<20+1)})
	if code != http.StatusOK || got["files"] != float64(0) {
		t.Fatal(code, got)
	}
	if names := h.saved(t); len(names) != 0 {
		t.Fatal(names)
	}
	if texts := h.waitTexts(t, 1); !strings.Contains(texts[0], "[file too large: a.pdf has 1048577 bytes; the limit is 1 MB]") {
		t.Fatalf("%q", texts[0])
	}
}

func TestUploadVoiceNoSTT(t *testing.T) {
	h := newUpHarness(t, nil)
	code, got := h.send(t, "", "", upFile{"voice", "blob", "audio/webm;codecs=opus", []byte{0x1A, 0x45, 0xDF, 0xA3, 'x'}})
	if code != http.StatusOK || got["files"] != float64(1) {
		t.Fatal(code, got)
	}
	if names := h.saved(t); len(names) != 1 || !savedName.MatchString(names[0]) || !strings.HasSuffix(names[0], ".webm") {
		t.Fatal(names)
	}
	if texts := h.waitTexts(t, 1); !strings.Contains(texts[0], "[voice message received, but transcription is not set up]") {
		t.Fatalf("%q", texts[0])
	}
}

func TestUploadVoiceSTT(t *testing.T) {
	var heard atomic.Value
	h := newUpHarness(t, func(d *UploadDeps) {
		d.STT = func(_ context.Context, path string) (string, error) {
			heard.Store(path)
			return "turn on the lights", nil
		}
	})
	if code, got := h.send(t, "", "", upFile{"voice", "blob", "audio/ogg;codecs=opus", []byte("OggS\x00\x02")}); code != http.StatusOK {
		t.Fatal(code, got)
	}
	names := h.saved(t)
	if len(names) != 1 || !strings.HasSuffix(names[0], ".ogg") || heard.Load() != filepath.Join(h.dir, names[0]) {
		t.Fatal(names, heard.Load())
	}
	if texts := h.waitTexts(t, 1); !strings.Contains(texts[0], `[The user sent a voice message~ Here's what they said: "turn on the lights"]`) {
		t.Fatalf("%q", texts[0])
	}
}

// A restart starts while an upload is in progress. The drain waits for the upload, and a
// retry with the same client message id does not send a second message.
func TestUploadRetrySameClientID(t *testing.T) {
	h := newUpHarness(t, nil)
	pr, pw := io.Pipe()
	// End the open upload before the harness cleanup closes the server. A failed check
	// must not leave srv.Close waiting for the body forever.
	t.Cleanup(func() { _ = pw.CloseWithError(errors.New("test ended")) })
	mw := multipart.NewWriter(pw)
	first := make(chan int, 1)
	go func() {
		code, _, _ := h.post(mw.FormDataContentType(), pr)
		first <- code
	}()
	_ = mw.WriteField("client_id", "c-7")
	_ = mw.WriteField("text", "deploy notes")
	fw, _ := mw.CreateFormFile("files", "plan.md")
	_, _ = fw.Write([]byte("# Plan\n"))
	upWait(t, func() bool { return h.ch.Inflight() == 1 })

	h.stopRecv()
	upWait(t, h.ch.Stopping)
	if n := h.ch.Inflight(); n != 1 {
		t.Fatalf("the upload must hold the drain: Inflight() = %d", n)
	}

	// The page retries after a proxy timeout. The retry waits for the first upload.
	ct, body := upForm("c-7", "deploy notes", upFile{"files", "plan.md", "text/markdown", []byte("# Plan\nstep 1\n")})
	type result struct {
		code int
		got  map[string]any
	}
	second := make(chan result, 1)
	go func() {
		code, got, _ := h.post(ct, bytes.NewReader(body))
		second <- result{code, got}
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case r := <-second:
		t.Fatalf("the retry must wait for the first upload: %+v", r)
	default:
	}

	_, _ = fw.Write([]byte("step 1\n"))
	_ = mw.Close()
	_ = pw.Close()
	if code := <-first; code != http.StatusOK {
		t.Fatal(code)
	}
	r := <-second
	if r.code != http.StatusOK || r.got["client_id"] != "c-7" || r.got["files"] != float64(1) {
		t.Fatalf("the retry gets the first result: %+v", r)
	}
	h.waitTexts(t, 1)
	time.Sleep(200 * time.Millisecond)
	if texts := h.userTexts(); len(texts) != 1 {
		t.Fatalf("one message, not %d: %q", len(texts), texts)
	}
	if names := h.saved(t); len(names) != 1 {
		t.Fatal("the retry saves no file:", names)
	}
	upWait(t, func() bool { return h.ch.Inflight() == 0 })

	// A new message after recvCtx ended gets 503.
	code, got := h.send(t, "c-8", "hello")
	if code != http.StatusServiceUnavailable || got["error"] != "The gateway is stopping." {
		t.Fatal(code, got)
	}
}

func TestUploadClientIDExpires(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).UnixNano())
	h := newUpHarness(t, func(d *UploadDeps) { d.Now = func() time.Time { return time.Unix(0, clock.Load()) } })
	if code, got := h.send(t, "c-9", "one"); code != http.StatusOK {
		t.Fatal(code, got)
	}
	h.waitTexts(t, 1)
	if code, got := h.send(t, "c-9", "one"); code != http.StatusOK || got["client_id"] != "c-9" {
		t.Fatal(code, got)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(h.userTexts()); n != 1 {
		t.Fatalf("a retry in 10 min submits nothing: %d messages", n)
	}
	clock.Add(int64(10*time.Minute + time.Second))
	if code, got := h.send(t, "c-9", "one"); code != http.StatusOK {
		t.Fatal(code, got)
	}
	h.waitTexts(t, 2)
}
