package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// defaultDDGEndpoint is the DuckDuckGo HTML search page.
const defaultDDGEndpoint = "https://html.duckduckgo.com/html/"

// The result limits of web_search.
const (
	defaultSearchLimit = 5
	maxSearchLimit     = 10
	// maxSearchBody limits the read of the result page.
	maxSearchBody = 2 << 20
)

// browserHint ends each web_search error.
const browserHint = "search with the browser tools"

// errDDGBlocked is the error when DuckDuckGo answers with its anomaly or
// captcha page.
var errDDGBlocked = errors.New("DuckDuckGo blocked the search (anomaly or captcha page); " + browserHint)

// errDDGShape is the error when the page has no results and no sign of a
// block. The page layout probably changed.
var errDDGShape = errors.New("the DuckDuckGo result page shape changed and no results could be read; " + browserHint)

// SearchResult is one web search result.
type SearchResult struct {
	Title, URL, Snippet string
}

// WebSearchTool is the web_search tool. It needs no API key. Make the Client
// once, with GuardedClient, and reuse it.
type WebSearchTool struct {
	Client   *http.Client
	Endpoint string

	once     sync.Once
	fallback *http.Client
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Name returns "web_search".
func (t *WebSearchTool) Name() string { return "web_search" }

// Toolset returns the web toolset.
func (t *WebSearchTool) Toolset() string { return SetWeb }

// ParallelSafe returns true, because a search changes no local state.
func (t *WebSearchTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (t *WebSearchTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "web_search",
		Description: "Search the web with DuckDuckGo. The result is a numbered list with the title, the URL, " +
			"and a snippet of each hit. Use web_fetch to read a result page.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "The search query."},
    "limit": {"type": "integer", "description": "The number of results. The default is 5 and the maximum is 10."}
  },
  "required": ["query"]
}`),
	}
}

func (t *WebSearchTool) endpoint() string {
	if t.Endpoint != "" {
		return t.Endpoint
	}
	return defaultDDGEndpoint
}

// client returns the configured client, or a guarded client when none is
// configured.
func (t *WebSearchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	t.once.Do(func() { t.fallback = GuardedClient(true, 30*time.Second, 3) })
	return t.fallback
}

// Run posts the query to DuckDuckGo and returns the parsed results.
func (t *WebSearchTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a searchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return "", toolErr("query is required")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	limit = min(limit, maxSearchLimit)

	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", toolErr("cannot make the search request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client().Do(req)
	if err != nil {
		return "", toolErr("%s; %s", strings.TrimPrefix(fetchError(err, req.URL.Hostname()).Error(), ErrToolError.Error()+": "), browserHint)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody))
	if err != nil {
		return "", toolErr("cannot read the DuckDuckGo answer: %v; %s", err, browserHint)
	}
	if resp.StatusCode >= 400 {
		if isBlockedPage(body) {
			return "", toolErr("%v", errDDGBlocked)
		}
		return "", toolErr("DuckDuckGo returned HTTP %s; %s", resp.Status, browserHint)
	}
	results, err := ParseDDG(body, limit)
	if err != nil {
		return "", toolErr("%v", err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("no results for %q", query), nil
	}
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return b.String(), nil
}

// ParseDDG reads up to limit results from a DuckDuckGo HTML result page. It
// reads the a.result__a links, unwraps the uddg redirect links, and skips
// ads. A page that says "No results" gives an empty list. A page with no
// results that has "anomaly" or "captcha" in it is the blocked error. Any
// other page with no results is the page shape error.
func ParseDDG(page []byte, limit int) ([]SearchResult, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("cannot parse the DuckDuckGo page: %v; %s", err, browserHint)
	}
	var results []SearchResult
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if limit > 0 && len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && hasClass(n, "result") {
			if r, ok := parseResult(n); ok {
				results = append(results, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(results) > 0 {
		return results, nil
	}
	if isBlockedPage(page) {
		return nil, errDDGBlocked
	}
	if findNode(doc, func(n *html.Node) bool { return hasClass(n, "no-results") }) != nil {
		return nil, nil
	}
	return nil, errDDGShape
}

// parseResult reads one result container. It returns false for an ad or a
// container without a usable link.
func parseResult(n *html.Node) (SearchResult, bool) {
	if hasClass(n, "result--ad") {
		return SearchResult{}, false
	}
	link := findNode(n, func(c *html.Node) bool { return c.Data == "a" && hasClass(c, "result__a") })
	if link == nil {
		return SearchResult{}, false
	}
	u := unwrapDDG(attr(link, "href"))
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
		return SearchResult{}, false
	}
	if strings.HasSuffix(pu.Hostname(), "duckduckgo.com") && strings.HasPrefix(pu.Path, "/y.js") {
		return SearchResult{}, false
	}
	r := SearchResult{Title: nodeText(link), URL: u}
	if s := findNode(n, func(c *html.Node) bool { return hasClass(c, "result__snippet") }); s != nil {
		r.Snippet = nodeText(s)
	}
	return r, r.Title != ""
}

// unwrapDDG returns the target of a DuckDuckGo redirect link
// (//duckduckgo.com/l/?uddg=<target>). Other links stay as they are.
func unwrapDDG(href string) string {
	full := href
	if strings.HasPrefix(full, "//") {
		full = "https:" + full
	}
	u, err := url.Parse(full)
	if err != nil {
		return href
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return full
}

// isBlockedPage reports whether page is the DuckDuckGo anomaly or captcha page.
func isBlockedPage(page []byte) bool {
	lower := bytes.ToLower(page)
	return bytes.Contains(lower, []byte("anomaly")) || bytes.Contains(lower, []byte("captcha"))
}

// hasClass reports whether the element n has the class token name.
func hasClass(n *html.Node, name string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == name {
			return true
		}
	}
	return false
}

// attr returns the value of the attribute key of n.
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// findNode returns the first element under n (n included) for which match
// is true, in document order.
func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findNode(c, match); f != nil {
			return f
		}
	}
	return nil
}

// nodeText returns the text of n with each run of white space collapsed to
// one space.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
