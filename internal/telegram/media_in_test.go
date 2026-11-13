package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
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

const supportedList = ".cfg, .csv, .docx, .ini, .json, .log, .md, .pdf, .pptx, .py, .sh, .toml, .ts, .txt, .xlsx, .xml, .yaml, .yml, .zip"

func docNote(name, path string) string {
	return "[The user sent a document: '" + name + "'. The file is saved at: " + path + ". Ask the user what they'd like you to do with it.]"
}

func textDocNote(name, path string) string {
	return "[The user sent a text document: '" + name + "'. Its content has been included below. The file is also saved at: " + path + "]"
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
	if len(files) != 1 || !strings.HasPrefix(txt, textDocNote("notes.md", files[0])+"\n\n[Content of notes.md]:\n# Notes\nline\n") {
		t.Fatal(txt[:min(len(txt), 200)], files)
	}
}

func TestDocumentContentBeforeCaption(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-a.txt", "documents/a.txt", []byte("hello"))
	m := docMsg("a.txt", "text/plain", 5)
	m.Caption = "summarize this"
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if len(files) != 1 || txt != textDocNote("a.txt", files[0])+"\n\n[Content of a.txt]:\nhello\n\nsummarize this" {
		t.Fatalf("%q", txt)
	}
}

func TestLargeTextDocumentSavedNotInlined(t *testing.T) {
	in, api := inbox(t)
	body := strings.Repeat("x", 200*1024)
	api.addFile("d-big.txt", "documents/big.txt", []byte(body))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("big.txt", "text/plain", int64(len(body)))})
	if len(files) != 1 || txt != docNote("big.txt", files[0]) || strings.Contains(txt, "included below") || strings.Contains(txt, "[Content of") {
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
	if len(files) != 1 || !strings.HasSuffix(files[0], ".pdf") || txt != docNote("report.pdf", files[0]) || len(imgs) != 0 {
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
	if txt != "Unsupported document type '.exe'. Supported types: "+supportedList || len(files) != 0 || api.count("getFile") != 0 {
		t.Fatal(txt, files, api.methods())
	}
}

func TestCSVDocumentNotIncludedBelow(t *testing.T) {
	in, api := inbox(t)
	api.addFile("d-data.csv", "documents/data.csv", []byte("a,b\n1,2\n"))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("data.csv", "text/csv", 8)})
	if len(files) != 1 || txt != docNote("data.csv", files[0]) {
		t.Fatalf("%q", txt)
	}
}

func TestUnsupportedTypeWithoutExtension(t *testing.T) {
	in, _ := inbox(t)
	txt, _, _, _ := in.Build(ctx, []Message{docMsg("", "application/x-foo", 10)})
	if txt != "Unsupported document type 'unknown'. Supported types: "+supportedList {
		t.Fatalf("%q", txt)
	}
}

