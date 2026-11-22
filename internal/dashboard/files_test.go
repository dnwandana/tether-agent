package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/media"
)

// filesPolicy returns the default media policy of a new data folder, the data folder, and
// the media folder of the policy.
func filesPolicy(t *testing.T) (*media.Policy, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	data := t.TempDir()
	dir := filepath.Join(data, "cache", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := media.NewPolicy(media.PolicyConfig{DataDir: data, Lookup: func(string) (string, bool) { return "", false },
		TrustRecent: true, TrustRecentS: 600})
	return p, data, dir
}

// filesWrite writes body to path and returns path.
func filesWrite(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// getFile sends GET /api/files/{id} through a mux with the routes of f.
func getFile(f *Files, id string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	f.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+id, nil))
	return rec
}

var fileIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestFilesServeMediaInline(t *testing.T) {
	p, _, dir := filesPolicy(t)
	f := NewFiles(p)
	for name, want := range map[string]string{"chart.png": "image/png", "reply.mp3": "audio/mpeg", "clip.mp4": "video/mp4"} {
		path := filesWrite(t, filepath.Join(dir, name), "DATA-"+name)
		id, ok := f.Link(path, false)
		if !ok || !fileIDRE.MatchString(id) {
			t.Fatalf("%s: %q %v", name, id, ok)
		}
		if again, _ := f.Link(path, false); again != id {
			t.Errorf("%s: the same path gets the same id: %q %q", name, id, again)
		}
		rec := getFile(f, id)
		if rec.Code != http.StatusOK || rec.Body.String() != "DATA-"+name || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %q %q", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		if rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %v", name, rec.Header())
		}
	}
	// Ids are random: a new Files gives a new id for the same path.
	png := filepath.Join(dir, "chart.png")
	a, _ := f.Link(png, false)
	b, _ := NewFiles(p).Link(png, false)
	if a == b {
		t.Fatal("two Files must not give the same id")
	}
}

func TestFilesServeOtherAsAttachment(t *testing.T) {
	p, _, dir := filesPolicy(t)
	f := NewFiles(p)
	for _, name := range []string{"page.html", "logo.svg", "notes.txt"} {
		id, ok := f.Link(filesWrite(t, filepath.Join(dir, name), "<script>alert(1)</script>"), false)
		if !ok {
			t.Fatal(name)
		}
		rec := getFile(f, id)
		h := rec.Header()
		if rec.Code != http.StatusOK || h.Get("Content-Type") != "application/octet-stream" ||
			h.Get("Content-Disposition") != `attachment; filename="`+name+`"` ||
			h.Get("Content-Security-Policy") != "sandbox" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %v", name, rec.Code, h)
		}
	}
}

func TestFilesCheckAtEachServe(t *testing.T) {
	p, data, dir := filesPolicy(t)
	f := NewFiles(p)
	secret := filesWrite(t, filepath.Join(data, ".env"), "API_KEY=x")
	if id, ok := f.Link(secret, false); ok || id != "" {
		t.Fatalf("a protected path gets no id: %q %v", id, ok)
	}
	// A file that becomes a link to a secret after the check gives 404.
	a := filesWrite(t, filepath.Join(dir, "a.png"), "A")
	idA, _ := f.Link(a, false)
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, a); err != nil {
		t.Skip("no symlinks:", err)
	}
	if rec := getFile(f, idA); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "API_KEY") {
		t.Fatalf("link to a secret: %d %q", rec.Code, rec.Body.String())
	}
	// A file that becomes a link to another allowed file also gives 404: the real path changed.
	b := filesWrite(t, filepath.Join(dir, "b.png"), "B")
	c := filesWrite(t, filepath.Join(dir, "c.png"), "C")
	idB, _ := f.Link(b, false)
	os.Remove(b)
	if err := os.Symlink(c, b); err != nil {
		t.Fatal(err)
	}
	if rec := getFile(f, idB); rec.Code != http.StatusNotFound {
		t.Fatalf("changed real path: %d", rec.Code)
	}
	// A deleted file and an unknown id give 404.
	d := filesWrite(t, filepath.Join(dir, "d.png"), "D")
	idD, _ := f.Link(d, false)
	os.Remove(d)
	for _, id := range []string{idD, "00112233445566778899aabbccddeeff"} {
		if rec := getFile(f, id); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", id, rec.Code)
		}
	}
}

func TestFilesHistoryAfterRestart(t *testing.T) {
	p, _, dir := filesPolicy(t)
	png := filesWrite(t, filepath.Join(dir, "chart.png"), "PNG")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(png, old, old); err != nil {
		t.Fatal(err)
	}
	// A new Files is the state after a restart: no id is known.
	f := NewFiles(p)
	clean, refs := f.Rewrite("Chart:\nMEDIA:"+png, true)
	if clean != "Chart:" || len(refs) != 1 || refs[0].Name != "chart.png" || refs[0].Kind != "image" || !fileIDRE.MatchString(refs[0].ID) {
		t.Fatalf("%q %+v", clean, refs)
	}
	if rec := getFile(f, refs[0].ID); rec.Code != http.StatusOK || rec.Body.String() != "PNG" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	// A file outside the media roots is shown in a live answer, but not from history.
	pdf := filesWrite(t, filepath.Join(t.TempDir(), "report.pdf"), "PDF")
	clean, refs = f.Rewrite("Report:\nMEDIA:"+pdf, true)
	if len(refs) != 0 || !strings.Contains(clean, "[blocked file: "+pdf+"]") {
		t.Fatalf("history: %q %+v", clean, refs)
	}
	if _, ok := f.Link(pdf, true); ok {
		t.Fatal("history link of a file outside the roots")
	}
	clean, refs = f.Rewrite("Report:\nMEDIA:"+pdf, false)
	if clean != "Report:" || len(refs) != 1 || refs[0].Kind != "file" || refs[0].Name != "report.pdf" {
		t.Fatalf("live: %q %+v", clean, refs)
	}
	// A nil policy refuses all files.
	if _, ok := NewFiles(nil).Link(png, false); ok {
		t.Fatal("nil policy")
	}
}

func TestFilesCookieAloneThroughAuth(t *testing.T) {
	p, _, dir := filesPolicy(t)
	f := NewFiles(p)
	id, ok := f.Link(filesWrite(t, filepath.Join(dir, "chart.png"), "PNG"), false)
	if !ok {
		t.Fatal("link")
	}
	a, _, _, cookie, header := authFixture(t, config.DashboardConfig{})
	mux := http.NewServeMux()
	f.Register(mux)
	h := a.Middleware(mux)
	req := authReq(http.MethodGet, "localhost:8787", cookie, "", nil)
	req.URL.Path = "/api/files/" + id
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "PNG" {
		t.Fatalf("cookie only: %d %q", rec.Code, rec.Body.String())
	}
	_ = header
}
