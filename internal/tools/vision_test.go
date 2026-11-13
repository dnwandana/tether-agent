package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// recordingProvider is a provider.Provider that answers each request with reply and
// records the requests.
type recordingProvider struct {
	name, reply string
	usage       provider.Usage

	mu       sync.Mutex
	requests []provider.Request
}

func (p *recordingProvider) Name() string  { return p.name }
func (p *recordingProvider) Model() string { return "vision-model" }
func (p *recordingProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: p.reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &p.usage}
	close(ch)
	return ch, nil
}

func recording(reply string) *recordingProvider {
	return &recordingProvider{name: "primary", reply: reply}
}

// pngBytes returns a small PNG image.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngFile writes a small PNG file and returns its path.
func pngFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(p, pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestViewImageSendsImagePart(t *testing.T) {
	fp := recording("A red chart.")
	out := runTool(t, VisionTool{Prov: fp, MaxBytes: 10 << 20}, fmt.Sprintf(`{"path":%q,"question":"What color?"}`, pngFile(t)))
	if out != "A red chart." || len(fp.requests) != 1 {
		t.Fatal(out)
	}
	req := fp.requests[0]
	m := req.Messages[0]
	if len(req.Messages) != 1 || len(m.Images) != 1 || m.Content != "What color?" || m.Images[0].MIME != "image/png" || len(req.Tools) != 0 {
		t.Fatalf("%+v", req)
	}
}

func TestViewImageDefaultQuestionAndRelativePath(t *testing.T) {
	fp := recording("ok")
	p := pngFile(t)
	env := newEnv(t)
	env.WorkDir = filepath.Dir(p)
	runToolEnv(t, VisionTool{Prov: fp}, `{"path":"chart.png"}`, env)
	if fp.requests[0].Messages[0].Content != "Describe this image in detail." {
		t.Fatal(fp.requests[0].Messages[0].Content)
	}
}

func TestViewImageURL(t *testing.T) {
	data := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer srv.Close()
	fp := recording("a pixel")
	out := runTool(t, VisionTool{Prov: fp, Client: GuardedClient(false, 5*time.Second, 3)}, fmt.Sprintf(`{"url":%q}`, srv.URL+"/x.png"))
	if out != "a pixel" || !bytes.Equal(fp.requests[0].Messages[0].Images[0].Data, data) {
		t.Fatal(out)
	}
}

func TestViewImageURLGuarded(t *testing.T) {
	fp := recording("no")
	out := runToolErr(t, VisionTool{Prov: fp, Client: GuardedClient(true, 2*time.Second, 3)}, `{"url":"http://169.254.169.254/x.png"}`)
	if !strings.Contains(out, "private or internal address") || len(fp.requests) != 0 {
		t.Fatal(out)
	}
	out = runToolErr(t, VisionTool{Prov: fp, Client: GuardedClient(true, 2*time.Second, 3)}, `{"url":"file:///etc/passwd"}`)
	if !strings.Contains(out, "http or https") {
		t.Fatal(out)
	}
}

func TestViewImageRefusesNonImageAndLargeFiles(t *testing.T) {
	fp := recording("no")
	if out := runToolErr(t, VisionTool{Prov: fp}, fmt.Sprintf(`{"path":%q}`, writeFile(t, "plain text"))); !strings.Contains(out, "not an image") {
		t.Fatal(out)
	}
	if out := runToolErr(t, VisionTool{Prov: fp, MaxBytes: 10}, fmt.Sprintf(`{"path":%q}`, pngFile(t))); !strings.Contains(out, "larger than 10 bytes") {
		t.Fatal(out)
	}
	if out := runToolErr(t, VisionTool{Prov: fp}, `{}`); !strings.Contains(out, "path or url is required") {
		t.Fatal(out)
	}
	if out := runToolErr(t, VisionTool{Prov: fp}, `{"path":"/no/such/file.png"}`); !strings.Contains(out, "cannot read") {
		t.Fatal(out)
	}
	if len(fp.requests) != 0 {
		t.Fatal("no model call for a bad image")
	}
}

func TestViewImageSchema(t *testing.T) {
	v := VisionTool{}
	if v.Name() != "view_image" || v.Toolset() != SetVision || !v.ParallelSafe() || !strings.Contains(string(v.Schema().Parameters), "question") {
		t.Fatal("name, toolset, parallel flag, or schema")
	}
}

func TestNotRegisteredWithoutVisionProvider(t *testing.T) {
	prim, other := recording("p"), recording("o")
	provs := map[string]provider.Provider{"primary": prim, "eyes": other}
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"primary": {Model: "glm-4.7"}, "eyes": {Model: "gpt-4o", Vision: true}}
	if _, ok := VisionProvider(&cfg, provs); ok {
		t.Fatal("no vision provider: view_image is not registered")
	}
	cfg.Agent.VisionProvider = "eyes"
	if p, ok := VisionProvider(&cfg, provs); !ok || p != other {
		t.Fatal("agent.vision_provider wins")
	}
	cfg.Agent.VisionProvider = "missing"
	if _, ok := VisionProvider(&cfg, provs); ok {
		t.Fatal("an unknown vision_provider gives nothing")
	}
	cfg.Agent.VisionProvider = ""
	cfg.Providers["primary"] = config.Provider{Model: "gpt-4o", Vision: true}
	if p, ok := VisionProvider(&cfg, provs); !ok || p != prim {
		t.Fatal("the primary with vision: true")
	}
}

