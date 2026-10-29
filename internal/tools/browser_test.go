package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/hostos"
)

// chromeOrSkip returns the path of a local Chrome, or skips the test.
func chromeOrSkip(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: the test starts Chrome")
	}
	p, err := browser.DetectChrome("", exec.LookPath, os.Getenv, "linux")
	if err != nil {
		t.Skip("Chrome is not installed:", err)
	}
	return p
}

// browserTools returns browser tools over a new Chrome manager. The test
// closes the browser at the end.
func browserTools(t *testing.T) *BrowserTools {
	t.Helper()
	path := chromeOrSkip(t)
	m := &browser.Manager{ChromePath: path, ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute, Log: slog.New(slog.DiscardHandler)}
	bt := &BrowserTools{M: m, ShotDir: t.TempDir()}
	t.Cleanup(m.Close)
	return bt
}

// sessEnv returns an Env for the session id.
func sessEnv(id string) *Env {
	return &Env{SessionID: id, Log: slog.New(slog.DiscardHandler), Values: map[string]any{}}
}

// mediaPath returns the path of the first MEDIA tag in out.
func mediaPath(t *testing.T, out string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "MEDIA:")
	if !ok {
		t.Fatalf("no MEDIA tag in %q", out)
	}
	return strings.Fields(rest)[0]
}

func TestNavigateReadsJSRenderedTable(t *testing.T) {
	srv := htmlServer(t, `<html><head><title>Shop</title></head><body><div id=app></div><script>document.getElementById('app').innerHTML='<table><tr><td>SKU-1</td><td>$20</td></tr></table>'</script></body></html>`)
	bt := browserTools(t)
	out := runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), sessEnv("s1"))
	if !strings.Contains(out, "SKU-1") || !strings.Contains(out, "title: Shop") || !strings.Contains(out, "final_url: "+srv.URL) {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_read"), `{"selector":"table"}`, sessEnv("s1")); !strings.Contains(out, "$20") {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_read"), `{}`, sessEnv("s1")); !strings.Contains(out, "SKU-1") {
		t.Fatal("read with no selector must return the body text:", out)
	}
}

func TestNavigateExcerptIsCut(t *testing.T) {
	srv := htmlServer(t, `<html><body><p>`+strings.Repeat("é", 5000)+`</p></body></html>`)
	bt := browserTools(t)
	out := runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), sessEnv("s1"))
	if n := strings.Count(out, "é"); n != navExcerptChars {
		t.Fatalf("the excerpt has %d characters, want %d", n, navExcerptChars)
	}
	if !strings.Contains(out, "browser_read") {
		t.Fatal("a cut excerpt must tell the model to use browser_read:", out)
	}
}

func TestNavigateShowsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	bt := browserTools(t)
	out := runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL+"/gone"), sessEnv("s1"))
	if !strings.Contains(out, "status: HTTP 404") {
		t.Fatal(out)
	}
}

func TestNavigateRefusesOtherSchemes(t *testing.T) {
	// The scheme check runs before the browser starts, so no Chrome is needed.
	bt := &BrowserTools{M: &browser.Manager{}}
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "chrome://settings", "data:text/html,hi", "example.com", ""} {
		out := runToolEnvErr(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, u), sessEnv("s1"))
		if !strings.Contains(out, "http") {
			t.Fatalf("%q: %s", u, out)
		}
	}
}

func TestScreenshotReturnsMediaPath(t *testing.T) {
	srv := htmlServer(t, `<html><body style="height:3000px"><h1>Shot</h1></body></html>`)
	bt := browserTools(t)
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), sessEnv("s:1"))
	for _, args := range []string{`{}`, `{"full_page":true}`} {
		out := runToolEnv(t, bt.Tool("browser_take_screenshot"), args, sessEnv("s:1"))
		if !strings.Contains(out, "view_image") {
			t.Fatal(out)
		}
		p := mediaPath(t, out)
		if filepath.Dir(p) != bt.ShotDir || !strings.HasSuffix(p, ".png") || strings.Contains(filepath.Base(p), ":") {
			t.Fatal("bad screenshot path:", p)
		}
		b, err := os.ReadFile(p)
		if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) {
			t.Fatal("the screenshot is not a PNG file:", err)
		}
	}
}