func TestUnsupportedTypeKeepsCaption(t *testing.T) {
	in, _ := inbox(t)
	m := docMsg("setup.exe", "application/x-msdownload", 100)
	m.Caption = "is this safe?"
	txt, _, _, _ := in.Build(ctx, []Message{m})
	if txt != "Unsupported document type '.exe'. Supported types: "+supportedList+"\n\nis this safe?" {
		t.Fatalf("%q", txt)
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

const mapsPin = "[The user shared a location pin.]\n"
const nearby = "Ask what they'd like to find nearby (restaurants, cafes, etc.) and any preferences."

func TestLocationNote(t *testing.T) {
	in, api := inbox(t)
	m := Message{MessageID: 7, Chat: Chat{ID: 1, Type: "private"}, Location: &Location{Latitude: -6.2088, Longitude: 106.8456}}
	txt, imgs, files, err := in.Build(ctx, []Message{m})
	want := mapsPin + "latitude: -6.2088\nlongitude: 106.8456\n" +
		"Map: https://www.google.com/maps/search/?api=1&query=-6.2088,106.8456\n" + nearby
	if err != nil || txt != want || len(imgs) != 0 || len(files) != 0 || api.count("getFile") != 0 {
		t.Fatalf("%q %v", txt, err)
	}
}

func TestVenueNote(t *testing.T) {
	in, _ := inbox(t)
	loc := Location{Latitude: 1.5, Longitude: 2.25}
	m := Message{MessageID: 8, Chat: Chat{ID: 1, Type: "private"}, Location: &loc,
		Venue: &Venue{Location: loc, Title: "Cafe Kopi", Address: "Jl. Sudirman 1"}}
	txt, _, _, _ := in.Build(ctx, []Message{m})
	want := mapsPin + "Venue: Cafe Kopi\nAddress: Jl. Sudirman 1\nlatitude: 1.5\nlongitude: 2.25\n" +
		"Map: https://www.google.com/maps/search/?api=1&query=1.5,2.25\n" + nearby
	if txt != want {
		t.Fatalf("%q", txt)
	}
}

func TestVenueWithoutTitleOrAddress(t *testing.T) {
	in, _ := inbox(t)
	loc := Location{Latitude: 3, Longitude: -4}
	txt, _, _, _ := in.Build(ctx, []Message{{Chat: Chat{ID: 1}, Location: &loc, Venue: &Venue{Location: loc}}})
	want := mapsPin + "latitude: 3\nlongitude: -4\nMap: https://www.google.com/maps/search/?api=1&query=3,-4\n" + nearby
	if txt != want {
		t.Fatalf("%q", txt)
	}
}

func audioNote(name, path string) string {
	return "[The user sent an audio file attachment: '" + name + "'. It is saved at: " + path +
		". Ask the user what they'd like you to do with it, or pass the path to a transcription or media tool.]"
}

func videoNote(name, path string) string {
	return "[The user sent a video: '" + name + "'. It is saved at: " + path + ". Ask the user what they'd like you to do with it.]"
}

// mediaMsg returns a private message without content. (poll_test.go has
// chatMsg already.)
func mediaMsg() Message { return Message{MessageID: 9, Chat: Chat{ID: 1, Type: "private"}} }

func TestAudioFileSavedNotTranscribed(t *testing.T) {
	in, api := inbox(t)
	in.Transcribe = func(context.Context, string) (string, error) {
		t.Fatal("an audio file must not be transcribed")
		return "", nil
	}
	api.addFile("a1", "music/file_1.mp3", []byte("ID3"))
	m := mediaMsg()
	m.Audio = &Audio{FileID: "a1", FileUniqueID: "ua", FileName: "meeting.mp3", MimeType: "audio/mpeg", FileSize: 3}
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if len(files) != 1 || !strings.HasSuffix(files[0], ".mp3") || txt != audioNote("meeting.mp3", files[0]) {
		t.Fatalf("%q %v", txt, files)
	}
}

func TestAudioWithoutFileName(t *testing.T) {
	in, api := inbox(t)
	api.addFile("a2", "music/file_2", []byte("OggS"))
	m := mediaMsg()
	m.Audio = &Audio{FileID: "a2", FileUniqueID: "ub", MimeType: "audio/ogg", FileSize: 4}
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if len(files) != 1 || !strings.HasSuffix(files[0], ".ogg") || txt != audioNote("audio.ogg", files[0]) {
		t.Fatalf("%q", txt)
	}
}

func TestVideoSavedBeforeCaption(t *testing.T) {
	in, api := inbox(t)
	api.addFile("v1", "videos/file_3.mp4", []byte("mp4"))
	m := mediaMsg()
	m.Caption = "what happens here?"
	m.Video = &Video{FileID: "v1", FileUniqueID: "uv", FileName: "clip.mp4", MimeType: "video/mp4", FileSize: 3}
	txt, _, files, _ := in.Build(ctx, []Message{m})
	if len(files) != 1 || txt != videoNote("clip.mp4", files[0])+"\n\nwhat happens here?" {
		t.Fatalf("%q", txt)
	}
}

func TestVideoTooLargeNotDownloaded(t *testing.T) {
	in, api := inbox(t)
	m := mediaMsg()
	m.Video = &Video{FileID: "v2", FileUniqueID: "uw", FileName: "clip.mp4", FileSize: 30 << 20}
	txt, _, _, _ := in.Build(ctx, []Message{m})
	if txt != "[file too large: clip.mp4 has 31457280 bytes; the limit is 20 MB]" || api.count("getFile") != 0 {
		t.Fatalf("%q", txt)
	}
}

func TestVideoDocuments(t *testing.T) {
	cases := []struct{ file, mime, name, ext string }{
		{"animation.gif.mp4", "video/mp4", "animation.gif.mp4", ".mp4"}, // a GIF
		{"CLIP.MOV", "video/quicktime", "CLIP.MOV", ".mov"},
		{"", "video/webm", "video.webm", ".webm"},
	}
	for _, c := range cases {
		in, api := inbox(t)
		api.addFile("d-"+c.file, "documents/x", []byte("vid"))
		txt, _, files, _ := in.Build(ctx, []Message{docMsg(c.file, c.mime, 3)})
		if len(files) != 1 || !strings.HasSuffix(files[0], c.ext) || txt != videoNote(c.name, files[0]) {
			t.Errorf("%s: %q %v", c.file, txt, files)
		}
	}
}

func TestMixedAlbumNotesInOrder(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/file_4.jpg", []byte("JPEG"))
	api.addFile("v3", "videos/file_5.mp4", []byte("mp4"))
	p := photoMsg("", sizes(320))
	v := mediaMsg()
	v.Video = &Video{FileID: "v3", FileUniqueID: "ux", FileName: "b.mp4", FileSize: 3}
	p.MediaGroupID, v.MediaGroupID = "g1", "g1"
	txt, imgs, files, _ := in.Build(ctx, []Message{p, v})
	if len(imgs) != 1 || len(files) != 1 || !strings.HasPrefix(txt, "[image: ") ||
		!strings.HasSuffix(txt, "\n\n"+videoNote("b.mp4", files[0])) {
		t.Fatalf("%q", txt)
	}
}

type mapStickers struct {
	m    map[string]StickerInfo
	puts int
}

func (c *mapStickers) Get(_ context.Context, id string) (StickerInfo, bool) {
	i, ok := c.m[id]
	return i, ok
}
func (c *mapStickers) Put(_ context.Context, id string, i StickerInfo) { c.m[id] = i; c.puts++ }

func stickerMsg(s Sticker) Message { m := mediaMsg(); m.Sticker = &s; return m }

const duckPrompt = "Describe this sticker in 1-2 sentences. Focus on what it depicts -- character, action, emotion. Be concise and objective."

func TestAnimatedStickerNotes(t *testing.T) {
	in, api := inbox(t)
	got, _, _, _ := in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s1", Emoji: "👍", IsAnimated: true})})
	if got != "[The user sent an animated sticker 👍~ I can't see animated ones yet, but the emoji suggests: 👍]" {
		t.Fatalf("%q", got)
	}
	got, _, _, _ = in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s2", IsVideo: true})})
	if got != "[The user sent an animated sticker~ I can't see animated ones yet]" || api.count("getFile") != 0 {
		t.Fatalf("%q", got)
	}
}

func TestStaticStickerDescribedAndCached(t *testing.T) {
	in, api := inbox(t)
	api.addFile("s3", "stickers/file_6.webp", []byte("RIFF"))
	cache := &mapStickers{m: map[string]StickerInfo{}}
	in.Stickers = cache
	in.Describe = func(_ context.Context, path, prompt string) (string, error) {
		if prompt != duckPrompt || !strings.HasSuffix(path, ".webp") {
			t.Errorf("%q %q", path, prompt)
		}
		return "A duck laughs and falls over", nil
	}
	txt, imgs, files, _ := in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s3", FileUniqueID: "us3", Emoji: "😂", SetName: "DuckPack"})})
	want := `[The user sent a sticker 😂 from "DuckPack"~ It shows: "A duck laughs and falls over" (=^.w.^=)]`
	if txt != want || len(imgs) != 0 || len(files) != 0 || cache.puts != 1 {
		t.Fatalf("%q imgs=%d files=%d puts=%d", txt, len(imgs), len(files), cache.puts)
	}
	if cache.m["us3"] != (StickerInfo{Description: "A duck laughs and falls over", Emoji: "😂", SetName: "DuckPack"}) {
		t.Fatalf("%+v", cache.m)
	}
}

func TestStaticStickerCacheHit(t *testing.T) {
	in, api := inbox(t)
	in.Stickers = &mapStickers{m: map[string]StickerInfo{"us4": {Description: "A cat waves", Emoji: "👋"}}}
	in.Describe = func(context.Context, string, string) (string, error) {
		t.Fatal("no vision call on a hit")
		return "", nil
	}
	txt, _, _, _ := in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s4", FileUniqueID: "us4", Emoji: "🙂", SetName: "Cats"})})
	if txt != `[The user sent a sticker 👋~ It shows: "A cat waves" (=^.w.^=)]` || api.count("getFile") != 0 {
		t.Fatalf("%q", txt)
	}
}

func TestStaticStickerFallbacks(t *testing.T) {
	old := describeTimeout
	describeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { describeTimeout = old })
	cases := []struct {
		name     string
		describe func(context.Context, string, string) (string, error)
		emoji    string
		want     string
	}{
		{"no vision", nil, "😂", `[The user sent a sticker 😂~ It shows: "a sticker with emoji 😂" (=^.w.^=)]`},
		{"vision error", func(context.Context, string, string) (string, error) { return "", errors.New("down") }, "",
			`[The user sent a sticker~ It shows: "a sticker" (=^.w.^=)]`},
		{"empty answer", func(context.Context, string, string) (string, error) { return "  ", nil }, "",
			`[The user sent a sticker~ It shows: "a sticker" (=^.w.^=)]`},
		{"timeout", func(c context.Context, _, _ string) (string, error) { <-c.Done(); return "", c.Err() }, "",
			`[The user sent a sticker~ It shows: "a sticker" (=^.w.^=)]`},
	}
	for _, c := range cases {
		in, api := inbox(t)
		api.addFile("s5", "stickers/file_7.webp", []byte("RIFF"))
		cache := &mapStickers{m: map[string]StickerInfo{}}
		in.Stickers, in.Describe = cache, c.describe
		txt, _, _, _ := in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s5", FileUniqueID: "us5", Emoji: c.emoji, SetName: "P"})})
		if txt != c.want || cache.puts != 0 {
			t.Errorf("%s: %q puts=%d", c.name, txt, cache.puts)
		}
	}
}

// inboxPhoto returns the JSON of a photo block of a rich message. The file id is "rp<n>",
// and the unique id is "ru<n>".
func inboxPhoto(n int, caption string) string {
	id := strconv.Itoa(n)
	b := `{"type":"photo","photo":[{"file_id":"rp` + id + `","file_unique_id":"ru` + id +
		`","width":320,"height":240,"file_size":4}]`
	if caption != "" {
		b += `,"caption":{"text":"` + caption + `"}`
	}
	return b + `}`
}

// inboxPhotos makes the fake serve the files of the photo blocks first to last. It returns
// the JSON of these blocks, joined with commas.
func inboxPhotos(api *fakeBot, first, last int) string {
	var blocks []string
	for n := first; n <= last; n++ {
		id := strconv.Itoa(n)
		api.addFile("rp"+id, "photos/rp"+id+".jpg", []byte("JPEG"+id))
		blocks = append(blocks, inboxPhoto(n, ""))
	}
	return strings.Join(blocks, ",")
}

// richLimitNote is the note for a photo after the first 10 media parts.
const richLimitNote = "[photo not read: Tether Agent reads the first 10 media files of a message]"

func TestRichMediaNoteBetweenParagraphs(t *testing.T) {
	in, api := inbox(t)
	api.addFile("rp1", "photos/rp1.jpg", []byte("JPEG1"))
	m := richMsg(`[{"type":"paragraph","text":"Before"},` + inboxPhoto(1, "Cap") +
		`,{"type":"paragraph","text":"After"}]`)
	txt, imgs, files, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 1 || len(files) != 0 {
		t.Fatal(txt, imgs, files, err)
	}
	if want := "Before\n\n[image: " + imgs[0].Path + "]\nCap\n\nAfter"; txt != want {
		t.Fatalf("got %q, want %q", txt, want)
	}
	if string(imgs[0].Data) != "JPEG1" || !strings.HasSuffix(imgs[0].Path, "-ru1.jpg") {
		t.Fatal(imgs[0].Path, string(imgs[0].Data))
	}
}

func TestRichMediaLimitOfTen(t *testing.T) {
	if maxRichMedia != 10 {
		t.Fatal(maxRichMedia)
	}
	in, api := inbox(t)
	// The fake does not have the file of photo 11, and the inbox must not ask for it.
	m := richMsg(`[` + inboxPhotos(api, 1, 10) + `,` + inboxPhoto(11, "eleven") + `]`)
	txt, imgs, _, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 10 || api.count("getFile") != 10 {
		t.Fatal(txt, len(imgs), api.count("getFile"), err)
	}
	if strings.Count(txt, "[image: ") != 10 || strings.Count(txt, richLimitNote) != 1 ||
		!strings.HasSuffix(txt, "\n\n"+richLimitNote+"\neleven") {
		t.Fatalf("%q", txt)
	}
}

func TestRichMediaKinds(t *testing.T) {
	in, api := inbox(t)
	in.Transcribe = func(context.Context, string) (string, error) { return "turn on the lights", nil }
	api.addFile("rd1", "documents/a.txt", []byte("hello"))
	api.addFile("rv1", "voice/v.oga", []byte("OGG"))
	api.addFile("rvid1", "videos/clip.mp4", []byte("mp4"))
	api.addFile("ran1", "animations/fun.mp4", []byte("mp4"))
	api.addFile("rau1", "music/song.mp3", []byte("ID3"))
	m := richMsg(`[
		{"type":"document","document":{"file_id":"rd1","file_unique_id":"ud1","file_name":"a.txt","mime_type":"text/plain","file_size":5}},
		{"type":"voice_note","voice_note":{"file_id":"rv1","file_unique_id":"uv1","file_size":3}},
		{"type":"video","video":{"file_id":"rvid1","file_unique_id":"uvid1","file_name":"clip.mp4","mime_type":"video/mp4","file_size":3}},
		{"type":"animation","animation":{"file_id":"ran1","file_unique_id":"uan1","file_name":"fun.mp4","mime_type":"video/mp4","file_size":3}},
		{"type":"audio","audio":{"file_id":"rau1","file_unique_id":"uau1","file_name":"song.mp3","mime_type":"audio/mpeg","file_size":3}}]`)
	txt, imgs, files, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 0 || len(files) != 5 {
		t.Fatal(txt, imgs, files, err)
	}
	want := strings.Join([]string{
		textDocNote("a.txt", files[0]) + "\n\n[Content of a.txt]:\nhello",
		`[The user sent a voice message~ Here's what they said: "turn on the lights"]`,
		videoNote("clip.mp4", files[2]),
		videoNote("fun.mp4", files[3]),
		audioNote("song.mp3", files[4]),
	}, "\n\n")
	if txt != want {
		t.Fatalf("got %q, want %q", txt, want)
	}
	if !strings.HasSuffix(files[1], "-uv1.ogg") || !strings.HasSuffix(files[3], "-uan1.mp4") {
		t.Fatal(files)
	}
}

func TestRichMediaInboxCollage(t *testing.T) {
	in, api := inbox(t)
	m := richMsg(`[{"type":"collage","blocks":[` + inboxPhotos(api, 1, 1) + `],"caption":{"text":"Trip"}}]`)
	txt, imgs, _, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 1 {
		t.Fatal(txt, imgs, err)
	}
	// The note comes first, and the caption of the collage is the last line.
	if !strings.HasPrefix(txt, "[image: "+imgs[0].Path+"]\n") || !strings.HasSuffix(txt, "\nTrip") {
		t.Fatalf("%q", txt)
	}
}

func TestRichMediaInQuotation(t *testing.T) {
	in, api := inbox(t)
	api.addFile("rp1", "photos/rp1.jpg", []byte("JPEG1"))
	m := richMsg(`[{"type":"blockquote","blocks":[{"type":"paragraph","text":"quoted"},` + inboxPhoto(1, "Cap") + `]}]`)
	txt, imgs, _, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 1 {
		t.Fatal(txt, imgs, err)
	}
	// The note line and the caption line have the prefix of the quotation.
	if !strings.HasPrefix(txt, "> quoted\n") || !strings.HasSuffix(txt, "\n> [image: "+imgs[0].Path+"]\n> Cap") {
		t.Fatalf("%q", txt)
	}
}

func TestRichMediaDownloadFailsForOnePart(t *testing.T) {
	in, api := inbox(t)
	// The fake does not have the file of photo 2.
	api.addFile("rp1", "photos/rp1.jpg", []byte("JPEG1"))
	api.addFile("rp3", "photos/rp3.jpg", []byte("JPEG3"))
	m := richMsg(`[` + inboxPhoto(1, "") + `,` + inboxPhoto(2, "") + `,` + inboxPhoto(3, "") + `]`)
	txt, imgs, _, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || len(imgs) != 2 {
		t.Fatal(txt, imgs, err)
	}
	want := "[image: " + imgs[0].Path + "]\n\n[photo could not be downloaded]\n\n[image: " + imgs[1].Path + "]"
	if txt != want || !strings.HasSuffix(imgs[1].Path, "-ru3.jpg") {
		t.Fatalf("got %q, want %q", txt, want)
	}
}

func TestRichMediaBotMentionRemoved(t *testing.T) {
	in, api := inbox(t)
	api.addFile("rp1", "photos/rp1.jpg", []byte("JPEG1"))
	m := richMsg(`[{"type":"paragraph","text":"@tether_bot what is this?"},` + inboxPhoto(1, "@tether_bot look") + `]`)
	txt, imgs, _, err := in.BuildFor(ctx, []Message{m}, "tether_bot")
	if err != nil || len(imgs) != 1 {
		t.Fatal(txt, imgs, err)
	}
	if want := "what is this?\n\n[image: " + imgs[0].Path + "]\nlook"; txt != want {
		t.Fatalf("got %q, want %q", txt, want)
	}
	// Build gives no bot user name, so the mention stays.
	txt, _, _, err = in.Build(ctx, []Message{m})
	if err != nil || !strings.HasPrefix(txt, "@tether_bot what is this?\n\n") || !strings.HasSuffix(txt, "\n@tether_bot look") {
		t.Fatalf("%q %v", txt, err)
	}
}

func TestRichMediaEmptyPhotoArray(t *testing.T) {
	in, api := inbox(t)
	m := richMsg(`[{"type":"photo","photo":[],"caption":{"text":"only the caption"}}]`)
	txt, imgs, files, err := in.BuildFor(ctx, []Message{m}, "")
	if err != nil || txt != "only the caption" || len(imgs) != 0 || len(files) != 0 || api.count("getFile") != 0 {
		t.Fatalf("%q %v %v %v", txt, imgs, files, err)
	}
}

func TestRichAlbumSharesTheLimit(t *testing.T) {
	in, api := inbox(t)
	a := richMsg(`[` + inboxPhotos(api, 1, 6) + `]`)
	b := richMsg(`[` + inboxPhotos(api, 7, 12) + `]`)
	a.MediaGroupID, b.MediaGroupID = "g1", "g1"
	txt, imgs, _, err := in.BuildFor(ctx, []Message{a, b}, "")
	if err != nil || len(imgs) != 10 || api.count("getFile") != 10 || strings.Count(txt, richLimitNote) != 2 {
		t.Fatal(txt, len(imgs), api.count("getFile"), err)
	}
}

func TestAlbumClassicPhotoDoesNotCount(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/classic.jpg", []byte("CLASSIC"))
	classic := photoMsg("", sizes(320))
	rich := richMsg(`[{"type":"paragraph","text":"Notes"},` + inboxPhotos(api, 1, 10) + `]`)
	classic.MediaGroupID, rich.MediaGroupID = "g1", "g1"
	txt, imgs, _, err := in.BuildFor(ctx, []Message{classic, rich}, "")
	if err != nil || len(imgs) != 11 || strings.Contains(txt, "not read") {
		t.Fatal(txt, len(imgs), err)
	}
	// The classic note comes first. The rich text takes the place of the caption.
	if string(imgs[0].Data) != "CLASSIC" ||
		!strings.HasPrefix(txt, "[image: "+imgs[0].Path+"]\n\nNotes\n\n[image: "+imgs[1].Path+"]") {
		t.Fatalf("%q", txt)
	}
}

func TestRichMediaContextEndsBetweenParts(t *testing.T) {
	in, api := inbox(t)
	var logb bytes.Buffer
	in.Log = slog.New(slog.NewTextHandler(&logb, nil))
	api.addFile("rv1", "voice/rv1.oga", []byte("OGG"))
	api.addFile("rv2", "voice/rv2.oga", []byte("OGG"))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The context ends when part 1 is ready.
	in.Transcribe = func(context.Context, string) (string, error) {
		cancel()
		return "one", nil
	}
	m := richMsg(`[{"type":"voice_note","voice_note":{"file_id":"rv1","file_unique_id":"uv1","file_size":3}},` +
		`{"type":"voice_note","voice_note":{"file_id":"rv2","file_unique_id":"uv2","file_size":3}}]`)
	txt, imgs, files, err := in.BuildFor(cctx, []Message{m}, "")
	if !errors.Is(err, context.Canceled) || txt != "" || imgs != nil || files != nil {
		t.Fatal(txt, imgs, files, err)
	}
	// Part 2 does not start, so no download fails.
	if api.count("getFile") != 1 || strings.Contains(logb.String(), "download failed") {
		t.Fatal(api.methods(), logb.String())
	}
}

// slowFiles is a transport that holds the download of each file whose path contains "slow"
// until the context of the request ends.
type slowFiles struct{ next http.RoundTripper }

func (s slowFiles) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/file/") && strings.Contains(r.URL.Path, "slow") {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return s.next.RoundTrip(r)
}

