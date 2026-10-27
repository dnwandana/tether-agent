package tools

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress is the error when the guarded client dials a private
// or internal address.
var ErrBlockedAddress = errors.New("blocked: private or internal address (web.block_private_networks is on)")

// blockedPrefixes are the ranges that IsBlockedIP refuses in addition to the
// netip class checks (loopback, link-local, multicast, unspecified, private).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),      // CGNAT
	netip.MustParsePrefix("255.255.255.255/32"), // limited broadcast
}

// IsBlockedIP reports whether ip is a loopback, private, link-local, CGNAT,
// unspecified, or multicast address. It also blocks a nil IP. It unmaps an
// IPv4-mapped IPv6 address before the check.
func IsBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// guardControl is a net.Dialer Control function. It checks the IP that the
// dialer connects to, after DNS resolution, so DNS rebinding cannot get past it.
func guardControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: cannot parse dial address %q", ErrBlockedAddress, address)
	}
	if IsBlockedIP(net.ParseIP(host)) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
	}
	return nil
}

// GuardedClient returns an HTTP client for the web tools. When block is true,
// the dialer refuses private and internal addresses, and the client ignores
// proxy settings, because a proxy would hide the real target from the check.
// The client follows maxRedirects redirects at most, and each hop must use
// the http or https scheme.
func GuardedClient(block bool, timeout time.Duration, maxRedirects int) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if block {
		dialer.Control = guardControl
		tr.Proxy = nil
	}
	tr.DialContext = dialer.DialContext
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if s := req.URL.Scheme; s != "http" && s != "https" {
				return fmt.Errorf("redirect to scheme %q is not allowed; only http and https are allowed", s)
			}
			return nil
		},
	}
}
