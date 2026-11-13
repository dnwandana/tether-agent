package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// orClient returns a client that sends each request to the server at url, so that
// the OpenRouter preset URL reaches the fake server.
func orClient(url string) *http.Client {
	return &http.Client{Transport: orRedirect{to: url}}
}

type orRedirect struct{ to string }

func (r orRedirect) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := neturl.Parse(r.to)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(req)
}

// orBody returns a list body with ids in order. The created times go up, so a
// newest-first sort reverses the order.
func orBody(ids ...string) string {
	items := make([]map[string]any, len(ids))
	for i, id := range ids {
		items[i] = map[string]any{"id": id, "created": i + 1}
	}
	b, _ := json.Marshal(map[string]any{"data": items})
	return string(b)
}

// orReply is the status and the body of one fake answer.
type orReply struct {
	code int
	body string
}

// orFake records the requests of a fake OpenRouter server.
type orFake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	queries []string
	headers []http.Header
}

// orServer starts a fake OpenRouter. replies maps the sort value of a request
// ("" for no sort) to its answer. Another sort value gets HTTP 404.
func orServer(t *testing.T, replies map[string]orReply) *orFake {
	t.Helper()
	f := &orFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.queries = append(f.queries, r.URL.RawQuery)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		rep, ok := replies[r.URL.Query().Get("sort")]
		if !ok {
			rep = orReply{404, `{"error":"not found"}`}
		}
		w.WriteHeader(rep.code)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// sortedQueries returns the raw queries of the requests in sorted order.
func (f *orFake) sortedQueries() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(slices.Sorted(slices.Values(f.queries)), " | ")
}

// orEntry is an OpenRouter entry on the preset URL with no model.
func orEntry() config.Provider {
	return config.Provider{Preset: "openrouter", Format: "openai", BaseURL: "https://openrouter.ai/api/v1", APIKey: "or-key"}
}

const (
	orTopQuery     = "sort=top-weekly&supported_parameters=tools&limit=30"
	orAgenticQuery = "sort=agentic-high-to-low&supported_parameters=tools&limit=30"
)

var (
	orTopBody = orBody("t/01", "t/02:batch", "t/03", "t/04-embedding", "t/05", "t/06", "t/07",
		"t/08", "t/09", "t/10", "t/11", "t/12", "t/13", "t/14")
	orAgenticBody = orBody("t/03", "a/01", "a/02:batch", "t/13", "a/03", "a/04", "a/05", "a/06", "a/07")
	orBoth        = map[string]orReply{"top-weekly": {200, orTopBody}, "agentic-high-to-low": {200, orAgenticBody}}
)

func TestOpenRouterGroups(t *testing.T) {
	f := orServer(t, orBoth)
	list, err := ListModels(ctx, "primary", orEntry(), orClient(f.srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 2 {
		t.Fatalf("%+v", list)
	}
	eq(t, list.Groups[0].Title, "Most used this week")
	eq(t, strings.Join(list.Groups[0].Models, ","), "t/01,t/03,t/05,t/06,t/07,t/08,t/09,t/10,t/11,t/12")
	eq(t, list.Groups[1].Title, "Best for agents")
	eq(t, strings.Join(list.Groups[1].Models, ","), "a/01,t/13,a/03,a/04,a/05")
	if !list.ToolsOnly || !list.Search || list.At.IsZero() {
		t.Fatalf("%+v", list)
	}
	eq(t, f.sortedQueries(), orAgenticQuery+" | "+orTopQuery)
	for i, h := range f.headers {
		eq(t, f.paths[i], "/api/v1/models")
		eq(t, h.Get("Authorization"), "Bearer or-key")
		eq(t, h.Get("Accept"), "application/json")
		eq(t, h.Get("HTTP-Referer"), "https://github.com/dnwandana/tether-agent")
		eq(t, h.Get("X-Title"), "Tether Agent")
	}
}

func TestOpenRouterOneGroupFails(t *testing.T) {
	cases := []struct {
		name, title, models string
		replies             map[string]orReply
	}{
		{"agentic fails", "Most used this week", "t/01,t/03,t/05,t/06,t/07,t/08,t/09,t/10,t/11,t/12",
			map[string]orReply{"top-weekly": {200, orTopBody}, "agentic-high-to-low": {500, "down"}}},
		{"top fails", "Best for agents", "t/03,a/01,t/13,a/03,a/04",
			map[string]orReply{"top-weekly": {500, "down"}, "agentic-high-to-low": {200, orAgenticBody}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := orServer(t, c.replies)
			list, err := ListModels(ctx, "primary", orEntry(), orClient(f.srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Groups) != 1 || !list.ToolsOnly || !list.Search {
				t.Fatalf("%+v", list)
			}
			eq(t, list.Groups[0].Title, c.title)
			eq(t, strings.Join(list.Groups[0].Models, ","), c.models)
		})
	}
}

func TestOpenRouterBothGroupsFail(t *testing.T) {
	f := orServer(t, map[string]orReply{"top-weekly": {503, "down for or-key"}, "agentic-high-to-low": {404, "no sort"}})
	_, err := ListModels(ctx, "primary", orEntry(), orClient(f.srv.URL))
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("got %v, want the HTTP 503 of the first group", err)
	}
	eq(t, ModelListCause(err), "HTTP 503")
	if strings.Contains(err.Error(), "or-key") {
		t.Fatal(err)
	}
}

func TestOpenRouterNoChatModels(t *testing.T) {
	body := orBody("x/a:batch", "x/text-embedding-3-large", "x/gpt-image-1")
	f := orServer(t, map[string]orReply{"top-weekly": {200, body}, "agentic-high-to-low": {200, body}})
	_, err := ListModels(ctx, "primary", orEntry(), orClient(f.srv.URL))
	if !errors.Is(err, ErrNoChatModels) {
		t.Fatalf("got %v, want ErrNoChatModels", err)
	}
}

func TestOpenRouterRulesOnlyOnPresetURL(t *testing.T) {
	cases := []struct {
		name    string
		p       func(srvURL string) config.Provider
		direct  bool // true sends to the server URL with no redirect
		queries string
		title   string
		quirk   bool
	}{
		{"preset on another host", func(u string) config.Provider {
			return config.Provider{Preset: "openrouter", Format: "openai", BaseURL: u + "/api/v1", APIKey: "k"}
		}, true, "", "", false},
		{"no preset", func(string) config.Provider {
			return config.Provider{Format: "openai", BaseURL: "https://openrouter.ai/api/v1", APIKey: "k"}
		}, false, "", "", false},
		{"trailing slash", func(string) config.Provider {
			return config.Provider{Preset: "openrouter", Format: "openai", BaseURL: "https://openrouter.ai/api/v1/", APIKey: "k"}
		}, false, orAgenticQuery + " | " + orTopQuery, "Most used this week", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := orServer(t, map[string]orReply{"": {200, orTopBody}, "top-weekly": {200, orTopBody},
				"agentic-high-to-low": {200, orAgenticBody}})
			hc := orClient(f.srv.URL)
			if c.direct {
				hc = f.srv.Client()
			}
			list, err := ListModels(ctx, "e", c.p(f.srv.URL), hc)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, f.sortedQueries(), c.queries)
			eq(t, list.Groups[0].Title, c.title)
			eq(t, list.ToolsOnly, c.quirk)
			eq(t, f.headers[0].Get("X-Title") != "", c.quirk)
		})
	}
}