func TestImagesFromMarkers(t *testing.T) {
	got := ImagesFromMarkers("look [image: /tmp/a b.png]\n[image: /tmp/c.jpg] and [image: ] [image received; no vision model is configured]")
	if len(got) != 2 || got[0] != "/tmp/a b.png" || got[1] != "/tmp/c.jpg" {
		t.Fatalf("%q", got)
	}
}

func TestDescribeImage(t *testing.T) {
	fp := recording("  A duck waves.  ")
	out, err := DescribeImage(ctx, fp, pngFile(t), "Describe this sticker.", nil)
	if err != nil || out != "A duck waves." || len(fp.requests) != 1 {
		t.Fatalf("%q %v", out, err)
	}
	m := fp.requests[0].Messages[0]
	if m.Content != "Describe this sticker." || len(m.Images) != 1 || m.Images[0].MIME != "image/png" {
		t.Fatalf("%+v", m)
	}
	if _, err := DescribeImage(ctx, recording(" "), pngFile(t), "q", nil); err == nil {
		t.Fatal("an empty answer must be an error")
	}
	txt := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(txt, []byte("hello"), 0o600)
	if _, err := DescribeImage(ctx, recording("x"), txt, "q", nil); err == nil {
		t.Fatal("a text file is not an image")
	}
}

// usageCall is one call of a provider.RecordUsage.
type usageCall struct {
	sessionID, task string
	prov            provider.Provider
	out             provider.Result
}

// recorder returns a provider.RecordUsage that appends each call to calls.
func recorder(calls *[]usageCall) provider.RecordUsage {
	var mu sync.Mutex
	return func(_ context.Context, sessionID, task string, prov provider.Provider, out provider.Result) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, usageCall{sessionID, task, prov, out})
	}
}

// failingVision fails in Stream, or in the stream when inStream is true.
type failingVision struct{ inStream bool }

func (failingVision) Name() string  { return "primary" }
func (failingVision) Model() string { return "vision-model" }
func (p failingVision) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	if !p.inStream {
		return nil, errors.New("provider down")
	}
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Type: provider.EventError, Err: errors.New("stream cut")}
	close(ch)
	return ch, nil
}

func TestViewImageRecordsUsage(t *testing.T) {
	fp := recording("A red chart.")
	// Collect fills the times only when they are 0, so the comparison below is exact.
	fp.usage = provider.Usage{InputTokens: 1200, OutputTokens: 30, CacheReadTokens: 800, FirstTokenMS: 5, TotalMS: 9}
	var calls []usageCall
	env := newEnv(t)
	env.SessionID = "s1"
	out := runToolEnv(t, VisionTool{Prov: fp, Record: recorder(&calls)}, fmt.Sprintf(`{"path":%q}`, pngFile(t)), env)
	if out != "A red chart." || len(calls) != 1 {
		t.Fatalf("%q %d", out, len(calls))
	}
	c := calls[0]
	if c.sessionID != "s1" || c.task != "vision" || c.prov != fp || c.out.Usage != fp.usage || c.out.Text != "A red chart." {
		t.Fatalf("%+v", c)
	}
}

// A billed call with an empty answer is recorded. The tool still returns an error.
func TestViewImageRecordsEmptyAnswer(t *testing.T) {
	var calls []usageCall
	env := newEnv(t)
	env.SessionID = "s1"
	out := runToolEnvErr(t, VisionTool{Prov: recording(" "), Record: recorder(&calls)}, fmt.Sprintf(`{"path":%q}`, pngFile(t)), env)
	if !strings.Contains(out, "empty answer") || len(calls) != 1 || calls[0].task != "vision" || calls[0].sessionID != "s1" {
		t.Fatalf("%q %+v", out, calls)
	}
}

func TestViewImageFailedCallRecordsNothing(t *testing.T) {
	for _, p := range []failingVision{{inStream: false}, {inStream: true}} {
		var calls []usageCall
		runToolErr(t, VisionTool{Prov: p, Record: recorder(&calls)}, fmt.Sprintf(`{"path":%q}`, pngFile(t)))
		if len(calls) != 0 {
			t.Fatalf("inStream=%v: a failed call must record nothing: %+v", p.inStream, calls)
		}
	}
	// A bad image makes no model call, so it records nothing.
	var calls []usageCall
	runToolErr(t, VisionTool{Prov: recording("x"), Record: recorder(&calls)}, `{"path":"/no/such/file.png"}`)
	if len(calls) != 0 {
		t.Fatal("a bad image must record nothing")
	}
}

