package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// mediaDeliveryExts are the file extensions that a MEDIA tag or a bare path
// can deliver. The list is Hermes MEDIA_DELIVERY_EXTS (gateway/platforms/base.py),
// plus "oga", the extension of a Telegram voice note.
var mediaDeliveryExts = []string{
	"png", "jpg", "jpeg", "gif", "webp", "bmp", "tiff", "svg",
	"mp4", "mov", "avi", "mkv", "webm",
	"mp3", "wav", "ogg", "oga", "opus", "m4a", "flac",
	"pdf", "docx", "doc", "odt", "rtf", "txt", "md", "epub",
	"xlsx", "xls", "ods", "csv", "tsv", "json", "xml", "yaml", "yml",
	"pptx", "ppt", "odp", "key",
	"zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "apk", "ipa",
	"html", "htm",
}

// extAlternation is the regexp alternation of mediaDeliveryExts, longest
// first, so a short extension never wins over a long one.
var extAlternation = func() string {
	exts := append([]string(nil), mediaDeliveryExts...)
	sort.SliceStable(exts, func(i, j int) bool { return len(exts[i]) > len(exts[j]) })
	return strings.Join(exts, "|")
}()

// mediaTagRE matches a MEDIA:<path> tag. It is Hermes MEDIA_TAG_CLEANUP_RE.
// Go regexp has no lookahead, so the regexp consumes the character after the
// path (group "end"). The tag ends at the end of group "path", plus the
// closing quote in group "quote" when there is one.
var mediaTagRE = regexp.MustCompile(`(?i)[` + "`" + `"']?MEDIA:\s*` +
	`(?P<path>` + "`[^`\\n]+`" + `|"[^"\n]+"|'[^'\n]+'|` +
	`(?:~/|/)\S+(?:[^\S\n]+\S+)*?\.(?:` + extAlternation + `))` +
	`(?:(?P<quote>[` + "`" + `"'])|[\s,;:)\]}]|$)`)

// mediaTag is one MEDIA tag in a text.
type mediaTag struct {
	start, end int    // the byte span of the tag
	path       string // the path, without quotes
}

// findMediaTags returns the MEDIA tags in text, in order.
func findMediaTags(text string) []mediaTag {
	if !strings.Contains(strings.ToUpper(text), "MEDIA:") {
		return nil
	}
	pathIdx := mediaTagRE.SubexpIndex("path")
	quoteIdx := mediaTagRE.SubexpIndex("quote")
	var out []mediaTag
	for _, m := range mediaTagRE.FindAllStringSubmatchIndex(text, -1) {
		end := m[2*pathIdx+1]
		if m[2*quoteIdx] >= 0 {
			end = m[2*quoteIdx+1]
		}
		p := strings.TrimSpace(text[m[2*pathIdx]:m[2*pathIdx+1]])
		if len(p) >= 2 && p[0] == p[len(p)-1] && strings.ContainsRune("`\"'", rune(p[0])) {
			p = strings.TrimSpace(p[1 : len(p)-1])
		}
		p = strings.TrimRight(strings.TrimLeft(p, "`\"'"), "`\"',.;:)}]")
		out = append(out, mediaTag{start: m[0], end: end, path: p})
	}
	return out
}

var reManyNewlines = regexp.MustCompile(`\n{3,}`)

// cleanForDisplay hides the MEDIA tags and the delivery flags in streamed
// text (Hermes _clean_for_display). The files go out after the answer.
func cleanForDisplay(text string) string {
	if !strings.Contains(strings.ToUpper(text), "MEDIA:") && !strings.Contains(text, "[[") {
		return text
	}
	cleaned := strings.ReplaceAll(text, flagVoice, "")
	cleaned = strings.ReplaceAll(cleaned, flagDocument, "")
	cleaned = removeTags(cleaned, findMediaTags(cleaned))
	cleaned = reManyNewlines.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimRight(cleaned, " \t\r\n")
}

// removeTags deletes the spans of tags from text.
func removeTags(text string, tags []mediaTag) string {
	if len(tags) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, t := range tags {
		b.WriteString(text[last:t.start])
		last = t.end
	}
	b.WriteString(text[last:])
	return b.String()
}

const (
	// flagVoice asks for audio as a voice note.
	flagVoice = "[[audio_as_voice]]"
	// flagDocument asks for images as documents.
	flagDocument = "[[as_document]]"
)

