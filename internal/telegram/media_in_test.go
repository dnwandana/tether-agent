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