// slowInbox returns an inbox whose client holds the download of each "slow" file.
func slowInbox(t *testing.T) (*Inbox, *fakeBot) {
	t.Helper()
	in, api := inbox(t)
	in.C.HC = &http.Client{Transport: slowFiles{next: api.srv.Client().Transport}}
	return in, api
}

// limitPhoto returns the JSON of a photo block of a rich message with the file id "lp<n>".
func limitPhoto(n int, caption string) string {
	id := strconv.Itoa(n)
	b := `{"type":"photo","photo":[{"file_id":"lp` + id + `","file_unique_id":"lu` + id +
		`","width":320,"height":240,"file_size":4}]`
	if caption != "" {
		b += `,"caption":{"text":"` + caption + `"}`
	}
	return b + `}`
}

// timeLimitNote is the note for a photo that the time limit stopped.
const timeLimitNote = "[photo not read: the time limit for the media of one message ended]"

func TestRichMediaLimitDefault(t *testing.T) {
	if defaultRichMediaLimit != 2*time.Minute {
		t.Fatal(defaultRichMediaLimit)
	}
	if got := (&Inbox{}).richMediaLimit(); got != 2*time.Minute {
		t.Fatal(got)
	}
	if got := (&Inbox{RichMediaLimit: 50 * time.Millisecond}).richMediaLimit(); got != 50*time.Millisecond {
		t.Fatal(got)
	}
}