func TestScreenshotNeedsShotDir(t *testing.T) {
	bt := &BrowserTools{M: &browser.Manager{}}
	if out := runToolEnvErr(t, bt.Tool("browser_take_screenshot"), `{}`, sessEnv("s1")); !strings.Contains(out, "screenshot") {
		t.Fatal(out)
	}
}

const formPage = `<html><body>
<input id=name>
<button id=go onclick="document.getElementById('out').textContent='hi '+document.getElementById('name').value">Go</button>
<div id=out></div>
</body></html>`

func TestClickFillRunJS(t *testing.T) {
	srv := htmlServer(t, formPage)
	bt := browserTools(t)
	env := sessEnv("s1")
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), env)
	runToolEnv(t, bt.Tool("browser_fill"), `{"selector":"#name","text":"old"}`, env)
	// A second fill replaces the text.
	runToolEnv(t, bt.Tool("browser_fill"), `{"selector":"#name","text":"Ana"}`, env)
	runToolEnv(t, bt.Tool("browser_click"), `{"selector":"#go"}`, env)
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"document.getElementById('out').textContent"}`, env); out != `"hi Ana"` {
		t.Fatal(out)
	}

	// A selector that starts with "/" is XPath.
	runToolEnv(t, bt.Tool("browser_fill"), `{"selector":"//input[@id='name']","text":"Bo"}`, env)
	runToolEnv(t, bt.Tool("browser_click"), `{"selector":"//button[@id='go']"}`, env)
	if out := runToolEnv(t, bt.Tool("browser_read"), `{"selector":"#out"}`, env); out != "hi Bo" {
		t.Fatal(out)
	}

	// The result is JSON. A promise is awaited.
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"({a: 1, b: [true, null]})"}`, env); out != `{"a":1,"b":[true,null]}` {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"Promise.resolve(42)"}`, env); out != "42" {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"undefined"}`, env); out != "undefined" {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, bt.Tool("browser_run_js"), `{"code":"throw new Error('boom')"}`, env); !strings.Contains(out, "boom") {
		t.Fatal(out)
	}
}

