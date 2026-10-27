package telegram

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestExtractMediaTagsAndFlags(t *testing.T) {
	png := tempFile(t, "chart.png")
	mp3 := tempFile(t, "reply.mp3")
	clean, files := openPolicy.ExtractMedia("Here.\nMEDIA:" + png + "\nMEDIA:" + mp3 + " [[audio_as_voice]]")
	if strings.Contains(clean, "MEDIA:") || strings.Contains(clean, "[[") || len(files) != 2 || files[0].Kind != "photo" || !files[1].AsVoice {
		t.Fatal(clean, files)
	}
	if clean != "Here." || files[0].Path != png || files[1].Kind != "audio" || files[0].AsVoice {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestExtractMediaQuotedAndSpaced(t *testing.T) {
	a := tempFile(t, "a.png")
	b := tempFile(t, "b.pdf")
	clean, files := openPolicy.ExtractMedia("See MEDIA:`" + a + "` and MEDIA: \"" + b + "\" now.")
	if len(files) != 2 || files[0].Path != a || files[1].Path != b || files[1].Kind != "document" {
		t.Fatalf("%q %+v", clean, files)
	}
	if strings.Contains(clean, "MEDIA") {
		t.Fatal(clean)
	}
}

func TestBarePathWithExtensionExtracted(t *testing.T) {
	pdf := tempFile(t, "report.pdf")
	clean, files := openPolicy.ExtractMedia("The report is ready: " + pdf + "\nRead it soon.")
	if len(files) != 1 || files[0].Path != pdf || files[0].Kind != "document" {
		t.Fatal(files)
	}
	if strings.Contains(clean, pdf) || !strings.Contains(clean, "Read it soon.") {
		t.Fatal(clean)
	}
}

func TestBarePathRules(t *testing.T) {
	pdf := tempFile(t, "report.pdf")
	cases := []string{
		"See https://example.com" + pdf,       // inside a URL
		"Run `cat " + pdf + "` first",         // inline code
		"```\n" + pdf + "\n```",               // fenced code
		"A missing /tmp/does/not/exist.pdf",   // no such file
		"Unknown " + filepath.Dir(pdf) + "/x", // no extension
	}
	for _, c := range cases {
		if clean, files := openPolicy.ExtractMedia(c); len(files) != 0 || clean != c {
			t.Errorf("%q → %q %+v", c, clean, files)
		}
	}
}

func TestMediaTagInCodeNotExtracted(t *testing.T) {
	// Hermes #35695: examples in code and quotes are not attachments.
	png := tempFile(t, "x.png")
	for _, text := range []string{
		"Use this form:\n```text\nMEDIA:" + png + "\n```",
		"Write `MEDIA:" + png + "` in the reply.",
		"> MEDIA:" + png,
	} {
		if clean, files := openPolicy.ExtractMedia(text); len(files) != 0 || clean != text {
			t.Error(clean, files)
		}
	}
}

func TestDuplicatePathsSentOnce(t *testing.T) {
	png := tempFile(t, "x.png")
	_, files := openPolicy.ExtractMedia("MEDIA:" + png + "\nAlso " + png)
	if len(files) != 1 {
		t.Fatal(files)
	}
}

func TestNonexistentPathIgnored(t *testing.T) {
	clean, files := openPolicy.ExtractMedia("Chart:\nMEDIA:/nope.png")
	if len(files) != 0 || !strings.Contains(clean, "[missing file: /nope.png]") || strings.Contains(clean, "MEDIA:") {
		t.Fatal(clean, files)
	}
}

func TestDirectoryNotSent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pics.png")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clean, files := openPolicy.ExtractMedia("MEDIA:" + dir)
	if len(files) != 0 || !strings.Contains(clean, "[missing file: ") {
		t.Fatal(clean, files)
	}
}

func TestKindFromExtension(t *testing.T) {
	cases := map[string]string{
		"a.png": "photo", "a.JPG": "photo", "a.jpeg": "photo", "a.webp": "photo", "a.gif": "photo",
		"a.mp3": "audio", "a.ogg": "audio", "a.oga": "audio", "a.opus": "audio", "a.m4a": "audio", "a.wav": "audio",
		"a.mp4": "video", "a.mov": "video", "a.webm": "video",
		"a.pdf": "document", "a.zip": "document", "a.svg": "document",
	}
	for name, want := range cases {
		if got := mediaKind(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func sender(t *testing.T) (*Sender, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	return &Sender{C: api.client()}, api
}

func TestSendByKind(t *testing.T) {
	s, api := sender(t)
	files := []OutFile{
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
	if err := s.SendFiles(ctx, 1, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestVoiceOggSentAsVoice(t *testing.T) {
	s, api := sender(t)
	ogg := tempFile(t, "reply.ogg")
	if err := s.SendFiles(ctx, 1, "", []OutFile{{Path: ogg, Kind: "audio", AsVoice: true}}); err != nil {
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
	_ = s.SendFiles(ctx, 1, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}})
	if c := api.all()[0]; c.Method != "sendVoice" || c.Body["_file_name"] != "conv.ogg" {
		t.Fatal(api.all())
	}
	s.ConvertVoice = func(context.Context, string) (string, error) { return "", errors.New("ffmpeg: not found") }
	_ = s.SendFiles(ctx, 1, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}})
	if api.all()[1].Method != "sendAudio" {
		t.Fatal(api.methods())
	}
}

func TestConvertedVoiceRemovedAfterSend(t *testing.T) {
	s, api := sender(t)
	mp3 := tempFile(t, "reply.mp3")
	ogg := tempFile(t, "reply.mp3.ogg")
	s.ConvertVoice = func(context.Context, string) (string, error) { return ogg, nil }
	if err := s.SendFiles(ctx, 1, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
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
	if err := s.SendFiles(ctx, 1, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err == nil {
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
	if err := s.SendFiles(ctx, 1, "", []OutFile{{Path: tempFile(t, "tall.png"), Kind: "photo"}}); err != nil {
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
	err := s.SendFiles(ctx, 1, "", []OutFile{{Path: vid, Kind: "video"}, {Path: tempFile(t, "d.pdf"), Kind: "document"}})
	if err == nil || !strings.Contains(err.Error(), "clip.mp4") {
		t.Fatal(err)
	}
	if api.count("sendDocument") != 1 {
		t.Fatal("one failed file must not stop the others:", api.methods())
	}
	err = s.SendFiles(ctx, 1, "", []OutFile{{Path: "/nope/gone.pdf", Kind: "document"}})
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
	if err := s.SendFiles(ctx, 5, "", []OutFile{{Path: wav, Kind: "audio", AsVoice: true}}); err != nil {
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
	if err := s.SendFiles(ctx, 5, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
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
	if err := s.SendFiles(ctx, 5, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
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
	if err := s.SendFiles(ctx, 5, "", []OutFile{{Path: mp3, Kind: "audio", AsVoice: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.methods(), " ") != "sendAudio" {
		t.Fatal(api.methods())
	}
}

// openPolicy is a non-strict policy with no denied paths. It sends each regular file.
var openPolicy = &MediaPolicy{}

// policyEnv returns a lookup function for NewMediaPolicy that reads only env.
func policyEnv(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// writeAt makes the file p with its folders and sets its modification time to age ago.
func writeAt(t *testing.T, p string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestMediaPolicyRefusesProtectedPaths(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	pol := NewMediaPolicy(data, policyEnv(nil), nil)
	for _, p := range []string{
		writeAt(t, filepath.Join(data, ".env"), time.Hour),
		writeAt(t, filepath.Join(data, "config.yaml"), time.Hour),
		writeAt(t, filepath.Join(data, "state.db"), time.Hour),
		writeAt(t, filepath.Join(data, "state.db-wal"), 0),
		writeAt(t, filepath.Join(data, "skills", "x", "SKILL.md"), 0),
		writeAt(t, filepath.Join(home, ".ssh", "id_ed25519"), time.Hour),
		writeAt(t, filepath.Join(home, ".aws", "credentials.txt"), 0),
		writeAt(t, filepath.Join(home, ".config", "gh", "hosts.yml"), 0),
	} {
		if _, err := pol.Check(p); !errors.Is(err, ErrMediaBlocked) {
			t.Errorf("%s: %v, want ErrMediaBlocked", p, err)
		}
		clean, files := pol.ExtractMedia("Here:\nMEDIA:`" + p + "`")
		if len(files) != 0 || !strings.Contains(clean, "[blocked file: "+p+"]") {
			t.Errorf("%s: %q %+v", p, clean, files)
		}
	}
	// A bare path of a protected file stays in the text and is not sent.
	cfg := filepath.Join(data, "config.yaml")
	if clean, files := pol.ExtractMedia("See " + cfg); len(files) != 0 || clean != "See "+cfg {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyProtectedBeatsAllowDir(t *testing.T) {
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	pol := NewMediaPolicy(data, policyEnv(map[string]string{EnvMediaAllowDirs: data}), nil)
	if _, err := pol.Check(writeAt(t, filepath.Join(data, ".env"), 0)); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal(err)
	}
	if _, err := pol.Check(writeAt(t, filepath.Join(data, "notes", "a.md"), time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestMediaPolicySymlinkToSecretRefused(t *testing.T) {
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	secret := writeAt(t, filepath.Join(data, ".env"), 0)
	link := filepath.Join(t.TempDir(), "chart.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	pol := NewMediaPolicy(data, policyEnv(nil), nil)
	if _, err := pol.Check(link); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal(err)
	}
	if clean, files := pol.ExtractMedia("Chart: " + link); len(files) != 0 || !strings.Contains(clean, link) {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyDefaultModeSendsOtherFiles(t *testing.T) {
	// Hermes TestMediaDeliveryDefaultMode: a stale file outside the roots is sent.
	t.Setenv("HOME", t.TempDir())
	pol := NewMediaPolicy(t.TempDir(), policyEnv(nil), nil)
	notes := writeAt(t, filepath.Join(t.TempDir(), "notes.md"), 2*time.Hour)
	got, err := pol.Check(notes)
	if err != nil || got != notes {
		t.Fatal(got, err)
	}
}

func TestMediaPolicyDeniedSystemPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeEtc := t.TempDir()
	pol := NewMediaPolicy(t.TempDir(), policyEnv(nil), nil)
	pol.Denied = append(pol.Denied, fakeEtc)
	if _, err := pol.Check(writeAt(t, filepath.Join(fakeEtc, "shadow.txt"), 0)); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc", "/proc", "/sys", "/dev", "/root", "/boot", "/var/log", "/var/lib", "/var/run"} {
		if !slices.Contains(pol.Denied, p) {
			t.Errorf("%s is not denied", p)
		}
	}
}

func TestMediaPolicyStrictMode(t *testing.T) {
	// Hermes TestMediaDeliveryPathValidation.
	t.Setenv("HOME", t.TempDir())
	data := t.TempDir()
	strict := func(extra map[string]string) *MediaPolicy {
		env := map[string]string{EnvMediaStrict: "1"}
		for k, v := range extra {
			env[k] = v
		}
		return NewMediaPolicy(data, policyEnv(env), nil)
	}
	noRecent := map[string]string{EnvMediaTrustRecent: "0"}
	cached := writeAt(t, filepath.Join(data, "cache", "media", "voice.ogg"), 2*time.Hour)
	shot := writeAt(t, filepath.Join(data, "cache", "screenshots", "page.png"), 2*time.Hour)
	for _, p := range []string{cached, shot} {
		if got, err := strict(noRecent).Check(p); err != nil || got != p {
			t.Errorf("%s: %s %v", p, got, err)
		}
	}
	stale := writeAt(t, filepath.Join(t.TempDir(), "old.pdf"), 2*time.Hour)
	if _, err := strict(noRecent).Check(stale); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal("stale file outside the roots:", err)
	}
	fresh := writeAt(t, filepath.Join(t.TempDir(), "build", "report.pdf"), 0)
	if _, err := strict(nil).Check(fresh); err != nil {
		t.Fatal("fresh file with recency trust:", err)
	}
	if _, err := strict(noRecent).Check(fresh); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal("fresh file without recency trust:", err)
	}
	if _, err := strict(map[string]string{EnvMediaTrustRecentSeconds: "60"}).Check(
		writeAt(t, filepath.Join(t.TempDir(), "r.pdf"), 2*time.Minute)); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal("file older than the window:", err)
	}
	extra := t.TempDir()
	inExtra := writeAt(t, filepath.Join(extra, "report.pdf"), 2*time.Hour)
	if _, err := strict(map[string]string{EnvMediaTrustRecent: "0", EnvMediaAllowDirs: "/nope," + extra}).Check(inExtra); err != nil {
		t.Fatal("file in TETHER_MEDIA_ALLOW_DIRS:", err)
	}
	// A fresh file in a denied folder is refused.
	sshKey := writeAt(t, filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa.txt"), 0)
	if _, err := strict(nil).Check(sshKey); !errors.Is(err, ErrMediaBlocked) {
		t.Fatal(err)
	}
	// A link in a root to a file outside the roots is refused.
	link := filepath.Join(data, "cache", "media", "safe-looking.png")
	if err := os.Symlink(stale, link); err == nil {
		if _, err := strict(noRecent).Check(link); !errors.Is(err, ErrMediaBlocked) {
			t.Fatal("symlink escape:", err)
		}
	}
}

func TestMediaPolicyStrictAliases(t *testing.T) {
	for raw, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "yes": true, "on": true,
		"0": false, "false": false, "no": false, "off": false, "": false} {
		pol := NewMediaPolicy("", policyEnv(map[string]string{EnvMediaStrict: raw}), nil)
		if pol.Strict != want {
			t.Errorf("%q: strict %v", raw, pol.Strict)
		}
	}
	if pol := NewMediaPolicy("", policyEnv(nil), nil); pol.Strict || pol.TrustRecent != 10*time.Minute {
		t.Fatalf("%+v", pol)
	}
}

func TestNilMediaPolicySendsNothing(t *testing.T) {
	png := tempFile(t, "chart.png")
	var pol *MediaPolicy
	clean, files := pol.ExtractMedia("MEDIA:" + png)
	if len(files) != 0 || !strings.Contains(clean, "[blocked file: ") {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyNullBytePath(t *testing.T) {
	good := tempFile(t, "good.png")
	_, files := openPolicy.ExtractMedia("here\nMEDIA:`~\x00evil.png`\nMEDIA:" + good)
	if len(files) != 1 || files[0].Path != good {
		t.Fatal(files)
	}
}
