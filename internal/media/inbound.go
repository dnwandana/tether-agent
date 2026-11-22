package media

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/provider"
)

const (
	// defaultMaxBytes is the largest inbound file.
	defaultMaxBytes = 20 << 20
	// defaultInlineTextMax is the largest .md or .txt file that goes into the message text
	// (Hermes MAX_TEXT_INJECT_BYTES).
	defaultInlineTextMax = 100 * 1024
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

var unsafeDisplayChars = regexp.MustCompile(`[^\w.\- ]`)

// DisplayName returns a safe file name for the text (Hermes).
func DisplayName(name, ext string) string {
	if name == "" {
		name = "document" + ext
	}
	return unsafeDisplayChars.ReplaceAllString(name, "_")
}

// ExtByMime returns the usual extension for a MIME type, or "".
func ExtByMime(mime string) string { return extByMime[strings.ToLower(mime)] }

// ImageMIME returns the MIME type of an image extension, or "" for an extension that is
// not an image.
func ImageMIME(ext string) string { return imageTypes[strings.ToLower(ext)] }

// VideoExt returns the extension of fileName when it is a video extension, else the
// extension for mime, else .mp4.
func VideoExt(fileName, mime string) string {
	if ext := strings.ToLower(filepath.Ext(fileName)); videoTypes[ext] != "" {
		return ext
	}
	if ext := videoExtByMime[strings.ToLower(mime)]; ext != "" {
		return ext
	}
	return ".mp4"
}

// Kind is the kind of a saved inbound file. It selects the note for the model.
type Kind string

const (
	KindImage      Kind = "image"
	KindVoice      Kind = "voice"
	KindInlineText Kind = "inline_text"
	KindDocument   Kind = "document"
	KindAudio      Kind = "audio"
	KindVideo      Kind = "video"
)

// Limits holds the size limits of the check step. A zero field uses the default.
type Limits struct {
	MaxBytes      int64
	InlineTextMax int
}

// Check decides the kind of an inbound file before the save. A reject text means: do not
// save the file, and give the text to the model.
func Check(name, mime string, size int64, lim Limits) (kind Kind, reject string) {
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = defaultMaxBytes
	}
	if lim.InlineTextMax <= 0 {
		lim.InlineTextMax = defaultInlineTextMax
	}
	mime = strings.ToLower(mime)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = extByMime[mime]
	}
	if size > lim.MaxBytes {
		return "", fmt.Sprintf("[file too large: %s has %d bytes; the limit is %d MB]", DisplayName(name, ext), size, lim.MaxBytes>>20)
	}
	if imageTypes[ext] != "" || strings.HasPrefix(mime, "image/") {
		return KindImage, ""
	}
	// A video sent as a file, and every GIF, arrives as a document (Hermes).
	vidExt := ext
	if vidExt == "" {
		vidExt = videoExtByMime[mime]
	}
	if videoTypes[vidExt] != "" {
		return KindVideo, ""
	}
	if _, ok := supportedDocumentTypes[ext]; !ok {
		return "", unsupportedNote(ext)
	}
	if (ext == ".md" || ext == ".txt") && size <= int64(lim.InlineTextMax) {
		return KindInlineText, ""
	}
	return KindDocument, ""
}

// NoteDeps holds the services of the note step.
type NoteDeps struct {
	// STT converts a voice note to text. Nil means that speech to text is not set up.
	STT func(ctx context.Context, path string) (string, error)
	// Vision is true when the caller attaches the image data to the message.
	Vision bool
}

// Part is the result of the note step for one saved file.
type Part struct {
	Text  string
	Image *provider.Image
	File  string
}

// Note returns the text for the model, and the image or the file, for a saved file.
func Note(ctx context.Context, kind Kind, path, name string, d NoteDeps) (Part, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch kind {
	case KindImage:
		data, err := os.ReadFile(path)
		if err != nil {
			return Part{}, err
		}
		p := Part{Text: "[image: " + path + "]"}
		if d.Vision {
			p.Image = &provider.Image{Path: path, MIME: imageTypes[ext], Data: data}
		}
		return p, nil
	case KindVoice:
		if d.STT == nil {
			return Part{Text: "[voice message received, but transcription is not set up]", File: path}, nil
		}
		said, err := d.STT(ctx, path)
		if err != nil {
			return Part{Text: fmt.Sprintf("[The user sent a voice message but I had trouble transcribing it~ (%s)]", err), File: path}, nil
		}
		// The transcript goes in as it is, with no Go escapes, like Hermes.
		return Part{Text: `[The user sent a voice message~ Here's what they said: "` + strings.TrimSpace(said) + `"]`, File: path}, nil
	case KindInlineText:
		shown := DisplayName(name, ext)
		if b, err := os.ReadFile(path); err == nil && utf8.Valid(b) {
			return Part{Text: savedNote(savedTextDocument, shown, path) + "\n\n" + fmt.Sprintf("[Content of %s]:\n%s", shown, b), File: path}, nil
		}
		return Part{Text: savedNote(savedDocument, shown, path), File: path}, nil
	case KindAudio:
		if name == "" {
			name = "audio" + ext
		}
		return Part{Text: savedNote(savedAudio, DisplayName(name, ext), path), File: path}, nil
	case KindVideo:
		if name == "" {
			name = "video" + ext
		}
		return Part{Text: savedNote(savedVideo, DisplayName(name, ext), path), File: path}, nil
	default:
		return Part{Text: savedNote(savedDocument, DisplayName(name, ext), path), File: path}, nil
	}
}
