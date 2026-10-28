package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// defaultVisionMaxBytes is the size limit of one image (10 MB).
const defaultVisionMaxBytes = 10 << 20

// defaultVisionQuestion is the question when the call gives none.
const defaultVisionQuestion = "Describe this image in detail."

// NoVisionMarker replaces each image marker when no vision model is configured.
const NoVisionMarker = "[image received; no vision model is configured]"

// imageMarkerRe finds the "[image: <path>]" markers of a user message.
var imageMarkerRe = regexp.MustCompile(`\[image: ([^\]\n]+)\]`)

// ImagesFromMarkers returns the paths of the "[image: <path>]" markers in text, in order.
func ImagesFromMarkers(text string) []string {
	var out []string
	for _, m := range imageMarkerRe.FindAllStringSubmatch(text, -1) {
		if p := strings.TrimSpace(m[1]); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ReplaceImageMarkers returns text with each "[image: <path>]" marker replaced by repl.
func ReplaceImageMarkers(text, repl string) string {
	return imageMarkerRe.ReplaceAllLiteralString(text, repl)
}

// VisionProvider returns the provider of view_image: agent.vision_provider when it is set,
// else the primary provider when its entry has vision: true. ok is false when no vision
// provider exists. Then view_image is not registered.
func VisionProvider(cfg *config.Config, provs map[string]provider.Provider) (provider.Provider, bool) {
	if cfg == nil {
		return nil, false
	}
	if name := strings.TrimSpace(cfg.Agent.VisionProvider); name != "" {
		p, ok := provs[name]
		return p, ok && p != nil
	}
	if cfg.Providers["primary"].Vision {
		p, ok := provs["primary"]
		return p, ok && p != nil
	}
	return nil, false
}

// VisionTool is the view_image tool. It sends one image and a question to the
// vision provider and returns the answer text.
type VisionTool struct {
	Prov provider.Provider
	// Client fetches an image URL. Use GuardedClient. Nil refuses URLs.
	Client *http.Client
	// MaxBytes limits the image size. Zero or less means 10 MB.
	MaxBytes int64
}

type visionArgs struct {
	Path     string `json:"path"`
	URL      string `json:"url"`
	Question string `json:"question"`
}

// Name returns "view_image".
func (t VisionTool) Name() string { return "view_image" }

// Toolset returns the vision toolset.
func (t VisionTool) Toolset() string { return SetVision }

// ParallelSafe returns true. The tool reads only.
func (t VisionTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (t VisionTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "view_image",
		Description: "Look at an image with the vision model and get a text answer. Give a local file path " +
			"(for example the path of an [image: <path>] marker or a browser screenshot) or an http(s) URL. " +
			"Ask a specific question to get a specific answer.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "A local image file. A relative path starts at the working folder."},
    "url": {"type": "string", "description": "An http or https image URL. Use path or url, not both."},
    "question": {"type": "string", "description": "The question about the image. The default is \"` + defaultVisionQuestion + `\""}
  }
}`),
	}
}

func (t VisionTool) maxBytes() int64 {
	if t.MaxBytes > 0 {
		return t.MaxBytes
	}
	return defaultVisionMaxBytes
}

// Run loads the image and asks the vision provider.
func (t VisionTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a visionArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	a.Path, a.URL = strings.TrimSpace(a.Path), strings.TrimSpace(a.URL)
	if t.Prov == nil {
		return "", toolErr("no vision model is configured. Set agent.vision_provider, or set vision: true on the primary provider")
	}
	var (
		img provider.Image
		err error
	)
	switch {
	case a.Path != "":
		img, err = t.loadFile(resolvePath(env, a.Path))
	case a.URL != "":
		img, err = t.loadURL(ctx, a.URL)
	default:
		return "", toolErr("path or url is required")
	}
	if err != nil {
		return "", err
	}
	q := strings.TrimSpace(a.Question)
	if q == "" {
		q = defaultVisionQuestion
	}
	return askVision(ctx, t.Prov, img, q)
}

// askVision sends one user message with the question and the image to prov and returns
// the answer text. An empty answer is an error.
func askVision(ctx context.Context, prov provider.Provider, img provider.Image, q string) (string, error) {
	ch, err := prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: q, Images: []provider.Image{img}}},
	})
	if err != nil {
		return "", toolErr("the vision model %s failed: %v", prov.Name(), err)
	}
	res, err := provider.Collect(ch, nil)
	if err != nil {
		return "", toolErr("the vision model %s failed: %v", prov.Name(), err)
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", toolErr("the vision model %s gave an empty answer", prov.Name())
	}
	return res.Text, nil
}

// loadFile reads an image file.
func (t VisionTool) loadFile(path string) (provider.Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return provider.Image{}, toolErr("cannot read the image %s: %v", path, err)
	}
	if info.IsDir() {
		return provider.Image{}, toolErr("%s is a folder, not an image", path)
	}
	if info.Size() > t.maxBytes() {
		return provider.Image{}, toolErr("the image %s is larger than %d bytes", path, t.maxBytes())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return provider.Image{}, toolErr("cannot read the image %s: %v", path, err)
	}
	return imageOf(path, data)
}

// loadURL downloads an image with the guarded client.
func (t VisionTool) loadURL(ctx context.Context, raw string) (provider.Image, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return provider.Image{}, toolErr("the url must be an http or https URL")
	}
	if t.Client == nil {
		return provider.Image{}, toolErr("image URLs are not available in this process. Download the image and give its path")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return provider.Image{}, toolErr("bad url: %v", err)
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := t.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return provider.Image{}, toolErr("cannot download the image %s: %v", raw, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return provider.Image{}, toolErr("cannot download the image %s: HTTP %d", raw, resp.StatusCode)
	}
	limit := t.maxBytes()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return provider.Image{}, toolErr("cannot download the image %s: %v", raw, err)
	}
	if int64(len(data)) > limit {
		return provider.Image{}, toolErr("the image %s is larger than %d bytes", raw, limit)
	}
	return imageOf(raw, data)
}

// imageOf checks that data is an image and returns it with its MIME type.
func imageOf(source string, data []byte) (provider.Image, error) {
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return provider.Image{}, toolErr("%s is not an image (%s)", source, mime)
	}
	return provider.Image{Path: source, MIME: mime, Data: data}, nil
}

// LoadImageFile reads an image file for an inline image part. It returns an error when
// the file is missing, larger than maxBytes (zero or less means 10 MB), or not an image.
func LoadImageFile(path string, maxBytes int64) (provider.Image, error) {
	img, err := VisionTool{MaxBytes: maxBytes}.loadFile(path)
	if err != nil {
		return provider.Image{}, errors.New(errorText("view_image", err))
	}
	return img, nil
}

// DescribeImage sends the image file at path and the prompt to the vision provider prov and
// returns the trimmed answer. The file must be an image of 10 MB or less. An empty answer is
// an error. The Telegram inbox uses it to describe a sticker.
func DescribeImage(ctx context.Context, prov provider.Provider, path, prompt string) (string, error) {
	if prov == nil {
		return "", errors.New("no vision model is configured")
	}
	img, err := VisionTool{}.loadFile(path)
	if err != nil {
		return "", errors.New(errorText("view_image", err))
	}
	out, err := askVision(ctx, prov, img, prompt)
	if err != nil {
		return "", errors.New(errorText("view_image", err))
	}
	return strings.TrimSpace(out), nil
}
