package dashboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

var authNow = time.UnixMilli(1_763_640_000_000)

// authFixture returns an Auth with a fixed clock over a new store, and the values of one session.
func authFixture(t *testing.T, cfg config.DashboardConfig) (a *Auth, st *Store, id, cookie, header string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st = NewStore(d)
	id, cookie, header, err = st.CreateSession(context.Background(), "Phone", "ua", "10.8.0.5", 24*time.Hour, authNow)
	if err != nil {
		t.Fatal(err)
	}
	return NewAuth(cfg, st, func() time.Time { return authNow }), st, id, cookie, header
}

// labelHandler writes the label of the device of the context.
var labelHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	d, ok := DeviceFrom(r.Context())
	if !ok {
		http.Error(w, "no device", 500)
		return
	}
	io.WriteString(w, d.Label)
})

func authReq(method, host, cookie, header string, set func(r *http.Request)) *http.Request {
	r := httptest.NewRequest(method, "http://"+host+"/api/sessions", nil)
	r.Host = host
	r.RemoteAddr = "127.0.0.1:5000"
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	if header != "" {
		r.Header.Set(HeaderName, header)
	}
	if set != nil {
		set(r)
	}
	return r
}

func TestMiddlewareNeedsCookieAndHeader(t *testing.T) {
	a, st, id, cookie, header := authFixture(t, config.Default().Dashboard)
	h := a.Middleware(labelHandler)
	const host = "127.0.0.1:8787"
	origin := func(o, site string) func(r *http.Request) {
		return func(r *http.Request) {
			if o != "" {
				r.Header.Set("Origin", o)
			}
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
		}
	}
	for _, c := range []struct {
		name string
		r    *http.Request
		code int
		body string
	}{
		{"none from loopback", authReq("GET", host, "", "", nil), 401, `{"error":"Sign in to the dashboard."}` + "\n"},
		{"cookie only", authReq("GET", host, cookie, "", nil), 401, ""},
		{"header only", authReq("GET", host, "", header, nil), 401, ""},
		{"both", authReq("GET", host, cookie, header, nil), 200, "Phone"},
		{"loopback name any port", authReq("GET", "localhost:9999", cookie, header, nil), 200, "Phone"},
		{"v6 loopback", authReq("GET", "[::1]:1234", cookie, header, nil), 200, "Phone"},
		{"wrong host", authReq("GET", "evil.example:8787", cookie, header, nil), 403,
			`{"error":"The Host header names a host that the dashboard does not accept."}` + "\n"},
		{"post no origin", authReq("POST", host, cookie, header, nil), 403,
			`{"error":"The request comes from an origin that the dashboard does not accept."}` + "\n"},
		{"post other origin", authReq("POST", host, cookie, header, origin("http://evil.example", "")), 403, ""},
		{"post null origin", authReq("POST", host, cookie, header, origin("null", "")), 403, ""},
		{"post cross site", authReq("POST", host, cookie, header, origin("http://127.0.0.1:8787", "cross-site")), 403, ""},
		{"post same origin", authReq("POST", host, cookie, header, origin("http://127.0.0.1:8787", "same-origin")), 200, "Phone"},
		{"post no fetch site", authReq("POST", host, cookie, header, origin("http://127.0.0.1:8787", "")), 200, "Phone"},
		{"wrong host before session", authReq("GET", "evil.example", "", "", nil), 403, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, c.r)
		if rec.Code != c.code || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s: %d %q", c.name, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: the API must send no CORS header", c.name)
		}
	}
	if err := st.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authReq("GET", host, cookie, header, nil))
	if rec.Code != 401 {
		t.Fatal("a revoked session must get 401 at once:", rec.Code)
	}
}

func TestMiddlewareTouches(t *testing.T) {
	_, st, _, cookie, header := authFixture(t, config.Default().Dashboard)
	later := authNow.Add(6 * time.Minute)
	a := NewAuth(config.Default().Dashboard, st, func() time.Time { return later })
	rec := httptest.NewRecorder()
	a.Middleware(labelHandler).ServeHTTP(rec, authReq("GET", "127.0.0.1:8787", cookie, header, nil))
	d, err := st.Lookup(context.Background(), cookie, header, later)
	if rec.Code != 200 || err != nil || !d.LastSeen.Equal(later) {
		t.Fatal(rec.Code, err, d.LastSeen)
	}
}