func TestRichMediaTimeLimit(t *testing.T) {
	in, api := slowInbox(t)
	in.RichMediaLimit = 50 * time.Millisecond
	api.addFile("lp1", "photos/lp1.jpg", []byte("ONE"))
	api.addFile("lp2", "slow/lp2.jpg", []byte("TWO"))
	api.addFile("lp3", "photos/lp3.jpg", []byte("THREE"))
	m := richMsg(`[{"type":"paragraph","text":"Intro"},` + limitPhoto(1, "first") + `,` + limitPhoto(2, "second") + `,` +
		limitPhoto(3, "third") + `,{"type":"paragraph","text":"End"}]`)
	// The context of the caller has a long limit. So a missing time limit fails the test and
	// does not block it.
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txt, imgs, files, err := in.BuildFor(cctx, []Message{m}, "")
	if err != nil || len(imgs) != 1 || len(files) != 0 {
		t.Fatal(txt, imgs, files, err)
	}
	// Part 1 is ready. The limit stops part 2. Part 3 does not start.
	want := "Intro\n\n[image: " + imgs[0].Path + "]\nfirst\n\n" + timeLimitNote + "\nsecond\n\n" +
		timeLimitNote + "\nthird\n\nEnd"
	if txt != want {
		t.Fatalf("got %q, want %q", txt, want)
	}
	if string(imgs[0].Data) != "ONE" || api.count("getFile") != 2 || api.lastCall("getFile").Body["file_id"] != "lp2" {
		t.Fatal(api.all())
	}
}

