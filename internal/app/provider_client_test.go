package app

import (
	"net/http"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// The provider adapters get a client with the header timeout. The other clients keep a.HTTP,
// and the clone does not change the default transport.
func TestProviderClientHasHeaderTimeout(t *testing.T) {
	a := newApp(t)
	tr, ok := a.provHTTP.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout != provider.HeaderTimeout {
		t.Fatalf("the provider client must have a header timeout of %s: %#v", provider.HeaderTimeout, a.provHTTP.Transport)
	}
	if a.HTTP == a.provHTTP || a.HTTP.Transport != nil {
		t.Fatal("a.HTTP must stay a separate client with no timeout")
	}
	if d := http.DefaultTransport.(*http.Transport).ResponseHeaderTimeout; d != 0 {
		t.Fatal("the clone changed http.DefaultTransport:", d)
	}
}
