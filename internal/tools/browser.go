package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// The limits and defaults of the browser tools.
const (
	// browserTimeout limits each browser action.
	browserTimeout = 30 * time.Second
	// navExcerptChars is the length of the text excerpt of browser_navigate.
	navExcerptChars = 2000
	// navSettle is the wait after the body is ready, so that the page
	// scripts can render.
	navSettle = 500 * time.Millisecond
	// defaultBrowserSession is the tab name for a call with no session id.
	defaultBrowserSession = "default"
)

// BrowserTools holds the state of the seven browser tools.
// Each session uses its own tab of M. DetectErr runs before each call; it
// finds Chrome on the first call and gives the "no Chrome" error. A nil
// DetectErr does no check.
type BrowserTools struct {
	M         *browser.Manager
	ShotDir   string // cache/screenshots
	DetectErr func() error

	// timeout replaces browserTimeout in tests.
	timeout time.Duration
}

// funcTool is a tool from a name, a toolset, a schema, and a run func. It
// is not parallel-safe. The browser tools are not parallel-safe, because the
// calls of one session share one tab.
type funcTool struct {
	name   string
	set    string
	desc   string
	params string
	run    func(ctx context.Context, raw json.RawMessage, env *Env) (string, error)
}

func (t funcTool) Name() string       { return t.name }
func (t funcTool) Toolset() string    { return t.set }
func (t funcTool) ParallelSafe() bool { return false }
func (t funcTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: t.name, Description: t.desc, Parameters: json.RawMessage(t.params)}
}
func (t funcTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	return t.run(ctx, raw, env)
}

// RegisterBrowserTools adds the seven browser tools of bt to r.
func RegisterBrowserTools(r *Registry, bt *BrowserTools) {
	for _, t := range bt.Tools() {
		r.Register(t)
	}
}

// Tools returns the seven browser tools.
func (bt *BrowserTools) Tools() []Tool {
	return []Tool{
		funcTool{
			set:  SetBrowser,
			name: "browser_navigate",
			desc: "Open a URL in the headless browser and wait for the page and its JavaScript. " +
				"Returns the title, the final URL, and the first 2000 characters of the page text. " +
				"Call it before the other browser tools. For plain pages, web_fetch is faster; " +
				"use the browser for pages that need JavaScript, a login, clicks, or forms. " +
				"The browser keeps cookies and logins between calls and restarts.",
			params: `{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "The http or https URL to open."}
  },
  "required": ["url"]
}`,
			run: bt.navigate,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_click",
			desc: "Click the first element that matches a selector on the current page. " +
				"A selector that starts with \"/\" is XPath; any other selector is CSS.",
			params: `{
  "type": "object",
  "properties": {
    "selector": {"type": "string", "description": "A CSS selector, or an XPath expression that starts with \"/\"."}
  },
  "required": ["selector"]
}`,
			run: bt.click,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_fill",
			desc: "Type text into the first input or textarea that matches a selector. " +
				"The tool clears the field first. A selector that starts with \"/\" is XPath; any other selector is CSS.",
			params: `{
  "type": "object",
  "properties": {
    "selector": {"type": "string", "description": "A CSS selector, or an XPath expression that starts with \"/\"."},
    "text": {"type": "string", "description": "The text to type."}
  },
  "required": ["selector", "text"]
}`,
			run: bt.fill,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_read",
			desc: "Return the visible text (innerText) of the current page, or of the first element that matches a selector. " +
				"With no selector, the tool returns the text of the whole body.",
			params: `{
  "type": "object",
  "properties": {
    "selector": {"type": "string", "description": "Optional. A CSS selector, or an XPath expression that starts with \"/\"."}
  }
}`,
			run: bt.read,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_take_screenshot",
			desc: "Take a PNG screenshot of the current page. Returns MEDIA:<path>. " +
				"Use view_image with the path to inspect the image. Put the MEDIA:<path> tag in your answer to send the image to the user.",
			params: `{
  "type": "object",
  "properties": {
    "full_page": {"type": "boolean", "description": "Capture the full page, not only the visible part. The default is false."}
  }
}`,
			run: bt.screenshot,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_run_js",
			desc: "Run JavaScript in the current page and return the result as JSON. " +
				"The value of the last expression is the result. The tool waits for a promise. " +
				"Use it to read data from the page or to do what the other browser tools cannot do.",
			params: `{
  "type": "object",
  "properties": {
    "code": {"type": "string", "description": "The JavaScript code. Example: document.querySelectorAll('a').length"}
  },
  "required": ["code"]
}`,
			run: bt.runJS,
		},
		funcTool{
			set:  SetBrowser,
			name: "browser_cookies",
			desc: "Read, set, or delete browser cookies. get: return the cookies of the current page, or of a domain. " +
				"set: set the cookie name=value for the current page, or for a domain. " +
				"clear: delete the cookie name, or all cookies of the current page or of a domain when you give no name.",
			params: `{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["get", "set", "clear"], "description": "The action to do."},
    "name": {"type": "string", "description": "The cookie name. Required for set. Optional for get and clear."},
    "value": {"type": "string", "description": "The cookie value for set."},
    "domain": {"type": "string", "description": "Optional. The cookie domain, for example example.com. The default is the domain of the current page."}
  },
  "required": ["action"]
}`,
			run: bt.cookies,
		},
	}
}

