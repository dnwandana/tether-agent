package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// htmlServer starts a fake server that answers each request with body as HTML.
func htmlServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return typedServer(t, "text/html; charset=utf-8", body)
}

// typedServer starts a fake server that answers each request with body and
// the content type ctype.
func typedServer(t *testing.T, ctype, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchTool(srv *httptest.Server) *WebFetchTool {
	return &WebFetchTool{Client: srv.Client(), MaxURLs: 5, MaxBytes: 750000}
}

func TestFetchArticleToMarkdown(t *testing.T) {
	srv := htmlServer(t, `<html><body><nav>menu</nav><article><h1>Title</h1><p>Body <a href="/x">link</a></p></article></body></html>`)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "# Title") || !strings.Contains(out, "Body [link]") {
		t.Fatal(out)
	}
	if !strings.HasPrefix(out, "## "+srv.URL) {
		t.Fatal(out)
	}
}

func TestFetchLongArticleDropsNav(t *testing.T) {
	para := strings.Repeat("This is a long sentence about the topic of the article, with real words in it. ", 20)
	page := `<html><head><title>Long Page</title></head><body><nav><a href="/a">Home</a> <a href="/b">NavLinkXYZ</a></nav>` +
		`<article><h1>Long Page</h1><p>` + para + `</p><p>` + para + `</p></article><footer>FooterXYZ</footer></body></html>`
	srv := htmlServer(t, page)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "long sentence") || strings.Contains(out, "NavLinkXYZ") {
		t.Fatal(out)
	}
}

func TestMax5URLs(t *testing.T) {
	out := runToolErr(t, &WebFetchTool{MaxURLs: 5}, `{"urls":["http://a","http://b","http://c","http://d","http://e","http://f"]}`)
	if !strings.Contains(out, "max 5 URLs per call") {
		t.Fatal(out)
	}
}

func TestNoURLs(t *testing.T) {
	out := runToolErr(t, &WebFetchTool{}, `{"urls":[]}`)
	if !strings.Contains(out, "urls") {
		t.Fatal(out)
	}
}

func TestPDFGivesPdftotextHint(t *testing.T) {
	srv := typedServer(t, "application/pdf", "%PDF-1.4")
	out := runToolErr(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "curl -sSL -o /tmp/doc.pdf") || !strings.Contains(out, "pdftotext") {
		t.Fatal(out)
	}
}

func TestJSOnlyPageHint(t *testing.T) {
	srv := htmlServer(t, `<html><body><div id="root"></div><script src="app.js"></script></body></html>`)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "use the browser tools") {
		t.Fatal(out)
	}
}

func TestBodyCap750KB(t *testing.T) {
	big := "<html><body><pre>" + strings.Repeat("abcdefghij\n", 200000) + "</pre></body></html>"
	srv := htmlServer(t, big)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "[body truncated at 750000 bytes]") {
		t.Fatal(out[len(out)-200:])
	}
}

func TestBlockedMetadataAddress(t *testing.T) {
	out := runToolErr(t, &WebFetchTool{Client: GuardedClient(true, 5*time.Second, 3), MaxURLs: 5, MaxBytes: 750000}, `{"urls":["http://169.254.169.254/latest"]}`)
	if !strings.Contains(out, "blocked: private or internal address") {
		t.Fatal(out)
	}
}

func TestFailedURLDoesNotStopOthers(t *testing.T) {
	srv := typedServer(t, "text/plain; charset=utf-8", "plain text body")
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":["ftp://x/y",%q]}`, srv.URL))
	if !strings.Contains(out, "## ftp://x/y\nerror:") || !strings.Contains(out, "plain text body") {
		t.Fatal(out)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	out := runToolErr(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "HTTP 404") {
		t.Fatal(out)
	}
}

func TestMaxCharsCutsSection(t *testing.T) {
	srv := typedServer(t, "text/plain", strings.Repeat("ü", 500))
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q],"max_chars":100}`, srv.URL))
	if strings.Count(out, "ü") != 100 || !strings.Contains(out, "max_chars") {
		t.Fatal(out)
	}
}

func TestLatin1PageDecoded(t *testing.T) {
	srv := typedServer(t, "text/html; charset=iso-8859-1", "<html><body><p>caf\xe9 cr\xe8me</p></body></html>")
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !utf8.ValidString(out) || !strings.Contains(out, "café crème") {
		t.Fatalf("%q", out)
	}
}

func TestBinaryTypeRefused(t *testing.T) {
	srv := typedServer(t, "image/png", "\x89PNG\r\n\x1a\n\x00\x00")
	out := runToolErr(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "image/png") {
		t.Fatal(out)
	}
}

func TestSendsBrowserUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(ua, "Mozilla/5.0") {
		t.Fatal(ua)
	}
}

func TestEmptyPageHint(t *testing.T) {
	srv := htmlServer(t, `<html><body></body></html>`)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if !strings.Contains(out, "use the browser tools") {
		t.Fatal(out)
	}
}

func TestShortPageWithoutScriptsHasNoHint(t *testing.T) {
	srv := htmlServer(t, `<html><body><p>Short but real text.</p></body></html>`)
	out := runTool(t, fetchTool(srv), fmt.Sprintf(`{"urls":[%q]}`, srv.URL))
	if strings.Contains(out, "browser tools") || !strings.Contains(out, "Short but real text.") {
		t.Fatal(out)
	}
}
