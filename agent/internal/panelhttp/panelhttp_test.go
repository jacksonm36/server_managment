package panelhttp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func TestPanelClient(t *testing.T) {
	ca, caKey := testCA(t)
	other, _ := testCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// The certificate names another host: trust comes from the CA alone.
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "panel"}, DNSNames: []string{"panel.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}}}
	srv.StartTLS()
	defer srv.Close()

	get := func(c *http.Client) error {
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	pool, wrong := x509.NewCertPool(), x509.NewCertPool()
	pool.AddCert(ca)
	wrong.AddCert(other)
	sum := sha256.Sum256(ca.Raw)
	osum := sha256.Sum256(other.Raw)
	if err := get(PanelClient(pool, 5*time.Second)); err != nil {
		t.Errorf("panel CA: %v", err)
	}
	if get(PanelClient(wrong, 5*time.Second)) == nil {
		t.Error("another CA was accepted")
	}
	if get(PanelClient(nil, 5*time.Second)) == nil {
		t.Error("no CA was accepted")
	}
	if err := get(PinnedPanelClient(hex.EncodeToString(sum[:]), 5*time.Second)); err != nil {
		t.Errorf("pinned: %v", err)
	}
	if get(PinnedPanelClient(hex.EncodeToString(osum[:]), 5*time.Second)) == nil {
		t.Error("wrong pin accepted")
	}
}
