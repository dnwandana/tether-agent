package tools

import (
	"bytes"
	"context"
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
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
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
	out, err := DescribeImage(ctx, fp, pngFile(t), "Describe this sticker.")
	if err != nil || out != "A duck waves." || len(fp.requests) != 1 {
		t.Fatalf("%q %v", out, err)
	}
	m := fp.requests[0].Messages[0]
	if m.Content != "Describe this sticker." || len(m.Images) != 1 || m.Images[0].MIME != "image/png" {
		t.Fatalf("%+v", m)
	}
	if _, err := DescribeImage(ctx, recording(" "), pngFile(t), "q"); err == nil {
		t.Fatal("an empty answer must be an error")
	}
	txt := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(txt, []byte("hello"), 0o600)
	if _, err := DescribeImage(ctx, recording("x"), txt, "q"); err == nil {
		t.Fatal("a text file is not an image")
	}
}
