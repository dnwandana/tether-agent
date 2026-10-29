package browser

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/dnwandana/tether-agent/internal/hostos"
)

var ctx = context.Background()

// fakeLook returns a lookPath function that finds only the names in m.
func fakeLook(m map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := m[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
}

// envWith returns a getenv function with one variable set.
func envWith(key, value string) func(string) string {
	return func(k string) string {
		if key != "" && k == key {
			return value
		}
		return ""
	}
}

// fakeClock is a clock that moves only when the test calls Advance.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// chromeOrSkip returns the path of a local Chrome, or skips the test.
func chromeOrSkip(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: the test starts Chrome")
	}
	p, err := DetectChrome("", exec.LookPath, func(string) string { return "" }, "linux")
	if err != nil {
		t.Skip("Chrome is not installed:", err)
	}
	return p
}

func TestDetectOrder(t *testing.T) {
	look := fakeLook(map[string]string{"chromium": "/usr/bin/chromium", "google-chrome": "/usr/bin/google-chrome"})
	if p, _ := DetectChrome("/cfg/chrome", look, envWith("CHROME_PATH", "/opt/c"), "linux"); p != "/cfg/chrome" {
		t.Fatal("config value must win:", p)
	}
	if p, _ := DetectChrome("", look, envWith("CHROME_PATH", "/opt/c"), "linux"); p != "/opt/c" {
		t.Fatal(p)
	}
	if p, _ := DetectChrome("", look, envWith("", ""), "linux"); p != "/usr/bin/google-chrome" {
		t.Fatal("google-chrome comes before chromium:", p)
	}
	look = fakeLook(map[string]string{"chromium": "/usr/bin/chromium"})
	if p, _ := DetectChrome("", look, envWith("", ""), "linux"); p != "/usr/bin/chromium" {
		t.Fatal(p)
	}
	look = fakeLook(map[string]string{"chromium-browser": "/usr/bin/chromium-browser"})
	if p, _ := DetectChrome("", look, envWith("", ""), "linux"); p != "/usr/bin/chromium-browser" {
		t.Fatal(p)
	}
	if _, err := DetectChrome("", fakeLook(nil), envWith("", ""), "linux"); err == nil || !strings.Contains(err.Error(), "browser.chrome_path") {
		t.Fatal(err)
	}
}

func TestDetectDarwinAppPath(t *testing.T) {
	look := fakeLook(map[string]string{macChromePath: macChromePath})
	if p, _ := DetectChrome("", look, envWith("", ""), "darwin"); p != macChromePath {
		t.Fatal(p)
	}
	// The macOS app path is not a candidate on linux.
	if _, err := DetectChrome("", look, envWith("", ""), "linux"); err == nil {
		t.Fatal("the macOS path must not be used on linux")
	}
}