func TestHostRules(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(d *config.DashboardConfig)
		host string
		ok   bool
	}{
		{"listen host", func(d *config.DashboardConfig) { d.Listen = "10.8.0.1:8787" }, "10.8.0.1:8787", true},
		{"listen host other port", func(d *config.DashboardConfig) { d.Listen = "10.8.0.1:8787" }, "10.8.0.1:9000", false},
		{"listen host not default", nil, "10.8.0.1:8787", false},
		{"public url", func(d *config.DashboardConfig) { d.PublicURL = "https://agent.example.com" }, "agent.example.com", true},
		{"public url default port", func(d *config.DashboardConfig) { d.PublicURL = "https://agent.example.com" }, "agent.example.com:443", true},
		{"public url case", func(d *config.DashboardConfig) { d.PublicURL = "https://agent.example.com" }, "Agent.Example.COM", true},
		{"public url other port", func(d *config.DashboardConfig) { d.PublicURL = "https://agent.example.com" }, "agent.example.com:8443", false},
		{"allowed host listen port", func(d *config.DashboardConfig) {
			d.Listen, d.AllowedHosts = "0.0.0.0:8787", []string{"agent.tail1234.ts.net"}
		}, "agent.tail1234.ts.net:8787", true},
		{"allowed host no port", func(d *config.DashboardConfig) {
			d.Listen, d.AllowedHosts = "0.0.0.0:8787", []string{"agent.tail1234.ts.net"}
		}, "agent.tail1234.ts.net", true},
		{"allowed host other port", func(d *config.DashboardConfig) {
			d.Listen, d.AllowedHosts = "0.0.0.0:8787", []string{"agent.tail1234.ts.net"}
		}, "agent.tail1234.ts.net:9", false},
		{"allowed host with port", func(d *config.DashboardConfig) { d.AllowedHosts = []string{"agent.lan:443"} }, "agent.lan:443", true},
		{"wildcard is never a host", func(d *config.DashboardConfig) {
			d.Listen, d.AllowedHosts = "0.0.0.0:8787", []string{"agent.lan"}
		}, "0.0.0.0:8787", false},
		{"rebinding", nil, "attacker.example:8787", false},
	} {
		cfg := config.Default().Dashboard
		if c.edit != nil {
			c.edit(&cfg)
		}
		a := NewAuth(cfg, nil, time.Now)
		r := authReq("GET", c.host, "", "", nil)
		if err := a.CheckHostOrigin(r); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
		// The same host in Origin passes the origin check of a POST.
		p := authReq("POST", "127.0.0.1:8787", "", "", func(r *http.Request) { r.Header.Set("Origin", "https://"+c.host) })
		if err := a.CheckHostOrigin(p); (err == nil) != c.ok {
			t.Errorf("%s origin: %v", c.name, err)
		}
	}
}

