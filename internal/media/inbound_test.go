package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

const supportedList = ".cfg, .csv, .docx, .ini, .json, .log, .md, .pdf, .pptx, .py, .sh, .toml, .ts, .txt, .xlsx, .xml, .yaml, .yml, .zip"

func TestCheck(t *testing.T) {
	lim := Limits{MaxBytes: 20 << 20, InlineTextMax: 100 * 1024}
	for _, c := range []struct {
		name, mime string
		size       int64
		kind       Kind
		reject     string
	}{
		{"notes.md", "text/markdown", 2048, KindInlineText, ""},
		{"a.txt", "text/plain", 5, KindInlineText, ""},
		{"big.txt", "text/plain", 200 * 1024, KindDocument, ""},
		{"report.pdf", "application/pdf", 4, KindDocument, ""},
		{"data.csv", "text/csv", 8, KindDocument, ""},
		{"", "text/csv", 3, KindDocument, ""},
		{"shot.png", "image/png", 3, KindImage, ""},
		{"photo", "image/heic", 3, KindImage, ""},
		{"clip.mov", "video/quicktime", 10, KindVideo, ""},
		{"", "video/mp4", 10, KindVideo, ""},
		{"setup.exe", "application/x-msdownload", 100, "", "Unsupported document type '.exe'. Supported types: " + supportedList},
		{"", "application/x-foo", 10, "", "Unsupported document type 'unknown'. Supported types: " + supportedList},
		{"huge.zip", "application/zip", 21 << 20, "", "[file too large: huge.zip has 22020096 bytes; the limit is 20 MB]"},
		{"bad name?.exe", "application/zip", 21 << 20, "", "[file too large: bad name_.exe has 22020096 bytes; the limit is 20 MB]"},
	} {
		kind, reject := Check(c.name, c.mime, c.size, lim)
		if kind != c.kind || reject != c.reject {
			t.Errorf("Check(%q, %q, %d) = %q, %q; want %q, %q", c.name, c.mime, c.size, kind, reject, c.kind, c.reject)
		}
	}
	// Zero limits use 20 MB and 100 KB.
	if kind, reject := Check("a.txt", "text/plain", 100*1024, Limits{}); kind != KindInlineText || reject != "" {
		t.Fatal(kind, reject)
	}
	if _, reject := Check("x.zip", "application/zip", 21<<20, Limits{}); reject != "[file too large: x.zip has 22020096 bytes; the limit is 20 MB]" {
		t.Fatal(reject)
	}
}

func saved(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNote(t *testing.T) {
	ctx := context.Background()
	img := saved(t, "1-a.png", []byte("PNG"))
	got, err := Note(ctx, KindImage, img, "shot.png", NoteDeps{Vision: true})
	want := Part{Text: "[image: " + img + "]", Image: &provider.Image{Path: img, MIME: "image/png", Data: []byte("PNG")}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := Note(ctx, KindImage, img, "shot.png", NoteDeps{}); err != nil || got.Image != nil || got.Text != "[image: "+img+"]" {
		t.Fatalf("no vision: %+v %v", got, err)
	}
	if _, err := Note(ctx, KindImage, filepath.Join(t.TempDir(), "gone.png"), "", NoteDeps{Vision: true}); err == nil {
		t.Fatal("a missing image must fail")
	}

	txt := saved(t, "2-a.txt", []byte("hello"))
	got, _ = Note(ctx, KindInlineText, txt, "a.txt", NoteDeps{})
	if got.File != txt || got.Text != "[The user sent a text document: 'a.txt'. Its content has been included below. The file is also saved at: "+txt+"]\n\n[Content of a.txt]:\nhello" {
		t.Fatalf("%q", got.Text)
	}
	bin := saved(t, "3-b.txt", []byte{0xff, 0xfe})
	got, _ = Note(ctx, KindInlineText, bin, "b.txt", NoteDeps{})
	if got.Text != "[The user sent a document: 'b.txt'. The file is saved at: "+bin+". Ask the user what they'd like you to do with it.]" {
		t.Fatalf("%q", got.Text)
	}

	pdf := saved(t, "4-x.pdf", []byte("%PDF"))
	got, _ = Note(ctx, KindDocument, pdf, "", NoteDeps{})
	if got.File != pdf || got.Text != "[The user sent a document: 'document.pdf'. The file is saved at: "+pdf+". Ask the user what they'd like you to do with it.]" {
		t.Fatalf("%q", got.Text)
	}
	mp3 := saved(t, "5-x.mp3", []byte("ID3"))
	got, _ = Note(ctx, KindAudio, mp3, "", NoteDeps{})
	if got.Text != "[The user sent an audio file attachment: 'audio.mp3'. It is saved at: "+mp3+". Ask the user what they'd like you to do with it, or pass the path to a transcription or media tool.]" {
		t.Fatalf("%q", got.Text)
	}
	mov := saved(t, "6-x.mov", []byte("MOV"))
	got, _ = Note(ctx, KindVideo, mov, "clip.mov", NoteDeps{})
	if got.Text != "[The user sent a video: 'clip.mov'. It is saved at: "+mov+". Ask the user what they'd like you to do with it.]" {
		t.Fatalf("%q", got.Text)
	}
}

func TestNoteVoice(t *testing.T) {
	ctx := context.Background()
	ogg := saved(t, "7-v.ogg", []byte("OggS"))
	got, err := Note(ctx, KindVoice, ogg, "", NoteDeps{})
	if err != nil || got.File != ogg || got.Text != "[voice message received, but transcription is not set up]" {
		t.Fatalf("%+v %v", got, err)
	}
	var heard string
	stt := func(_ context.Context, path string) (string, error) {
		heard = path
		return ` scrape the "sale" — café `, nil
	}
	got, _ = Note(ctx, KindVoice, ogg, "", NoteDeps{STT: stt})
	if heard != ogg || got.Text != `[The user sent a voice message~ Here's what they said: "scrape the "sale" — café"]` {
		t.Fatalf("%q", got.Text)
	}
	fail := func(context.Context, string) (string, error) { return "", errors.New("stt endpoint: HTTP 500") }
	got, err = Note(ctx, KindVoice, ogg, "", NoteDeps{STT: fail})
	if err != nil || got.File != ogg || got.Text != "[The user sent a voice message but I had trouble transcribing it~ (stt endpoint: HTTP 500)]" {
		t.Fatalf("%+v %v", got, err)
	}
}
