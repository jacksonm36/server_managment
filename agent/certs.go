package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// certStore holds the agent's client certificate. It renews it over the
// panel stream once a third of its lifetime is left, and swaps it in for the
// next connection without a restart.
type certStore struct {
	dir  string
	pool *x509.CertPool

	mu      sync.Mutex
	cert    *tls.Certificate
	pending *ecdsa.PrivateKey
}

func loadCertStore(dir string, pool *x509.CertPool) (*certStore, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"))
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	return &certStore{dir: dir, pool: pool, cert: &cert}, nil
}

func (s *certStore) clientCert(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert, nil
}

func (s *certStore) leaf() *x509.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert.Leaf
}

// renewDue reports whether less than a third of the certificate's lifetime
// (and never more than 120 days) is left.
func renewDue(leaf *x509.Certificate, now time.Time) bool {
	if leaf == nil {
		return false
	}
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	window := min(life/3, 120*24*time.Hour)
	return leaf.NotAfter.Sub(now) < window
}

// newCSR makes a fresh key and a CSR for it. The key is kept until the
// signed certificate comes back.
func (s *certStore) newCSR() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	hostname, _ := os.Hostname()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: hostname}}, key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.pending = key
	s.mu.Unlock()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// apply checks the renewed certificate (same server, our new key, signed by
// the panel CA for client auth), writes it to disk and uses it from now on.
func (s *certStore) apply(certPEM string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.pending
	if key == nil {
		return errors.New("no renewal pending")
	}
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil || b.Type != "CERTIFICATE" {
		return errors.New("renewed certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: s.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if s.cert.Leaf != nil && leaf.Subject.CommonName != s.cert.Leaf.Subject.CommonName {
		return errors.New("renewed certificate names another server")
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return errors.New("renewed certificate is not for our key")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Key first, then certificate, each by rename: a crash in between
	// leaves a mismatched pair, so keep the old pair as *.prev to recover.
	for _, f := range []string{"agent.key", "agent.crt"} {
		if b, err := os.ReadFile(filepath.Join(s.dir, f)); err == nil {
			_ = writeFileAtomic(filepath.Join(s.dir, f+".prev"), b)
		}
	}
	if err := writeFileAtomic(filepath.Join(s.dir, "agent.key"), keyPEM); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.dir, "agent.crt"), []byte(certPEM)); err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	if err != nil {
		return err
	}
	pair.Leaf = leaf
	s.cert, s.pending = &pair, nil
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadKeyPair loads agent.crt/agent.key, falling back to the previous pair
// if a renewal was interrupted between the two writes.
func loadKeyPair(dir string) error {
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key")); err == nil {
		return nil
	}
	crt, err1 := os.ReadFile(filepath.Join(dir, "agent.crt.prev"))
	key, err2 := os.ReadFile(filepath.Join(dir, "agent.key.prev"))
	if err1 != nil || err2 != nil {
		return nil // nothing to recover; the caller reports the real error
	}
	if _, err := tls.X509KeyPair(crt, key); err != nil {
		return nil
	}
	_ = writeFileAtomic(filepath.Join(dir, "agent.key"), key)
	return writeFileAtomic(filepath.Join(dir, "agent.crt"), crt)
}
