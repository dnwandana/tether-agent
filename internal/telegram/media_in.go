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

	"github.com/dnwandana/tether-agent/internal/media"
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
	// maxRichMedia is the most media parts that one BuildFor call reads from rich
	// messages. It is the size of a Telegram album.
	maxRichMedia = 10
	// defaultRichMediaLimit is the time limit for the media parts of the rich messages of one
	// BuildFor call. BuildFor runs in the worker of the chat, so each later update of the chat
	// waits for it.
	defaultRichMediaLimit = 2 * time.Minute
	// stickerVisionPrompt is the Hermes prompt for a static sticker.
	stickerVisionPrompt = "Describe this sticker in 1-2 sentences. Focus on what it depicts -- character, action, emotion. Be concise and objective."
)

// describeTimeout is the limit for the vision call of a sticker. Build runs
// in the worker of the chat, so a slow call stops the next message. Tests
// change it.
var describeTimeout = 30 * time.Second

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
	// RichMediaLimit is the time limit for the media parts of the rich messages of one
	// BuildFor call. Zero means defaultRichMediaLimit. Tests change it.
	RichMediaLimit time.Duration
	// InlineTextMaxBytes is the largest .md or .txt file that goes into the message text.
	// Zero means maxInlineTextBytes.
	InlineTextMaxBytes int
	// DescribeTimeout is the limit for the vision call of a sticker. Zero means
	// describeTimeout.
	DescribeTimeout time.Duration
}

// inlineTextMax returns the largest text file that goes into the message text.
func (in *Inbox) inlineTextMax() int {
	if in.InlineTextMaxBytes <= 0 {
		return maxInlineTextBytes
	}
	return in.InlineTextMaxBytes
}

// describeLimit returns the limit for the vision call of a sticker.
func (in *Inbox) describeLimit() time.Duration {
	if in.DescribeTimeout <= 0 {
		return describeTimeout
	}
	return in.DescribeTimeout
}

// richMediaLimit returns the time limit for the media parts of the rich messages.
func (in *Inbox) richMediaLimit() time.Duration {
	if in.RichMediaLimit <= 0 {
		return defaultRichMediaLimit
	}
	return in.RichMediaLimit
}

// Build is BuildFor with no bot user name.
func (in *Inbox) Build(ctx context.Context, msgs []Message) (text string, images []provider.Image, files []string, err error) {
	return in.BuildFor(ctx, msgs, "")
}