// maxUploadBytes is the Bot API limit for a file upload.
const maxUploadBytes = 50 << 20

// OutFile is one file to send after the answer text. Kind is "photo",
// "audio", "video", or "document".
type OutFile struct {
	Path       string
	Kind       string
	AsVoice    bool
	AsDocument bool
}

// mediaKind returns the send kind for a file name, from its extension.
func mediaKind(name string) string {
	switch strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".") {
	case "png", "jpg", "jpeg", "webp", "gif":
		return "photo"
	case "mp3", "ogg", "oga", "opus", "m4a", "wav":
		return "audio"
	case "mp4", "mov", "webm":
		return "video"
	}
	return "document"
}

var (
	reMaskFenced = regexp.MustCompile("(?s)```[^\n]*\n.*?```")
	reMaskInline = regexp.MustCompile("`[^`\n]+`")
	reMaskQuote  = regexp.MustCompile(`(?m)^>.*$`)
	reTagBefore  = regexp.MustCompile(`MEDIA:\s*$`)
	// barePathRE matches an absolute or home path with a deliverable
	// extension (Hermes extract_local_files). Go regexp has no lookbehind,
	// so ExtractMedia checks the character before the match.
	barePathRE = regexp.MustCompile(`(?i)(?:~/|/)(?:[\w.\-]+/)*[\w.\-]+\.(?:` + extAlternation + `)\b`)
)

// maskProtected replaces fenced code, inline code, and blockquote lines with
// spaces, so examples in them are not sent as files (Hermes
// _mask_protected_spans). The byte offsets do not change. A backtick path
// right after "MEDIA:" is not masked.
func maskProtected(text string) string {
	if !strings.ContainsAny(text, "`>") {
		return text
	}
	b := []byte(text)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	for _, m := range reMaskFenced.FindAllStringIndex(text, -1) {
		blank(m[0], m[1])
	}
	for _, m := range reMaskInline.FindAllStringIndex(text, -1) {
		if reTagBefore.MatchString(text[max(0, m[0]-20):m[0]]) {
			continue
		}
		blank(m[0], m[1])
	}
	for _, m := range reMaskQuote.FindAllStringIndex(text, -1) {
		blank(m[0], m[1])
	}
	return string(b)
}

// ExtractMedia takes the files out of an answer text. It takes
// out the MEDIA:<path> tags, and the bare absolute paths of existing files
// with a deliverable extension. Only the files that the policy p accepts are
// sent. A tag for a file that does not exist becomes "[missing file: <path>]"
// in the text, and a tag for a file that p refuses becomes
// "[blocked file: <path>]". A bare path that p refuses stays in the text. The
// flags [[audio_as_voice]] and [[as_document]] apply to all audio files and
// all images of the text. A nil p refuses all files.
func (p *MediaPolicy) ExtractMedia(text string) (clean string, files []OutFile) {
	asVoice := strings.Contains(text, flagVoice)
	asDoc := strings.Contains(text, flagDocument)
	clean = strings.ReplaceAll(text, flagVoice, "")
	clean = strings.ReplaceAll(clean, flagDocument, "")
	changed := clean != text

	seen := map[string]bool{}
	add := func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		kind := mediaKind(path)
		files = append(files, OutFile{Path: path, Kind: kind, AsVoice: asVoice && kind == "audio", AsDocument: asDoc && kind == "photo"})
	}

	if tags := findMediaTags(maskProtected(clean)); len(tags) > 0 {
		var b strings.Builder
		last := 0
		for _, t := range tags {
			b.WriteString(clean[last:t.start])
			last = t.end
			path, err := p.Check(t.path)
			switch {
			case err == nil:
				add(path)
			case errors.Is(err, ErrMediaBlocked):
				b.WriteString("[blocked file: " + t.path + "]")
			default:
				b.WriteString("[missing file: " + t.path + "]")
			}
		}
		b.WriteString(clean[last:])
		clean, changed = b.String(), true
	}

	masked := maskProtected(clean)
	var b strings.Builder
	last := 0
	for _, m := range barePathRE.FindAllStringIndex(masked, -1) {
		if m[0] > 0 && pathCannotStartAfter(masked[m[0]-1]) {
			continue
		}
		path, err := p.Check(clean[m[0]:m[1]])
		if err != nil {
			continue
		}
		add(path)
		b.WriteString(clean[last:m[0]])
		last = m[1]
	}
	if last > 0 {
		b.WriteString(clean[last:])
		clean, changed = b.String(), true
	}
	if changed {
		clean = strings.TrimSpace(reManyNewlines.ReplaceAllString(clean, "\n\n"))
	}
	return clean, files
}