func TestMissingSelectorTimesOut(t *testing.T) {
	srv := htmlServer(t, formPage)
	bt := browserTools(t)
	bt.timeout = 2 * time.Second
	env := sessEnv("s1")
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), env)
	for _, name := range []string{"browser_click", "browser_read"} {
		out := runToolEnvErr(t, bt.Tool(name), `{"selector":"#missing","text":"x"}`, env)
		if !strings.Contains(out, "#missing") || !strings.Contains(out, "2s") {
			t.Fatalf("%s: %s", name, out)
		}
	}
	// The tab still works after a timeout.
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"1+1"}`, env); out != "2" {
		t.Fatal(out)
	}
}

func TestSessionsUseOwnTabs(t *testing.T) {
	a := htmlServer(t, `<html><body>page A</body></html>`)
	b := htmlServer(t, `<html><body>page B</body></html>`)
	bt := browserTools(t)
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, a.URL), sessEnv("s1"))
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, b.URL), sessEnv("s2"))
	if out := runToolEnv(t, bt.Tool("browser_read"), `{}`, sessEnv("s1")); out != "page A" {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_read"), `{}`, sessEnv("s2")); out != "page B" {
		t.Fatal(out)
	}
}

func TestCookiesSetGetClear(t *testing.T) {
	srv := htmlServer(t, `<html><body>c</body></html>`)
	bt := browserTools(t)
	env := sessEnv("s1")
	cookies := bt.Tool("browser_cookies")

	// Without a page and without a domain, set has no target.
	if out := runToolEnvErr(t, cookies, `{"action":"set","name":"a","value":"1"}`, env); !strings.Contains(out, "domain") {
		t.Fatal(out)
	}
	runToolEnv(t, bt.Tool("browser_navigate"), fmt.Sprintf(`{"url":%q}`, srv.URL), env)
	runToolEnv(t, cookies, `{"action":"set","name":"a","value":"1"}`, env)
	runToolEnv(t, cookies, `{"action":"set","name":"b","value":"2"}`, env)

	var got []map[string]any
	out := runToolEnv(t, cookies, `{"action":"get"}`, env)
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 {
		t.Fatal(out, err)
	}
	if out := runToolEnv(t, cookies, `{"action":"get","name":"b"}`, env); !strings.Contains(out, `"value":"2"`) || strings.Contains(out, `"name":"a"`) {
		t.Fatal(out)
	}
	if out := runToolEnv(t, bt.Tool("browser_run_js"), `{"code":"document.cookie"}`, env); !strings.Contains(out, "a=1") {
		t.Fatal("the page must see the cookie:", out)
	}

	runToolEnv(t, cookies, `{"action":"clear","name":"a"}`, env)
	if out := runToolEnv(t, cookies, `{"action":"get"}`, env); strings.Contains(out, `"name":"a"`) || !strings.Contains(out, `"name":"b"`) {
		t.Fatal(out)
	}
	runToolEnv(t, cookies, `{"action":"clear"}`, env)
	if out := runToolEnv(t, cookies, `{"action":"get"}`, env); out != "[]" {
		t.Fatal(out)
	}
	// A domain cookie does not need the current page.
	runToolEnv(t, cookies, `{"action":"set","name":"d","value":"4","domain":"example.org"}`, env)
	if out := runToolEnv(t, cookies, `{"action":"get","domain":"example.org"}`, env); !strings.Contains(out, `"name":"d"`) || !strings.Contains(out, `"value":"4"`) {
		t.Fatal(out)
	}
	runToolEnv(t, cookies, `{"action":"clear","domain":"example.org"}`, env)
	if out := runToolEnv(t, cookies, `{"action":"get","domain":"example.org"}`, env); out != "[]" {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, cookies, `{"action":"eat"}`, env); !strings.Contains(out, "get, set, or clear") {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, cookies, `{"action":"set","value":"1"}`, env); !strings.Contains(out, "name") {
		t.Fatal(out)
	}
}

func TestNoChromeErrorFromFirstCall(t *testing.T) {
	t.Cleanup(hostos.Set(hostos.Ubuntu))
	calls := 0
	bt := &BrowserTools{M: &browser.Manager{}, ShotDir: t.TempDir(), DetectErr: func() error {
		calls++
		return browser.NoChromeError()
	}}
	for _, name := range bt.names() {
		out := runToolEnvErr(t, bt.Tool(name), `{"url":"https://example.com","selector":"a","text":"x","code":"1","action":"get"}`, sessEnv("s1"))
		if !strings.Contains(out, "browser.chrome_path") || !strings.Contains(out, "sudo apt install chromium-browser") {
			t.Fatalf("%s: %s", name, out)
		}
	}
	if calls != len(bt.names()) {
		t.Fatal("each call must ask DetectErr:", calls)
	}
	// With no DetectErr, the manager gives the same error.
	bt.DetectErr = nil
	if out := runToolEnvErr(t, bt.Tool("browser_read"), `{}`, sessEnv("s1")); !strings.Contains(out, "browser.chrome_path") {
		t.Fatal(out)
	}
	if bt.detect() != nil {
		t.Fatal("a nil DetectErr must give no error")
	}
}

func TestRegisterBrowserTools(t *testing.T) {
	r := NewRegistry()
	RegisterBrowserTools(r, &BrowserTools{M: &browser.Manager{}})
	want := []string{"browser_click", "browser_cookies", "browser_fill", "browser_navigate", "browser_read", "browser_run_js", "browser_take_screenshot"}
	all := r.All()
	if len(all) != len(want) {
		t.Fatal(len(all))
	}
	for i, tool := range all {
		if tool.Name() != want[i] || tool.Toolset() != SetBrowser || tool.ParallelSafe() {
			t.Fatal(tool.Name())
		}
		s := tool.Schema()
		if s.Name != tool.Name() || s.Description == "" || !json.Valid(s.Parameters) {
			t.Fatal("bad schema:", tool.Name())
		}
	}
}
