package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func inbox(t *testing.T) (*Inbox, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	return &Inbox{C: api.client(), MediaDir: filepath.Join(t.TempDir(), "media")}, api
}

// sizes returns photo sizes with the given widths. Each size has the file id
// "p<width>".
func sizes(widths ...int) []PhotoSize {
	out := make([]PhotoSize, len(widths))
	for i, w := range widths {
		id := "p" + strconv.Itoa(w)
		out[i] = PhotoSize{FileID: id, FileUniqueID: "u" + strconv.Itoa(w), Width: w, Height: w * 3 / 4, FileSize: int64(w * 10)}
	}
	return out
}

func photoMsg(caption string, ps []PhotoSize) Message {
	return Message{MessageID: 5, Chat: Chat{ID: 1, Type: "private"}, Caption: caption, Photo: ps}
}

func docMsg(name, mime string, size int64) Message {
	return Message{MessageID: 6, Chat: Chat{ID: 1, Type: "private"},
		Document: &Document{FileID: "d-" + name, FileUniqueID: "ud", FileName: name, MimeType: mime, FileSize: size}}
}

func TestPhotoLargestSizeSaved(t *testing.T) {
	in, api := inbox(t)
	for _, w := range []int{90, 320, 1280} {
		api.addFile("p"+strconv.Itoa(w), "photos/file_"+strconv.Itoa(w)+".jpg", []byte("JPEG"+strconv.Itoa(w)))
	}
	txt, imgs, _, err := in.Build(ctx, []Message{photoMsg("caption here", sizes(90, 320, 1280))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "caption here") || !strings.Contains(txt, "[image: ") || len(imgs) != 1 {
		t.Fatal(txt, imgs)
	}
	if api.lastCall("getFile").Body["file_id"] != "p1280" {
		t.Fatal(api.all())
	}
	img := imgs[0]
	if string(img.Data) != "JPEG1280" || img.MIME != "image/jpeg" || !strings.Contains(txt, "[image: "+img.Path+"]") {
		t.Fatal(img.Path, img.MIME, string(img.Data), txt)
	}
	if !strings.HasSuffix(img.Path, "-u1280.jpg") || filepath.Dir(img.Path) != in.MediaDir {
		t.Fatal("file name must be <unix-ms>-<file_unique_id>.<ext>:", img.Path)
	}
	st, err := os.Stat(img.Path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(st, err)
	}
}

func TestPhotoExtensionFromFilePath(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/file_1.png", []byte("PNG"))
	_, imgs, _, _ := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if len(imgs) != 1 || !strings.HasSuffix(imgs[0].Path, ".png") || imgs[0].MIME != "image/png" {
		t.Fatal(imgs)
	}
}

func TestTextDocumentInlined(t *testing.T) {
	in, api := inbox(t)
	body := strings.Repeat("# Notes\nline\n", 150) // about 2 KB
	api.addFile("d-notes.md", "documents/file_9.md", []byte(body))
	txt, _, files, err := in.Build(ctx, []Message{docMsg("notes.md", "text/markdown", int64(len(body)))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(txt, "[Content of notes.md]:\n# Notes\nline\n") || len(files) != 1 {
		t.Fatal(txt[:40], files)
	}
}

func TestDocumentContentBeforeCaption(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-a.txt", "documents/a.txt", []byte("hello"))
	m := docMsg("a.txt", "text/plain", 5)
	m.Caption = "summarize this"
	txt, _, _, _ := in.Build(ctx, []Message{m})
	if txt != "[Content of a.txt]:\nhello\n\nsummarize this" {
		t.Fatalf("%q", txt)
	}
}

func TestLargeTextDocumentSavedNotInlined(t *testing.T) {
	in, api := inbox(t)
	body := strings.Repeat("x", 200*1024)
	api.addFile("d-big.txt", "documents/big.txt", []byte(body))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("big.txt", "text/plain", int64(len(body)))})
	if strings.Contains(txt, "[Content of") || len(files) != 1 || !strings.Contains(txt, "[file: "+files[0]+"]") {
		t.Fatal(txt[:min(len(txt), 80)], files)
	}
	if b, err := os.ReadFile(files[0]); err != nil || len(b) != len(body) {
		t.Fatal(err)
	}
}

func TestOtherSupportedDocumentSaved(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-report.pdf", "documents/report.pdf", []byte("%PDF"))
	txt, imgs, files, _ := in.Build(ctx, []Message{docMsg("report.pdf", "application/pdf", 4)})
	if len(files) != 1 || !strings.HasSuffix(files[0], ".pdf") || txt != "[file: "+files[0]+"]" || len(imgs) != 0 {
		t.Fatal(txt, files)
	}
}

func TestDocumentTooLargeNotDownloaded(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-huge.zip", "documents/huge.zip", []byte("PK"))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("huge.zip", "application/zip", 21<<20)})
	if api.count("getFile") != 0 || api.count("download") != 0 || len(files) != 0 {
		t.Fatal("the size check must come before the download:", api.methods())
	}
	if !strings.Contains(txt, "too large") || !strings.Contains(txt, "20 MB") {
		t.Fatal(txt)
	}
	// The getFile size is checked too, for a document with no size.
	in.MaxDocBytes = 1
	api.addFile("d-two.zip", "documents/two.zip", []byte("PK"))
	txt, _, files, _ = in.Build(ctx, []Message{docMsg("two.zip", "application/zip", 0)})
	if len(files) != 0 || !strings.Contains(txt, "too large") || api.count("download") != 0 {
		t.Fatal(txt, api.methods())
	}
}

func TestImageDocumentTreatedAsPhoto(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-shot.png", "documents/shot.png", []byte("PNG"))
	txt, imgs, files, _ := in.Build(ctx, []Message{docMsg("shot.png", "image/png", 3)})
	if len(imgs) != 1 || len(files) != 0 || !strings.Contains(txt, "[image: ") {
		t.Fatal(txt, imgs, files)
	}
}

func TestDocumentExtensionFromMime(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-", "documents/x", []byte("a,b"))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("", "text/csv", 3)})
	if len(files) != 1 || !strings.HasSuffix(files[0], ".csv") {
		t.Fatal(txt, files)
	}
}

func TestVoiceWithoutSTT(t *testing.T) {
	in, api := inbox(t)
	api.addFile("v1", "voice/file_3.oga", []byte("OGG"))
	m := Message{MessageID: 7, Chat: Chat{ID: 1}, Voice: &Voice{FileID: "v1", FileUniqueID: "uv", MimeType: "audio/ogg", FileSize: 3}}
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if txt != "[voice message received, but transcription is not set up]" {
		t.Fatal(txt)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "-uv.ogg") {
		t.Fatal(files)
	}
}

func TestVoiceWithSTT(t *testing.T) {
	in, api := inbox(t)
	api.addFile("v1", "voice/file_3.oga", []byte("OGG"))
	var got string
	in.Transcribe = func(_ context.Context, path string) (string, error) {
		got = path
		return "turn on the lights", nil
	}
	m := Message{MessageID: 7, Chat: Chat{ID: 1}, Voice: &Voice{FileID: "v1", FileUniqueID: "uv", FileSize: 3}}
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if txt != `[The user sent a voice message~ Here's what they said: "turn on the lights"]` || got != files[0] {
		t.Fatal(txt, got, files)
	}
	in.Transcribe = func(context.Context, string) (string, error) { return "", errors.New("whisper-cli: not found") }
	txt, _, _, _ = in.Build(ctx, []Message{m})
	if !strings.Contains(txt, "trouble transcribing") || !strings.Contains(txt, "whisper-cli: not found") {
		t.Fatal(txt)
	}
}

func TestAlbumBuildMergesCaptions(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/a.jpg", []byte("A"))
	api.addFile("p640", "photos/b.jpg", []byte("B"))
	a := photoMsg("Trip", sizes(320))
	b := photoMsg("Trip", sizes(640))
	txt, imgs, _, _ := in.Build(ctx, []Message{a, b})
	if len(imgs) != 2 || strings.Count(txt, "Trip") != 1 || strings.Count(txt, "[image: ") != 2 {
		t.Fatal(txt, imgs)
	}
}

func TestAlbumMergedAfter800ms(t *testing.T) {
	if defaultAlbumWait != 800*time.Millisecond {
		t.Fatal(defaultAlbumWait)
	}
	var mu sync.Mutex
	var flushed [][]Message
	done := make(chan struct{}, 4)
	flush := func(ms []Message) {
		mu.Lock()
		flushed = append(flushed, ms)
		mu.Unlock()
		done <- struct{}{}
	}
	ab := &AlbumBuffer{Wait: 50 * time.Millisecond}
	for i := 0; i < 3; i++ {
		m := photoMsg("", sizes(90))
		m.MediaGroupID = "g1"
		m.MessageID = i
		ab.Add(m, flush)
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("album not flushed")
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 1 || len(flushed[0]) != 3 || flushed[0][2].MessageID != 2 {
		t.Fatal(flushed)
	}
}

func TestAlbumBufferPassesSingleMessage(t *testing.T) {
	ab := &AlbumBuffer{}
	var got []Message
	ab.Add(photoMsg("one", sizes(90)), func(ms []Message) { got = ms })
	if len(got) != 1 {
		t.Fatal("a message without media_group_id must pass at once")
	}
}

func TestUnsupportedType(t *testing.T) {
	in, api := inbox(t)
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("setup.exe", "application/x-msdownload", 100)})
	if txt != "[unsupported file type: application/x-msdownload]" || len(files) != 0 || api.count("getFile") != 0 {
		t.Fatal(txt, files, api.methods())
	}
}

