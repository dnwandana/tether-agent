package dashboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/media"
	"github.com/dnwandana/tether-agent/internal/provider"
)

const (
	// clientIDLife is the time that a client message id keeps its first result.
	clientIDLife = 10 * time.Minute
	// uploadReadLimit is the read deadline of one upload.
	uploadReadLimit = 5 * time.Minute
	// upTextMax is the largest text part.
	upTextMax = 64 << 10
	// upDefaultFileMax is the file limit when Limits.MaxBytes is not set.
	upDefaultFileMax = 20 << 20
)

const (
	textEmptyMessage = "The message is empty."
	textBadUpload    = "The request is not a valid message form."
	textBadClientID  = "The client message id is not valid."
	textOneVoice     = "Only one voice note is allowed in a message."
	textUploadFailed = "The server could not save the upload."
)

var (
	upClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	upExt      = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)
)

// UploadDeps holds the settings of POST /api/messages.
type UploadDeps struct {
	// MediaDir is the media inbox of the agent.
	MediaDir string
	// Limits holds the limit for each file and telegram.inline_text_max_bytes.
	Limits media.Limits
	// STT converts a voice note to text. Nil means that speech to text is not set up.
	STT func(ctx context.Context, path string) (string, error)
	// FFmpeg is the path of ffmpeg, or "" when the host has no ffmpeg.
	FFmpeg string
	// MaxRequestBytes is dashboard.max_upload_mb in bytes.
	MaxRequestBytes int64
	// Now gives the time of the client id check. Nil uses time.Now.
	Now func() time.Time
}

// MessageResult is the answer of POST /api/messages.
type MessageResult struct {
	ClientID string `json:"client_id"`
	Files    int    `json:"files"`
}

func init() { registerAPI(MessageResult{}) }

// upEntry is the state of one client message id.
type upEntry struct {
	done   chan struct{}
	status int
	result MessageResult
	errMsg string
	expiry time.Time
}

// Messages is the message route.
type Messages struct {
	ch *Channel
	d  UploadDeps

	mu      sync.Mutex
	entries map[string]*upEntry
}

// NewMessages returns the message route of ch.
func NewMessages(ch *Channel, d UploadDeps) *Messages {
	return &Messages{ch: ch, d: d, entries: map[string]*upEntry{}}
}

// Register adds POST /api/messages.
func (m *Messages) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/messages", m.handle)
}

func (m *Messages) now() time.Time {
	if m.d.Now != nil {
		return m.d.Now()
	}
	return time.Now()
}

func (m *Messages) fileMax() int64 {
	if m.d.Limits.MaxBytes > 0 {
		return m.d.Limits.MaxBytes
	}
	return upDefaultFileMax
}

// claim returns the entry of id and true when the caller owns a new entry. It returns false
// and the old entry when the id is known.
func (m *Messages) claim(id string) (*upEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for k, e := range m.entries {
		if !e.expiry.IsZero() && now.After(e.expiry) {
			delete(m.entries, k)
		}
	}
	if e, ok := m.entries[id]; ok {
		return e, false
	}
	e := &upEntry{done: make(chan struct{})}
	m.entries[id] = e
	return e, true
}

// finish stores the result of an owned entry and wakes the waiting retries. A result that is
// not 200 removes the entry, so a later retry runs again.
func (m *Messages) finish(id string, e *upEntry, status int, res MessageResult, errMsg string) {
	m.mu.Lock()
	e.status, e.result, e.errMsg = status, res, errMsg
	if status == http.StatusOK {
		e.expiry = m.now().Add(clientIDLife)
	} else if m.entries[id] == e {
		delete(m.entries, id)
	}
	m.mu.Unlock()
	close(e.done)
}

// upState holds what one request has collected.
type upState struct {
	text   string
	notes  []string
	saved  []upSaved
	hasFil bool
	voice  bool
}

// upSaved is one file in the media inbox.
type upSaved struct {
	kind       media.Kind
	path, name string
}

// upFail is an error that has an HTTP status.
type upFail struct {
	code int
	text string
}

func (f *upFail) Error() string { return f.text }