func TestRichMediaTimeLimitStopsTranscription(t *testing.T) {
	in, api := inbox(t)
	in.RichMediaLimit = 50 * time.Millisecond
	// The transcription waits until its context ends.
	in.Transcribe = func(c context.Context, _ string) (string, error) {
		<-c.Done()
		return "", c.Err()
	}
	api.addFile("lv1", "voice/lv1.oga", []byte("OGG"))
	m := richMsg(`[{"type":"voice_note","voice_note":{"file_id":"lv1","file_unique_id":"luv1","file_size":3},` +
		`"caption":{"text":"listen"}}]`)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txt, imgs, files, err := in.BuildFor(cctx, []Message{m}, "")
	// The stopped part gives the note and its caption. Its file is not in the result.
	want := "[voice note not read: the time limit for the media of one message ended]\nlisten"
	if err != nil || txt != want || len(imgs) != 0 || len(files) != 0 {
		t.Fatalf("%q %v %v %v", txt, imgs, files, err)
	}
}

func TestClassicMediaIsNotUnderTheRichLimit(t *testing.T) {
	in, api := slowInbox(t)
	in.RichMediaLimit = 50 * time.Millisecond
	api.addFile("lp1", "slow/lp1.jpg", []byte("ONE"))
	api.addFile("p320", "photos/classic.jpg", []byte("CLASSIC"))
	rich := richMsg(`[` + limitPhoto(1, "") + `]`)
	classic := photoMsg("", sizes(320))
	rich.MediaGroupID, classic.MediaGroupID = "g1", "g1"
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// The classic photo comes after the end of the limit, and the inbox reads it.
	txt, imgs, _, err := in.BuildFor(cctx, []Message{rich, classic}, "")
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != "CLASSIC" {
		t.Fatal(txt, imgs, err)
	}
	if want := "[image: " + imgs[0].Path + "]\n\n" + timeLimitNote; txt != want {
		t.Fatalf("got %q, want %q", txt, want)
	}
}