func TestPeerIP(t *testing.T) {
	cfg := config.Default().Dashboard
	cfg.TrustedProxies = []string{"10.0.0.1", "192.168.0.0/16"}
	a := NewAuth(cfg, nil, time.Now)
	for _, c := range []struct{ remote, xff, want string }{
		{"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},
		{"10.0.0.1:4000", "1.2.3.4", "1.2.3.4"},
		{"10.0.0.1:4000", "1.2.3.4, 192.168.1.5", "1.2.3.4"},
		{"10.0.0.1:4000", "6.6.6.6, 1.2.3.4, 192.168.1.5", "1.2.3.4"},
		{"10.0.0.1:4000", "", "10.0.0.1"},
		{"[::1]:4000", "1.2.3.4", "::1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := a.PeerIP(r); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}

func TestSetSessionCookie(t *testing.T) {
	cookieOf := func(cfg config.DashboardConfig, v string) *http.Cookie {
		rec := httptest.NewRecorder()
		NewAuth(cfg, nil, time.Now).SetSession(rec, v)
		cs := rec.Result().Cookies()
		if len(cs) != 1 {
			t.Fatal(cs)
		}
		return cs[0]
	}
	cfg := config.Default().Dashboard
	c := cookieOf(cfg, "v1")
	if c.Name != "tether_dash" || c.Value != "v1" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode ||
		c.Path != "/" || c.MaxAge != 86400 || c.Secure {
		t.Fatalf("%+v", c)
	}
	cfg.PublicURL = "https://agent.example.com"
	cfg.SessionHours = 2
	if c := cookieOf(cfg, "v1"); !c.Secure || c.MaxAge != 7200 {
		t.Fatalf("%+v", c)
	}
	cfg = config.Default().Dashboard
	cfg.TLSCert, cfg.TLSKey = "/c.pem", "/k.pem"
	if c := cookieOf(cfg, "v1"); !c.Secure {
		t.Fatalf("%+v", c)
	}
	if c := cookieOf(cfg, ""); c.MaxAge != -1 || c.Value != "" {
		t.Fatalf("an empty value must clear the cookie: %+v", c)
	}
}

// fail records n failed tries of peer at now.
func fail(t *testing.T, l *Limiter, peer string, now time.Time, n int) {
	t.Helper()
	for range n {
		release, err := l.Try(peer, now)
		if err != nil {
			t.Fatal(peer, err)
		}
		release(true)
	}
}

func allowed(l *Limiter, peer string, now time.Time) error {
	release, err := l.Try(peer, now)
	if err == nil {
		release(false)
	}
	return err
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(5, 10*time.Minute, 10*time.Minute, 30, time.Hour)
	now := authNow
	for i := range 4 {
		fail(t, l, "a", now.Add(time.Duration(i)*time.Second), 1)
	}
	if allowed(l, "a", now.Add(5*time.Second)) != nil {
		t.Fatal("4 failures must not lock the peer")
	}
	fail(t, l, "a", now.Add(5*time.Second), 1)
	if !errors.Is(allowed(l, "a", now.Add(6*time.Second)), errPeerLocked) || allowed(l, "b", now.Add(6*time.Second)) != nil {
		t.Fatal("5 failures lock that peer and not another peer")
	}
	if allowed(l, "a", now.Add(10*time.Minute+6*time.Second)) != nil {
		t.Fatal("the lock ends after 10 minutes")
	}
	// Old failures leave the window.
	g := NewLimiter(5, 10*time.Minute, 10*time.Minute, 30, time.Hour)
	for i := range 4 {
		fail(t, g, "c", now.Add(time.Duration(i)*time.Minute), 1)
	}
	fail(t, g, "c", now.Add(11*time.Minute), 1)
	if allowed(g, "c", now.Add(11*time.Minute)) != nil {
		t.Fatal("only the failures of the last 10 minutes count")
	}
	// The global limit counts every peer: 5 failures of c and 25 of other peers make 30.
	for i := range 25 {
		fail(t, g, string(rune('A'+i%26))+string(rune('a'+i/26)), now.Add(20*time.Minute), 1)
	}
	if !errors.Is(allowed(g, "new", now.Add(21*time.Minute)), errGlobalLocked) {
		t.Fatal("30 failures in an hour must stop every peer")
	}
	// A loopback peer keeps its own limit only.
	for _, lo := range []string{"127.0.0.1", "127.8.0.1", "::1"} {
		if err := allowed(g, lo, now.Add(21*time.Minute)); err != nil {
			t.Fatal("the global limit must not stop a loopback peer:", lo, err)
		}
	}
	fail(t, g, "::1", now.Add(21*time.Minute), 5)
	if !errors.Is(allowed(g, "::1", now.Add(21*time.Minute)), errPeerLocked) {
		t.Fatal("a loopback peer keeps its own limit")
	}
	if allowed(g, "new", now.Add(81*time.Minute)) != nil {
		t.Fatal("the global limit ends after an hour")
	}
}

// A try that runs counts against the limits until its release.
func TestLimiterCountsPendingTries(t *testing.T) {
	l := NewLimiter(5, 10*time.Minute, 10*time.Minute, 7, time.Hour)
	var releases []func(bool)
	for range 5 {
		release, err := l.Try("a", authNow)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if !errors.Is(allowed(l, "a", authNow), errPeerLocked) {
		t.Fatal("5 running tries must stop a sixth try of the peer")
	}
	r1, _ := l.Try("b", authNow)
	r2, _ := l.Try("c", authNow)
	if !errors.Is(allowed(l, "d", authNow), errGlobalLocked) {
		t.Fatal("7 running tries must stop every peer")
	}
	r1(false)
	r2(false)
	for _, r := range releases {
		r(false)
		r(true) // a second call does nothing
	}
	if allowed(l, "a", authNow) != nil || allowed(l, "d", authNow) != nil {
		t.Fatal("a released try that did not fail must not count")
	}
	if len(l.pending) != 0 || l.globalPending != 0 {
		t.Fatal(l.pending, l.globalPending)
	}
}

// The entries of old peers go away, so the maps do not grow with each new peer.
func TestLimiterPurgesOldPeers(t *testing.T) {
	l := NewLimiter(5, 10*time.Minute, 10*time.Minute, 1000, time.Hour)
	for i := range 100 {
		peer := "10.0." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250)
		n := 1
		if i%2 == 0 {
			n = 5 // a locked peer
		}
		fail(t, l, peer, authNow, n)
	}
	if len(l.fails) != 50 || len(l.locked) != 50 {
		t.Fatal(len(l.fails), len(l.locked))
	}
	_ = allowed(l, "other", authNow.Add(11*time.Minute))
	if len(l.fails) != 0 || len(l.locked) != 0 {
		t.Fatalf("old entries stay: %d fails, %d locks", len(l.fails), len(l.locked))
	}
}

func TestMiddlewareFilesCookieOnly(t *testing.T) {
	a, _, _, cookie, _ := authFixture(t, config.Default().Dashboard)
	h := a.Middleware(labelHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authReq("GET", "127.0.0.1:8787", cookie, "", nil))
	if rec.Code != 401 {
		t.Fatal("another route needs the header too:", rec.Code)
	}
	// A POST to a files path still needs both values.
	r := authReq("POST", "127.0.0.1:8787", cookie, "", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8787") })
	r.URL.Path = "/api/files/abc"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatal("only GET of a file takes the cookie alone:", rec.Code)
	}
	r = authReq("GET", "127.0.0.1:8787", cookie, "", nil)
	r.URL.Path = "/api/files/abc"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal("a GET of a file takes the cookie alone:", rec.Code)
	}
}
