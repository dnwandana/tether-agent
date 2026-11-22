package telegram

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/media"
)

// tempFile makes a small file with the given name in a new folder.
func tempFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// openPolicy is a non-strict policy with no denied paths. It sends each regular file.
var openPolicy = &media.Policy{}

func sender(t *testing.T) (*Sender, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	return &Sender{C: api.client()}, api
}

func TestSendByKind(t *testing.T) {
	s, api := sender(t)
	files := []media.OutFile{
		{Path: tempFile(t, "a.png"), Kind: "photo"},
		{Path: tempFile(t, "b.mp3"), Kind: "audio"},
		{Path: tempFile(t, "c.mp4"), Kind: "video"},
		{Path: tempFile(t, "d.pdf"), Kind: "document"},
		{Path: tempFile(t, "e.wav"), Kind: "audio"},
	}
	if err := s.SendFiles(ctx, 42, "17585", files); err != nil {
		t.Fatal(err)
	}
	want := "sendPhoto sendAudio sendVideo sendDocument sendDocument"
	if strings.Join(api.methods(), " ") != want {
		t.Fatal(api.methods())
	}
	for _, c := range api.all() {
		if c.Body["chat_id"] != "42" || c.Body["message_thread_id"] != "17585" {
			t.Fatal(c.Body)
		}
	}
	fields := []string{"photo", "audio", "video", "document", "document"}
	for i, c := range api.all() {
		if c.Body["_file_field"] != fields[i] {
			t.Fatal(i, c.Body)
		}
	}
}

func TestAsDocumentFlag(t *testing.T) {
	png := tempFile(t, "big.png")
	_, files := openPolicy.ExtractMedia("MEDIA:" + png + "\n[[as_document]]")
	if len(files) != 1 || !files[0].AsDocument {
		t.Fatal(files)
	}
	s, api := sender(t)
	if err := s.SendFiles(ctx, 1, "", files); err != nil {
		t.Fatal(err)
	}
	if api.methods()[0] != "sendDocument" {
		t.Fatal(api.methods())
	}
	if _, ok := api.all()[0].Body["message_thread_id"]; ok {
		t.Fatal("no thread id expected")
	}
}

func TestVoiceFallsBackToAudio(t *testing.T) {
	s, api := sender(t)
	mp3 := tempFile(t, "reply.mp3")
	if err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestVoiceOggSentAsVoice(t *testing.T) {
	s, api := sender(t)
	ogg := tempFile(t, "reply.ogg")
	if err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: ogg, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if c := api.all()[0]; c.Method != "sendVoice" || c.Body["_file_field"] != "voice" {
		t.Fatal(api.methods())
	}
}

func TestVoiceConverted(t *testing.T) {
	s, api := sender(t)
	mp3 := tempFile(t, "reply.mp3")
	ogg := tempFile(t, "conv.ogg")
	s.ConvertVoice = func(_ context.Context, in string) (string, error) {
		if in != mp3 {
			t.Error(in)
		}
		return ogg, nil
	}
	_ = s.SendFiles(ctx, 1, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}})
	if c := api.all()[0]; c.Method != "sendVoice" || c.Body["_file_name"] != "conv.ogg" {
		t.Fatal(api.all())
	}
	s.ConvertVoice = func(context.Context, string) (string, error) { return "", errors.New("ffmpeg: not found") }
	_ = s.SendFiles(ctx, 1, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}})
	if api.all()[1].Method != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestConvertedVoiceRemovedAfterSend(t *testing.T) {
	s, api := sender(t)
	mp3 := tempFile(t, "reply.mp3")
	ogg := tempFile(t, "reply.mp3.ogg")
	s.ConvertVoice = func(context.Context, string) (string, error) { return ogg, nil }
	if err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if api.all()[0].Method != "sendVoice" {
		t.Fatal(api.methods())
	}
	if _, err := os.Stat(ogg); !os.IsNotExist(err) {
		t.Fatal("the converted file must be removed after the send:", err)
	}
	if _, err := os.Stat(mp3); err != nil {
		t.Fatal("the source file must stay:", err)
	}
	// A failed send also removes the converted file.
	api.setReply(func(string, map[string]any) *APIError { return &APIError{Code: 400, Description: "Bad Request"} })
	ogg2 := tempFile(t, "again.mp3.ogg")
	s.ConvertVoice = func(context.Context, string) (string, error) { return ogg2, nil }
	if err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err == nil {
		t.Fatal("the send must fail")
	}
	if _, err := os.Stat(ogg2); !os.IsNotExist(err) {
		t.Fatal("the converted file must be removed after a failed send:", err)
	}
}

func TestPhotoRejectedSentAsDocument(t *testing.T) {
	s, api := sender(t)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendPhoto" {
			return &APIError{Code: 400, Description: "Bad Request: PHOTO_INVALID_DIMENSIONS"}
		}
		return nil
	})
	if err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: tempFile(t, "tall.png"), Kind: "photo"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendPhoto sendDocument" {
		t.Fatal(api.methods())
	}
}

func TestSendFilesErrorsNameTheFile(t *testing.T) {
	s, api := sender(t)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendVideo" {
			return &APIError{Code: 400, Description: "Bad Request: wrong file"}
		}
		return nil
	})
	vid := tempFile(t, "clip.mp4")
	err := s.SendFiles(ctx, 1, "", []media.OutFile{{Path: vid, Kind: "video"}, {Path: tempFile(t, "d.pdf"), Kind: "document"}})
	if err == nil || !strings.Contains(err.Error(), "clip.mp4") {
		t.Fatal(err)
	}
	if api.count("sendDocument") != 1 {
		t.Fatal("one failed file must not stop the others:", api.methods())
	}
	err = s.SendFiles(ctx, 1, "", []media.OutFile{{Path: "/nope/gone.pdf", Kind: "document"}})
	if err == nil || !strings.Contains(err.Error(), "gone.pdf") {
		t.Fatal(err)
	}
}

