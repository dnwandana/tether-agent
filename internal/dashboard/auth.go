package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// The names of the two session values.
const (
	CookieName = "tether_dash"
	HeaderName = "X-Tether-Session"
)

// The texts of the JSON error bodies.
const (
	textSignIn    = "Sign in to the dashboard."
	textBadHost   = "The Host header names a host that the dashboard does not accept."
	textBadOrigin = "The request comes from an origin that the dashboard does not accept."
)

var (
	errBadHost   = errors.New("dashboard: the Host header is not an allowed host")
	errBadOrigin = errors.New("dashboard: the Origin header is not an allowed origin")
)

// Auth checks the requests of the dashboard: the host, the origin, and the device session.
type Auth struct {
	cfg     config.DashboardConfig
	st      *Store
	now     func() time.Time
	proxies []netip.Prefix
}

type deviceKey struct{}

// NewAuth returns an Auth. It parses cfg.TrustedProxies once; an entry that is not an address
// or a prefix is ignored, because Check of the config already reports it.
func NewAuth(cfg config.DashboardConfig, st *Store, now func() time.Time) *Auth {
	a := &Auth{cfg: cfg, st: st, now: now}
	for _, p := range cfg.TrustedProxies {
		if pf, err := netip.ParsePrefix(p); err == nil {
			a.proxies = append(a.proxies, pf.Masked())
		} else if ip, err := netip.ParseAddr(p); err == nil {
			ip = ip.Unmap()
			a.proxies = append(a.proxies, netip.PrefixFrom(ip, ip.BitLen()))
		}
	}
	return a
}

// authError writes the JSON body {"error": text} with the status code.
func authError(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
}

// Middleware checks the host, the origin of a method that changes state, and the device
// session, in this order. It puts the Device in the context of the request.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.CheckHostOrigin(r); err != nil {
			text := textBadOrigin
			if errors.Is(err, errBadHost) {
				text = textBadHost
			}
			authError(w, http.StatusForbidden, text)
			return
		}
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			authError(w, http.StatusUnauthorized, textSignIn)
			return
		}
		header := r.Header.Get(HeaderName)
		now := a.now()
		var dev Device
		if header == "" && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/files/") {
			// A media tag cannot send the header. The file ids are 128-bit random values.
			dev, err = a.st.lookupCookie(r.Context(), c.Value, now)
		} else {
			dev, err = a.st.Lookup(r.Context(), c.Value, header, now)
		}
		if err != nil {
			authError(w, http.StatusUnauthorized, textSignIn)
			return
		}
		_ = a.st.Touch(r.Context(), dev.ID, now)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, dev)))
	})
}

// DeviceFrom returns the Device that Middleware put in ctx.
func DeviceFrom(ctx context.Context) (Device, bool) {
	d, ok := ctx.Value(deviceKey{}).(Device)
	return d, ok
}

// CheckHostOrigin checks the Host header of each request. For a method that is not GET or HEAD,
// it also checks the Origin header and Sec-Fetch-Site.
func (a *Auth) CheckHostOrigin(r *http.Request) error {
	if !a.hostAllowed(r.Host) {
		return errBadHost
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return errBadOrigin
	}
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !a.hostAllowed(u.Host) {
		return errBadOrigin
	}
	return nil
}

// splitHost returns the lower-case name and the port of a Host value. The port can be empty.
func splitHost(h string) (name, port string) {
	h = strings.ToLower(strings.TrimSpace(h))
	if n, p, err := net.SplitHostPort(h); err == nil {
		return n, p
	}
	return strings.Trim(h, "[]"), ""
}

// hostAllowed reports whether host (a Host header value, or the host of an Origin) is allowed.
func (a *Auth) hostAllowed(host string) bool {
	name, port := splitHost(host)
	if name == "" {
		return false
	}
	if name == "localhost" || name == "127.0.0.1" || name == "::1" {
		return true
	}
	lname, lport := splitHost(a.cfg.Listen)
	if ip, err := netip.ParseAddr(lname); lname != "" && (err != nil || !ip.IsUnspecified()) {
		if name == lname && port == lport {
			return true
		}
	}
	if a.cfg.PublicURL != "" {
		if u, err := url.Parse(a.cfg.PublicURL); err == nil && u.Host != "" {
			uname, uport := splitHost(u.Host)
			def := map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
			if uport == "" {
				if name == uname && (port == "" || port == def) {
					return true
				}
			} else if name == uname && port == uport {
				return true
			}
		}
	}
	for _, e := range a.cfg.AllowedHosts {
		ename, eport := splitHost(e)
		if name != ename {
			continue
		}
		if eport != "" {
			if port == eport {
				return true
			}
		} else if port == "" || port == lport {
			return true
		}
	}
	return false
}

