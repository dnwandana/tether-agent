package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// toolNames returns the names of ts as a set.
func toolNames(ts []Tool) map[string]bool {
	out := map[string]bool{}
	for _, t := range ts {
		out[t.Name()] = true
	}
	return out
}

// tool returns the media tool name of m, or fails the test.
func tool(m *MediaSet, name string) Tool {
	for _, t := range m.Tools() {
		if t.Name() == name {
			return t
		}
	}
	panic("no media tool " + name)
}

// mediaCfg returns a media config with only the backend of kind (tts, stt,
// or image) set. For "command", arg is the template; for "endpoint", arg is
// the provider name.
func mediaCfg(kind, backend, arg string) config.MediaConfig {
	b := config.MediaBackend{Backend: backend}
	if backend == "command" {
		b.Command = arg
	} else {
		b.Provider = arg
	}
	var c config.MediaConfig
	switch kind {
	case "tts":
		c.TTS = b
	case "stt":
		c.STT = b
	case "image":
		c.Image = b
	}
	return c
}

// noFFmpeg is a lookPath that finds no program.
func noFFmpeg(string) (string, error) { return "", exec.ErrNotFound }

// mediaServer starts a fake OpenAI-format media endpoint. It records the
// JSON request bodies in got.
func mediaServer(t *testing.T, got *[]map[string]any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k1" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var req map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&req)
			if got != nil {
				*got = append(*got, req)
			}
		}
		switch r.URL.Path {
		case "/v1/audio/speech":
			_, _ = w.Write([]byte("ID3 fake mp3"))
		case "/v1/audio/transcriptions":
			if f, _, err := r.FormFile("file"); err != nil {
				http.Error(w, "no file", http.StatusBadRequest)
				return
			} else {
				f.Close()
			}
			_, _ = io.WriteString(w, `{"text":"hello"}`)
		case "/v1/images/generations":
			if req["prompt"] == "url" {
				_, _ = fmt.Fprintf(w, `{"data":[{"url":%q}]}`, srv.URL+"/img.png")
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString([]byte("\x89PNG fake")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// endpointSet returns a media set whose kind backend is the fake endpoint.
func endpointSet(t *testing.T, kind string, srv *httptest.Server) *MediaSet {
	t.Helper()
	return &MediaSet{
		Cfg:       mediaCfg(kind, "endpoint", ""),
		Providers: map[string]config.Provider{"primary": {BaseURL: srv.URL + "/v1", APIKey: "k1", Model: "m1"}},
		Dir:       t.TempDir(),
	}
}

func TestImageHiddenByDefault(t *testing.T) {
	names := toolNames((&MediaSet{Cfg: config.Default().Media}).Tools())
	if names["image_generate"] || !names["text_to_speech"] || !names["transcribe_audio"] {
		t.Fatal(names)
	}
	if len(toolNames((&MediaSet{}).Tools())) != 0 {
		t.Fatal("an empty backend must hide every tool")
	}
	for _, tl := range (&MediaSet{Cfg: config.Default().Media}).Tools() {
		if tl.Toolset() != SetMedia || tl.ParallelSafe() || !json.Valid(tl.Schema().Parameters) || tl.Schema().Name != tl.Name() {
			t.Fatal("bad tool metadata:", tl.Name())
		}
	}
}

func TestTTSCommandReturnsVoiceTag(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("tts", "command", "sh -c 'echo audio > \"$0\"' {out}"), Dir: t.TempDir()}
	out := runTool(t, tool(m, "text_to_speech"), `{"text":"hi"}`)
	if !strings.HasPrefix(out, "MEDIA:") || !strings.HasSuffix(out, "[[audio_as_voice]]") {
		t.Fatal(out)
	}
	p := strings.Fields(strings.TrimPrefix(out, "MEDIA:"))[0]
	if filepath.Dir(p) != m.Dir || filepath.Ext(p) != ".mp3" {
		t.Fatal("bad path:", p)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "audio\n" {
		t.Fatal(string(b), err)
	}
}

func TestTTSCommandPassesTextAsOneArgument(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("tts", "command", `sh -c 'printf %s "$1" > "$0"' {out} {text}`), Dir: t.TempDir()}
	text := `a "b"; rm -rf / $(id)`
	out := runTool(t, tool(m, "text_to_speech"), fmt.Sprintf(`{"text":%s}`, jsonString(text)))
	p := strings.Fields(strings.TrimPrefix(out, "MEDIA:"))[0]
	if b, _ := os.ReadFile(p); string(b) != text {
		t.Fatal(string(b))
	}
}

func TestCommandUsesEnv(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("tts", "command", `sh -c 'printf %s "$FOO" > "$0"' {out}`), Dir: t.TempDir(),
		Env: []string{"PATH=" + os.Getenv("PATH"), "FOO=bar", "OPENAI_API_KEY=secret"}}
	out := runTool(t, tool(m, "text_to_speech"), `{"text":"hi"}`)
	p := strings.Fields(strings.TrimPrefix(out, "MEDIA:"))[0]
	if b, _ := os.ReadFile(p); string(b) != "bar" {
		t.Fatal(string(b))
	}
}

func TestCommandErrors(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("tts", "command", "sh -c true {out}"), Dir: t.TempDir()}
	if out := runToolErr(t, tool(m, "text_to_speech"), `{"text":"hi"}`); !strings.Contains(out, "did not write") {
		t.Fatal(out)
	}
	if out := runToolErr(t, tool(m, "text_to_speech"), `{"text":"  "}`); !strings.Contains(out, "text is required") {
		t.Fatal(out)
	}
	m = &MediaSet{Cfg: mediaCfg("tts", "command", "no-such-tts-program {text} {out}"), Dir: t.TempDir()}
	if out := runToolErr(t, tool(m, "text_to_speech"), `{"text":"hi"}`); !strings.Contains(out, "no-such-tts-program not found") {
		t.Fatal(out)
	}
	m = &MediaSet{Cfg: mediaCfg("tts", "command", `sh -c 'echo bad voice >&2; exit 3'`), Dir: t.TempDir()}
	if out := runToolErr(t, tool(m, "text_to_speech"), `{"text":"hi"}`); !strings.Contains(out, "bad voice") || !strings.Contains(out, "exit 3") {
		t.Fatal(out)
	}
}