func (m *Messages) handle(w http.ResponseWriter, r *http.Request) {
	done := m.ch.BeginInflight()
	defer done()
	stopping := m.ch.Stopping()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadLimit))
	if m.d.MaxRequestBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, m.d.MaxRequestBytes)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		authError(w, http.StatusBadRequest, textBadUpload)
		return
	}

	var (
		st       upState
		clientID string
		entry    *upEntry
		answered bool
	)
	// reply ends the request and settles the client id entry.
	reply := func(code int, res MessageResult, text string) {
		if answered {
			return
		}
		answered = true
		if entry != nil {
			m.finish(clientID, entry, code, res, text)
		}
		if code == http.StatusOK {
			writeJSON(w, res)
			return
		}
		authError(w, code, text)
	}
	fail := func(code int, text string) {
		m.removeSaved(st.saved)
		reply(code, MessageResult{}, text)
	}
	defer func() {
		if !answered && entry != nil {
			m.removeSaved(st.saved)
			m.finish(clientID, entry, http.StatusInternalServerError, MessageResult{}, textUploadFailed)
		}
	}()

	checked, seenPart := false, false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			m.readFail(err, fail)
			return
		}
		field := part.FormName()
		if field == "client_id" && !seenPart && entry == nil {
			seenPart = true
			b, err := io.ReadAll(io.LimitReader(part, 128))
			if err != nil {
				m.readFail(err, fail)
				return
			}
			id := strings.TrimSpace(string(b))
			if !upClientID.MatchString(id) {
				fail(http.StatusBadRequest, textBadClientID)
				return
			}
			e, owner := m.claim(id)
			if !owner {
				select {
				case <-e.done:
				case <-r.Context().Done():
					return
				}
				answered = true
				if e.status == http.StatusOK {
					writeJSON(w, e.result)
				} else {
					authError(w, e.status, e.errMsg)
				}
				return
			}
			clientID, entry = id, e
			continue
		}
		seenPart = true
		if !checked {
			checked = true
			if stopping {
				fail(http.StatusServiceUnavailable, textStopping)
				return
			}
		}
		switch field {
		case "text":
			b, err := io.ReadAll(io.LimitReader(part, upTextMax))
			if err != nil {
				m.readFail(err, fail)
				return
			}
			st.text = string(b)
		case "files":
			if err := m.saveFile(&st, part); err != nil {
				m.readFail(err, fail)
				return
			}
		case "voice":
			if st.voice {
				fail(http.StatusBadRequest, textOneVoice)
				return
			}
			st.voice = true
			if err := m.saveVoice(r.Context(), &st, part); err != nil {
				m.readFail(err, fail)
				return
			}
		}
	}
	if !checked && stopping {
		fail(http.StatusServiceUnavailable, textStopping)
		return
	}
	if !st.hasFil && strings.TrimSpace(st.text) == "" {
		fail(http.StatusBadRequest, textEmptyMessage)
		return
	}

	// The note step runs after the save.
	var images []provider.Image
	var files []string
	kept := st.saved[:0]
	for _, s := range st.saved {
		p, err := media.Note(r.Context(), s.kind, s.path, s.name, media.NoteDeps{STT: m.d.STT, Vision: true})
		if err != nil {
			_ = os.Remove(s.path)
			st.notes = append(st.notes, "["+media.DisplayName(s.name, "")+" could not be read]")
			continue
		}
		kept = append(kept, s)
		if p.Text != "" {
			st.notes = append(st.notes, p.Text)
		}
		if p.Image != nil {
			images = append(images, *p.Image)
		}
		if p.File != "" {
			files = append(files, p.File)
		}
	}
	st.saved = kept
	parts := append([]string{}, st.notes...)
	if t := strings.TrimSpace(st.text); t != "" {
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		fail(http.StatusBadRequest, textEmptyMessage)
		return
	}
	gw := m.ch.Gateway()
	if gw == nil {
		fail(http.StatusServiceUnavailable, textStopping)
		return
	}
	in := m.ch.Inbound(st.text)
	in.Text = strings.Join(parts, "\n\n")
	in.Images = images
	in.Files = files
	if st.hasFil {
		in.IsText = false
	}
	ch := m.ch
	gw.Submit(ch.WorkCtx(), in, func() gateway.Renderer { return ch.NewRenderer("") }, nil)
	reply(http.StatusOK, MessageResult{ClientID: clientID, Files: len(st.saved)}, "")
}

