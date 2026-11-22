package dashboard

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// CertReloader gives the TLS certificate of the dashboard. It loads the PEM files again when
// their modification time changes, so a renewed certificate works with no restart.
type CertReloader struct {
	certFile, keyFile string
	mu                sync.Mutex
	cert              *tls.Certificate
	notAfter          time.Time
	certMod, keyMod   time.Time
}

// NewCertReloader loads the pair. It returns an error that names both files when the load fails.
func NewCertReloader(certFile, keyFile string) (*CertReloader, error) {
	certMod, keyMod, err := fileTimes(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("dashboard: cannot load the TLS files %s and %s: %w", certFile, keyFile, err)
	}
	cert, notAfter, err := loadPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("dashboard: cannot load the TLS files %s and %s: %w", certFile, keyFile, err)
	}
	return &CertReloader{
		certFile: certFile, keyFile: keyFile,
		cert: cert, notAfter: notAfter, certMod: certMod, keyMod: keyMod,
	}, nil
}

// GetCertificate returns the current pair. It loads the pair again when a file time changed.
// A failed Stat or a bad pair keeps the old pair, and the next handshake tries again.
func (c *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	certMod, keyMod, err := fileTimes(c.certFile, c.keyFile)
	c.mu.Lock()
	if err != nil || (certMod.Equal(c.certMod) && keyMod.Equal(c.keyMod)) {
		cert := c.cert
		c.mu.Unlock()
		return cert, nil
	}
	c.mu.Unlock()

	cert, notAfter, err := loadPair(c.certFile, c.keyFile)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.cert, c.notAfter, c.certMod, c.keyMod = cert, notAfter, certMod, keyMod
	}
	return c.cert, nil
}

// NotAfter returns the end of the leaf certificate of the pair in use.
func (c *CertReloader) NotAfter() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notAfter
}

// fileTimes returns the modification times of both files.
func fileTimes(certFile, keyFile string) (certMod, keyMod time.Time, err error) {
	ci, err := os.Stat(certFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	ki, err := os.Stat(keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return ci.ModTime(), ki.ModTime(), nil
}

// loadPair reads the pair and the leaf NotAfter. It takes no lock.
func loadPair(certFile, keyFile string) (*tls.Certificate, time.Time, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, time.Time{}, err
	}
	cert.Leaf = leaf
	return &cert, leaf.NotAfter, nil
}
