package dashboard

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"syscall"
	"time"
)

// csp is the Content-Security-Policy of each response.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// The limits of the server and of the listen retry.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	retryMax          = time.Minute
)

// listenTCP and retryFirst are variables, so the tests can replace them.
var (
	listenTCP  = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
	retryFirst = time.Second
)

// SecurityHeaders sets the security headers, then calls next. It does not wrap the
// ResponseWriter, so Flush and ResponseController keep working.
func SecurityHeaders(next http.Handler, tls bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if tls {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// NewHTTPServer returns the server for h. It has no write timeout, because the event stream
// stays open for a long time.
func NewHTTPServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, IdleTimeout: idleTimeout}
}

// ListenRetry listens on addr. While the address is not available yet (for example a VPN
// address that is not up), it waits 1 s, then 2 s, and so on, up to 1 minute, until ctx ends.
// Each other error returns at once.
func ListenRetry(ctx context.Context, addr string, log *slog.Logger) (net.Listener, error) {
	wait := retryFirst
	for {
		ln, err := listenTCP(addr)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return nil, err
		}
		log.Warn("dashboard: the listen address is not available yet; the gateway tries again", "addr", addr, "wait", wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		wait = nextWait(wait)
	}
}

// nextWait returns the wait after d: two times d, at most retryMax.
func nextWait(d time.Duration) time.Duration { return min(2*d, retryMax) }
