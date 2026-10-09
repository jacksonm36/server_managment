package ops

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	leProduction = "https://acme-v02.api.letsencrypt.org/directory"
	leStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	acmeKeyPath  = "/etc/servermgr-agent/acme-account.key"
	renewBefore  = 30 * 24 * time.Hour
)

func writeCert(s *Site, chainDER [][]byte, key crypto.Signer) (time.Time, error) {
	if err := os.MkdirAll(s.certDir(), 0o700); err != nil {
		return time.Time{}, err
	}
	var chain []byte
	for _, der := range chainDER {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return time.Time{}, err
	}
	if err := writeFileAtomic(filepath.Join(s.certDir(), "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return time.Time{}, err
	}
	if err := writeFileAtomic(filepath.Join(s.certDir(), "fullchain.pem"), chain, 0o644); err != nil {
		return time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(chainDER[0])
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}

func selfSigned(s *Site) (time.Time, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return time.Time{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: s.Domain, Organization: []string{"servermgr self-signed"}},
		DNSNames:     s.names(),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return time.Time{}, err
	}
	return writeCert(s, [][]byte{der}, key)
}

func acmeAccountKey() (crypto.Signer, error) {
	if b, err := os.ReadFile(acmeKeyPath); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, errors.New("invalid ACME account key")
		}
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, err
		}
		return k.(crypto.Signer), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	if err := os.MkdirAll(filepath.Dir(acmeKeyPath), 0o700); err != nil {
		return nil, err
	}
	return key, os.WriteFile(acmeKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

// letsEncrypt obtains a certificate with HTTP-01. nginx serves
// /.well-known/acme-challenge/ from acmeWebroot for every managed site.
func letsEncrypt(ctx context.Context, s *Site, logf func(string, ...any)) (time.Time, error) {
	accountKey, err := acmeAccountKey()
	if err != nil {
		return time.Time{}, err
	}
	dir := leProduction
	if s.TLSStaging {
		dir = leStaging
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: dir, UserAgent: "servermgr-agent"}
	acct := &acme.Account{}
	if s.TLSEmail != "" {
		acct.Contact = []string{"mailto:" + s.TLSEmail}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return time.Time{}, fmt.Errorf("ACME register: %w", err)
	}
	logf("requesting certificate for %s from %s", strings.Join(s.names(), ", "), dir)
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(s.names()...))
	if err != nil {
		return time.Time{}, fmt.Errorf("ACME order: %w", err)
	}
	challengeDir := filepath.Join(acmeWebroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengeDir, 0o755); err != nil {
		return time.Time{}, err
	}
	for _, zurl := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, zurl)
		if err != nil {
			return time.Time{}, err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range z.Challenges {
			if c.Type == "http-01" {
				chal = c
			}
		}
		if chal == nil {
			return time.Time{}, fmt.Errorf("no http-01 challenge offered for %s", z.Identifier.Value)
		}
		resp, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return time.Time{}, err
		}
		tokenPath := filepath.Join(challengeDir, chal.Token)
		if err := os.WriteFile(tokenPath, []byte(resp), 0o644); err != nil {
			return time.Time{}, err
		}
		defer os.Remove(tokenPath)
		logf("validating %s (http-01)", z.Identifier.Value)
		if _, err := client.Accept(ctx, chal); err != nil {
			return time.Time{}, err
		}
		if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
			return time.Time{}, fmt.Errorf("validation of %s failed (is port 80 reachable from the internet and DNS pointing here?): %w", z.Identifier.Value, err)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return time.Time{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: s.names()}, key)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return time.Time{}, err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return time.Time{}, fmt.Errorf("ACME finalize: %w", err)
	}
	return writeCert(s, chain, key)
}

func issueCert(ctx context.Context, s *Site, logf func(string, ...any)) error {
	var exp time.Time
	var err error
	switch s.TLS {
	case "selfsigned":
		exp, err = selfSigned(s)
	case "letsencrypt":
		exp, err = letsEncrypt(ctx, s, logf)
	default:
		return errors.New("unknown TLS mode")
	}
	if err != nil {
		return err
	}
	s.TLSExpires = exp
	logf("certificate valid until %s", exp.Format(time.RFC3339))
	return nil
}

func siteTLS(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	switch mode := p["mode"]; mode {
	case "off":
		s.TLS, s.TLSExpires = "", time.Time{}
		if err := applyNginx(ctx, s, logf); err != nil {
			return nil, err
		}
		_ = os.RemoveAll(s.certDir())
	case "selfsigned", "letsencrypt":
		s.TLS = mode
		s.TLSStaging = p["staging"] == "true"
		if e := strings.TrimSpace(p["email"]); e != "" {
			if !emailRe.MatchString(strings.ToLower(e)) {
				return nil, errors.New("invalid email address")
			}
			s.TLSEmail = e
		}
		// Make sure the ACME location is live before validation.
		if err := applyNginx(ctx, s, logf); err != nil {
			return nil, err
		}
		if err := issueCert(ctx, s, logf); err != nil {
			return nil, err
		}
		if err := applyNginx(ctx, s, logf); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("mode must be off, selfsigned or letsencrypt")
	}
	if err := saveSite(s); err != nil {
		return nil, err
	}
	return webStatus(ctx)
}

// StartRenewer renews Let's Encrypt certificates that expire within 30 days.
// It checks shortly after start and then every 12 hours.
func StartRenewer(ctx context.Context, log *slog.Logger) {
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			renewDue(ctx, log)
			t.Reset(12 * time.Hour)
		}
	}()
}

func renewDue(ctx context.Context, log *slog.Logger) {
	sites, err := listSites()
	if err != nil {
		return
	}
	logf := func(format string, a ...any) { log.Info(fmt.Sprintf(format, a...)) }
	for _, s := range sites {
		if s.TLS != "letsencrypt" || time.Until(s.TLSExpires) > renewBefore {
			continue
		}
		log.Info("renewing certificate", "domain", s.Domain, "expires", s.TLSExpires)
		siteMu.Lock()
		err := issueCert(ctx, s, logf)
		if err == nil {
			err = applyNginx(ctx, s, logf)
		}
		if err == nil {
			err = saveSite(s)
		}
		siteMu.Unlock()
		if err != nil {
			log.Error("certificate renewal failed", "domain", s.Domain, "err", err)
		}
	}
}