func TestTTSEndpointSendsVoice(t *testing.T) {
	var got []map[string]any
	m := endpointSet(t, "tts", mediaServer(t, &got))
	out := runTool(t, tool(m, "text_to_speech"), `{"text":"hi","voice":"nova"}`)
	if !strings.HasPrefix(out, "MEDIA:"+m.Dir) || !strings.HasSuffix(out, " [[audio_as_voice]]") {
		t.Fatal(out)
	}
	if len(got) != 1 || got[0]["voice"] != "nova" || got[0]["input"] != "hi" || got[0]["model"] != "m1" {
		t.Fatal(got)
	}
}

func TestSTTEndpoint(t *testing.T) {
	m := endpointSet(t, "stt", mediaServer(t, nil))
	env := newEnv(t)
	if err := os.WriteFile(filepath.Join(env.WorkDir, "a.ogg"), []byte("OggS fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A relative path is read from the work folder.
	if out := runToolEnv(t, tool(m, "transcribe_audio"), `{"path":"a.ogg"}`, env); out != "hello" {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, tool(m, "transcribe_audio"), `{"path":"missing.ogg"}`, env); !strings.Contains(out, "missing.ogg") {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, tool(m, "transcribe_audio"), `{}`, env); !strings.Contains(out, "path is required") {
		t.Fatal(out)
	}
}

func TestSTTCommandReadsOutBaseTxt(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("stt", "command", `sh -c 'printf "hi there\n" > "$1.txt"' {in} {out_base}`), Dir: t.TempDir(), lookPath: noFFmpeg}
	in := writeFile(t, "audio")
	if out := runTool(t, tool(m, "transcribe_audio"), fmt.Sprintf(`{"path":%q}`, in)); out != "hi there" {
		t.Fatal(out)
	}
	if left, _ := os.ReadDir(m.Dir); len(left) != 0 {
		t.Fatal("the work files must be removed:", left)
	}
	// A command that writes no text file is an error.
	m.Cfg = mediaCfg("stt", "command", `sh -c true {in} {out_base}`)
	if out := runToolErr(t, tool(m, "transcribe_audio"), fmt.Sprintf(`{"path":%q}`, in)); !strings.Contains(out, ".txt") {
		t.Fatal(out)
	}
}

func TestSTTCommandConvertsWithFFmpeg(t *testing.T) {
	// The fake ffmpeg writes "wav:" and the input path to its last argument.
	bin := t.TempDir()
	fake := filepath.Join(bin, "ff mpeg")
	script := "#!/bin/sh\nfor a; do last=$a; done\nprintf 'wav:%s' \"$6\" > \"$last\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var asked string
	m := &MediaSet{
		Cfg: mediaCfg("stt", "command", `sh -c 'cat "$0" > "$1.txt"' {in} {out_base}`),
		Dir: t.TempDir(),
		lookPath: func(name string) (string, error) {
			asked = name
			return fake, nil
		},
	}
	in := writeFile(t, "audio")
	out := runTool(t, tool(m, "transcribe_audio"), fmt.Sprintf(`{"path":%q}`, in))
	if asked != "ffmpeg" || out != "wav:"+in {
		t.Fatal(asked, out)
	}
	if left, _ := os.ReadDir(m.Dir); len(left) != 0 {
		t.Fatal("the WAV file must be removed:", left)
	}
}

func TestImageEndpointReturnsMedia(t *testing.T) {
	var got []map[string]any
	m := endpointSet(t, "image", mediaServer(t, &got))
	out := runTool(t, tool(m, "image_generate"), `{"prompt":"a cat","size":"512x512"}`)
	if !strings.HasPrefix(out, "MEDIA:") || !strings.HasSuffix(out, ".png") || strings.Contains(out, "audio_as_voice") {
		t.Fatal(out)
	}
	if b, err := os.ReadFile(strings.TrimPrefix(out, "MEDIA:")); err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["prompt"] != "a cat" || got[0]["size"] != "512x512" {
		t.Fatal(got)
	}
	if out := runToolErr(t, tool(m, "image_generate"), `{}`); !strings.Contains(out, "prompt is required") {
		t.Fatal(out)
	}
}

func TestImageCommandGetsDefaultSize(t *testing.T) {
	m := &MediaSet{Cfg: mediaCfg("image", "command", `sh -c 'printf "%s|%s" "$1" "$2" > "$0"' {out} {prompt} {size}`), Dir: t.TempDir()}
	out := runTool(t, tool(m, "image_generate"), `{"prompt":"a dog"}`)
	if b, _ := os.ReadFile(strings.TrimPrefix(out, "MEDIA:")); string(b) != "a dog|1024x1024" {
		t.Fatal(string(b))
	}
}

func TestImageURLUsesDownloadClient(t *testing.T) {
	srv := mediaServer(t, nil)
	m := endpointSet(t, "image", srv)
	// The endpoint answers with a URL on its own loopback address. The
	// guarded download client must refuse it.
	m.Download = GuardedClient(true, 5*time.Second, 3)
	out := runToolErr(t, tool(m, "image_generate"), `{"prompt":"url"}`)
	if !strings.Contains(out, "blocked") {
		t.Fatal(out)
	}
}

func TestEndpointNeedsProvider(t *testing.T) {
	var logs bytes.Buffer
	cfg := mediaCfg("image", "endpoint", "nope")
	cfg.STT = config.MediaBackend{Backend: "command"} // no template
	cfg.TTS = config.MediaBackend{Backend: "carrier"} // unknown backend
	m := &MediaSet{Cfg: cfg, Providers: map[string]config.Provider{"primary": {}}, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	if names := toolNames(m.Tools()); len(names) != 0 {
		t.Fatal(names)
	}
	for _, want := range []string{"nope", "media.image.provider", "media.stt.command", "carrier", "media.tts.backend"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("no warning with %s in:\n%s", want, logs.String())
		}
	}
	// An empty provider uses primary.
	m.Cfg = mediaCfg("image", "endpoint", "")
	if names := toolNames(m.Tools()); !names["image_generate"] {
		t.Fatal(names)
	}
}

func TestTranscribeFunc(t *testing.T) {
	if f := (&MediaSet{}).TranscribeFunc(); f != nil {
		t.Fatal("no STT must give a nil func")
	}
	if f := (&MediaSet{Cfg: mediaCfg("stt", "endpoint", "nope")}).TranscribeFunc(); f != nil {
		t.Fatal("a bad STT setup must give a nil func")
	}
	m := endpointSet(t, "stt", mediaServer(t, nil))
	f := m.TranscribeFunc()
	if f == nil {
		t.Fatal("STT is set up")
	}
	text, err := f(ctx, writeFile(t, "audio"))
	if err != nil || text != "hello" {
		t.Fatal(text, err)
	}
	if _, err := f(ctx, filepath.Join(t.TempDir(), "gone.ogg")); err == nil || errors.Is(err, ErrToolError) {
		t.Fatal("a missing file must give a plain error:", err)
	}
}