func (a *Auth) trusted(ip netip.Addr) bool {
	for _, p := range a.proxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// PeerIP returns the IP of RemoteAddr. When that IP is a trusted proxy, it walks
// X-Forwarded-For from the right and returns the first IP that is not a trusted proxy.
func (a *Auth) PeerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	if !a.trusted(ip) {
		return ip.String()
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for _, p := range slices.Backward(parts) {
		f, err := netip.ParseAddr(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		f = f.Unmap()
		if !a.trusted(f) {
			return f.String()
		}
	}
	return ip.String()
}

// SetSession sets the session cookie. An empty value clears the cookie.
func (a *Auth) SetSession(w http.ResponseWriter, cookie string) {
	c := &http.Cookie{
		Name:     CookieName,
		Value:    cookie,
		Path:     "/",
		MaxAge:   a.cfg.SessionHours * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.cfg.TLS() || strings.HasPrefix(a.cfg.PublicURL, "https://"),
	}
	if cookie == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// The errors of Limiter.Try.
var (
	errPeerLocked   = errors.New("dashboard: too many wrong codes from this peer")
	errGlobalLocked = errors.New("dashboard: too many wrong codes from all peers")
)

// Limiter counts the failed sign-in tries of each peer and of all peers. A try that runs
// counts as a failure until its release, so parallel tries cannot pass the limits.
type Limiter struct {
	mu                         sync.Mutex
	perPeer, global            int
	window, lock, globalWindow time.Duration
	fails                      map[string][]time.Time
	locked                     map[string]time.Time
	globalFails                []time.Time
	pending                    map[string]int
	globalPending              int
	lastPurge                  time.Time
}

// NewLimiter returns a Limiter. It locks a peer for lock after perPeer failures in window.
// It refuses every peer that is not loopback while global failures are in the last globalWindow.
func NewLimiter(perPeer int, window, lock time.Duration, global int, globalWindow time.Duration) *Limiter {
	return &Limiter{perPeer: perPeer, window: window, lock: lock, global: global, globalWindow: globalWindow,
		fails: map[string][]time.Time{}, locked: map[string]time.Time{}, pending: map[string]int{}}
}

// recent returns the times that are newer than now-d.
func recent(ts []time.Time, now time.Time, d time.Duration) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(now.Add(-d)) {
			out = append(out, t)
		}
	}
	return out
}

// isLoopback reports whether peer is an IP in 127.0.0.0/8 or ::1. A loopback peer is on the
// host, so the global limit does not stop the owner who runs tether dashboard link there.
func isLoopback(peer string) bool {
	ip, err := netip.ParseAddr(peer)
	return err == nil && ip.Unmap().IsLoopback()
}

// purgeLocked drops the failures that left the window and the locks that ended. It runs at
// most once in each window, so a call stays cheap. The caller holds l.mu.
func (l *Limiter) purgeLocked(now time.Time) {
	if now.Sub(l.lastPurge) < l.window {
		return
	}
	l.lastPurge = now
	for peer, ts := range l.fails {
		if ts = recent(ts, now, l.window); len(ts) == 0 {
			delete(l.fails, peer)
		} else {
			l.fails[peer] = ts
		}
	}
	for peer, until := range l.locked {
		if !now.Before(until) {
			delete(l.locked, peer)
		}
	}
}

// Try reserves one try of the peer. It returns errPeerLocked or errGlobalLocked when the
// limits refuse the try. Else the caller must call release once, with failed true for a
// wrong code.
func (l *Limiter) Try(peer string, now time.Time) (release func(failed bool), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.purgeLocked(now)
	if until, ok := l.locked[peer]; ok {
		if now.Before(until) {
			return nil, errPeerLocked
		}
		delete(l.locked, peer)
	}
	l.globalFails = recent(l.globalFails, now, l.globalWindow)
	if !isLoopback(peer) && len(l.globalFails)+l.globalPending >= l.global {
		return nil, errGlobalLocked
	}
	// recent changes the slice in place, so keep its result.
	f := recent(l.fails[peer], now, l.window)
	if len(f) == 0 {
		delete(l.fails, peer)
	} else {
		l.fails[peer] = f
	}
	if len(f)+l.pending[peer] >= l.perPeer {
		return nil, errPeerLocked
	}
	l.pending[peer]++
	l.globalPending++
	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.pending[peer]--; l.pending[peer] <= 0 {
				delete(l.pending, peer)
			}
			l.globalPending--
			if failed {
				l.failLocked(peer, now)
			}
		})
	}, nil
}

// failLocked records one failed try of the peer. The caller holds l.mu.
func (l *Limiter) failLocked(peer string, now time.Time) {
	l.globalFails = append(recent(l.globalFails, now, l.globalWindow), now)
	f := append(recent(l.fails[peer], now, l.window), now)
	if len(f) >= l.perPeer {
		l.locked[peer] = now.Add(l.lock)
		delete(l.fails, peer)
		return
	}
	l.fails[peer] = f
}