// BuildFor returns the user text for msgs (one message, or the parts of an
// album), the images for the model, and the paths of the other saved files.
// The notes about the media come first, then the captions (Hermes). A file
// that fails gets a note in the text. err is set only when ctx ends. botUser
// is the bot user name without "@". The inbox removes the mention of this bot
// from the text of a rich message. The media parts of the rich messages share one time limit.
// The end of the limit gives a note and no error.
func (in *Inbox) BuildFor(ctx context.Context, msgs []Message, botUser string) (text string, images []provider.Image, files []string, err error) {
	var notes, captions []string
	read := 0
	var end time.Time
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
			notes = in.addVideo(ctx, notes, &files, v.FileID, v.FileUniqueID, v.FileName, media.VideoExt(v.FileName, v.MimeType), v.FileSize)
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
		if d := richOf(m); d != nil {
			// The rich content is active only when Caption and Text are empty. So the text
			// from the rich parts takes the place of the caption.
			if c, err = in.buildRich(ctx, d.Lines(botUser), &read, &end, &images, &files); err != nil {
				return "", nil, nil, err
			}
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

// buildRich returns the text of one rich message from its lines. Each media part gets the
// note of its download at the position of its block. read counts the media parts of the rich
// messages of one BuildFor call. end is the end of the time limit. It is zero until the first
// media part starts.
func (in *Inbox) buildRich(ctx context.Context, lines []richLine, read *int, end *time.Time, images *[]provider.Image, files *[]string) (string, error) {
	notes := map[*richMedia]string{}
	for _, l := range lines {
		if l.Media == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if *read >= maxRichMedia {
			notes[l.Media] = "[" + l.Media.KindName() + " not read: Tether Agent reads the first 10 media files of a message]"
			continue
		}
		*read++
		if end.IsZero() {
			// The limit starts at the first media part of a rich message.
			*end = time.Now().Add(in.richMediaLimit())
		}
		ended := "[" + l.Media.KindName() + " not read: the time limit for the media of one message ended]"
		if !time.Now().Before(*end) {
			notes[l.Media] = ended
			continue
		}
		nImages, nFiles := len(*images), len(*files)
		pctx, cancel := context.WithDeadline(ctx, *end)
		note := in.readRichMedia(pctx, l.Media, images, files)
		// Read the two contexts before cancel, because cancel also ends pctx.
		stopped := pctx.Err() != nil && ctx.Err() == nil
		cancel()
		if stopped {
			// Keep only the parts that the text names.
			*images, *files = (*images)[:nImages], (*files)[:nFiles]
			note = ended
		}
		notes[l.Media] = note
	}
	return richJoin(lines, func(m *richMedia) string { return notes[m] }), nil
}

// readRichMedia reads one media part with the function of the classic media. It returns the
// note of that function. A part with no usable object keeps its placeholder.
func (in *Inbox) readRichMedia(ctx context.Context, m *richMedia, images *[]provider.Image, files *[]string) string {
	var notes []string
	switch {
	case m.Kind == "photo" && len(m.Photo) > 0:
		p := largestPhoto(m.Photo)
		notes = in.addImage(ctx, nil, images, p.FileID, p.FileUniqueID, p.FileSize, "", "photo")
	case m.Kind == "voice_note" && m.Voice != nil:
		notes = in.addVoice(ctx, nil, files, m.Voice)
	case m.Kind == "audio" && m.Audio != nil:
		notes = in.addAudio(ctx, nil, files, m.Audio)
	case (m.Kind == "video" || m.Kind == "animation") && m.Video != nil:
		v := m.Video
		notes = in.addVideo(ctx, nil, files, v.FileID, v.FileUniqueID, v.FileName, media.VideoExt(v.FileName, v.MimeType), v.FileSize)
	case m.Kind == "document" && m.Document != nil:
		notes = in.addDocument(ctx, nil, images, files, m.Document)
	default:
		return m.Placeholder
	}
	return strings.Join(notes, "\n")
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
		if e := strings.ToLower(filepath.Ext(filePath)); media.ImageMIME(e) != "" {
			return e
		}
		return ".jpg"
	})
	if err != nil {
		return append(notes, in.failed(what, err))
	}
	part, err := media.Note(ctx, media.KindImage, path, "", media.NoteDeps{Vision: true})
	if err != nil {
		return append(notes, in.failed(what, err))
	}
	if part.Image != nil {
		*images = append(*images, *part.Image)
	}
	return append(notes, part.Text)
}

// limits returns the limits of the shared check step.
func (in *Inbox) limits() media.Limits {
	return media.Limits{MaxBytes: in.maxBytes(), InlineTextMax: in.inlineTextMax()}
}

// addNote saves the part of a note step in files and gives its text.
func addNote(notes []string, files *[]string, part media.Part) []string {
	if part.File != "" {
		*files = append(*files, part.File)
	}
	return append(notes, part.Text)
}

func (in *Inbox) addVoice(ctx context.Context, notes []string, files *[]string, v *Voice) []string {
	if v.FileSize > in.maxBytes() {
		return append(notes, in.tooLarge("voice message", v.FileSize))
	}
	path, err := in.fetch(ctx, v.FileID, v.FileUniqueID, func(string) string { return ".ogg" })
	if err != nil {
		return append(notes, in.failed("voice message", err))
	}
	var stt func(context.Context, string) (string, error)
	if in.Transcribe != nil {
		stt = func(ctx context.Context, p string) (string, error) {
			said, err := in.Transcribe(ctx, p)
			if err != nil {
				in.logger().Warn("telegram voice transcription failed", "err", err.Error())
			}
			return said, err
		}
	}
	part, _ := media.Note(ctx, media.KindVoice, path, "", media.NoteDeps{STT: stt})
	return addNote(notes, files, part)
}

