package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/media"
)

// The media backend names of media.*.backend.
const (
	mediaCommand  = "command"
	mediaEndpoint = "endpoint"
)

// defaultImageSize is the {size} value of an image command when the call
// gives no size. It is the OpenAI default.
const defaultImageSize = "1024x1024"

// ffmpegWAV converts {in} to 16 kHz mono WAV at {out}, as whisper.cpp needs.
// The caller puts the quoted ffmpeg path before it.
const ffmpegWAV = " -nostdin -y -loglevel error -i {in} -ar 16000 -ac 1 -c:a pcm_s16le {out}"

// mediaSeq makes the output file names unique in one process.
var mediaSeq atomic.Uint64

// MediaSet builds the media tools from the media config. Dir is
// cache/media/: the work folder of the commands and the output folder. HC is
// the client for the endpoint calls; nil uses http.DefaultClient. Download is
// the client for an image URL in an endpoint response; set it to a guarded
// client (GuardedClient), because the URL comes from the network. Env is the
// base environment of the commands; nil uses os.Environ(). The programs run
// only during a call.
type MediaSet struct {
	Cfg       config.MediaConfig
	Providers map[string]config.Provider
	Dir       string
	HC        *http.Client
	Download  *http.Client
	Env       []string
	Log       *slog.Logger

	// lookPath finds ffmpeg. Tests replace it; nil uses exec.LookPath.
	lookPath func(string) (string, error)
}

// mediaRunner is one configured backend. Exactly one field is set.
type mediaRunner struct {
	cmd *media.CommandBackend
	ep  *media.Endpoint
}

// runner returns the backend of the media.<kind> config, or nil when the tool
// is off. The warning is not empty when the config is set but not usable.
func (m *MediaSet) runner(kind string, b config.MediaBackend) (r *mediaRunner, warning string) {
	switch b.Backend {
	case "":
		return nil, ""
	case mediaCommand:
		if strings.TrimSpace(b.Command) == "" {
			return nil, fmt.Sprintf("media.%s.command is empty", kind)
		}
		return &mediaRunner{cmd: &media.CommandBackend{Template: b.Command, WorkDir: m.Dir, Env: m.Env}}, ""
	case mediaEndpoint:
		name := b.Provider
		if name == "" {
			name = "primary"
		}
		p, ok := m.Providers[name]
		if !ok {
			return nil, fmt.Sprintf("media.%s.provider %q is not in providers", kind, name)
		}
		return &mediaRunner{ep: &media.Endpoint{P: p, HC: m.HC, Download: m.Download}}, ""
	default:
		return nil, fmt.Sprintf("media.%s.backend %q is not known; use command or endpoint", kind, b.Backend)
	}
}

func (m *MediaSet) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// Tools returns the media tools that are set up. An empty backend hides its
// tool. A backend that is set but not usable hides its tool and logs a
// warning.
func (m *MediaSet) Tools() []Tool {
	var out []Tool
	add := func(kind, name string, b config.MediaBackend, build func(*mediaRunner) Tool) {
		r, warning := m.runner(kind, b)
		if warning != "" {
			m.log().Warn("media: tool is off: "+warning, "tool", name)
		}
		if r != nil {
			out = append(out, build(r))
		}
	}
	add("tts", "text_to_speech", m.Cfg.TTS, m.ttsTool)
	add("stt", "transcribe_audio", m.Cfg.STT, m.sttTool)
	add("image", "image_generate", m.Cfg.Image, m.imageTool)
	return out
}

// TranscribeFunc returns the speech-to-text func for Telegram voice notes,
// or nil when STT is not set up.
func (m *MediaSet) TranscribeFunc() func(ctx context.Context, path string) (string, error) {
	r, _ := m.runner("stt", m.Cfg.STT)
	if r == nil {
		return nil
	}
	return func(ctx context.Context, path string) (string, error) {
		return m.transcribe(ctx, r, path)
	}
}

// outPath returns a new file path in Dir with the prefix and the extension.
func (m *MediaSet) outPath(prefix, ext string) string {
	name := fmt.Sprintf("%s-%s-%d%s", prefix, time.Now().UTC().Format("20060102-150405"), mediaSeq.Add(1), ext)
	return filepath.Join(m.Dir, name)
}

// checkOutput returns an error when a command did not write the file path.
func checkOutput(path, key string) error {
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return toolErr("the command finished but did not write %s; check %s", path, key)
	}
	return nil
}

