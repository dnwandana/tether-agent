package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fileServer starts a fake server that answers each request with the HTML
// file at path.
func fileServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return htmlServer(t, string(b))
}

func TestParseDDGFive(t *testing.T) {
	b, _ := os.ReadFile("testdata/ddg_results.html")
	rs, err := ParseDDG(b, 5)
	if err != nil || len(rs) != 5 || rs[0].URL != "https://go.dev/doc/devel/release" || rs[0].Title == "" || rs[0].Snippet == "" {
		t.Fatal(rs, err)
	}
	if rs[0].Title != "Release History - The Go Programming Language" {
		t.Fatalf("%q", rs[0].Title)
	}
	if !strings.HasPrefix(rs[0].Snippet, "This page summarizes the changes between official stable releases of Go.") {
		t.Fatalf("%q", rs[0].Snippet)
	}
}

func TestParseDDGSkipsAds(t *testing.T) {
	b, _ := os.ReadFile("testdata/ddg_results.html")
	rs, err := ParseDDG(b, 10)
	if err != nil || len(rs) != 8 {
		t.Fatal(len(rs), err)
	}
	for _, r := range rs {
		if strings.Contains(r.URL, "duckduckgo.com") || strings.Contains(r.Title, "Course") {
			t.Fatal(r)
		}
	}
}

func TestUnwrapsRedirectLinks(t *testing.T) {
	cases := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=abc":     "https://go.dev/",
		"https://duckduckgo.com/l/?uddg=https%3A%2F%2Fa.b%2Fc%3Fd%3D1": "https://a.b/c?d=1",
		"https://example.com/direct":                                   "https://example.com/direct",
	}
	for in, want := range cases {
		if got := unwrapDDG(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestBlockedPageGivesBrowserHint(t *testing.T) {
	srv := fileServer(t, "testdata/ddg_blocked.html")
	out := runToolErr(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"go release"}`)
	if !strings.Contains(out, "DuckDuckGo blocked the search") || !strings.Contains(out, "search with the browser tools") {
		t.Fatal(out)
	}
}

func TestChangedPageShapeIsError(t *testing.T) {
	srv := htmlServer(t, `<html><body><div class="new-layout">x</div></body></html>`)
	out := runToolErr(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"go"}`)
	if !strings.Contains(out, "page shape") || !strings.Contains(out, "search with the browser tools") {
		t.Fatal(out)
	}
}

func TestNoResultsIsNotAnError(t *testing.T) {
	srv := htmlServer(t, `<html><body><div class="no-results">No results.</div></body></html>`)
	out := runTool(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"zzqqxx"}`)
	if !strings.Contains(out, "no results") {
		t.Fatal(out)
	}
}

func TestLimitMax10(t *testing.T) {
	var page strings.Builder
	page.WriteString("<html><body>")
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&page, `<div class="result results_links web-result"><h2 class="result__title"><a class="result__a" href="https://e.com/%d">T%d</a></h2><a class="result__snippet">S%d</a></div>`, i, i, i)
	}
	page.WriteString("</body></html>")
	srv := htmlServer(t, page.String())
	out := runTool(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"x","limit":50}`)
	if !strings.Contains(out, "10. T9") || strings.Contains(out, "11. ") {
		t.Fatal(out)
	}
	out = runTool(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"x"}`)
	if !strings.Contains(out, "5. T4") || strings.Contains(out, "6. ") {
		t.Fatal(out)
	}
}

func TestOutputFormat(t *testing.T) {
	srv := fileServer(t, "testdata/ddg_results.html")
	out := runTool(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"go release","limit":1}`)
	want := "1. Release History - The Go Programming Language\n   https://go.dev/doc/devel/release\n   This page summarizes"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("%q", out)
	}
}

func TestSendsQueryAsFormPost(t *testing.T) {
	b, _ := os.ReadFile("testdata/ddg_results.html")
	var method, q, ctype, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ctype, ua = r.Method, r.Header.Get("Content-Type"), r.UserAgent()
		_ = r.ParseForm()
		q = r.PostForm.Get("q")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	runTool(t, &WebSearchTool{Client: srv.Client(), Endpoint: srv.URL}, `{"query":"go release & notes"}`)
	if method != http.MethodPost || q != "go release & notes" || ctype != "application/x-www-form-urlencoded" || !strings.Contains(ua, "Mozilla/5.0") {
		t.Fatal(method, q, ctype, ua)
	}
}

func TestEmptyQueryIsError(t *testing.T) {
	out := runToolErr(t, &WebSearchTool{}, `{"query":" "}`)
	if !strings.Contains(out, "query is required") {
		t.Fatal(out)
	}
}

func TestDefaultEndpoint(t *testing.T) {
	if (&WebSearchTool{}).endpoint() != "https://html.duckduckgo.com/html/" {
		t.Fatal("wrong default endpoint")
	}
}
