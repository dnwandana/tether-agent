// Package browser manages one headless Chrome for the browser tools. The
// browser starts on the first call and stops when no tab is open.
package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// macChromePath is the Chrome binary of the macOS app.
const macChromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// ErrNoChrome is the error when no Chrome or Chromium is found.
var ErrNoChrome = errors.New("no Chrome or Chromium found; install chromium (apt install chromium) or set browser.chrome_path")

// reapInterval is the time between two automatic calls of Reap.
const reapInterval = time.Minute

// closeTimeout limits the time that a graceful browser close can take.
const closeTimeout = 10 * time.Second

// DetectChrome returns the path of the Chrome binary. The order is the config
// value, then CHROME_PATH, then google-chrome, chromium, and chromium-browser
// in PATH, then the macOS app path on darwin.
func DetectChrome(configPath string, lookPath func(string) (string, error), getenv func(string) string, goos string) (string, error) {
	if configPath != "" {
		return configPath, nil
	}
	if p := getenv("CHROME_PATH"); p != "" {
		return p, nil
	}
	candidates := []string{"google-chrome", "chromium", "chromium-browser"}
	if goos == "darwin" {
		candidates = append(candidates, macChromePath)
	}
	for _, name := range candidates {
		if p, err := lookPath(name); err == nil && p != "" {
			return p, nil
		}
	}
	return "", ErrNoChrome
}

// tab is the browser tab of one session.
type tab struct {
	ctx     context.Context
	cancel  context.CancelFunc
	lastUse time.Time
}

// Manager owns one headless Chrome with one tab per session. The zero value
// does not start a process. The first call of Tab starts the browser.
type Manager struct {
	ChromePath string
	ProfileDir string        // cache/chrome-profile, so logins stay after a restart
	IdleClose  time.Duration // a tab that is idle longer than this closes (10 min)
	Now        func() time.Time
	Log        *slog.Logger

	mu            sync.Mutex
	allocCancel   context.CancelFunc
	browserCtx    context.Context // owns the first (blank) tab; keeps the browser alive
	browserCancel context.CancelFunc
	tabs          map[string]*tab
	stopReaper    chan struct{}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Tab returns the chromedp tab context of the session. It starts the browser
// on the first call, and it opens a tab on the first call for a session. Each
// call updates the last-use time of the tab. The tab does not end when ctx
// ends; ctx only limits the start of the browser and of the tab.
func (m *Manager) Tab(ctx context.Context, sessionID string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.browserCtx != nil && m.browserCtx.Err() != nil {
		// The browser stopped by itself, for example after a crash.
		m.log().Warn("browser: the Chrome process stopped; it starts again on this call")
		m.stopLocked()
	}
	if t, ok := m.tabs[sessionID]; ok {
		if t.ctx.Err() == nil {
			t.lastUse = m.now()
			return t.ctx, nil
		}
		// The page closed the tab. Open a new one.
		delete(m.tabs, sessionID)
	}
	if m.browserCtx == nil {
		if err := m.startLocked(ctx); err != nil {
			return nil, err
		}
	}

	tctx, tc := chromedp.NewContext(m.browserCtx)
	tcancel := sync.OnceFunc(tc)
	if err := runBounded(ctx, tctx, tcancel); err != nil {
		if len(m.tabs) == 0 {
			m.stopLocked()
		}
		return nil, fmt.Errorf("browser: open a tab for session %s: %w", sessionID, err)
	}
	m.tabs[sessionID] = &tab{ctx: tctx, cancel: tcancel, lastUse: m.now()}
	m.log().Info("browser: tab opened", "session", sessionID, "tabs", len(m.tabs))
	return tctx, nil
}

// startLocked starts the Chrome process. The caller holds m.mu.
func (m *Manager) startLocked(ctx context.Context) error {
	if m.ChromePath == "" {
		return ErrNoChrome
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(m.ChromePath),
		chromedp.UserDataDir(m.ProfileDir),
		chromedp.DisableGPU,
	)
	// The browser lives until Reap or Close stops it, not until ctx ends.
	// A chromedp cancel function can block when it runs a second time after a
	// failed start, so each one runs once only.
	allocCtx, ac := chromedp.NewExecAllocator(context.Background(), opts...)
	allocCancel := sync.OnceFunc(ac)
	bctx, bc := chromedp.NewContext(allocCtx)
	bcancel := sync.OnceFunc(bc)
	if err := runBounded(ctx, bctx, bcancel); err != nil {
		bcancel()
		allocCancel()
		return fmt.Errorf("browser: start Chrome at %s: %w", m.ChromePath, err)
	}
	m.allocCancel = allocCancel
	m.browserCtx = bctx
	m.browserCancel = bcancel
	m.tabs = map[string]*tab{}
	m.stopReaper = make(chan struct{})
	go m.reapLoop(m.stopReaper)
	m.log().Info("browser: Chrome started", "path", m.ChromePath)
	return nil
}

// runBounded runs the first chromedp action on target, so that the browser or
// the tab binds to target and not to ctx. It returns early when ctx ends, and
// then cancels target.
func runBounded(ctx, target context.Context, cancel context.CancelFunc) error {
	done := make(chan error, 1)
	go func() { done <- chromedp.Do(target) }()
	select {
	case err := <-done:
		if err != nil {
			cancel()
		}
		return err
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	}
}

// reapLoop calls Reap every reapInterval until stop closes.
func (m *Manager) reapLoop(stop chan struct{}) {
	tk := time.NewTicker(reapInterval)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			m.Reap()
		}
	}
}

// Reap closes the tabs that are idle longer than IdleClose. It stops the
// browser process when no tab stays open.
func (m *Manager) Reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browserCtx == nil {
		return
	}
	now := m.now()
	for id, t := range m.tabs {
		if t.ctx.Err() != nil || now.Sub(t.lastUse) > m.IdleClose {
			t.cancel()
			delete(m.tabs, id)
			m.log().Info("browser: idle tab closed", "session", id)
		}
	}
	if len(m.tabs) == 0 {
		m.stopLocked()
	}
}

// Running reports whether the browser process runs.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browserCtx != nil && m.browserCtx.Err() == nil
}

// Close closes all tabs and stops the browser.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// stopLocked closes all tabs and stops the browser gracefully, so that Chrome
// writes the cookies to the profile. The caller holds m.mu.
func (m *Manager) stopLocked() {
	if m.browserCtx == nil {
		return
	}
	for id, t := range m.tabs {
		t.cancel()
		delete(m.tabs, id)
	}
	close(m.stopReaper)

	cctx, cancel := context.WithTimeout(m.browserCtx, closeTimeout)
	if err := chromedp.Cancel(cctx); err != nil && !errors.Is(err, context.Canceled) {
		m.log().Warn("browser: graceful close failed; the process is stopped", "err", err)
	}
	cancel()
	m.browserCancel()
	m.allocCancel()

	m.browserCtx, m.browserCancel, m.allocCancel = nil, nil, nil
	m.tabs, m.stopReaper = nil, nil
	m.log().Info("browser: Chrome stopped")
}
