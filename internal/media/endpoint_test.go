package media

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// endpoint returns an Endpoint for the fake server srv.
func endpoint(srv *httptest.Server, model string) *Endpoint {
	return &Endpoint{P: config.Provider{BaseURL: srv.URL + "/v1/", APIKey: "k", Model: model}, HC: srv.Client()}
}

func TestSpeechWritesMP3(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("Content-Type") != "application/json" {
			t.Error(r.Method, r.URL.Path, r.Header)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte("ID3fake"))
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "a.mp3")
	if err := (&Endpoint{P: config.Provider{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "tts-1"}, HC: srv.Client()}).Speech(ctx, "hi", "alloy", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "ID3fake" {
		t.Fatal(string(b))
	}
	if body["model"] != "tts-1" || body["input"] != "hi" || body["voice"] != "alloy" || body["response_format"] != "mp3" {
		t.Fatal(body)
	}
}

func TestSpeechDefaultVoice(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte("ID3"))
	}))
	defer srv.Close()
	if err := endpoint(srv, "tts-1").Speech(ctx, "hi", "", filepath.Join(t.TempDir(), "a.mp3")); err != nil {
		t.Fatal(err)
	}
	if body["voice"] != DefaultVoice {
		t.Fatal(body)
	}
}

func TestTranscribeMultipart(t *testing.T) {
	in := filepath.Join(t.TempDir(), "voice.ogg")
	audio := bytes.Repeat([]byte("OggS"), 1000)
	os.WriteFile(in, audio, 0o600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Error(r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("model") != "whisper-1" {
			t.Error("model", r.FormValue("model"))
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		got, _ := io.ReadAll(f)
		if h.Filename != "voice.ogg" || !bytes.Equal(got, audio) {
			t.Error("file", h.Filename, len(got))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"hello"}`))
	}))
	defer srv.Close()
	text, err := endpoint(srv, "whisper-1").Transcribe(ctx, in)
	if err != nil || text != "hello" {
		t.Fatal(text, err)
	}
}

func TestTranscribeMissingFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	}))
	defer srv.Close()
	if _, err := endpoint(srv, "whisper-1").Transcribe(ctx, filepath.Join(t.TempDir(), "none.ogg")); err == nil {
		t.Fatal("want an error")
	}
}

func TestImageB64(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Error(r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"data":[{"b64_json":"iVBORw0KGgo="}]}`))
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "i.png")
	if err := endpoint(srv, "gpt-image-1").Image(ctx, "a cat", "1024x1024", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal(b)
	}
	if body["model"] != "gpt-image-1" || body["prompt"] != "a cat" || body["size"] != "1024x1024" || body["n"] != float64(1) {
		t.Fatal(body)
	}
}

func TestImageURL(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var dlAuth string
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["size"]; ok {
			t.Error("empty size must be left out", body)
		}
		w.Write([]byte(`{"data":[{"url":"` + srv.URL + `/img.png"}]}`))
	})
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) {
		dlAuth = r.Header.Get("Authorization")
		w.Write([]byte("PNGDATA"))
	})
	e := endpoint(srv, "dall-e-3")
	e.Download = srv.Client() // the guard is off in the test
	out := filepath.Join(t.TempDir(), "i.png")
	if err := e.Image(ctx, "a cat", "", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "PNGDATA" {
		t.Fatal(string(b))
	}
	if dlAuth != "" {
		t.Fatal("the API key went to the image URL")
	}
}

func TestImageEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "i.png")
	err := endpoint(srv, "m").Image(ctx, "a cat", "", out)
	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a failed call left a file")
	}
}

func TestHTTPErrorText(t *testing.T) {
	const key = "sk-proj-abcdefghijklmnopqrstuvwxyz0123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + key + `"}}`))
	}))
	defer srv.Close()
	e := endpoint(srv, "tts-1")
	e.P.APIKey = key
	out := filepath.Join(t.TempDir(), "a.mp3")
	err := e.Speech(ctx, "hi", "alloy", out)
	if err == nil || !strings.Contains(err.Error(), "audio/speech: HTTP 401") || !strings.Contains(err.Error(), "Incorrect API key") || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a failed call left a file")
	}
	if _, err := e.Transcribe(ctx, writeTemp(t)); err == nil || !strings.Contains(err.Error(), "audio/transcriptions: HTTP 401") || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	if err := e.Image(ctx, "x", "", out); err == nil || !strings.Contains(err.Error(), "images/generations: HTTP 401") || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
}

func TestNoBaseURL(t *testing.T) {
	e := &Endpoint{P: config.Provider{APIKey: "k"}}
	if err := e.Speech(ctx, "hi", "", filepath.Join(t.TempDir(), "a.mp3")); err == nil {
		t.Fatal("want an error")
	}
}

func writeTemp(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(p, []byte("RIFF"), 0o600)
	return p
}
