package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var distFiles embed.FS

// immutableCache is the Cache-Control value of a hashed asset.
const immutableCache = "public, max-age=31536000, immutable"

// DistFS returns the embedded files of the dashboard page, with dist/ as the root.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFiles, "dist")
	if err != nil {
		// The embed directive guarantees that dist exists.
		panic(err)
	}
	return sub
}

var buildID = sync.OnceValue(func() string {
	index, err := fs.ReadFile(DistFS(), "index.html")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(index)
	return hex.EncodeToString(sum[:])[:12]
})

// BuildID returns the first 12 hex characters of the SHA-256 of index.html.
func BuildID() string { return buildID() }

// StaticHandler serves the page and its assets from dist. It serves index.html for each path
// that names no file, so the page router can handle the path.
func StaticHandler(dist fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		hidden := hasDotSegment(name)
		if strings.HasPrefix(name, "assets/") {
			if hidden || !serveFile(w, r, dist, name, immutableCache) {
				http.NotFound(w, r)
			}
			return
		}
		if name != "" && !hidden && serveFile(w, r, dist, name, "no-cache") {
			return
		}
		if !serveFile(w, r, dist, "index.html", "no-cache") {
			// A binary built before make web has only the placeholder of dist/.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(textNotBuilt + "\n"))
		}
	})
}

// textNotBuilt is the answer of the page routes when the binary has no built page.
const textNotBuilt = "The dashboard page is not built. Run make web in the source tree, then build tether again."

// pageBuilt reports whether dist holds a built page.
func pageBuilt(dist fs.FS) bool {
	_, err := fs.Stat(dist, "index.html")
	return err == nil
}

// hasDotSegment reports whether a segment of name starts with a dot.
func hasDotSegment(name string) bool {
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// serveFile sends the regular file name of dist. It prefers a .br or .gz copy that the browser
// accepts. It returns false when the file does not exist.
func serveFile(w http.ResponseWriter, r *http.Request, dist fs.FS, name, cache string) bool {
	if info, err := fs.Stat(dist, name); err != nil || !info.Mode().IsRegular() {
		return false
	}
	enc := ""
	body, err := fs.ReadFile(dist, name)
	if err != nil {
		return false
	}
	accept := r.Header.Get("Accept-Encoding")
	for _, c := range []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
		if !acceptsEncoding(accept, c.token) {
			continue
		}
		if b, err := fs.ReadFile(dist, name+c.ext); err == nil {
			body, enc = b, c.token
			break
		}
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", cache)
	if enc != "" {
		w.Header().Set("Content-Encoding", enc)
	}
	w.Header().Add("Vary", "Accept-Encoding")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	return true
}

// acceptsEncoding reports whether the Accept-Encoding value lists token with a q value above 0.
func acceptsEncoding(header, token string) bool {
	for part := range strings.SplitSeq(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if strings.TrimSpace(fields[0]) != token {
			continue
		}
		for _, p := range fields[1:] {
			if p = strings.ReplaceAll(strings.TrimSpace(p), " ", ""); p == "q=0" || p == "q=0.0" {
				return false
			}
		}
		return true
	}
	return false
}