func TestViewImageNilEnvAndNilRecord(t *testing.T) {
	var calls []usageCall
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, pngFile(t)))
	if _, err := (VisionTool{Prov: recording("ok"), Record: recorder(&calls)}).Run(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].sessionID != "" || calls[0].task != "vision" {
		t.Fatalf("a nil env gives an empty session ID: %+v", calls)
	}
	// A nil Record records nothing and does not panic.
	if out, err := (VisionTool{Prov: recording("ok")}).Run(ctx, raw, newEnv(t)); err != nil || out != "ok" {
		t.Fatal(out, err)
	}
}

func TestDescribeImageRecordsSticker(t *testing.T) {
	fp := recording("  A duck waves.  ")
	fp.usage = provider.Usage{InputTokens: 700, OutputTokens: 9, FirstTokenMS: 5, TotalMS: 9}
	var calls []usageCall
	if out, err := DescribeImage(ctx, fp, pngFile(t), "Describe this sticker.", recorder(&calls)); err != nil || out != "A duck waves." {
		t.Fatal(out, err)
	}
	if len(calls) != 1 || calls[0].sessionID != "" || calls[0].task != "sticker" || calls[0].prov != fp || calls[0].out.Usage != fp.usage {
		t.Fatalf("%+v", calls)
	}
	// An empty answer is an error, but the billed call is recorded.
	if _, err := DescribeImage(ctx, recording(" "), pngFile(t), "q", recorder(&calls)); err == nil || len(calls) != 2 || calls[1].task != "sticker" {
		t.Fatal(err, calls)
	}
	// A failed call and a file that is not an image record nothing.
	if _, err := DescribeImage(ctx, failingVision{}, pngFile(t), "q", recorder(&calls)); err == nil || len(calls) != 2 {
		t.Fatal(err, calls)
	}
	txt := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(txt, []byte("hello"), 0o600)
	if _, err := DescribeImage(ctx, recording("x"), txt, "q", recorder(&calls)); err == nil || len(calls) != 2 {
		t.Fatal(err, calls)
	}
}

func TestViewImageRefusesProtectedPath(t *testing.T) {
	const want = "is a protected path. The tool does not send it to the provider."
	fp := recording("no")
	p := pngFile(t)
	if out := runToolErr(t, VisionTool{Prov: fp, Protected: []string{p}}, fmt.Sprintf(`{"path":%q}`, p)); !strings.Contains(out, p+" "+want) {
		t.Fatal(out)
	}
	link := filepath.Join(t.TempDir(), "link.png")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if out := runToolErr(t, VisionTool{Prov: fp, Protected: []string{p}}, fmt.Sprintf(`{"path":%q}`, link)); !strings.Contains(out, want) {
		t.Fatal(out)
	}
	if out := runToolErr(t, VisionTool{Prov: fp, Protected: []string{filepath.Dir(p) + "/"}}, fmt.Sprintf(`{"path":%q}`, p)); !strings.Contains(out, want) {
		t.Fatal(out)
	}
	if len(fp.requests) != 0 {
		t.Fatal("no model call for a protected path")
	}
	// A nil Protected checks nothing.
	if out := runTool(t, VisionTool{Prov: recording("ok")}, fmt.Sprintf(`{"path":%q}`, p)); out != "ok" {
		t.Fatal(out)
	}
}

func TestViewImageDownloadTimeoutField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	fp := recording("no")
	tool := VisionTool{Prov: fp, Client: GuardedClient(false, 30*time.Second, 3), DownloadTimeout: 100 * time.Millisecond}
	start := time.Now()
	out := runToolErr(t, tool, fmt.Sprintf(`{"url":%q}`, srv.URL+"/x.png"))
	if !strings.Contains(out, "cannot download the image") || time.Since(start) > 2*time.Second || len(fp.requests) != 0 {
		t.Fatal(out, time.Since(start))
	}
	if (VisionTool{}).downloadTimeout() != 60*time.Second || tool.downloadTimeout() != 100*time.Millisecond {
		t.Fatal((VisionTool{}).downloadTimeout(), tool.downloadTimeout())
	}
}

func TestDescribeImageWithinSizeLimit(t *testing.T) {
	png := pngFile(t)
	if _, err := DescribeImageWithin(ctx, recording("x"), png, "q", nil, 10); err == nil || !strings.Contains(err.Error(), "larger than 10 bytes") {
		t.Fatal(err)
	}
	out, err := DescribeImageWithin(ctx, recording("  A duck.  "), png, "q", nil, 0)
	if err != nil || out != "A duck." {
		t.Fatalf("%q %v", out, err)
	}
}