func (m *MediaSet) ttsTool(r *mediaRunner) Tool {
	return funcTool{
		name: "text_to_speech",
		set:  SetMedia,
		desc: "Convert text to speech audio. Returns MEDIA:<path> [[audio_as_voice]]. " +
			"Put this tag in your answer to send the audio to the user; Telegram sends it as a voice note.",
		params: `{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "The text to speak."},
    "voice": {"type": "string", "description": "Optional. The voice name for an OpenAI-format endpoint, for example alloy or nova. A command backend uses the voice of its command."}
  },
  "required": ["text"]
}`,
		run: func(ctx context.Context, raw json.RawMessage, _ *Env) (string, error) {
			var a struct {
				Text  string `json:"text"`
				Voice string `json:"voice"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Text) == "" {
				return "", toolErr("text is required")
			}
			out := m.outPath("tts", ".mp3")
			if r.cmd != nil {
				if err := r.cmd.Run(ctx, map[string]string{"text": a.Text, "out": out}); err != nil {
					return "", mediaErr(ctx, err)
				}
				if err := checkOutput(out, "media.tts.command"); err != nil {
					return "", err
				}
			} else if err := r.ep.Speech(ctx, a.Text, a.Voice, out); err != nil {
				return "", mediaErr(ctx, err)
			}
			return "MEDIA:" + out + " [[audio_as_voice]]", nil
		},
	}
}

func (m *MediaSet) sttTool(r *mediaRunner) Tool {
	return funcTool{
		name: "transcribe_audio",
		set:  SetMedia,
		desc: "Transcribe an audio file (speech to text). Returns the text.",
		params: `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "The path of the audio file, for example a voice note. A relative path starts at the work folder."}
  },
  "required": ["path"]
}`,
		run: func(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Path) == "" {
				return "", toolErr("path is required")
			}
			text, err := m.transcribe(ctx, r, resolvePath(env, a.Path))
			if err != nil {
				return "", mediaErr(ctx, err)
			}
			if text == "" {
				return "[no speech found in the audio]", nil
			}
			return text, nil
		},
	}
}

// transcribe returns the text of the audio file at path. The command
// backend converts the file to 16 kHz mono WAV with ffmpeg first when ffmpeg
// is installed, and then reads {out_base}.txt. It removes its work files.
func (m *MediaSet) transcribe(ctx context.Context, r *mediaRunner, path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the audio file %s: %w", path, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder, not an audio file", path)
	}
	if r.ep != nil {
		text, err := r.ep.Transcribe(ctx, path)
		return strings.TrimSpace(text), err
	}

	base := m.outPath("stt", "")
	in := path
	look := m.lookPath
	if look == nil {
		look = exec.LookPath
	}
	if ff, err := look("ffmpeg"); err == nil {
		wav := base + ".wav"
		defer os.Remove(wav)
		conv := &media.CommandBackend{Template: quoteWord(ff) + ffmpegWAV, WorkDir: m.Dir, Env: m.Env}
		if err := conv.Run(ctx, map[string]string{"in": path, "out": wav}); err != nil {
			return "", fmt.Errorf("convert the audio to 16 kHz WAV: %w", err)
		}
		in = wav
	}
	txt := base + ".txt"
	defer os.Remove(txt)
	if err := r.cmd.Run(ctx, map[string]string{"in": in, "out_base": base, "out": txt}); err != nil {
		return "", err
	}
	b, err := os.ReadFile(txt)
	if err != nil {
		return "", fmt.Errorf("the command did not write %s; check media.stt.command: %w", txt, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// quoteWord quotes s for media.SplitTemplate, so that s stays one word.
func quoteWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m *MediaSet) imageTool(r *mediaRunner) Tool {
	return funcTool{
		name: "image_generate",
		set:  SetMedia,
		desc: "Generate an image from a text prompt. Returns MEDIA:<path>. " +
			"Put this tag in your answer to send the image to the user.",
		params: `{
  "type": "object",
  "properties": {
    "prompt": {"type": "string", "description": "A detailed description of the image."},
    "size": {"type": "string", "description": "Optional. The image size, for example 1024x1024."}
  },
  "required": ["prompt"]
}`,
		run: func(ctx context.Context, raw json.RawMessage, _ *Env) (string, error) {
			var a struct {
				Prompt string `json:"prompt"`
				Size   string `json:"size"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Prompt) == "" {
				return "", toolErr("prompt is required")
			}
			out := m.outPath("image", ".png")
			if r.cmd != nil {
				size := a.Size
				if size == "" {
					size = defaultImageSize
				}
				if err := r.cmd.Run(ctx, map[string]string{"prompt": a.Prompt, "size": size, "out": out}); err != nil {
					return "", mediaErr(ctx, err)
				}
				if err := checkOutput(out, "media.image.command"); err != nil {
					return "", err
				}
			} else if err := r.ep.Image(ctx, a.Prompt, a.Size, out); err != nil {
				return "", mediaErr(ctx, err)
			}
			return "MEDIA:" + out, nil
		},
	}
}

// mediaErr returns err as a message for the model. A stopped call keeps the
// context error.
func mediaErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrToolError) {
		return err
	}
	return toolErr("%v", err)
}
