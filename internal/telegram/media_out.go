package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/media"
)

// maxUploadBytes is the Bot API limit for a file upload.
const maxUploadBytes = 50 << 20

// Sender sends the files of an answer.
type Sender struct {
	C *Client
	// ConvertVoice converts an audio file to OGG/Opus for a voice note. Nil
	// sends the audio as a file. FFmpegToOpus gives the ffmpeg converter.
	ConvertVoice func(ctx context.Context, in string) (string, error)
}

// FileOpts are the options of SendFilesOpts.
type FileOpts struct {
	// NoSound sends each file with no sound.
	NoSound bool
}

// SendFiles sends each file by its kind, with the sound. It calls SendFilesOpts with the zero FileOpts.
func (s *Sender) SendFiles(ctx context.Context, chatID int64, threadID string, files []media.OutFile) error {
	return s.SendFilesOpts(ctx, chatID, threadID, files, FileOpts{})
}

// SendFilesOpts sends each file by its kind, with the options o. A failed file does not stop
// the others. The error names each file that failed.
func (s *Sender) SendFilesOpts(ctx context.Context, chatID int64, threadID string, files []media.OutFile, o FileOpts) error {
	var errs []error
	for _, f := range files {
		if err := s.sendFile(ctx, chatID, threadID, f, o); err != nil {
			errs = append(errs, fmt.Errorf("send %s: %w", filepath.Base(f.Path), err))
		}
	}
	return errors.Join(errs...)
}

func (s *Sender) sendFile(ctx context.Context, chatID int64, threadID string, f media.OutFile, o FileOpts) error {
	st, err := os.Stat(f.Path)
	if err != nil {
		return err
	}
	if st.Size() > maxUploadBytes {
		return fmt.Errorf("the file has %d bytes; the Telegram upload limit is 50 MB", st.Size())
	}
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if id, ok := ThreadParam(threadID, false); ok {
		fields["message_thread_id"] = strconv.Itoa(id)
	}
	if o.NoSound {
		fields["disable_notification"] = "true"
	}
	ext := strings.ToLower(filepath.Ext(f.Path))
	switch f.Kind {
	case "photo":
		if f.AsDocument {
			return s.upload(ctx, "sendDocument", "document", fields, f.Path)
		}
		err := s.upload(ctx, "sendPhoto", "photo", fields, f.Path)
		if isBadRequest(err) {
			// For example, a photo with too large dimensions. A document
			// has no such limits.
			return s.upload(ctx, "sendDocument", "document", fields, f.Path)
		}
		return err
	case "audio":
		if f.AsVoice {
			if ext == ".ogg" || ext == ".opus" || ext == ".oga" {
				return s.upload(ctx, "sendVoice", "voice", fields, f.Path)
			}
			if s.ConvertVoice != nil {
				out, err := s.ConvertVoice(ctx, f.Path)
				if err == nil {
					err = s.upload(ctx, "sendVoice", "voice", fields, out)
					// The converted file is a temp file. Without this remove, each voice
					// reply stays on the disk.
					if out != f.Path {
						if rerr := os.Remove(out); rerr != nil && !os.IsNotExist(rerr) {
							s.C.logger().Warn("telegram: cannot remove the converted voice file", "path", out, "err", rerr.Error())
						}
					}
					return err
				}
				s.C.logger().Info("telegram voice conversion failed, send an audio file", "err", err.Error())
			}
		}
		// sendAudio takes only MP3 and M4A.
		if ext == ".mp3" || ext == ".m4a" {
			return s.upload(ctx, "sendAudio", "audio", fields, f.Path)
		}
		return s.upload(ctx, "sendDocument", "document", fields, f.Path)
	case "video":
		return s.upload(ctx, "sendVideo", "video", fields, f.Path)
	}
	return s.upload(ctx, "sendDocument", "document", fields, f.Path)
}

func (s *Sender) upload(ctx context.Context, method, field string, fields map[string]string, path string) error {
	return s.C.Upload(ctx, method, fields, field, path, nil)
}

// ffmpegTimeout limits one voice conversion.
const ffmpegTimeout = 60 * time.Second

// FFmpegToOpus returns FFmpegToOpusWithin with the default time limit.
func FFmpegToOpus(ffmpegPath string) func(ctx context.Context, in string) (string, error) {
	return FFmpegToOpusWithin(ffmpegPath, 0)
}

// FFmpegToOpusWithin returns a converter that makes an OGG/Opus voice note from an
// audio file with ffmpeg. The output is a new temp file, so a conversion never
// overwrites a file of the user. The Sender removes it after the send. With no
// ffmpeg (an empty path), the result is nil, so the Sender sends an audio file.
// The timeout limits one conversion. A value of 0 or less uses 60 s.
func FFmpegToOpusWithin(ffmpegPath string, timeout time.Duration) func(ctx context.Context, in string) (string, error) {
	if ffmpegPath == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = ffmpegTimeout
	}
	return func(ctx context.Context, in string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		tmp, err := os.CreateTemp("", "tether-voice-*.ogg")
		if err != nil {
			return "", fmt.Errorf("ffmpeg: cannot create the output file: %w", err)
		}
		out := tmp.Name()
		_ = tmp.Close()
		cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-loglevel", "error", "-i", in,
			"-c:a", "libopus", "-b:a", "48k", "-ac", "1", out)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			_ = os.Remove(out)
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[len(msg)-300:]
			}
			if ctx.Err() != nil {
				return "", fmt.Errorf("ffmpeg: the conversion to opus took more than %s", timeout)
			}
			return "", fmt.Errorf("ffmpeg: the conversion to opus failed: %w: %s", err, msg)
		}
		if st, err := os.Stat(out); err != nil || st.Size() == 0 {
			_ = os.Remove(out)
			return "", fmt.Errorf("ffmpeg: the conversion to opus gave no output file")
		}
		return out, nil
	}
}