func (in *Inbox) addDocument(ctx context.Context, notes []string, images *[]provider.Image, files *[]string, d *Document) []string {
	ext := strings.ToLower(filepath.Ext(d.FileName))
	mime := strings.ToLower(d.MimeType)
	if ext == "" {
		ext = media.ExtByMime(mime)
	}
	name := media.DisplayName(d.FileName, ext)
	// Check the size before the download, so an image document cannot pass
	// the limit through the image path (Hermes).
	kind, reject := media.Check(d.FileName, d.MimeType, d.FileSize, in.limits())
	if reject != "" {
		return append(notes, reject)
	}
	switch kind {
	case media.KindImage:
		imgExt := ext
		if media.ImageMIME(imgExt) == "" {
			imgExt = media.ExtByMime(mime)
		}
		if media.ImageMIME(imgExt) == "" {
			imgExt = ".jpg"
		}
		return in.addImage(ctx, notes, images, d.FileID, d.FileUniqueID, d.FileSize, imgExt, name)
	case media.KindVideo:
		return in.addVideo(ctx, notes, files, d.FileID, d.FileUniqueID, d.FileName, media.VideoExt(d.FileName, d.MimeType), d.FileSize)
	}
	path, err := in.fetch(ctx, d.FileID, d.FileUniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	if d.FileSize == 0 {
		// Telegram can omit the size. The inline rule then uses the size of the saved file.
		if info, err := os.Stat(path); err == nil {
			kind, _ = media.Check(d.FileName, d.MimeType, info.Size(), in.limits())
		}
	}
	part, err := media.Note(ctx, kind, path, d.FileName, media.NoteDeps{})
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	return addNote(notes, files, part)
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
	name = media.DisplayName(name, ext)
	if a.FileSize > in.maxBytes() {
		return append(notes, in.tooLarge(name, a.FileSize))
	}
	path, err := in.fetch(ctx, a.FileID, a.FileUniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	part, err := media.Note(ctx, media.KindAudio, path, a.FileName, media.NoteDeps{})
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	return addNote(notes, files, part)
}

// addVideo saves a video with the extension ext. The video case and the
// video-document path both use it.
func (in *Inbox) addVideo(ctx context.Context, notes []string, files *[]string, fileID, uniqueID, fileName, ext string, size int64) []string {
	name := fileName
	if name == "" {
		name = "video" + ext
	}
	name = media.DisplayName(name, ext)
	if size > in.maxBytes() {
		return append(notes, in.tooLarge(name, size))
	}
	path, err := in.fetch(ctx, fileID, uniqueID, func(string) string { return ext })
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	part, err := media.Note(ctx, media.KindVideo, path, fileName, media.NoteDeps{})
	if err != nil {
		return append(notes, in.failed(name, err))
	}
	return addNote(notes, files, part)
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
	dctx, cancel := context.WithTimeout(ctx, in.describeLimit())
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

// errTooLarge is the fetch error for a file over the size limit.
type errTooLarge struct{ size int64 }

func (e errTooLarge) Error() string {
	return "file too large: " + strconv.FormatInt(e.size, 10) + " bytes"
}

// fetch downloads a file to MediaDir as <unix-ms>-<file_unique_id><ext>,
// with mode 0600. It checks the getFile size before the download. It reuses a
// cached file with the same ID and size instead of a new download.
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
	id := unsafeNameChars.ReplaceAllString(uniqueID, "_")
	x := ext(f.FilePath)
	if p := in.cachedFile(id, x, f.FileSize); p != "" {
		return p, nil
	}
	name := fmt.Sprintf("%d-%s%s", time.Now().UnixMilli(), id, x)
	path := filepath.Join(in.MediaDir, name)
	if err := in.C.DownloadFile(ctx, f.FilePath, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// cachedFile returns the path of a file in MediaDir that an earlier fetch saved for the same
// Telegram file, or "". The name must be exactly <digits>-<id><ext>, the file must be a
// regular file, and its size must be size. The size check also catches a file that a tool
// changed. A reused file gets the modification time now, so the media retention keeps it.
func (in *Inbox) cachedFile(id, ext string, size int64) string {
	if size <= 0 {
		return ""
	}
	entries, err := os.ReadDir(in.MediaDir)
	if err != nil {
		return ""
	}
	re, err := regexp.Compile(`^\d+-` + regexp.QuoteMeta(id+ext) + `$`)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !re.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != size {
			continue
		}
		path := filepath.Join(in.MediaDir, e.Name())
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			return ""
		}
		return path
	}
	return ""
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (in *Inbox) tooLarge(what string, size int64) string {
	return fmt.Sprintf("[file too large: %s has %d bytes; the limit is %d MB]", what, size, in.maxBytes()>>20)
}

func (in *Inbox) failed(what string, err error) string {
	var e errTooLarge
	if errors.As(err, &e) {
		return in.tooLarge(what, e.size)
	}
	// A stop by a time limit or by a cancel is not a download failure. The
	// caller gives its own note, so no warn line is needed.
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		in.logger().Warn("telegram media download failed", "what", what, "err", err.Error())
	}
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