func TestSearchModels(t *testing.T) {
	cases := []struct{ text, query string }{
		{"glm 5", "q=glm+5&" + orTopQuery},
		{"a&b", "q=a%26b&" + orTopQuery},
	}
	for _, c := range cases {
		body := orBody("z-ai/glm-5.3", "z-ai/glm-5.3:batch", "z-ai/glm-5.3-flash", "z-ai/glm-image")
		f := orServer(t, map[string]orReply{"top-weekly": {200, body}})
		ids, err := SearchModels(ctx, "primary", orEntry(), orClient(f.srv.URL), c.text)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, strings.Join(ids, ","), "z-ai/glm-5.3,z-ai/glm-5.3-flash")
		eq(t, f.sortedQueries(), c.query)
		eq(t, f.paths[0], "/api/v1/models")
		eq(t, f.headers[0].Get("Authorization"), "Bearer or-key")
	}
}

func TestSearchModelsNoMatch(t *testing.T) {
	f := orServer(t, map[string]orReply{"top-weekly": {200, `{"data":[]}`}})
	ids, err := SearchModels(ctx, "primary", orEntry(), orClient(f.srv.URL), "glm-6")
	if err != nil || len(ids) != 0 {
		t.Fatalf("got %v, %v; want no IDs and no error", ids, err)
	}
}

func TestSearchModelsFailure(t *testing.T) {
	f := orServer(t, map[string]orReply{"top-weekly": {500, "down"}})
	_, err := SearchModels(ctx, "primary", orEntry(), orClient(f.srv.URL), "glm")
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("got %v, want HTTP 500", err)
	}
}

func TestSearchModelsOtherEntry(t *testing.T) {
	f := orServer(t, orBoth)
	entries := []config.Provider{
		{Preset: "deepseek", Format: "openai", BaseURL: f.srv.URL, APIKey: "k"},
		{Preset: "openrouter", Format: "openai", BaseURL: f.srv.URL + "/api/v1", APIKey: "k"},
		{Format: "openai", BaseURL: f.srv.URL, APIKey: "k"},
	}
	for _, p := range entries {
		if _, err := SearchModels(ctx, "e", p, f.srv.Client(), "glm"); !errors.Is(err, ErrNoSearch) {
			t.Fatalf("%+v: got %v, want ErrNoSearch", p, err)
		}
	}
	eq(t, len(f.queries), 0)
}

func TestOpenRouterWithinTimeout(t *testing.T) {
	srv := slowServer(t)
	calls := map[string]func(time.Duration) error{
		"list": func(d time.Duration) error {
			_, err := ListModelsWithin(ctx, "primary", orEntry(), orClient(srv.URL), d)
			return err
		},
		"search": func(d time.Duration) error {
			_, err := SearchModelsWithin(ctx, "primary", orEntry(), orClient(srv.URL), "glm", d)
			return err
		},
	}
	for name, call := range calls {
		start := time.Now()
		if err := call(50 * time.Millisecond); err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("%s: err %v after %v", name, err, time.Since(start))
		}
	}
	p := config.Provider{Format: "openai", BaseURL: srv.URL}
	if _, err := SearchModelsWithin(ctx, "e", p, srv.Client(), "glm", time.Second); !errors.Is(err, ErrNoSearch) {
		t.Fatalf("an entry that is not OpenRouter has no search: %v", err)
	}
}
