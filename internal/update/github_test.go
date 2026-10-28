package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// releaseJSON returns a GitHub release body for v0.4.0 with assets under base.
func releaseJSON(base string) string {
	names := []string{
		"tether-agent_0.4.0_linux_amd64.tar.gz",
		"tether-agent_0.4.0_linux_arm64.tar.gz",
		"tether-agent_0.4.0_darwin_arm64.tar.gz",
		"checksums.txt",
	}
	assets := make([]string, len(names))
	for i, n := range names {
		assets[i] = fmt.Sprintf(`{"name":%q,"size":1,"browser_download_url":%q}`, n, base+"/"+n)
	}
	return `{"tag_name":"v0.4.0","name":"v0.4.0","draft":false,` +
		`"published_at":"2026-10-20T10:00:00Z","assets":[` + strings.Join(assets, ",") + `]}`
}

func TestLatestParsesRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "tether-agent/test" {
			t.Errorf("ua %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("accept %q", r.Header.Get("Accept"))
		}
		fmt.Fprint(w, releaseJSON("http://dl.test"))
	}))
	defer srv.Close()
	rel, err := (&Client{APIBase: srv.URL, UserAgent: "tether-agent/test"}).Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.4.0" || rel.Tag != "v0.4.0" {
		t.Fatal(rel)
	}
	if !rel.PublishedAt.Equal(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)) {
		t.Fatal(rel.PublishedAt)
	}
	if rel.Assets["tether-agent_0.4.0_linux_amd64.tar.gz"] != "http://dl.test/tether-agent_0.4.0_linux_amd64.tar.gz" {
		t.Fatal(rel.Assets)
	}
	if rel.Assets["checksums.txt"] == "" {
		t.Fatal("no checksums asset")
	}
}

func TestDefaultUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "tether-agent" {
			t.Errorf("ua %q", r.Header.Get("User-Agent"))
		}
		fmt.Fprint(w, releaseJSON("http://dl.test"))
	}))
	defer srv.Close()
	if _, err := (&Client{APIBase: srv.URL}).Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTagUsesTagPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/tags/v0.3.0" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, releaseJSON("http://dl.test"))
	}))
	defer srv.Close()
	if _, err := (&Client{APIBase: srv.URL}).Tag(context.Background(), "v0.3.0"); err != nil {
		t.Fatal(err)
	}
}

func rateLimitServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1793200000")
		w.WriteHeader(status)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}))
}

func checkRateLimitErr(t *testing.T, err error) {
	t.Helper()
	want := "GitHub rate limit reached; try again after " +
		time.Unix(1793200000, 0).Local().Format("2006-01-02 15:04:05")
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestRateLimitError(t *testing.T) {
	srv := rateLimitServer(http.StatusForbidden)
	defer srv.Close()
	_, err := (&Client{APIBase: srv.URL}).Latest(context.Background())
	checkRateLimitErr(t, err)
}

func TestRateLimit429(t *testing.T) {
	srv := rateLimitServer(http.StatusTooManyRequests)
	defer srv.Close()
	_, err := (&Client{APIBase: srv.URL}).Latest(context.Background())
	checkRateLimitErr(t, err)
}

func TestForbiddenWithoutRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "12")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := (&Client{APIBase: srv.URL}).Latest(context.Background())
	if err == nil || err.Error() != "GitHub returned 403 Forbidden for /releases/latest" {
		t.Fatal(err)
	}
}

func TestNotFoundError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := (&Client{APIBase: srv.URL}).Latest(context.Background())
	if err == nil || err.Error() != "GitHub returned 404 Not Found for /releases/latest" {
		t.Fatal(err)
	}
	_, err = (&Client{APIBase: srv.URL}).Tag(context.Background(), "v9.9.9")
	if err == nil || err.Error() != "GitHub returned 404 Not Found for /releases/tags/v9.9.9" {
		t.Fatal(err)
	}
}

func TestBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.4.0","pad":"`+strings.Repeat("x", 2<<20)+`"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := (&Client{APIBase: srv.URL}).Latest(ctx); err == nil {
		t.Fatal("a body larger than 1 MB must give an error")
	}
}

func TestDefaultHTTPClientTimeout(t *testing.T) {
	if got := (&Client{}).httpClient().Timeout; got != 30*time.Second {
		t.Fatalf("timeout %v", got)
	}
	hc := &http.Client{}
	if (&Client{HC: hc}).httpClient() != hc {
		t.Fatal("HC must be used when it is set")
	}
}

func TestDownloadClientHasNoTotalTimeout(t *testing.T) {
	if got := (&Client{}).downloadClient().Timeout; got != 0 {
		t.Fatalf("timeout %v; a slow link needs more than 30 s for the archive", got)
	}
	hc := &http.Client{}
	if (&Client{HC: hc}).downloadClient() != hc {
		t.Fatal("HC must be used when it is set")
	}
}

func TestAssetName(t *testing.T) {
	for _, p := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		got, err := AssetName("0.4.0", p[0], p[1])
		if err != nil || got != "tether-agent_0.4.0_"+p[0]+"_"+p[1]+".tar.gz" {
			t.Fatal(got, err)
		}
	}
	_, err := AssetName("0.4.0", "darwin", "amd64")
	if err == nil || err.Error() != "no release for darwin/amd64; the release platforms are linux/amd64, linux/arm64, and darwin/arm64" {
		t.Fatal(err)
	}
	if _, err := AssetName("0.4.0", "windows", "amd64"); err == nil {
		t.Fatal("windows is not a release platform")
	}
}
