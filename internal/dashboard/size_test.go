package dashboard

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// needBuiltPage skips a test that reads the built page when dist/ has only the placeholder.
// Git keeps no built page, so a fresh clone runs the other tests before make web.
func needBuiltPage(t *testing.T) {
	t.Helper()
	if !pageBuilt(DistFS()) {
		t.Skip("internal/dashboard/dist has no built page. Run make web.")
	}
}

// The parser finds the first-load files in page order, each one time, and skips an icon.
func TestFirstLoadAssets(t *testing.T) {
	page := `<!doctype html><html><head>
<script type="module" crossorigin src="/assets/index-a1.js"></script>
<link rel="modulepreload" crossorigin href="/assets/vendor-c3.js">
<link rel="stylesheet" crossorigin href="/assets/index-b2.css">
<link rel="icon" href="/assets/mark-d4.svg">
<script type="module" src="./assets/index-a1.js"></script>
</head><body><div id="app"></div></body></html>`
	got := firstLoadAssets([]byte(page))
	want := []string{"assets/index-a1.js", "assets/vendor-c3.js", "assets/index-b2.css"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The page loads at most 200 KB of gzip JS and CSS on the first load.
func TestDashboardFirstLoadSize(t *testing.T) {
	needBuiltPage(t)
	dist := DistFS()
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	assets := firstLoadAssets(index)
	if len(assets) == 0 {
		t.Fatal("dist/index.html loads no JS or CSS file. Run make web.")
	}
	total := 0
	for _, a := range assets {
		b, err := fs.ReadFile(dist, a)
		if err != nil {
			t.Fatalf("dist/index.html names %s, but dist has no such file: %v. Run make web.", a, err)
		}
		total += gzipSize(t, b)
	}
	if total > maxFirstLoadGzip {
		t.Fatalf("the first load is %d bytes gzip (%s). The limit is %d bytes. Make the page smaller.",
			total, strings.Join(assets, ", "), maxFirstLoadGzip)
	}
	t.Logf("first load: %d of %d bytes gzip", total, maxFirstLoadGzip)
}

// The first-load limit is 200 KB gzip. The shell loads its overlays at once.
func TestMaxFirstLoadGzip(t *testing.T) {
	if maxFirstLoadGzip != 200*1024 {
		t.Fatalf("maxFirstLoadGzip is %d, want %d", maxFirstLoadGzip, 200*1024)
	}
}

// Each built file and each asset of index.html gives 200 through the static handler.
func TestDashboardAssetsServed(t *testing.T) {
	needBuiltPage(t)
	dist := DistFS()
	h := StaticHandler(dist)
	var paths []string
	err := fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(p, ".gz") || strings.HasSuffix(p, ".br") {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, firstLoadAssets(index)...)
	if !slices.Contains(paths, "index.html") || len(paths) < 3 {
		t.Fatalf("dist has too few files: %q. Run make web.", paths)
	}
	for _, p := range paths {
		url := "/" + p
		if p == "index.html" {
			url = "/"
		}
		for _, enc := range []string{"", "gzip", "br, gzip"} {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if enc != "" {
				req.Header.Set("Accept-Encoding", enc)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
				t.Errorf("GET %s (Accept-Encoding %q): status %d, %d bytes", url, enc, rec.Code, rec.Body.Len())
			}
		}
	}
}

// maxFirstLoadGzip is the limit for the gzip size of the JS and CSS files that index.html loads.
const maxFirstLoadGzip = 200 * 1024

// assetRef finds the src of a script and the href of a link that point to a JS or CSS file in
// assets/. The prefix "/" or "./" is not part of the match.
var assetRef = regexp.MustCompile(`<(?:script[^>]*\ssrc|link[^>]*\shref)="(?:\./|/)?(assets/[^"]+\.(?:js|css))"`)

// firstLoadAssets returns the JS and CSS files that index loads, in page order, each one time.
func firstLoadAssets(index []byte) []string {
	var out []string
	for _, m := range assetRef.FindAllSubmatch(index, -1) {
		name := string(m[1])
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// gzipSize returns the size of b after gzip at the best compression.
func gzipSize(t *testing.T, b []byte) int {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Len()
}
