package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serveStatic(h http.Handler, method, path, accept string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if accept != "" {
		r.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

var testDist = fstest.MapFS{
	"index.html":              {Data: []byte("<!doctype html><title>t</title>")},
	"favicon.svg":             {Data: []byte("<svg/>")},
	"assets/app-1a2b3c.js":    {Data: []byte("console.log(1)")},
	"assets/app-1a2b3c.js.gz": {Data: []byte("GZ")},
	"assets/app-1a2b3c.js.br": {Data: []byte("BR")},
	"assets/_plugin-x-9f.js":  {Data: []byte("x")},
	"assets/style-4d5e.css":   {Data: []byte("body{}")},
	".source-hash":            {Data: []byte("abc")},
	"assets/.hidden.js":       {Data: []byte("no")},
}

func TestStaticHandlerRoutes(t *testing.T) {
	h := StaticHandler(testDist)
	for _, c := range []struct {
		method, path, accept string
		code                 int
		body, cache, enc     string
		ctype                string
	}{
		{"GET", "/", "", 200, "<!doctype html><title>t</title>", "no-cache", "", "text/html"},
		{"GET", "/sessions/abc", "", 200, "<!doctype html><title>t</title>", "no-cache", "", "text/html"},
		{"GET", "/index.html", "", 200, "<!doctype html><title>t</title>", "no-cache", "", "text/html"},
		{"HEAD", "/", "", 200, "", "no-cache", "", "text/html"},
		{"GET", "/favicon.svg", "", 200, "<svg/>", "no-cache", "", "image/svg+xml"},
		{"GET", "/assets/app-1a2b3c.js", "", 200, "console.log(1)", "public, max-age=31536000, immutable", "", "text/javascript"},
		{"GET", "/assets/app-1a2b3c.js", "gzip, deflate", 200, "GZ", "public, max-age=31536000, immutable", "gzip", "text/javascript"},
		{"GET", "/assets/app-1a2b3c.js", "gzip, br", 200, "BR", "public, max-age=31536000, immutable", "br", "text/javascript"},
		{"GET", "/assets/_plugin-x-9f.js", "", 200, "x", "public, max-age=31536000, immutable", "", "text/javascript"},
		{"GET", "/assets/style-4d5e.css", "", 200, "body{}", "public, max-age=31536000, immutable", "", "text/css"},
		{"GET", "/assets/missing.js", "", 404, "", "", "", ""},
		{"GET", "/assets/.hidden.js", "", 404, "", "", "", ""},
		{"GET", "/.source-hash", "", 200, "<!doctype html><title>t</title>", "no-cache", "", "text/html"},
		{"POST", "/", "", 405, "", "", "", ""},
	} {
		rec := serveStatic(h, c.method, c.path, c.accept)
		name := c.method + " " + c.path + " " + c.accept
		if rec.Code != c.code {
			t.Errorf("%s: code %d", name, rec.Code)
			continue
		}
		if c.code != 200 {
			continue
		}
		if c.method == "GET" && rec.Body.String() != c.body {
			t.Errorf("%s: body %q", name, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: cache %q", name, got)
		}
		if got := rec.Header().Get("Content-Encoding"); got != c.enc {
			t.Errorf("%s: encoding %q", name, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, c.ctype) {
			t.Errorf("%s: type %q", name, got)
		}
	}
	if v := serveStatic(h, "GET", "/assets/app-1a2b3c.js", "gzip").Header().Get("Vary"); v != "Accept-Encoding" {
		t.Fatalf("Vary %q", v)
	}
}

func TestBuildID(t *testing.T) {
	needBuiltPage(t)
	index, err := fs.ReadFile(DistFS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	if got := BuildID(); got != hex.EncodeToString(sum[:])[:12] {
		t.Fatalf("%q", got)
	}
}

// With no built page, a page route answers 503 with a note, and an asset gives 404.
func TestStaticHandlerNotBuilt(t *testing.T) {
	h := StaticHandler(fstest.MapFS{".keep": {Data: []byte("placeholder\n")}})
	rec := serveStatic(h, "GET", "/", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Run make web") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := serveStatic(h, "GET", "/.keep", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatal("the placeholder must not be served:", rec.Code)
	}
	if rec := serveStatic(h, "GET", "/assets/app.js", ""); rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}
}
