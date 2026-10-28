package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/provider"
)

const (
	// defaultMaxDocBytes is the largest file that the inbox downloads.
	defaultMaxDocBytes = 20 << 20
	// maxInlineTextBytes is the largest .md or .txt file that goes into the
	// message text (Hermes MAX_TEXT_INJECT_BYTES).
	maxInlineTextBytes = 100 * 1024
	// defaultAlbumWait is the wait for the other parts of an album (Hermes
	// MEDIA_GROUP_WAIT_SECONDS).
	defaultAlbumWait = 800 * time.Millisecond
	// stickerVisionPrompt is the Hermes prompt for a static sticker.
	stickerVisionPrompt = "Describe this sticker in 1-2 sentences. Focus on what it depicts -- character, action, emotion. Be concise and objective."
)

// describeTimeout is the limit for the vision call of a sticker. Build runs
// in the worker of the chat, so a slow call stops the next message. Tests
// change it.
var describeTimeout = 30 * time.Second

// supportedDocumentTypes are the document types that the inbox saves. The
// list is Hermes SUPPORTED_DOCUMENT_TYPES (gateway/platforms/base.py).
var supportedDocumentTypes = map[string]string{
	".pdf":  "application/pdf",
	".md":   "text/markdown",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".log":  "text/plain",
	".json": "application/json",
	".xml":  "application/xml",
	".yaml": "application/yaml",
	".yml":  "application/yaml",
	".toml": "application/toml",
	".ini":  "text/plain",
	".cfg":  "text/plain",
	".zip":  "application/zip",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ts":   "text/plain",
	".py":   "text/plain",
	".sh":   "text/plain",
}

// extByMime gives the extension for a document without a file name. Where
// several extensions have the same type, it has the usual one.
var extByMime = map[string]string{
	"application/pdf":  ".pdf",
	"text/markdown":    ".md",
	"text/plain":       ".txt",
	"text/csv":         ".csv",
	"application/json": ".json",
	"application/xml":  ".xml",
	"application/yaml": ".yaml",
	"application/toml": ".toml",
	"application/zip":  ".zip",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// imageTypes are the image types that go to the model as images (Hermes
// SUPPORTED_IMAGE_DOCUMENT_TYPES).
var imageTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// videoTypes are the video types that the inbox saves as a video. The list
// is Hermes SUPPORTED_VIDEO_TYPES (gateway/platforms/base.py).
var videoTypes = map[string]string{
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
}

// videoExtByMime is the reverse of videoTypes. It gives the extension for a
// video without a file name.
var videoExtByMime = func() map[string]string {
	m := make(map[string]string, len(videoTypes))
	for ext, mime := range videoTypes {
		m[mime] = ext
	}
	return m
}()

// audioExtByMime gives the extension for an audio file without a file name.
// An unknown type gets .mp3.
var audioExtByMime = map[string]string{
	"audio/mpeg":  ".mp3",
	"audio/mp4":   ".m4a",
	"audio/x-m4a": ".m4a",
	"audio/ogg":   ".ogg",
	"audio/wav":   ".wav",
	"audio/x-wav": ".wav",
	"audio/flac":  ".flac",
}

// safeExt matches an extension from a file name that is safe in the saved
// file name.
var safeExt = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)

// Inbox turns the media of incoming messages into text, images, and saved
// files.
type Inbox struct {
	C        *Client
	MediaDir string
	// Transcribe converts a voice note to text. It is nil when speech to
	// text is not set up.
	Transcribe func(ctx context.Context, path string) (string, error)
	// Describe asks the vision model about an image file. It is nil when no vision model
	// is set up. A static sticker then gets the fallback text.
	Describe func(ctx context.Context, path, prompt string) (string, error)
	// Stickers keeps the sticker descriptions. Nil means no cache.
	Stickers    StickerCache
	MaxDocBytes int64
	Log         *slog.Logger
}

