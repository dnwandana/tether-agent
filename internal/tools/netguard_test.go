package tools

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBlockedRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.254",
		"::1", "fd00::1", "fc00::1", "fe80::1", "0.0.0.0", "::",
		"224.0.0.1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
	}
	for _, s := range blocked {
		if !IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "172.32.0.1", "100.128.0.1", "2606:4700::1111", "::ffff:8.8.8.8"}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
	if !IsBlockedIP(nil) {
		t.Error("a nil IP must be blocked")
	}
}

func TestClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := GuardedClient(true, 5*time.Second, 3).Get(srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatal(err)
	}
}

func TestClientRefusesLoopbackByHostName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	u := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, err := GuardedClient(true, 5*time.Second, 3).Get(u)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatal(err)
	}
}

// redirectServer redirects n times, then answers 200.
func redirectServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(r.URL.Query().Get("i"))
		if i < n {
			http.Redirect(w, r, "/?i="+strconv.Itoa(i+1), http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRedirectLimit3(t *testing.T) {
	srv := redirectServer(t, 4)
	_, err := GuardedClient(false, 5*time.Second, 3).Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") {
		t.Fatal(err)
	}
}

func TestRedirect3Allowed(t *testing.T) {
	srv := redirectServer(t, 3)
	resp, err := GuardedClient(false, 5*time.Second, 3).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
}

func TestRedirectRefusesOtherScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer srv.Close()
	_, err := GuardedClient(false, 5*time.Second, 3).Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatal(err)
	}
}

func TestBlockFalseAllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	resp, err := GuardedClient(false, 5*time.Second, 3).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
}
