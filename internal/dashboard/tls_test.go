package dashboard

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCert writes a new self-signed certificate that ends at notAfter, and its key, as PEM
// files in dir. It sets the modification time of both files to mod.
func writeCert(t *testing.T, dir string, notAfter, mod time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "agent.tail1234.ts.net"},
		DNSNames:     []string{"agent.tail1234.ts.net"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writePEM(t, certFile, "CERTIFICATE", der, mod)
	writePEM(t, keyFile, "EC PRIVATE KEY", kb, mod)
	return certFile, keyFile
}

func writePEM(t *testing.T, path, kind string, der []byte, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestCertReloaderLoadsAgainOnChange(t *testing.T) {
	dir := t.TempDir()
	mod := time.Now().Add(-time.Hour)
	first := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	certFile, keyFile := writeCert(t, dir, first, mod)
	c, err := NewCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if !c.NotAfter().Equal(first) {
		t.Fatalf("%v, want %v", c.NotAfter(), first)
	}
	old, err := c.GetCertificate(nil)
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	// A renewal writes new files with a new modification time.
	second := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	writeCert(t, dir, second, mod.Add(time.Minute))
	got, err := c.GetCertificate(nil)
	if err != nil || got == old || !c.NotAfter().Equal(second) {
		t.Fatalf("the renewed pair must load: %v %v", c.NotAfter(), err)
	}
	// The same times load nothing.
	if again, _ := c.GetCertificate(nil); again != got {
		t.Fatal("no change must keep the pair")
	}
}

func TestCertReloaderKeepsOldPairOnBadFile(t *testing.T) {
	dir := t.TempDir()
	mod := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	certFile, keyFile := writeCert(t, dir, notAfter, mod)
	c, err := NewCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := c.GetCertificate(nil)
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nhalf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certFile, mod.Add(time.Minute), mod.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetCertificate(nil); err != nil || got != good || !c.NotAfter().Equal(notAfter) {
		t.Fatal("a bad file must keep the old pair", err)
	}
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetCertificate(nil); err != nil || got != good {
		t.Fatal("a missing file must keep the old pair", err)
	}
}

func TestNewCertReloaderErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewCertReloader(filepath.Join(dir, "none.pem"), filepath.Join(dir, "none.key")); err == nil ||
		!strings.Contains(err.Error(), "none.pem") {
		t.Fatal(err)
	}
	// A key of another pair does not match.
	a, b := t.TempDir(), t.TempDir()
	certA, _ := writeCert(t, a, time.Now().Add(time.Hour), time.Now())
	_, keyB := writeCert(t, b, time.Now().Add(time.Hour), time.Now())
	if _, err := NewCertReloader(certA, keyB); err == nil {
		t.Fatal("a key that does not match must fail")
	}
}