// Build returns the user text for msgs (one message, or the parts of an
// album), the images for the model, and the paths of the other saved files.
// The notes about the media come first, then the captions (Hermes). A file
// that fails gets a note in the text. err is set only when ctx ends.
func (in *Inbox) Build(ctx context.Context, msgs []Message) (text string, images []provider.Image, files []string, err error) {
	var notes, captions []string
	for _, m := range msgs {
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		switch {
		case len(m.Photo) > 0:
			p := largestPhoto(m.Photo)
			notes = in.addImage(ctx, notes, &images, p.FileID, p.FileUniqueID, p.FileSize, "", "photo")
		case m.Voice != nil:
			notes = in.addVoice(ctx, notes, &files, m.Voice)
		case m.Audio != nil:
			notes = in.addAudio(ctx, notes, &files, m.Audio)
		case m.Video != nil:
			v := m.Video
			notes = in.addVideo(ctx, notes, &files, v.FileID, v.FileUniqueID, v.FileName, videoExt(v.FileName, v.MimeType), v.FileSize)
		case m.Sticker != nil:
			notes = in.addSticker(ctx, notes, m.Sticker)
		case m.Document != nil:
			notes = in.addDocument(ctx, notes, &images, &files, m.Document)
		case m.Venue != nil:
			// A venue message also has a location, so this case comes first.
			notes = append(notes, locationNote(m.Venue.Location, m.Venue))
		case m.Location != nil:
			notes = append(notes, locationNote(*m.Location, nil))
		}
		c := m.Caption
		if c == "" {
			c = m.Text
		}
		if c = strings.TrimSpace(c); c != "" && !containsString(captions, c) {
			captions = append(captions, c)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, nil, err
	}
	return strings.Join(append(notes, captions...), "\n\n"), images, files, nil
}

// locationNote returns the note for a location pin. v is nil for a plain
// location. The text is the text of Hermes _handle_location_message.
func locationNote(loc Location, v *Venue) string {
	lat := strconv.FormatFloat(loc.Latitude, 'f', -1, 64)
	lon := strconv.FormatFloat(loc.Longitude, 'f', -1, 64)
	lines := []string{"[The user shared a location pin.]"}
	if v != nil && v.Title != "" {
		lines = append(lines, "Venue: "+v.Title)
	}
	if v != nil && v.Address != "" {
		lines = append(lines, "Address: "+v.Address)
	}
	lines = append(lines,
		"latitude: "+lat,
		"longitude: "+lon,
		"Map: https://www.google.com/maps/search/?api=1&query="+lat+","+lon,
		"Ask what they'd like to find nearby (restaurants, cafes, etc.) and any preferences.",
	)
	return strings.Join(lines, "\n")
}

// largestPhoto returns the size with the most pixels. On a tie, the later
// size wins, because Telegram lists the sizes from small to large.
func largestPhoto(ps []PhotoSize) PhotoSize {
	best := ps[0]
	for _, p := range ps[1:] {
		if p.Width*p.Height >= best.Width*best.Height {
			best = p
		}
	}
	return best
}

func (in *Inbox) addImage(ctx context.Context, notes []string, images *[]provider.Image, fileID, uniqueID string, size int64, ext, what string) []string {
	if size > in.maxBytes() {
		return append(notes, in.tooLarge(what, size))
	}
	path, err := in.fetch(ctx, fileID, uniqueID, func(filePath string) string {
		if ext != "" {
			return ext
		}
		if e := strings.ToLower(filepath.Ext(filePath)); imageTypes[e] != "" {
			return e
		}
		return ".jpg"
	})
	if err != nil {
		return append(notes, in.failed(what, err))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return append(notes, in.failed(what, err))
	}
	*images = append(*images, provider.Image{Path: path, MIME: imageTypes[filepath.Ext(path)], Data: data})
	return append(notes, "[image: "+path+"]")
}

func (in *Inbox) addVoice(ctx context.Context, notes []string, files *[]string, v *Voice) []string {
	if v.FileSize > in.maxBytes() {
		return append(notes, in.tooLarge("voice message", v.FileSize))
	}
	path, err := in.fetch(ctx, v.FileID, v.FileUniqueID, func(string) string { return ".ogg" })
	if err != nil {
		return append(notes, in.failed("voice message", err))
	}
	*files = append(*files, path)
	if in.Transcribe == nil {
		return append(notes, "[voice message received, but transcription is not set up]")
	}
	said, err := in.Transcribe(ctx, path)
	if err != nil {
		in.logger().Warn("telegram voice transcription failed", "err", err.Error())
		return append(notes, fmt.Sprintf("[The user sent a voice message but I had trouble transcribing it~ (%s)]", err))
	}
	// The transcript goes in as it is, with no Go escapes, like Hermes.
	return append(notes, `[The user sent a voice message~ Here's what they said: "`+strings.TrimSpace(said)+`"]`)
}

func (in *Inbox) addDocument(ctx context.Context, notes []string, images *[]provider.Image, files *[]string, d *Document) []string {
	ext := strings.ToLower(filepath.Ext(d.FileName))
	mime := strings.ToLower(d.MimeType)
	if ext == "" {
		ext = extByMime[mime]
	}
	name := displayName(d.FileName, ext)
	// Check the size before the download, so an image document cannot pass
	// the limit through the image path (Hermes).
	if d.FileSize > in.maxBytes() {
		return append(notes, in.tooLarge(name, d.FileSize))
	}
	if imageTypes[ext] != "" || strings.HasPrefix(mime, "image/") {
		imgExt := ext
		if imageTypes[imgExt] == "" {
			imgExt = extByMime[mime]
		}
		if imageTypes[imgExt] == "" {
			imgExt = ".jpg"
		}
		return in.addImage(ctx, notes, images, d.FileID, d.FileUniqueID, d.FileSize, imgExt, name)
	}
	// A video sent as a file, and every GIF, arrives as a document (Hermes).
	vidExt := ext
	if vidExt == "" {
		vidExt = videoExtByMime[mime]
	}
	if videoTypes[vidExt] != "" {
		return in.addVideo(ctx, notes, files, d.FileID, d.FileUniqueID, d.FileName, vidExt, d.FileSize)
	}
	if _, ok := supportedDocumentTypes[ext]; !ok {
		return append(notes, unsupportedNote(ext))
	}
	path, err := in.fetch(ctx, d.FileID, d.FileUniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	*files = append(*files, path)
	if ext == ".md" || ext == ".txt" {
		if b, err := os.ReadFile(path); err == nil && len(b) <= maxInlineTextBytes && utf8.Valid(b) {
			return append(notes, savedNote(savedTextDocument, name, path)+"\n\n"+fmt.Sprintf("[Content of %s]:\n%s", name, b))
		}
	}
	return append(notes, savedNote(savedDocument, name, path))
}

// addAudio saves an audio file. It does not transcribe the file, because
// Hermes transcribes only a voice note.
func (in *Inbox) addAudio(ctx context.Context, notes []string, files *[]string, a *Audio) []string {
	ext := strings.ToLower(filepath.Ext(a.FileName))
	if !safeExt.MatchString(ext) {
		ext = audioExtByMime[strings.ToLower(a.MimeType)]
	}
	if ext == "" {
		ext = ".mp3"
	}
	name := a.FileName
	if name == "" {
		name = "audio" + ext
	}
	name = displayName(name, ext)
	if a.FileSize > in.maxBytes() {
		return append(notes, in.tooLarge(name, a.FileSize))
	}
	path, err := in.fetch(ctx, a.FileID, a.FileUniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	*files = append(*files, path)
	return append(notes, savedNote(savedAudio, name, path))
}

// videoExt returns the extension for a video: the extension of fileName
// when it is in videoTypes, else the extension for mime, else .mp4.
func videoExt(fileName, mime string) string {
	if ext := strings.ToLower(filepath.Ext(fileName)); videoTypes[ext] != "" {
		return ext
	}
	if ext := videoExtByMime[strings.ToLower(mime)]; ext != "" {
		return ext
	}
	return ".mp4"
}

// addVideo saves a video with the extension ext. The video case and the
// video-document path both use it.
func (in *Inbox) addVideo(ctx context.Context, notes []string, files *[]string, fileID, uniqueID, fileName, ext string, size int64) []string {
	name := fileName
	if name == "" {
		name = "video" + ext
	}
	name = displayName(name, ext)
	if size > in.maxBytes() {
		return append(notes, in.tooLarge(name, size))
	}
	path, err := in.fetch(ctx, fileID, uniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	*files = append(*files, path)
	return append(notes, savedNote(savedVideo, name, path))
}

// addSticker gives the note for a sticker. The vision model describes a
// static sticker, and the cache keeps the description. The sticker file does
// not go to the model and does not go into files (Hermes).
func (in *Inbox) addSticker(ctx context.Context, notes []string, s *Sticker) []string {
	if s.IsAnimated || s.IsVideo {
		return append(notes, animatedStickerNote(s.Emoji))
	}
	useCache := in.Stickers != nil && s.FileUniqueID != ""
	if useCache {
		if info, ok := in.Stickers.Get(ctx, s.FileUniqueID); ok {
			return append(notes, stickerNote(info.Description, info.Emoji, info.SetName))
		}
	}
	// The fallback has no set name, like Hermes. It does not go into the
	// cache, so a later copy of the sticker gets a new vision call.
	fallback := "a sticker"
	if s.Emoji != "" {
		fallback = "a sticker with emoji " + s.Emoji
	}
	fallback = stickerNote(fallback, s.Emoji, "")
	if in.Describe == nil {
		return append(notes, fallback)
	}
	path, err := in.fetch(ctx, s.FileID, s.FileUniqueID, func(string) string { return ".webp" })
	if err != nil {
		in.logger().Warn("telegram media download failed", "what", "sticker", "err", err.Error())
		return append(notes, fallback)
	}
	dctx, cancel := context.WithTimeout(ctx, describeTimeout)
	desc, err := in.Describe(dctx, path, stickerVisionPrompt)
	cancel()
	if err != nil {
		in.logger().Warn("telegram sticker description failed", "err", err.Error())
		return append(notes, fallback)
	}
	if desc = strings.TrimSpace(desc); desc == "" {
		return append(notes, fallback)
	}
	if useCache {
		in.Stickers.Put(ctx, s.FileUniqueID, StickerInfo{Description: desc, Emoji: s.Emoji, SetName: s.SetName})
	}
	return append(notes, stickerNote(desc, s.Emoji, s.SetName))
}

// stickerNote returns the note for a static sticker. It is the port of
// Hermes build_sticker_injection.
func stickerNote(description, emoji, setName string) string {
	about := ""
	if emoji != "" && setName != "" {
		about = " " + emoji + ` from "` + setName + `"`
	} else if emoji != "" {
		about = " " + emoji
	}
	return "[The user sent a sticker" + about + `~ It shows: "` + description + `" (=^.w.^=)]`
}

// animatedStickerNote returns the note for an animated or a video sticker.
// It is the port of Hermes build_animated_sticker_injection.
func animatedStickerNote(emoji string) string {
	if emoji != "" {
		return "[The user sent an animated sticker " + emoji + "~ I can't see animated ones yet, but the emoji suggests: " + emoji + "]"
	}
	return "[The user sent an animated sticker~ I can't see animated ones yet]"
}

// savedKind selects the text of a note for a saved file.
type savedKind int

const (
	// savedDocument is a document that the text does not contain.
	savedDocument savedKind = iota
	// savedTextDocument is a .md or .txt document that the text contains.
	savedTextDocument
	// savedAudio is an audio file, not a voice note.
	savedAudio
	// savedVideo is a video, also a video that arrives as a document.
	savedVideo
)

// savedNote returns the note for a file that the inbox saved at path. The
// texts are Hermes texts, and the video text has the shape of the document
// text. The text-document note says that the content is in
// the text, so use it only when the content is in the text.
func savedNote(kind savedKind, name, path string) string {
	switch kind {
	case savedTextDocument:
		return "[The user sent a text document: '" + name + "'. Its content has been included below. The file is also saved at: " + path + "]"
	case savedAudio:
		return "[The user sent an audio file attachment: '" + name + "'. It is saved at: " + path + ". Ask the user what they'd like you to do with it, or pass the path to a transcription or media tool.]"
	case savedVideo:
		return "[The user sent a video: '" + name + "'. It is saved at: " + path + ". Ask the user what they'd like you to do with it.]"
	default:
		return "[The user sent a document: '" + name + "'. The file is saved at: " + path + ". Ask the user what they'd like you to do with it.]"
	}
}

// unsupportedNote returns the Hermes text for a document type that is not in
// supportedDocumentTypes. ext is the extension with its dot, or empty.
func unsupportedNote(ext string) string {
	if ext == "" {
		ext = "unknown"
	}
	list := strings.Join(slices.Sorted(maps.Keys(supportedDocumentTypes)), ", ")
	return "Unsupported document type '" + ext + "'. Supported types: " + list
}

// errTooLarge is the fetch error for a file over the size limit.
type errTooLarge struct{ size int64 }

func (e errTooLarge) Error() string {
	return "file too large: " + strconv.FormatInt(e.size, 10) + " bytes"
}

// fetch downloads a file to MediaDir as <unix-ms>-<file_unique_id><ext>,
// with mode 0600. It checks the getFile size before the download.
func (in *Inbox) fetch(ctx context.Context, fileID, uniqueID string, ext func(filePath string) string) (string, error) {
	f, err := in.C.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	if f.FileSize > in.maxBytes() {
		return "", errTooLarge{f.FileSize}
	}
	if uniqueID == "" {
		uniqueID = fileID
	}
	name := fmt.Sprintf("%d-%s%s", time.Now().UnixMilli(), unsafeNameChars.ReplaceAllString(uniqueID, "_"), ext(f.FilePath))
	path := filepath.Join(in.MediaDir, name)
	if err := in.C.DownloadFile(ctx, f.FilePath, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

var (
	unsafeNameChars    = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	unsafeDisplayChars = regexp.MustCompile(`[^\w.\- ]`)
)

// displayName returns a safe file name for the text (Hermes).
func displayName(name, ext string) string {
	if name == "" {
		name = "document" + ext
	}
	return unsafeDisplayChars.ReplaceAllString(name, "_")
}

func (in *Inbox) tooLarge(what string, size int64) string {
	return fmt.Sprintf("[file too large: %s has %d bytes; the limit is %d MB]", what, size, in.maxBytes()>>20)
}

func (in *Inbox) failed(what string, err error) string {
	var e errTooLarge
	if errors.As(err, &e) {
		return in.tooLarge(what, e.size)
	}
	in.logger().Warn("telegram media download failed", "what", what, "err", err.Error())
	return "[" + what + " could not be downloaded]"
}

func (in *Inbox) maxBytes() int64 {
	if in.MaxDocBytes <= 0 {
		return defaultMaxDocBytes
	}
	return in.MaxDocBytes
}

func (in *Inbox) logger() *slog.Logger {
	if in.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return in.Log
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// AlbumBuffer merges the messages of one album. Telegram sends each part of
// an album as its own update with the same media_group_id. Without the merge,
// the second part looks like a new message and interrupts the first (Hermes).
type AlbumBuffer struct {
	// Wait is the time after the last part before the flush. It is 0.8 s
	// when it is zero.
	Wait time.Duration

	mu     sync.Mutex
	groups map[string]*albumGroup
}

type albumGroup struct {
	msgs  []Message
	timer *time.Timer
	flush func([]Message)
}

// Add adds m. A message without media_group_id goes to flush at once. The
// parts of an album go to flush together, one wait after the last part.
// flush runs in a timer goroutine for an album.
func (ab *AlbumBuffer) Add(m Message, flush func([]Message)) {
	if m.MediaGroupID == "" {
		flush([]Message{m})
		return
	}
	key := strconv.FormatInt(m.Chat.ID, 10) + ":" + m.MediaGroupID
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if ab.groups == nil {
		ab.groups = map[string]*albumGroup{}
	}
	g := ab.groups[key]
	if g == nil {
		g = &albumGroup{}
		ab.groups[key] = g
	}
	g.msgs = append(g.msgs, m)
	g.flush = flush
	if g.timer != nil {
		g.timer.Stop()
	}
	wait := ab.Wait
	if wait <= 0 {
		wait = defaultAlbumWait
	}
	g.timer = time.AfterFunc(wait, func() {
		ab.mu.Lock()
		if ab.groups[key] != g {
			ab.mu.Unlock()
			return
		}
		delete(ab.groups, key)
		msgs, f := g.msgs, g.flush
		ab.mu.Unlock()
		f(msgs)
	})
}