func TestRichMediaCallerContextEnds(t *testing.T) {
	in, api := slowInbox(t)
	in.RichMediaLimit = time.Minute
	api.addFile("lp1", "slow/lp1.jpg", []byte("ONE"))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	time.AfterFunc(20*time.Millisecond, cancel)
	txt, imgs, files, err := in.BuildFor(cctx, []Message{richMsg(`[` + limitPhoto(1, "") + `]`)}, "")
	if !errors.Is(err, context.Canceled) || txt != "" || imgs != nil || files != nil {
		t.Fatal(txt, imgs, files, err)
	}
}

func TestFetchReusesCachedFile(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/file_1.jpg", []byte("JPEG"))
	_, first, _, err := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(first[0].Path, old, old); err != nil {
		t.Fatal(err)
	}
	_, second, _, err := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if err != nil || len(second) != 1 || second[0].Path != first[0].Path || string(second[0].Data) != "JPEG" {
		t.Fatal(second, err)
	}
	// Count the downloads, not the getFile calls: fetch needs getFile for the size.
	if api.count("download") != 1 || api.count("getFile") != 2 {
		t.Fatal(api.methods())
	}
	st, err := os.Stat(second[0].Path)
	if err != nil || time.Since(st.ModTime()) > time.Minute {
		t.Fatal("a reused file must get a new modification time", st, err)
	}
}

