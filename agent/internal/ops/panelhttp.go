package ops

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

// PanelCA is the panel CA saved at enrollment; HTTPS to the panel trusts
// it and nothing else.
var PanelCA *x509.CertPool

// PanelClient talks HTTPS to the panel, trusting only certificates the
// panel CA issued, whatever address the panel is reached by.
func PanelClient(pool *x509.CertPool, timeout time.Duration) *http.Client {
	return panelClient(timeout, func([]*x509.Certificate) (*x509.CertPool, error) { return pool, nil })
}

// PinnedPanelClient is for enrollment, before the agent has the CA: the
// panel's chain must hold the CA whose SHA-256 the install command gave.
func PinnedPanelClient(caSHA256 string, timeout time.Duration) *http.Client {
	return panelClient(timeout, func(chain []*x509.Certificate) (*x509.CertPool, error) {
		for _, c := range chain {
			sum := sha256.Sum256(c.Raw)
			if strings.EqualFold(hex.EncodeToString(sum[:]), caSHA256) {
				pool := x509.NewCertPool()
				pool.AddCert(c)
				return pool, nil
			}
		}
		return nil, errors.New("the panel's certificate is not from the expected CA (fingerprint mismatch)")
	})
}

func panelClient(timeout time.Duration, roots func([]*x509.Certificate) (*x509.CertPool, error)) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The host name is not checked: the panel may be reached by an
		// address missing from its certificate. Trust comes from the CA.
		InsecureSkipVerify: true, // #nosec G402 -- verified in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate from the panel")
			}
			pool, err := roots(cs.PeerCertificates)
			if err != nil {
				return err
			}
			if pool == nil {
				return errors.New("no panel CA to check the panel against; re-enroll the agent")
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err = cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}