// Tool returns the browser tool with the name, or nil.
func (bt *BrowserTools) Tool(name string) Tool {
	for _, t := range bt.Tools() {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// names returns the names of the browser tools.
func (bt *BrowserTools) names() []string {
	var out []string
	for _, t := range bt.Tools() {
		out = append(out, t.Name())
	}
	return out
}

func (bt *BrowserTools) detect() error {
	if bt.DetectErr == nil {
		return nil
	}
	return bt.DetectErr()
}

func (bt *BrowserTools) limit() time.Duration {
	if bt.timeout > 0 {
		return bt.timeout
	}
	return browserTimeout
}

// tab returns the action context of the session tab, with the action
// timeout. The context also ends when ctx ends. Call done after the action.
func (bt *BrowserTools) tab(ctx context.Context, env *Env) (actx context.Context, done func(), err error) {
	if err := bt.detect(); err != nil {
		return nil, nil, toolErr("%v", err)
	}
	if bt.M == nil {
		return nil, nil, toolErr("the browser is not available in this session")
	}
	id := env.SessionID
	if id == "" {
		id = defaultBrowserSession
	}
	tabCtx, err := bt.M.Tab(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, toolErr("%v", err)
	}
	// A plain timeout context does not close the tab when it ends. Only the
	// manager closes tabs.
	actx, cancel := context.WithTimeout(tabCtx, bt.limit())
	stop := context.AfterFunc(ctx, cancel)
	return actx, func() { stop(); cancel() }, nil
}

// fail returns the error of a failed action. what names the action for the
// model.
func (bt *BrowserTools) fail(ctx, actx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(actx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return toolErr("%s: no result after %s", what, bt.limit())
	}
	return toolErr("%s: %v", what, err)
}

// selectorFail returns the error of a failed selector action. A timeout
// usually means that no element matches.
func (bt *BrowserTools) selectorFail(ctx, actx context.Context, verb, sel string, err error) error {
	if ctx.Err() == nil && (errors.Is(actx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
		return toolErr("%s %q: no result after %s; no element matches, or the element is not visible", verb, sel, bt.limit())
	}
	return bt.fail(ctx, actx, fmt.Sprintf("%s %q", verb, sel), err)
}

// bySel returns the CSS action for sel, or the XPath action when sel starts
// with "/". chromedp.Search resolves an XPath expression.
func bySel[T any](sel string,
	css func(chromedp.CSS, ...chromedp.QueryOption) chromedp.Action[T],
	xpath func(chromedp.Search, ...chromedp.QueryOption) chromedp.Action[T],
) chromedp.Action[T] {
	if strings.HasPrefix(sel, "/") {
		return xpath(chromedp.Search(sel))
	}
	return css(chromedp.CSS(sel))
}

// pageInfo is the state of the page after a navigation.
type pageInfo struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

const pageInfoJS = `({title: document.title, url: location.href, text: document.body ? document.body.innerText : ""})`

// checkNavURL accepts only an absolute http or https URL. Other schemes,
// such as file and javascript, are refused.
func checkNavURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return toolErr("url is required; give an http or https URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return toolErr("bad url %q: only absolute http and https URLs are allowed", raw)
	}
	return nil
}

func (bt *BrowserTools) navigate(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if err := checkNavURL(a.URL); err != nil {
		return "", err
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	resp, err := chromedp.Run(actx, chromedp.NavigateResponse(a.URL))
	if err != nil {
		return "", bt.fail(ctx, actx, "open "+a.URL, err)
	}
	if err := chromedp.Do(actx, chromedp.WaitReady(chromedp.CSS("body")), chromedp.Sleep(navSettle)); err != nil {
		return "", bt.fail(ctx, actx, "wait for the page body", err)
	}
	info, err := chromedp.Run(actx, chromedp.Evaluate[pageInfo](pageInfoJS))
	if err != nil {
		return "", bt.fail(ctx, actx, "read the page", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\nfinal_url: %s\n", info.Title, info.URL)
	if resp != nil && resp.Status >= 400 {
		fmt.Fprintf(&b, "status: HTTP %d\n", resp.Status)
	}
	text := strings.TrimSpace(info.Text)
	if r := []rune(text); len(r) > navExcerptChars {
		text = string(r[:navExcerptChars]) + fmt.Sprintf("\n[the text is cut at %d characters; use browser_read for the full text]", navExcerptChars)
	}
	if text == "" {
		text = "[the page has no text]"
	}
	b.WriteString("\n")
	b.WriteString(text)
	return b.String(), nil
}

// clearFieldJS clears the value of the first element that matches a
// selector. The arguments are the selector as a JSON string, and true for an
// XPath selector.
const clearFieldJS = `(function(sel, xpath) {
  const el = xpath
    ? document.evaluate(sel, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue
    : document.querySelector(sel);
  if (!el) return;
  if ("value" in el) {
    el.value = "";
    el.dispatchEvent(new Event("input", {bubbles: true}));
  } else if (el.isContentEditable) {
    el.textContent = "";
  }
})(%s, %t)`

// selectorArgs are the arguments of the selector tools.
type selectorArgs struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
}

func (bt *BrowserTools) click(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a selectorArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", toolErr("selector is required")
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	if err := chromedp.Do(actx, bySel(a.Selector, chromedp.Click[chromedp.CSS], chromedp.Click[chromedp.Search])); err != nil {
		return "", bt.selectorFail(ctx, actx, "click", a.Selector, err)
	}
	return fmt.Sprintf("clicked %q", a.Selector), nil
}

func (bt *BrowserTools) fill(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a selectorArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Selector) == "" {
		return "", toolErr("selector is required")
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	// The field is cleared with a script, because chromedp.SetValue cannot set
	// an empty value. Then key events type the text, so that page scripts see
	// the input.
	sel, err := json.Marshal(a.Selector)
	if err != nil {
		return "", toolErr("bad selector: %v", err)
	}
	clearJS := fmt.Sprintf(clearFieldJS, sel, strings.HasPrefix(a.Selector, "/"))
	err = chromedp.Do(actx,
		bySel(a.Selector, chromedp.WaitVisible[chromedp.CSS], chromedp.WaitVisible[chromedp.Search]),
		chromedp.Evaluate[chromedp.Void](clearJS),
		bySel(a.Selector,
			func(s chromedp.CSS, o ...chromedp.QueryOption) chromedp.Action[chromedp.Void] {
				return chromedp.SendKeys(s, a.Text, o...)
			},
			func(s chromedp.Search, o ...chromedp.QueryOption) chromedp.Action[chromedp.Void] {
				return chromedp.SendKeys(s, a.Text, o...)
			}),
	)
	if err != nil {
		return "", bt.selectorFail(ctx, actx, "fill", a.Selector, err)
	}
	return fmt.Sprintf("filled %q with %d characters", a.Selector, len([]rune(a.Text))), nil
}

func (bt *BrowserTools) read(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a selectorArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	var text string
	if strings.TrimSpace(a.Selector) == "" {
		text, err = chromedp.Run(actx, chromedp.Evaluate[string](`document.body ? document.body.innerText : ""`))
		if err != nil {
			return "", bt.fail(ctx, actx, "read the page", err)
		}
	} else {
		text, err = chromedp.Run(actx, bySel(a.Selector,
			func(s chromedp.CSS, o ...chromedp.QueryOption) chromedp.Action[string] {
				return chromedp.JavascriptAttribute[string](s, "innerText", o...)
			},
			func(s chromedp.Search, o ...chromedp.QueryOption) chromedp.Action[string] {
				return chromedp.JavascriptAttribute[string](s, "innerText", o...)
			}))
		if err != nil {
			return "", bt.selectorFail(ctx, actx, "read", a.Selector, err)
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "[no text]", nil
	}
	return text, nil
}

func (bt *BrowserTools) screenshot(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a struct {
		FullPage bool `json:"full_page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if bt.ShotDir == "" {
		return "", toolErr("the screenshot folder is not set")
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	shot := chromedp.CaptureScreenshot()
	if a.FullPage {
		shot = chromedp.FullScreenshot(100)
	}
	png, err := chromedp.Run(actx, shot)
	if err != nil {
		return "", bt.fail(ctx, actx, "take the screenshot", err)
	}
	if err := os.MkdirAll(bt.ShotDir, 0o700); err != nil {
		return "", toolErr("create the screenshot folder: %v", err)
	}
	name := fmt.Sprintf("%s-%s.png", safeFileName(env.SessionID), time.Now().UTC().Format("20060102-150405.000000"))
	path := filepath.Join(bt.ShotDir, name)
	if err := os.WriteFile(path, png, 0o600); err != nil {
		return "", toolErr("write the screenshot: %v", err)
	}
	return "MEDIA:" + path + "\nUse view_image to inspect it.", nil
}

// safeFileName keeps only letters, digits, "-", and "_" of s. Other
// characters become "_".
func safeFileName(s string) string {
	if s == "" {
		return defaultBrowserSession
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}

func (bt *BrowserTools) runJS(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a struct {
		Code string `json:"code"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Code) == "" {
		return "", toolErr("code is required")
	}
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()
	res, err := chromedp.Run(actx, chromedp.Evaluate[[]byte](a.Code, chromedp.EvalAwaitPromise))
	if err != nil {
		return "", bt.fail(ctx, actx, "run the code", err)
	}
	if len(bytes.TrimSpace(res)) == 0 {
		return "undefined", nil
	}
	var out bytes.Buffer
	if err := json.Compact(&out, res); err != nil {
		return string(res), nil
	}
	return out.String(), nil
}

// cookieOut is one cookie in the browser_cookies result.
type cookieOut struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"http_only,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

func (bt *BrowserTools) cookies(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Value  string `json:"value"`
		Domain string `json:"domain"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	switch a.Action {
	case "get", "clear":
	case "set":
		if a.Name == "" {
			return "", toolErr("set needs a cookie name")
		}
	default:
		return "", toolErr("unknown action %q; use get, set, or clear", a.Action)
	}
	a.Domain = strings.TrimSpace(a.Domain)
	actx, done, err := bt.tab(ctx, env)
	if err != nil {
		return "", err
	}
	defer done()

	// urls are the URLs whose cookies the call reads: the domain, or the
	// current page.
	var urls []string
	if a.Domain != "" {
		host := strings.TrimPrefix(a.Domain, ".")
		urls = []string{"https://" + host + "/", "http://" + host + "/"}
	} else {
		loc, err := chromedp.Run(actx, chromedp.Evaluate[string](`location.href`))
		if err != nil {
			return "", bt.fail(ctx, actx, "read the page URL", err)
		}
		if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
			return "", toolErr("%s needs a domain, or an http or https page; open a page with browser_navigate or give a domain", a.Action)
		}
		urls = []string{loc}
	}

	switch a.Action {
	case "set":
		p := network.SetCookieParams{Name: a.Name, Value: a.Value, URL: urls[0]}
		if a.Domain != "" {
			p.Domain, p.Path = a.Domain, "/"
		}
		if _, err := chromedp.Call(actx, network.SetCookie, p); err != nil {
			return "", bt.fail(ctx, actx, "set the cookie "+a.Name, err)
		}
		return fmt.Sprintf("cookie %q is set for %s", a.Name, cookieScope(a.Domain, urls[0])), nil
	}

	res, err := chromedp.Call(actx, network.GetCookies, network.GetCookiesParams{URLs: urls})
	if err != nil {
		return "", bt.fail(ctx, actx, "read the cookies", err)
	}
	var match []*network.Cookie
	for _, c := range res.Cookies {
		if c != nil && (a.Name == "" || c.Name == a.Name) {
			match = append(match, c)
		}
	}

	if a.Action == "clear" {
		for _, c := range match {
			p := network.DeleteCookiesParams{Name: c.Name, Domain: c.Domain, Path: c.Path}
			if _, err := chromedp.Call(actx, network.DeleteCookies, p); err != nil {
				return "", bt.fail(ctx, actx, "delete the cookie "+c.Name, err)
			}
		}
		return fmt.Sprintf("deleted %d cookies for %s", len(match), cookieScope(a.Domain, urls[0])), nil
	}

	out := make([]cookieOut, 0, len(match))
	for _, c := range match {
		o := cookieOut{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, HTTPOnly: c.HTTPOnly, Secure: c.Secure}
		if !c.Session && c.Expires > 0 {
			o.Expires = time.Unix(int64(c.Expires), 0).UTC().Format(time.RFC3339)
		}
		out = append(out, o)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", toolErr("encode the cookies: %v", err)
	}
	return string(b), nil
}

// cookieScope names the domain or the page of a cookie call.
func cookieScope(domain, page string) string {
	if domain != "" {
		return "domain " + domain
	}
	return page
}