func TestFetchDownloadsChangedFile(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/file_1.jpg", []byte("JPEG"))
	_, first, _, err := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	// A tool changed the cached file, so its size is not the getFile size.
	if err := os.WriteFile(first[0].Path, []byte("JPEG changed by a tool"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, second, _, err := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if err != nil || len(second) != 1 || string(second[0].Data) != "JPEG" || api.count("download") != 2 {
		t.Fatal(second, err, api.methods())
	}
}

func TestFetchIgnoresOtherNames(t *testing.T) {
	in, api := inbox(t)
	api.addFile("p320", "photos/file_1.jpg", []byte("JPEG"))
	if err := os.MkdirAll(in.MediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	planted := []string{"1-x-u320.jpg", "abc-u320.jpg", "1-u320.jpg.bak", "1-u320.png"}
	for _, name := range planted {
		if err := os.WriteFile(filepath.Join(in.MediaDir, name), []byte("JPEG"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A symbolic link with the right name and the right size is not a regular file.
	target := filepath.Join(t.TempDir(), "x.jpg")
	if err := os.WriteFile(target, []byte("JPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(in.MediaDir, "3-u320.jpg")); err != nil {
		t.Fatal(err)
	}
	planted = append(planted, "3-u320.jpg")
	_, imgs, _, err := in.Build(ctx, []Message{photoMsg("", sizes(320))})
	if err != nil || len(imgs) != 1 || api.count("download") != 1 {
		t.Fatal(imgs, err, api.methods())
	}
	for _, name := range planted {
		if filepath.Base(imgs[0].Path) == name {
			t.Fatal("fetch reused a file that does not match:", name)
		}
	}
}

func TestInlineTextLimitFromField(t *testing.T) {
	body := strings.Repeat("x", 2000)
	// A field of 1024 bytes saves a 2000-byte .txt file and does not inline it.
	in, api := inbox(t)
	in.InlineTextMaxBytes = 1024
	api.addFile("d-mid.txt", "documents/mid.txt", []byte(body))
	txt, _, files, _ := in.Build(ctx, []Message{docMsg("mid.txt", "text/plain", int64(len(body)))})
	if len(files) != 1 || txt != docNote("mid.txt", files[0]) {
		t.Fatalf("limit 1024: %q %v", txt[:min(len(txt), 120)], files)
	}
	// A zero field keeps 100 KB, so the same file is inlined.
	in, api = inbox(t)
	api.addFile("d-mid.txt", "documents/mid.txt", []byte(body))
	txt, _, files, _ = in.Build(ctx, []Message{docMsg("mid.txt", "text/plain", int64(len(body)))})
	if len(files) != 1 || !strings.HasPrefix(txt, textDocNote("mid.txt", files[0])) {
		t.Fatalf("zero field: %q %v", txt[:min(len(txt), 120)], files)
	}
	// A larger field inlines a file of 150 KB, which the default refuses.
	big := strings.Repeat("y", 150*1024)
	in, api = inbox(t)
	in.InlineTextMaxBytes = 200 * 1024
	api.addFile("d-big.md", "documents/big.md", []byte(big))
	txt, _, files, _ = in.Build(ctx, []Message{docMsg("big.md", "text/markdown", int64(len(big)))})
	if len(files) != 1 || !strings.HasPrefix(txt, textDocNote("big.md", files[0])) {
		t.Fatalf("limit 200 KB: %q %v", txt[:min(len(txt), 120)], files)
	}
}

func TestStickerDescribeTimeoutFromField(t *testing.T) {
	const fallback = `[The user sent a sticker~ It shows: "a sticker" (=^.w.^=)]`
	in, api := inbox(t)
	api.addFile("s6", "stickers/file_8.webp", []byte("RIFF"))
	in.DescribeTimeout = 20 * time.Millisecond
	in.Describe = func(c context.Context, _, _ string) (string, error) { <-c.Done(); return "", c.Err() }
	start := time.Now()
	txt, _, _, _ := in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s6", FileUniqueID: "us6"})})
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the describe call waited %s", el)
	}
	if txt != fallback {
		t.Fatalf("%q", txt)
	}
	// A zero field keeps the package limit of 30 s.
	in, api = inbox(t)
	api.addFile("s7", "stickers/file_9.webp", []byte("RIFF"))
	var left time.Duration
	in.Describe = func(c context.Context, _, _ string) (string, error) {
		dl, _ := c.Deadline()
		left = time.Until(dl)
		return "A cat", nil
	}
	in.Build(ctx, []Message{stickerMsg(Sticker{FileID: "s7", FileUniqueID: "us7"})})
	if left <= 29*time.Second || left > 30*time.Second {
		t.Fatalf("deadline in %s, want about 30s", left)
	}
}