func TestDownloadFailureNoted(t *testing.T) {
	in, _ := inbox(t)
	txt, imgs, _, err := in.Build(ctx, []Message{photoMsg("look", sizes(320))})
	if err != nil || len(imgs) != 0 || !strings.Contains(txt, "look") || !strings.Contains(txt, "could not be downloaded") {
		t.Fatal(txt, err)
	}
}

func TestPlainTextMessage(t *testing.T) {
	in, _ := inbox(t)
	txt, imgs, files, err := in.Build(ctx, []Message{{Text: "hello"}})
	if err != nil || txt != "hello" || imgs != nil || files != nil {
		t.Fatal(txt, imgs, files, err)
	}
}

func voiceMsg() Message {
	return Message{MessageID: 8, Chat: Chat{ID: 1, Type: "private"}, Voice: &Voice{FileID: "v9", FileUniqueID: "uv9", FileSize: 3}}
}

func TestVoiceTranscribedText(t *testing.T) {
	in, api := inbox(t)
	api.addFile("v9", "voice/file_9.oga", []byte("OGG"))
	in.Transcribe = func(context.Context, string) (string, error) { return ` scrape the "sale" — café `, nil }
	txt, _, _, _ := in.Build(ctx, []Message{voiceMsg()})
	if txt != `[The user sent a voice message~ Here's what they said: "scrape the "sale" — café"]` {
		t.Fatal(txt)
	}
}

func TestVoiceTranscribeErrorIsVisible(t *testing.T) {
	in, api := inbox(t)
	api.addFile("v9", "voice/file_9.oga", []byte("OGG"))
	in.Transcribe = func(context.Context, string) (string, error) { return "", errors.New("stt endpoint: HTTP 500") }
	txt, _, files, _ := in.Build(ctx, []Message{voiceMsg()})
	if txt != "[The user sent a voice message but I had trouble transcribing it~ (stt endpoint: HTTP 500)]" || len(files) != 1 {
		t.Fatal(txt, files)
	}
}
