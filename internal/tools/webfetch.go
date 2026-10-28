package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// browserUserAgent is a normal desktop browser string. Some sites refuse the
// Go default User-Agent.
const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// The web_fetch defaults.
const (
	defaultFetchMaxURLs  = 5
	defaultFetchMaxBytes = 750000
	// jsHintMinChars is the text length under which a page with scripts
	// gets the browser hint.
	jsHintMinChars = 200
)

// WebFetchTool is the web_fetch tool. Make the Client once,
// with GuardedClient, and reuse it.
type WebFetchTool struct {
	Client   *http.Client
	MaxURLs  int
	MaxBytes int64

	once     sync.Once
	fallback *http.Client
}

type fetchArgs struct {
	URLs     []string `json:"urls"`
	MaxChars int      `json:"max_chars"`
}

// Name returns "web_fetch".
func (t *WebFetchTool) Name() string { return "web_fetch" }

// Toolset returns the web toolset.
func (t *WebFetchTool) Toolset() string { return SetWeb }

// ParallelSafe returns true, because a fetch changes no local state.
func (t *WebFetchTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (t *WebFetchTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "web_fetch",
		Description: fmt.Sprintf("Fetch web pages with a plain HTTP GET and return the main text of each page as markdown. "+
			"No JavaScript runs; for pages that need JavaScript, use the browser tools. "+
			"Max %d URLs per call. PDF files are not supported; download them with curl and convert them with pdftotext.", t.maxURLs()),
		Parameters: json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "urls": {"type": "array", "items": {"type": "string"}, "maxItems": %d, "description": "The http or https URLs to fetch."},
    "max_chars": {"type": "integer", "description": "The maximum number of characters for each page. The default is no extra limit."}
  },
  "required": ["urls"]
}`, t.maxURLs())),
	}
}

func (t *WebFetchTool) maxURLs() int {
	if t.MaxURLs > 0 {
		return t.MaxURLs
	}
	return defaultFetchMaxURLs
}

func (t *WebFetchTool) maxBytes() int64 {
	if t.MaxBytes > 0 {
		return t.MaxBytes
	}
	return defaultFetchMaxBytes
}

// client returns the configured client, or a guarded client that blocks
// private addresses when none is configured.
func (t *WebFetchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	t.once.Do(func() { t.fallback = GuardedClient(true, 30*time.Second, 3) })
	return t.fallback
}

// Run fetches each URL of the call. Each URL gets its own section. A failed
// URL gets an error section, and the other URLs still run. The result is an
// error only when every URL failed.
func (t *WebFetchTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a fetchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if len(a.URLs) == 0 {
		return "", toolErr("urls is required; give 1 to %d URLs", t.maxURLs())
	}
	if len(a.URLs) > t.maxURLs() {
		return "", toolErr("max %d URLs per call; you gave %d", t.maxURLs(), len(a.URLs))
	}

	sections := make([]string, len(a.URLs))
	failed := make([]bool, len(a.URLs))
	var wg sync.WaitGroup
	for i, u := range a.URLs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title, body, err := t.fetchOne(ctx, strings.TrimSpace(u))
			if err != nil {
				failed[i] = true
				sections[i] = fmt.Sprintf("## %s\nerror: %s", u, strings.TrimPrefix(err.Error(), ErrToolError.Error()+": "))
				return
			}
			sections[i] = fmt.Sprintf("## %s\n%s", title, capChars(body, a.MaxChars))
		}()
	}
	wg.Wait()

	out := strings.Join(sections, "\n\n")
	for _, f := range failed {
		if !f {
			return out, nil
		}
	}
	return "", toolErr("%s", out)
}

// fetchOne fetches one URL and returns the final URL and the page text.
func (t *WebFetchTool) fetchOne(ctx context.Context, raw string) (finalURL, text string, err error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", toolErr("not a valid http or https URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", toolErr("cannot make the request: %v", err)
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client().Do(req)
	if err != nil {
		return "", "", fetchError(err, u.Hostname())
	}
	defer resp.Body.Close()
	finalURL = resp.Request.URL.String()
	if resp.StatusCode >= 400 {
		return "", "", toolErr("HTTP %s", resp.Status)
	}

	ctype := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(ctype)
	if mediaType == "application/pdf" || strings.HasSuffix(strings.ToLower(resp.Request.URL.Path), ".pdf") {
		return "", "", pdfHint(finalURL)
	}

	limit := t.maxBytes()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", "", toolErr("cannot read the body: %v", err)
	}
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}
	if bytes.HasPrefix(body, []byte("%PDF-")) {
		return "", "", pdfHint(finalURL)
	}
	if mediaType == "" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}

	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		text, err = htmlToText(body, ctype, resp.Request.URL)
		if err != nil {
			return "", "", err
		}
	case strings.HasPrefix(mediaType, "text/") || isTextType(mediaType):
		text = string(decodeCharset(body, ctype))
		if looksBinary(body[:min(len(body), binarySniffLen)]) {
			return "", "", toolErr("the body is binary data, not text")
		}
	default:
		return "", "", toolErr("content type %s is not text; download it with curl through the terminal tool", mediaType)
	}
	text = ctxmgr.SanitizeUTF8(text)
	if truncated {
		text += fmt.Sprintf("\n\n[body truncated at %d bytes]", limit)
	}
	return finalURL, text, nil
}

// isTextType reports whether a non-text/* media type holds text.
func isTextType(mediaType string) bool {
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript",
		"application/ld+json", "application/rss+xml", "application/atom+xml", "application/x-yaml", "application/yaml":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

// decodeCharset converts body to UTF-8 from the charset of the content type
// or of the page. On failure it returns body unchanged.
func decodeCharset(body []byte, ctype string) []byte {
	r, err := charset.NewReader(bytes.NewReader(body), ctype)
	if err != nil {
		return body
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return body
	}
	return out
}

// htmlToText extracts the main content of the page with Readability and
// converts it to markdown. When Readability finds nothing, it converts the
// full page. A page with little text gets the browser hint.
func htmlToText(body []byte, ctype string, pageURL *url.URL) (string, error) {
	doc, err := html.Parse(bytes.NewReader(decodeCharset(body, ctype)))
	if err != nil {
		return "", toolErr("cannot parse the HTML: %v", err)
	}
	hasScripts := hasScriptTag(doc)

	var md string
	article, err := readability.FromDocument(doc, pageURL)
	if err == nil && article.Node != nil {
		var b []byte
		b, err = htmltomarkdown.ConvertNode(article.Node, converter.WithDomain(pageURL.String()))
		md = string(b)
		if err == nil && article.Title != "" && !strings.Contains(md, article.Title) {
			md = "# " + article.Title + "\n\n" + md
		}
	}
	if err != nil || strings.TrimSpace(md) == "" {
		removeNodes(doc, "script", "style", "noscript", "template")
		b, cerr := htmltomarkdown.ConvertNode(doc, converter.WithDomain(pageURL.String()))
		if cerr != nil {
			return "", toolErr("cannot convert the page to markdown: %v", cerr)
		}
		md = string(b)
	}
	md = strings.TrimSpace(md)
	switch {
	case hasScripts && utf8.RuneCountInString(md) < jsHintMinChars:
		hint := "This page needs JavaScript; use the browser tools."
		if md == "" {
			return hint, nil
		}
		return md + "\n\n" + hint, nil
	case md == "":
		return "This page has no readable text; use the browser tools.", nil
	}
	return md, nil
}

// hasScriptTag reports whether the document has a script element that is
// not JSON data.
func hasScriptTag(n *html.Node) bool {
	if n.Type == html.ElementNode && n.Data == "script" {
		for _, a := range n.Attr {
			if a.Key == "type" && strings.Contains(a.Val, "json") {
				return false
			}
		}
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasScriptTag(c) {
			return true
		}
	}
	return false
}

// removeNodes removes the elements with the given tag names from the tree.
func removeNodes(n *html.Node, tags ...string) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		drop := false
		if c.Type == html.ElementNode {
			for _, tag := range tags {
				if c.Data == tag {
					drop = true
					break
				}
			}
		}
		if drop {
			n.RemoveChild(c)
		} else {
			removeNodes(c, tags...)
		}
		c = next
	}
}

// pdfHint returns the error for a PDF document.
func pdfHint(u string) error {
	return toolErr("This is a PDF. Run: curl -sSL -o /tmp/doc.pdf '%s' && pdftotext /tmp/doc.pdf - | head -c 50000",
		strings.ReplaceAll(u, "'", "%27"))
}

// fetchError returns the model error for a failed request to host.
func fetchError(err error, host string) error {
	if errors.Is(err, ErrBlockedAddress) {
		return toolErr("%v; host %s or a redirect target is blocked", ErrBlockedAddress, host)
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return toolErr("the request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return toolErr("the request was stopped")
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return toolErr("request failed: %v", uerr.Err)
	}
	return toolErr("request failed: %v", err)
}

// capChars cuts s to max characters when max is positive.
func capChars(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return s[:runeByteOffset(s, max)] + fmt.Sprintf("\n[cut at %d characters; call again with a larger max_chars to see more]", max)
}
