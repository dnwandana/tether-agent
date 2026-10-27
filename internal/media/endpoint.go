package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// The timeouts of the endpoint calls.
const (
	speechTimeout     = 60 * time.Second
	transcribeTimeout = 120 * time.Second
	imageTimeout      = 120 * time.Second
)

// DefaultVoice is the speech voice when the caller gives none. It is the
// Hermes DEFAULT_OPENAI_VOICE.
const DefaultVoice = "alloy"

const (
	// maxErrorBody is the number of body bytes in an HTTP error.
	maxErrorBody = 512
	// maxJSONBody bounds a JSON response. A b64_json image can be large.
	maxJSONBody = 64 << 20
	// maxFileBody bounds an audio or image file from the endpoint.
	maxFileBody = 100 << 20
)

// Endpoint calls the OpenAI-format media paths of a provider entry. P gives
// the base URL, the API key, and the model. A nil HC uses
// http.DefaultClient. Download is the client for an image URL in a
// response. Set it to a guarded client (tools.GuardedClient), because the
// URL comes from the network. A nil Download uses HC.
type Endpoint struct {
	P        config.Provider
	HC       *http.Client
	Download *http.Client
}

// Speech sends text to {base}/audio/speech and writes the MP3 body to
// outPath. An empty voice uses DefaultVoice.
func (e *Endpoint) Speech(ctx context.Context, text, voice, outPath string) error {
	if voice == "" {
		voice = DefaultVoice
	}
	ctx, cancel := context.WithTimeout(ctx, speechTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{"model": e.P.Model, "input": text, "voice": voice, "response_format": "mp3"})
	if err != nil {
		return fmt.Errorf("audio/speech: encode the request: %w", err)
	}
	resp, err := e.post(ctx, "audio/speech", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeFile(outPath, resp.Body, "audio/speech")
}

// Transcribe sends the audio file at path to {base}/audio/transcriptions as
// multipart form data, and returns the text of the JSON response. The file
// streams from disk, so it is not held in memory.
func (e *Endpoint) Transcribe(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("audio/transcriptions: open the audio file: %w", err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeForm(mw, e.P.Model, filepath.Base(path), f))
	}()
	resp, err := e.post(ctx, "audio/transcriptions", mw.FormDataContentType(), pr)
	// The writer goroutine stops when the pipe closes.
	pr.Close()
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return "", fmt.Errorf("audio/transcriptions: %w", err)
	}
	return out.Text, nil
}

// writeForm writes the fields model and file.
func writeForm(mw *multipart.Writer, model, name string, f io.Reader) error {
	if err := mw.WriteField("model", model); err != nil {
		return err
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	return mw.Close()
}

// Image sends prompt to {base}/images/generations and writes the first
// image to outPath. It accepts data[0].b64_json and data[0].url. An empty
// size is left out of the request.
func (e *Endpoint) Image(ctx context.Context, prompt, size, outPath string) error {
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	req := map[string]any{"model": e.P.Model, "prompt": prompt, "n": 1}
	if size != "" {
		req["size"] = size
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("images/generations: encode the request: %w", err)
	}
	resp, err := e.post(ctx, "images/generations", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return fmt.Errorf("images/generations: %w", err)
	}
	switch {
	case len(out.Data) == 0 || (out.Data[0].B64JSON == "" && out.Data[0].URL == ""):
		return errors.New("images/generations: the response has no image")
	case out.Data[0].B64JSON != "":
		img, err := base64.StdEncoding.DecodeString(out.Data[0].B64JSON)
		if err != nil {
			return fmt.Errorf("images/generations: bad b64_json: %w", err)
		}
		return writeFile(outPath, bytes.NewReader(img), "images/generations")
	default:
		return e.download(ctx, out.Data[0].URL, outPath)
	}
}

// download gets an image URL with the Download client. It sends no API key.
func (e *Endpoint) download(ctx context.Context, url, outPath string) error {
	hc := e.Download
	if hc == nil {
		hc = e.client()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("images/generations: bad image URL: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("images/generations: download the image: %s", e.redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("images/generations: download the image: HTTP %d", resp.StatusCode)
	}
	return writeFile(outPath, resp.Body, "images/generations")
}

// post sends one request to {base}/path with the API key. It returns the
// response only for a 2xx status.
func (e *Endpoint) post(ctx context.Context, path, contentType string, body io.Reader) (*http.Response, error) {
	if e.P.BaseURL == "" {
		return nil, fmt.Errorf("%s: the provider has no base_url", path)
	}
	url := strings.TrimRight(e.P.BaseURL, "/") + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("%s: bad request URL: %s", path, e.redact(err.Error()))
	}
	req.Header.Set("Content-Type", contentType)
	if e.P.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.P.APIKey)
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", path, e.redact(err.Error()))
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4*maxErrorBody))
	return nil, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, shortBody(e.redact(string(b))))
}

func (e *Endpoint) client() *http.Client {
	if e.HC == nil {
		return http.DefaultClient
	}
	return e.HC
}

// redact removes the API key and other credentials from s.
func (e *Endpoint) redact(s string) string {
	if len(e.P.APIKey) >= 4 {
		s = strings.ReplaceAll(s, e.P.APIKey, "[REDACTED]")
	}
	return security.NewRedactor(nil).Redact(s)
}

// shortBody cuts s to maxErrorBody bytes on a rune border, on one line.
func shortBody(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxErrorBody {
		return s
	}
	return strings.ToValidUTF8(s[:maxErrorBody], "") + "…"
}

func decodeJSON(r io.Reader, v any) error {
	if err := json.NewDecoder(io.LimitReader(r, maxJSONBody)).Decode(v); err != nil {
		return fmt.Errorf("bad JSON response: %w", err)
	}
	return nil
}

// writeFile writes r to path through a temporary file, so that a failed
// call leaves no partial file.
func writeFile(path string, r io.Reader, what string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("%s: create folder: %w", what, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("%s: create file: %w", what, err)
	}
	n, err := io.Copy(tmp, io.LimitReader(r, maxFileBody+1))
	if err == nil && n > maxFileBody {
		err = fmt.Errorf("the file is larger than %d MB", maxFileBody>>20)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("the response body is empty")
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("%s: write %s: %w", what, path, err)
	}
	return nil
}
