package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenTofu workspaces tied to a Proxmox cluster. The Proxmox provider
// (bpg/proxmox) gets an endpoint on 127.0.0.1 served by the panel for the
// length of the run: a TLS reverse proxy that checks a per-run token and
// forwards to the cluster over the pinned connection with the cluster's
// real token. The real token is never in the run's environment, files or
// state, and stops working for tofu when the run ends.

// tofuPVEUser is the token ID the provider is given; the proxy swaps it.
const tofuPVEUser = "servermgr@pve!tofu"

// tofuProxmox starts the proxy and returns the environment for tofu, the
// run's token (to mask in output) and a stop function.
func (a *API) tofuProxmox(ctx context.Context, clusterID, work string) (env []string, token string, stop func(), err error) {
	_, cl, err := a.pveClient(ctx, clusterID)
	if err != nil {
		return nil, "", nil, err
	}
	cert, certPEM, err := loopbackCert()
	if err != nil {
		return nil, "", nil, err
	}
	// Go programs (tofu, providers) read SSL_CERT_FILE instead of the
	// system roots, so it holds both: registry downloads still verify.
	bundle := append(systemRoots(), certPEM...)
	caFile := filepath.Join(work, "servermgr-ca.pem")
	if err := os.WriteFile(caFile, bundle, 0o600); err != nil {
		return nil, "", nil, err
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, "", nil, err
	}
	token = randToken()
	want := []byte("PVEAPIToken=" + tofuPVEUser + "=" + token)
	fwd := cl.Forwarder()
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
				http.Error(w, "authentication failure", http.StatusUnauthorized)
				return
			}
			if !strings.HasPrefix(r.URL.Path, "/api2/json/") {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			fwd.ServeHTTP(w, r)
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	env = []string{
		"PROXMOX_VE_ENDPOINT=https://" + ln.Addr().String() + "/",
		"PROXMOX_VE_API_TOKEN=" + tofuPVEUser + "=" + token,
		"PROXMOX_VE_INSECURE=false",
		"SSL_CERT_FILE=" + caFile,
	}
	return env, token, func() {
		_ = srv.Close()
		_ = os.Remove(caFile)
	}, nil
}

// loopbackCert makes a throwaway self-signed certificate for 127.0.0.1.
func loopbackCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "servermgr tofu proxy"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(tofuTimeout + 10*time.Minute),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// systemRoots reads the system CA bundle (Debian, RHEL, Alpine paths).
func systemRoots() []byte {
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if b, err := os.ReadFile(p); err == nil {
			return append(b, '\n')
		}
	}
	return nil
}
