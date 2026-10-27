package tools

import (
	"net/url"
	"testing"
)

// FuzzParseDDG feeds random pages to ParseDDG. It must not panic, it must keep
// the limit, and each result must have a title and an http or https URL.
func FuzzParseDDG(f *testing.F) {
	f.Add([]byte("<html></html>"))
	f.Add([]byte(`<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F">Ex</a>` +
		`<a class="result__snippet">snip</a></div>`))
	f.Add([]byte(`<div class="result result--ad"><a class="result__a" href="https://ad.example">Ad</a></div>`))
	f.Add([]byte(`<div class="no-results">No results.</div>`))
	f.Add([]byte(`<p>anomaly detected</p>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		const limit = 10
		results, err := ParseDDG(b, limit)
		if err != nil && len(results) > 0 {
			t.Fatalf("error %v with %d results", err, len(results))
		}
		if len(results) > limit {
			t.Fatalf("%d results, limit %d", len(results), limit)
		}
		for _, r := range results {
			u, perr := url.Parse(r.URL)
			if r.Title == "" || perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatalf("bad result %+v", r)
			}
		}
	})
}