// pathCannotStartAfter reports whether a bare path cannot start after c. This
// keeps out URLs and relative paths, like the Hermes (?<![/:\w.]) lookbehind.
func pathCannotStartAfter(c byte) bool {
	return c == '/' || c == ':' || c == '.' || c == '_' ||
		('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// The environment variables of the media delivery policy. They are the Hermes
// HERMES_MEDIA_* variables (gateway/platforms/base.py) with the TETHER_ prefix.
const (
	// EnvMediaStrict turns on strict mode: 1, true, yes, or on.
	EnvMediaStrict = "TETHER_MEDIA_DELIVERY_STRICT"
	// EnvMediaAllowDirs adds folders whose files are always sent. The
	// separator is the path list separator (":") or ",".
	EnvMediaAllowDirs = "TETHER_MEDIA_ALLOW_DIRS"
	// EnvMediaTrustRecent turns off the recency trust of strict mode: 0,
	// false, no, or off.
	EnvMediaTrustRecent = "TETHER_MEDIA_TRUST_RECENT_FILES"
	// EnvMediaTrustRecentSeconds is the recency window of strict mode.
	EnvMediaTrustRecentSeconds = "TETHER_MEDIA_TRUST_RECENT_SECONDS"
)

// defaultTrustRecent is the Hermes recency window of strict mode.
const defaultTrustRecent = 10 * time.Minute

// ErrMediaBlocked marks a file that the media policy refuses to send.
var ErrMediaBlocked = errors.New("the media policy blocks the file")

// errMediaMissing marks a path that is not a regular file that can be read.
var errMediaMissing = errors.New("no such regular file")

// deniedSystemPrefixes are the Hermes _MEDIA_DELIVERY_DENIED_PREFIXES.
var deniedSystemPrefixes = []string{"/etc", "/proc", "/sys", "/dev", "/root", "/boot", "/var/log", "/var/lib", "/var/run"}

// deniedHomeDirs are the Hermes _MEDIA_DELIVERY_DENIED_HOME_SUBPATHS. They
// hold credentials.
var deniedHomeDirs = []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".config", ".azure", ".gcloud", "Library/Keychains"}

// MediaPolicy decides which local files the model can send to a chat with a
// MEDIA tag or a bare path. It is the Hermes rule validate_media_delivery_path
// (gateway/platforms/base.py). The rules, in order:
//
//  1. The path must be absolute and name a regular file that can be read.
//     The checks use the real path, after the symlinks.
//  2. A protected path (security.ProtectedPaths) is always refused.
//  3. A file in Roots is sent.
//  4. A file in Denied is refused.
//  5. In the default mode, all other files are sent. In strict mode, a file is
//     sent only when it changed within TrustRecent.
//
// A prompt injection can make the model name any path, so the denied paths
// hold the credentials and the system files. Strict mode is for a bot that
// users who are not the operator can use.
type MediaPolicy struct {
	// Protected holds the protected paths, in the form of
	// security.ProtectedPaths. Each is refused, also in Roots.
	Protected []string
	// Roots holds the folders whose files are always sent.
	Roots []string
	// Denied holds the folders and files that are refused unless they are in
	// Roots.
	Denied []string
	// Strict sends only the files in Roots and the recent files.
	Strict bool
	// TrustRecent is the recency window of strict mode. Zero turns off the
	// recency trust.
	TrustRecent time.Duration
	// Log receives a warning for each refused file. Nil logs nothing.
	Log *slog.Logger

	// now is the clock of the recency check. Nil uses time.Now.
	now func() time.Time
}

// NewMediaPolicy returns the media policy for the data folder dataDir. The
// roots are <data>/cache/media (the received files, and the images and audio
// of the media tools) and <data>/cache/screenshots (the browser),
// plus the folders of TETHER_MEDIA_ALLOW_DIRS. The denied paths are the Hermes
// system folders and the credential folders in the home folder. lookup reads
// the TETHER_MEDIA_* variables, for example security.EnvLookup(dotenv).
func NewMediaPolicy(dataDir string, lookup func(string) (string, bool), log *slog.Logger) *MediaPolicy {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := func(name string) string {
		v, _ := lookup(name)
		return strings.TrimSpace(v)
	}
	home, _ := os.UserHomeDir()
	p := &MediaPolicy{
		Protected:   security.ProtectedPaths(dataDir, home),
		Denied:      append([]string(nil), deniedSystemPrefixes...),
		Strict:      isTruthy(env(EnvMediaStrict)),
		TrustRecent: defaultTrustRecent,
		Log:         log,
	}
	if dataDir != "" {
		p.Roots = append(p.Roots, filepath.Join(dataDir, "cache", "media"), filepath.Join(dataDir, "cache", "screenshots"))
	}
	for _, chunk := range filepath.SplitList(env(EnvMediaAllowDirs)) {
		for _, raw := range strings.Split(chunk, ",") {
			if root := expandHome(strings.TrimSpace(raw)); filepath.IsAbs(root) {
				p.Roots = append(p.Roots, root)
			}
		}
	}
	if home != "" {
		for _, sub := range deniedHomeDirs {
			p.Denied = append(p.Denied, filepath.Join(home, sub))
		}
	}
	switch strings.ToLower(env(EnvMediaTrustRecent)) {
	case "0", "false", "no", "off":
		p.TrustRecent = 0
	default:
		if raw := env(EnvMediaTrustRecentSeconds); raw != "" {
			if s, err := strconv.ParseFloat(raw, 64); err == nil {
				p.TrustRecent = time.Duration(max(s, 0) * float64(time.Second))
			}
		}
	}
	return p
}

// isTruthy reports whether an environment value means "on" (Hermes).
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Check returns the real path of a file that the policy lets the model send.
// The error wraps ErrMediaBlocked for a refused file. Other errors mean that
// path is not a regular file that can be read. A nil p refuses all files.
func (p *MediaPolicy) Check(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errMediaMissing
	}
	abs := expandHome(path)
	if !filepath.IsAbs(abs) {
		return "", errMediaMissing
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !isReadableFile(real) {
		return "", errMediaMissing
	}
	if p == nil {
		return "", p.refuse(path, "no media policy is set")
	}
	if err := security.CheckWritable(abs, p.Protected, false); err != nil {
		return "", p.refuse(path, "protected path")
	}
	if err := security.CheckWritable(real, p.Protected, false); err != nil {
		return "", p.refuse(path, "protected path")
	}
	if under(real, p.Roots) {
		return real, nil
	}
	if under(real, p.Denied) {
		return "", p.refuse(path, "denied folder")
	}
	if !p.Strict {
		return real, nil
	}
	if p.TrustRecent > 0 {
		if st, err := os.Stat(real); err == nil && p.clock().Sub(st.ModTime()) <= p.TrustRecent {
			return real, nil
		}
	}
	return "", p.refuse(path, "strict mode: not in a media folder and not recent")
}

// refuse logs the refused path and returns the error for it.
func (p *MediaPolicy) refuse(path, reason string) error {
	if p != nil && p.Log != nil {
		p.Log.Warn("telegram: a file is not sent: the media policy blocks it",
			"path", logSafePath(path), "reason", reason)
	}
	return fmt.Errorf("%s: %w (%s)", path, ErrMediaBlocked, reason)
}

func (p *MediaPolicy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// under reports whether the real path real is one of roots or is in one of
// them. Each root is compared with its real path too.
func under(real string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		bases := []string{filepath.Clean(root)}
		if r, err := filepath.EvalSymlinks(root); err == nil && r != bases[0] {
			bases = append(bases, r)
		}
		for _, b := range bases {
			if real == b || b == string(filepath.Separator) ||
				strings.HasPrefix(real, b+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// logSafePath returns path on one line and at most 200 bytes, so a model path
// cannot make a false log line (Hermes _log_safe_path).
func logSafePath(path string) string {
	path = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 {
			return '?'
		}
		return r
	}, path)
	if len(path) > 200 {
		path = path[:200]
	}
	return path
}

// isReadableFile reports whether p is a regular file that can be read.
func isReadableFile(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && st.Mode().IsRegular()
}

// Sender sends the files of an answer.
type Sender struct {
	C *Client
	// ConvertVoice converts an audio file to OGG/Opus for a voice note. Nil
	// sends the audio as a file. FFmpegToOpus gives the ffmpeg converter.
	ConvertVoice func(ctx context.Context, in string) (string, error)
}

// SendFiles sends each file by its kind. A failed file does not stop the
// others. The error names each file that failed.
func (s *Sender) SendFiles(ctx context.Context, chatID int64, threadID string, files []OutFile) error {
	var errs []error
	for _, f := range files {
		if err := s.sendFile(ctx, chatID, threadID, f); err != nil {
			errs = append(errs, fmt.Errorf("send %s: %w", filepath.Base(f.Path), err))
		}
	}
	return errors.Join(errs...)
}

func (s *Sender) sendFile(ctx context.Context, chatID int64, threadID string, f OutFile) error {
	st, err := os.Stat(f.Path)
	if err != nil {
		return err
	}
	if st.Size() > maxUploadBytes {
		return fmt.Errorf("the file has %d bytes; the Telegram upload limit is 50 MB", st.Size())
	}
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if id, ok := ThreadParam(threadID, false); ok {
		fields["message_thread_id"] = strconv.Itoa(id)
	}
	ext := strings.ToLower(filepath.Ext(f.Path))
	switch f.Kind {
	case "photo":
		if f.AsDocument {
			return s.upload(ctx, "sendDocument", "document", fields, f.Path)
		}
		err := s.upload(ctx, "sendPhoto", "photo", fields, f.Path)
		if isBadRequest(err) {
			// For example, a photo with too large dimensions. A document
			// has no such limits.
			return s.upload(ctx, "sendDocument", "document", fields, f.Path)
		}
		return err
	case "audio":
		if f.AsVoice {
			if ext == ".ogg" || ext == ".opus" || ext == ".oga" {
				return s.upload(ctx, "sendVoice", "voice", fields, f.Path)
			}
			if s.ConvertVoice != nil {
				out, err := s.ConvertVoice(ctx, f.Path)
				if err == nil {
					err = s.upload(ctx, "sendVoice", "voice", fields, out)
					// The converted file is a temp file. Without this remove, each voice
					// reply stays on the disk.
					if out != f.Path {
						if rerr := os.Remove(out); rerr != nil && !os.IsNotExist(rerr) {
							s.C.logger().Warn("telegram: cannot remove the converted voice file", "path", out, "err", rerr.Error())
						}
					}
					return err
				}
				s.C.logger().Info("telegram voice conversion failed, send an audio file", "err", err.Error())
			}
		}
		// sendAudio takes only MP3 and M4A.
		if ext == ".mp3" || ext == ".m4a" {
			return s.upload(ctx, "sendAudio", "audio", fields, f.Path)
		}
		return s.upload(ctx, "sendDocument", "document", fields, f.Path)
	case "video":
		return s.upload(ctx, "sendVideo", "video", fields, f.Path)
	}
	return s.upload(ctx, "sendDocument", "document", fields, f.Path)
}

func (s *Sender) upload(ctx context.Context, method, field string, fields map[string]string, path string) error {
	return s.C.Upload(ctx, method, fields, field, path, nil)
}

// ffmpegTimeout limits one voice conversion.
const ffmpegTimeout = 60 * time.Second

// FFmpegToOpus returns a converter that makes an OGG/Opus voice note from an
// audio file with ffmpeg. The output is a new temp file, so a conversion never
// overwrites a file of the user. The Sender removes it after the send. With no
// ffmpeg (an empty path), the result is nil, so the Sender sends an audio file.
func FFmpegToOpus(ffmpegPath string) func(ctx context.Context, in string) (string, error) {
	if ffmpegPath == "" {
		return nil
	}
	return func(ctx context.Context, in string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
		defer cancel()
		tmp, err := os.CreateTemp("", "tether-voice-*.ogg")
		if err != nil {
			return "", fmt.Errorf("ffmpeg: cannot create the output file: %w", err)
		}
		out := tmp.Name()
		_ = tmp.Close()
		cmd := exec.CommandContext(ctx, ffmpegPath, "-y", "-loglevel", "error", "-i", in,
			"-c:a", "libopus", "-b:a", "48k", "-ac", "1", out)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			_ = os.Remove(out)
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[len(msg)-300:]
			}
			if ctx.Err() != nil {
				return "", fmt.Errorf("ffmpeg: the conversion to opus took more than %s", ffmpegTimeout)
			}
			return "", fmt.Errorf("ffmpeg: the conversion to opus failed: %w: %s", err, msg)
		}
		if st, err := os.Stat(out); err != nil || st.Size() == 0 {
			_ = os.Remove(out)
			return "", fmt.Errorf("ffmpeg: the conversion to opus gave no output file")
		}
		return out, nil
	}
}
