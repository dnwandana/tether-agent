package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/dnwandana/tether-agent/internal/media"
)

// maxFileIDs is the number of file ids that Files keeps. The oldest id goes first.
const maxFileIDs = 4096

// fileTypes maps the extension of an inline file to its Content-Type.
var fileTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "mp3": "audio/mpeg", "ogg": "audio/ogg", "oga": "audio/ogg",
	"opus": "audio/ogg", "m4a": "audio/mp4", "wav": "audio/wav", "mp4": "video/mp4",
	"mov": "video/quicktime", "webm": "video/webm",
}

// fileKey is the real path and the check kind of a linked file.
type fileKey struct {
	real    string
	history bool
}

// Files gives ids to local files that the media policy accepts, and serves them.
type Files struct {
	policy *media.Policy
	// histPolicy is the policy for text from saved history: media roots only.
	histPolicy *media.Policy

	mu    sync.Mutex
	byID  map[string]fileKey
	byKey map[fileKey]string
	order []string // ids, oldest first
}

// NewFiles returns a Files that checks each path with p. A nil p refuses all files.
func NewFiles(p *media.Policy) *Files {
	f := &Files{policy: p, byID: map[string]fileKey{}, byKey: map[fileKey]string{}}
	if p != nil {
		h := *p
		h.Strict = true
		h.TrustRecent = 0
		f.histPolicy = &h
	}
	return f
}

// check runs the policy check of the given kind.
func (f *Files) check(path string, history bool) (string, error) {
	if f.policy == nil {
		return "", os.ErrNotExist
	}
	if history {
		return f.policy.CheckHistory(path)
	}
	return f.policy.Check(path)
}

// Link checks path and returns its id. history selects CheckHistory instead of Check.
func (f *Files) Link(path string, history bool) (id string, ok bool) {
	real, err := f.check(path, history)
	if err != nil {
		return "", false
	}
	key := fileKey{real, history}
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[key]; ok {
		return id, true
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false
	}
	id = hex.EncodeToString(b[:])
	f.byID[id] = key
	f.byKey[key] = id
	f.order = append(f.order, id)
	for len(f.order) > maxFileIDs {
		old := f.order[0]
		f.order = f.order[1:]
		delete(f.byKey, f.byID[old])
		delete(f.byID, old)
	}
	return id, true
}

// fileKind maps a media send kind to the kind of a FileRef.
func fileKind(sendKind string) string {
	switch sendKind {
	case "photo":
		return "image"
	case "audio":
		return "audio"
	case "video":
		return "video"
	}
	return "file"
}

// Rewrite takes the MEDIA tags and the bare paths out of text, and returns a FileRef for
// each accepted file. A refused tag becomes "[blocked file: <path>]" in clean.
func (f *Files) Rewrite(text string, history bool) (clean string, files []FileRef) {
	p := f.policy
	if history {
		p = f.histPolicy
	}
	if p == nil {
		return text, nil
	}
	clean, out := p.ExtractMedia(text)
	var blocked []string
	for _, o := range out {
		id, ok := f.Link(o.Path, history)
		if !ok {
			blocked = append(blocked, fmt.Sprintf("[blocked file: %s]", o.Path))
			continue
		}
		real, err := f.check(o.Path, history)
		if err != nil {
			blocked = append(blocked, fmt.Sprintf("[blocked file: %s]", o.Path))
			continue
		}
		files = append(files, FileRef{ID: id, Name: filepath.Base(real), Kind: fileKind(media.SendKind(real))})
	}
	if len(blocked) > 0 {
		clean = strings.TrimRight(clean, "\n") + "\n" + strings.Join(blocked, "\n")
	}
	return clean, files
}

// Register adds GET /api/files/{id}.
func (f *Files) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/files/{id}", f.serve)
}

// serve checks the path again and sends the file. Each failure gives 404.
func (f *Files) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	key, ok := f.byID[r.PathValue("id")]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	real, err := f.check(key.real, key.history)
	if err != nil || real != key.real {
		http.NotFound(w, r)
		return
	}
	file, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(real)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if ct, ok := fileTypes[strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")]; ok {
		h.Set("Content-Type", ct)
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", `attachment; filename="`+quoteFileName(name)+`"`)
		h.Set("Content-Security-Policy", "sandbox")
	}
	http.ServeContent(w, r, name, st.ModTime(), file)
}

// quoteFileName removes the characters that break a quoted header value.
func quoteFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
}
