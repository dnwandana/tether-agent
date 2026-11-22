package dashboard

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func TestSecurityHeaders(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the wrapper must keep the Flusher")
		}
	})
	for _, tls := range []bool{false, true} {
		rec := httptest.NewRecorder()
		SecurityHeaders(next, tls).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		h := rec.Header()
		if h.Get("Content-Security-Policy") != wantCSP || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%v", h)
		}
		if got := h.Get("Strict-Transport-Security"); (got == "max-age=31536000") != tls || (!tls && got != "") {
			t.Fatalf("tls %v: HSTS %q", tls, got)
		}
	}
}

func TestNewHTTPServer(t *testing.T) {
	s := NewHTTPServer(http.NotFoundHandler())
	if s.ReadHeaderTimeout != 10*time.Second || s.IdleTimeout != 120*time.Second || s.WriteTimeout != 0 || s.Handler == nil {
		t.Fatalf("%+v", s)
	}
}

// stubListen makes listenTCP return errs in order, then a real loopback listener.
func stubListen(t *testing.T, errs ...error) *int {
	t.Helper()
	oldListen, oldFirst := listenTCP, retryFirst
	n := 0
	listenTCP = func(addr string) (net.Listener, error) {
		n++
		if n <= len(errs) {
			return nil, errs[n-1]
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}
	retryFirst = time.Millisecond
	t.Cleanup(func() { listenTCP, retryFirst = oldListen, oldFirst })
	return &n
}

func notAvail() error {
	return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)}
}

func TestListenRetryOnAddrNotAvail(t *testing.T) {
	n := stubListen(t, notAvail(), notAvail())
	var buf bytes.Buffer
	ln, err := ListenRetry(context.Background(), "100.64.0.1:8787", slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if *n != 3 || strings.Count(buf.String(), "dashboard: the listen address is not available yet") != 2 {
		t.Fatalf("%d %q", *n, buf.String())
	}
}

func TestListenRetryOtherErrorFails(t *testing.T) {
	inUse := &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	n := stubListen(t, inUse)
	if _, err := ListenRetry(context.Background(), "127.0.0.1:8787", slog.Default()); !errors.Is(err, syscall.EADDRINUSE) || *n != 1 {
		t.Fatal(err, *n)
	}
}

func TestListenRetryStopsWithContext(t *testing.T) {
	errs := make([]error, 1000)
	for i := range errs {
		errs[i] = notAvail()
	}
	stubListen(t, errs...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := ListenRetry(ctx, "100.64.0.1:8787", slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestNextWait(t *testing.T) {
	for _, c := range [][2]time.Duration{
		{time.Second, 2 * time.Second}, {16 * time.Second, 32 * time.Second},
		{40 * time.Second, time.Minute}, {time.Minute, time.Minute},
	} {
		if got := nextWait(c[0]); got != c[1] {
			t.Errorf("nextWait(%v) = %v, want %v", c[0], got, c[1])
		}
	}
}
