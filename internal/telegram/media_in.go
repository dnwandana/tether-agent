package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
)

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

// Inbox turns the media of incoming messages into text, images, and saved
// files.
type Inbox struct {
	C        *Client
	MediaDir string
	// Transcribe converts a voice note to text. It is nil when speech to
	// text is not set up.
	Transcribe  func(ctx context.Context, path string) (string, error)
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
		case m.Document != nil:
			notes = in.addDocument(ctx, notes, &images, &files, m.Document)
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
	if _, ok := supportedDocumentTypes[ext]; !ok {
		kind := mime
		if kind == "" {
			kind = ext
		}
		if kind == "" {
			kind = "unknown"
		}
		return append(notes, "[unsupported file type: "+kind+"]")
	}
	path, err := in.fetch(ctx, d.FileID, d.FileUniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	*files = append(*files, path)
	if ext == ".md" || ext == ".txt" {
		if b, err := os.ReadFile(path); err == nil && len(b) <= maxInlineTextBytes && utf8.Valid(b) {
			return append(notes, fmt.Sprintf("[Content of %s]:\n%s", name, b))
		}
	}
	return append(notes, "[file: "+path+"]")
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