func TestNoChromeErrorPerSystem(t *testing.T) {
	t.Cleanup(hostos.Set(hostos.Ubuntu))
	err := NoChromeError()
	if !errors.Is(err, ErrNoChrome) {
		t.Fatal("NoChromeError must wrap ErrNoChrome:", err)
	}
	want := "no Chrome or Chromium found; install Chromium (sudo apt install chromium-browser) or set browser.chrome_path"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestDetectChromeSnapPath(t *testing.T) {
	look := fakeLook(map[string]string{"/snap/bin/chromium": "/snap/bin/chromium"})
	if p, err := DetectChrome("", look, envWith("", ""), "linux"); err != nil || p != "/snap/bin/chromium" {
		t.Fatal(p, err)
	}
	// The snap path is not a candidate on darwin.
	if _, err := DetectChrome("", look, envWith("", ""), "darwin"); !errors.Is(err, ErrNoChrome) {
		t.Fatal("the snap path must not be used on darwin:", err)
	}
}

func TestDetectChromeMacPaths(t *testing.T) {
	const (
		userChrome = "/Users/u/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		chromium   = "/Applications/Chromium.app/Contents/MacOS/Chromium"
	)
	env := envWith("HOME", "/Users/u")
	look := fakeLook(map[string]string{userChrome: userChrome})
	if p, err := DetectChrome("", look, env, "darwin"); err != nil || p != userChrome {
		t.Fatal(p, err)
	}
	look = fakeLook(map[string]string{chromium: chromium})
	if p, err := DetectChrome("", look, env, "darwin"); err != nil || p != chromium {
		t.Fatal(p, err)
	}
	look = fakeLook(map[string]string{macChromePath: macChromePath, userChrome: userChrome, chromium: chromium})
	if p, err := DetectChrome("", look, env, "darwin"); err != nil || p != macChromePath {
		t.Fatal("the app in /Applications comes first:", p, err)
	}
	// With no HOME, the path in the home folder is not a candidate.
	look = fakeLook(map[string]string{userChrome: userChrome})
	if _, err := DetectChrome("", look, envWith("", ""), "darwin"); !errors.Is(err, ErrNoChrome) {
		t.Fatal("no HOME must give no home path:", err)
	}
}

func TestNoProcessBeforeFirstCall(t *testing.T) {
	m := &Manager{ChromePath: "/does/not/exist", ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute}
	if m.Running() {
		t.Fatal("no process before the first call")
	}
	m.Reap()
	m.Close()
	if m.Running() {
		t.Fatal("Reap and Close must not start a browser")
	}
}

func TestTabFailsWithBadChromePath(t *testing.T) {
	m := &Manager{ChromePath: t.TempDir() + "/no-chrome", ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute}
	defer m.Close()
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := m.Tab(tctx, "s1"); err == nil {
		t.Fatal("a bad Chrome path must give an error")
	}
	if m.Running() {
		t.Fatal("the browser must not run after a failed start")
	}
}

func TestTabNeedsChromePath(t *testing.T) {
	m := &Manager{ProfileDir: t.TempDir()}
	if _, err := m.Tab(ctx, "s1"); err == nil || !strings.Contains(err.Error(), "browser.chrome_path") {
		t.Fatal(err)
	}
}

func TestLazyStartAndIdleClose(t *testing.T) {
	path := chromeOrSkip(t)
	clk := newClock()
	m := &Manager{ChromePath: path, ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute, Now: clk.Now}
	defer m.Close()
	if m.Running() {
		t.Fatal("no process before first call")
	}
	if _, err := m.Tab(ctx, "s1"); err != nil || !m.Running() {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Minute)
	m.Reap()
	if !m.Running() {
		t.Fatal("a tab that is used 5 minutes ago must stay open")
	}
	clk.Advance(6 * time.Minute)
	m.Reap()
	if m.Running() {
		t.Fatal("browser exits when no tabs")
	}
	// The next call starts the browser again.
	if _, err := m.Tab(ctx, "s1"); err != nil || !m.Running() {
		t.Fatal("restart:", err)
	}
}

func TestReapKeepsActiveTab(t *testing.T) {
	path := chromeOrSkip(t)
	clk := newClock()
	m := &Manager{ChromePath: path, ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute, Now: clk.Now}
	defer m.Close()
	t1, err := m.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(8 * time.Minute)
	if _, err := m.Tab(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(3 * time.Minute)
	m.Reap()
	if !m.Running() {
		t.Fatal("s2 is active, so the browser must run")
	}
	if t1.Err() == nil {
		t.Fatal("the idle tab s1 must be closed")
	}
}

func TestOneTabPerSession(t *testing.T) {
	path := chromeOrSkip(t)
	m := &Manager{ChromePath: path, ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute}
	defer m.Close()
	a, err := m.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Tab(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("the same session must get the same tab")
	}
	if a == c {
		t.Fatal("another session must get another tab")
	}
	if chromedp.FromContext(a).Target.TargetID == chromedp.FromContext(c).Target.TargetID {
		t.Fatal("the two sessions share one target")
	}
	// The tab of a session works after the caller's context ends.
	short, cancel := context.WithCancel(ctx)
	d, err := m.Tab(short, "s3")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := chromedp.Run(d, chromedp.Evaluate[int](`1 + 2`)); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

func TestCloseStopsBrowser(t *testing.T) {
	path := chromeOrSkip(t)
	m := &Manager{ChromePath: path, ProfileDir: t.TempDir(), IdleClose: 10 * time.Minute}
	tab, err := m.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if m.Running() {
		t.Fatal("Close must stop the browser")
	}
	if _, err := chromedp.Run(tab, chromedp.Evaluate[int](`1`)); !errors.Is(err, context.Canceled) {
		t.Fatal("a tab must not work after Close:", err)
	}
}

func TestProfilePersistsCookies(t *testing.T) {
	path := chromeOrSkip(t)
	profile := t.TempDir()
	expires := cdp.TimeSinceEpoch(float64(time.Now().Add(24 * time.Hour).Unix()))

	m := &Manager{ChromePath: path, ProfileDir: profile, IdleClose: 10 * time.Minute}
	tab, err := m.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Call(tab, network.SetCookie, network.SetCookieParams{
		Name: "tether", Value: "kept", URL: "https://example.com/", Expires: expires,
	}); err != nil {
		t.Fatal(err)
	}
	m.Close()

	m2 := &Manager{ChromePath: path, ProfileDir: profile, IdleClose: 10 * time.Minute}
	defer m2.Close()
	tab2, err := m2.Tab(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := chromedp.Call(tab2, network.GetCookies, network.GetCookiesParams{URLs: []string{"https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Cookies {
		if c.Name == "tether" && c.Value == "kept" {
			return
		}
	}
	t.Fatalf("cookie not found after restart: %v", res.Cookies)
}