// ffmpegOrSkip returns the path of ffmpeg, or skips the test when ffmpeg is missing.
func ffmpegOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	return p
}

// sampleWAV writes 0.2 s of 8 kHz mono 16-bit silence as a WAV file.
func sampleWAV(t *testing.T) string {
	t.Helper()
	const rate, samples = 8000, 1600
	data := make([]byte, samples*2)
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, 1) // mono
	b = binary.LittleEndian.AppendUint32(b, rate)
	b = binary.LittleEndian.AppendUint32(b, rate*2)
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	p := filepath.Join(t.TempDir(), "reply.wav")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTTSReplySentAsVoiceWithFFmpeg(t *testing.T) {
	ff := ffmpegOrSkip(t)
	wav := sampleWAV(t)
	conv := FFmpegToOpus(ff)
	out, err := conv(ctx, wav)
	if err != nil {
		t.Skip("ffmpeg cannot encode opus here: ", err)
	}
	defer os.Remove(out)
	if !strings.HasPrefix(filepath.Base(out), "tether-voice-") || filepath.Ext(out) != ".ogg" {
		t.Fatal(out)
	}
	s, api := sender(t)
	s.ConvertVoice = conv
	if err := s.SendFiles(ctx, 5, "", []media.OutFile{{Path: wav, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if c := api.lastCall("sendVoice"); c.Body["_file_field"] != "voice" || !strings.HasSuffix(c.Body["_file_name"].(string), ".ogg") {
		t.Fatal(api.methods(), c.Body)
	}
}

// fakeFFmpeg returns an ffmpeg script that writes "opus" to its output path, the last argument.
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	ff := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nfor a; do out=\"$a\"; done\nprintf opus > \"$out\"\n"
	if err := os.WriteFile(ff, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return ff
}

func TestFFmpegKeepsTheFileNextToTheInput(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	mp3 := tempFile(t, "song.mp3")
	userOgg := mp3 + ".ogg"
	if err := os.WriteFile(userOgg, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := FFmpegToOpus(fakeFFmpeg(t))
	out, err := conv(ctx, mp3)
	if err != nil {
		t.Fatal(err)
	}
	if out == userOgg || filepath.Dir(out) == filepath.Dir(mp3) {
		t.Fatal("the output must be a new temp file, not next to the input:", out)
	}
	if b, _ := os.ReadFile(out); string(b) != "opus" {
		t.Fatal(string(b))
	}
	s, api := sender(t)
	s.ConvertVoice = conv
	if err := s.SendFiles(ctx, 5, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendVoice" {
		t.Fatal(api.methods())
	}
	if b, err := os.ReadFile(userOgg); err != nil || string(b) != "keep" {
		t.Fatal("the send changed or removed the file of the user:", string(b), err)
	}
	// The temp dir holds only the file of the first conv call. The send removed its own file.
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "tether-voice-*.ogg")); len(left) != 1 || left[0] != out {
		t.Fatal(left)
	}
}

func TestNoFFmpegSendsAudio(t *testing.T) {
	if FFmpegToOpus("") != nil {
		t.Fatal("no ffmpeg must give a nil converter")
	}
	s, api := sender(t)
	s.ConvertVoice = FFmpegToOpus("")
	mp3 := tempFile(t, "reply.mp3")
	if err := s.SendFiles(ctx, 5, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestFFmpegFailureFallsBackToAudio(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bad := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'Unknown encoder libopus' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	conv := FFmpegToOpus(bad)
	t.Setenv("TMPDIR", t.TempDir())
	mp3 := tempFile(t, "reply.mp3")
	if _, err := conv(ctx, mp3); err == nil || !strings.Contains(err.Error(), "Unknown encoder libopus") {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "tether-voice-*")); len(left) != 0 {
		t.Fatal("a failed conversion must leave no file:", left)
	}
	s, api := sender(t)
	s.ConvertVoice = conv
	if err := s.SendFiles(ctx, 5, "", []media.OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestFFmpegToOpusWithinTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	slow := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	conv := FFmpegToOpusWithin(slow, 200*time.Millisecond)
	start := time.Now()
	if _, err := conv(ctx, tempFile(t, "reply.mp3")); err == nil || !strings.Contains(err.Error(), "took more than 200ms") || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
	if FFmpegToOpusWithin("", time.Second) != nil {
		t.Fatal("no ffmpeg must give a nil converter")
	}
}

// SendFilesOpts with NoSound adds disable_notification to each upload. SendFiles keeps the sound.
func TestSendFilesOptsNoSound(t *testing.T) {
	s, api := sender(t)
	files := []media.OutFile{{Path: tempFile(t, "a.png"), Kind: "photo"}, {Path: tempFile(t, "d.pdf"), Kind: "document"}}
	if err := s.SendFilesOpts(ctx, 42, "", files, FileOpts{NoSound: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range api.all() {
		if c.Body["disable_notification"] != "true" {
			t.Fatal(c.Method, c.Body)
		}
	}
	n := len(api.all())
	if err := s.SendFiles(ctx, 42, "", files); err != nil {
		t.Fatal(err)
	}
	for _, c := range api.all()[n:] {
		if _, ok := c.Body["disable_notification"]; ok {
			t.Fatal(c.Method, c.Body)
		}
	}
}