// readFail maps a read or save error to a reply.
func (m *Messages) readFail(err error, fail func(int, string)) {
	var tooBig *http.MaxBytesError
	var uf *upFail
	switch {
	case errors.As(err, &tooBig):
		fail(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("The upload is larger than dashboard.max_upload_mb (%d MB).", m.d.MaxRequestBytes>>20))
	case errors.As(err, &uf):
		fail(uf.code, uf.text)
	default:
		fail(http.StatusBadRequest, textBadUpload)
	}
}

func (m *Messages) removeSaved(saved []upSaved) {
	for _, s := range saved {
		_ = os.Remove(s.path)
	}
}

// create opens a new file in the media inbox. The name never holds the client file name.
func (m *Messages) create(ext string) (*os.File, error) {
	id, err := randHex(8)
	if err != nil {
		return nil, &upFail{http.StatusInternalServerError, textUploadFailed}
	}
	path := filepath.Join(m.d.MediaDir, fmt.Sprintf("%d-%s%s", m.now().UnixMilli(), id, ext))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, &upFail{http.StatusInternalServerError, textUploadFailed}
	}
	return f, nil
}

// upStream copies at most limit+1 bytes of r into a new file. It returns the path and the
// number of bytes that it read. A part over the limit gives the real size, the file is
// deleted, and the path is "".
func (m *Messages) upStream(r io.Reader, ext string, limit int64) (path string, size int64, err error) {
	f, err := m.create(ext)
	if err != nil {
		return "", 0, err
	}
	path = f.Name()
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	if n > limit {
		rest, derr := io.Copy(io.Discard, r)
		_ = os.Remove(path)
		if derr != nil {
			return "", 0, derr
		}
		return "", n + rest, nil
	}
	return path, n, nil
}

func (m *Messages) saveFile(st *upState, part *multipart.Part) error {
	st.hasFil = true
	name, mime := part.FileName(), part.Header.Get("Content-Type")
	kind, reject := media.Check(name, mime, 0, m.d.Limits)
	if reject != "" {
		st.notes = append(st.notes, reject)
		return nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !upExt.MatchString(ext) {
		ext = media.ExtByMime(mime)
		if !upExt.MatchString(ext) {
			ext = ".bin"
		}
	}
	if kind == media.KindImage && media.ImageMIME(ext) == "" {
		ext = ".jpg"
	}
	path, size, err := m.upStream(part, ext, m.fileMax())
	if err != nil {
		return err
	}
	if path == "" {
		_, reject := media.Check(name, mime, size, m.d.Limits)
		st.notes = append(st.notes, reject)
		return nil
	}
	if kind == media.KindInlineText || kind == media.KindDocument {
		k, reject := media.Check(name, mime, size, m.d.Limits)
		if reject != "" {
			_ = os.Remove(path)
			st.notes = append(st.notes, reject)
			return nil
		}
		kind = k
	}
	st.saved = append(st.saved, upSaved{kind: kind, path: path, name: name})
	return nil
}

func (m *Messages) saveVoice(ctx context.Context, st *upState, part *multipart.Part) error {
	st.hasFil = true
	br := bufio.NewReader(part)
	head, err := br.Peek(12)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return err
	}
	ext := voiceExt(head)
	if ext == "" {
		ext = ".bin"
	}
	path, size, err := m.upStream(br, ext, m.fileMax())
	if err != nil {
		return err
	}
	if path == "" {
		_, reject := media.Check("voice"+ext, "", size, m.d.Limits)
		if reject == "" {
			reject = "[voice message too large]"
		}
		st.notes = append(st.notes, reject)
		return nil
	}
	if size == 0 {
		_ = os.Remove(path)
		return nil
	}
	if m.d.FFmpeg != "" && (ext == ".webm" || ext == ".m4a") {
		if out, err := convertVoice(ctx, m.d.FFmpeg, path); err != nil {
			if m.ch.d.Log != nil {
				m.ch.d.Log.Warn("dashboard: voice note conversion failed", "err", err)
			}
		} else {
			path = out
		}
	}
	st.saved = append(st.saved, upSaved{kind: media.KindVoice, path: path, name: "voice"})
	return nil
}
