package api

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/jacksonm36/servermgr/panel/internal/dns"
)

// The panel's own HTTPS certificate. By default the UI is served with a
// certificate from the panel CA (browsers warn unless /ca.crt is
// installed). Admins can give it a public certificate instead:
//
//   - "acme": Let's Encrypt, proven with a DNS-01 TXT record through one of
//     the panel's DNS providers, so it works for a panel that the internet
//     can't reach (a LAN address behind NAT).
//   - "custom": a certificate and key uploaded by the admin.
//
// The public certificate is only served to browsers that ask for its name
// (SNI). Agents and install commands use SM_PUBLIC_HOST and keep getting
// the pinned panel-CA certificate, so the public name must differ from it.

const (
	panelCertKey     = "panel_cert"      // panelCertSettings
	panelCertPEMKey  = "panel_cert_pem"  // sealed {"cert","key"}
	panelACMEAcctKey = "panel_acme_acct" // sealed PEM account key
	panelCertRenew   = 30 * 24 * time.Hour
)

type panelCertSettings struct {
	Mode      string    `json:"mode"` // internal | acme | custom
	Domain    string    `json:"domain"`
	Email     string    `json:"email"`
	Staging   bool      `json:"staging"`
	Expires   time.Time `json:"expires"`
	Issuer    string    `json:"issuer"`
	LastError string    `json:"last_error"`
	Issuing   bool      `json:"issuing"`
	UpdatedAt time.Time `json:"updated_at"`
}

// panelCert is the loaded public certificate (nil: internal only).
type panelCert struct {
	cert  *tls.Certificate
	names []string
}

var (
	activePanelCert atomic.Pointer[panelCert]
	panelCertMu     sync.Mutex // one issuance at a time
)

var domainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// TLSCertificate is the HTTPS listener's GetCertificate: the public
// certificate for its own name, the panel-CA one for everything else.
func (a *API) TLSCertificate(internal tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if pc := activePanelCert.Load(); pc != nil && h.ServerName != "" {
			name := strings.ToLower(h.ServerName)
			for _, n := range pc.names {
				if n == name || (strings.HasPrefix(n, "*.") && strings.HasSuffix(name, n[1:]) && !strings.Contains(strings.TrimSuffix(name, n[1:]), ".")) {
					return pc.cert, nil
				}
			}
		}
		return &internal, nil
	}
}

// publicCertFor reports whether host is served with the public certificate
// (for HSTS).
func publicCertFor(host string) bool {
	pc := activePanelCert.Load()
	if pc == nil {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	for _, n := range pc.names {
		if n == host {
			return true
		}
	}
	return false
}

func (a *API) publicHost() string {
	u, err := url.Parse(a.Cfg.PublicURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (a *API) loadPanelCertSettings(ctx context.Context) panelCertSettings {
	s := panelCertSettings{Mode: "internal"}
	_ = a.Store.GetSetting(ctx, panelCertKey, &s)
	if s.Mode == "" {
		s.Mode = "internal"
	}
	return s
}

// LoadPanelCert installs the stored public certificate at start-up.
func (a *API) LoadPanelCert(ctx context.Context) {
	s := a.loadPanelCertSettings(ctx)
	if s.Mode == "internal" {
		activePanelCert.Store(nil)
		return
	}
	var sealed string
	if err := a.Store.GetSetting(ctx, panelCertPEMKey, &sealed); err != nil || sealed == "" {
		return
	}
	plain, err := a.open(sealed)
	if err != nil {
		a.Log.Warn("panel certificate: can't open the stored certificate", "err", err)
		return
	}
	var pair struct{ Cert, Key string }
	if json.Unmarshal(plain, &pair) != nil {
		return
	}
	if err := installPanelCert([]byte(pair.Cert), []byte(pair.Key)); err != nil {
		a.Log.Warn("panel certificate", "err", err)
	}
}

func installPanelCert(certPEM, keyPEM []byte) error {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return err
	}
	c.Leaf = leaf
	names := []string{}
	for _, n := range leaf.DNSNames {
		names = append(names, strings.ToLower(n))
	}
	activePanelCert.Store(&panelCert{cert: &c, names: names})
	return nil
}

func (a *API) savePanelCert(ctx context.Context, certPEM, keyPEM []byte) error {
	plain, _ := json.Marshal(map[string]string{"Cert": string(certPEM), "Key": string(keyPEM)})
	sealed, err := a.seal(plain) // the panel key: HTTPS must work before the vault is unlocked
	if err != nil {
		return err
	}
	return a.Store.SetSetting(ctx, panelCertPEMKey, sealed)
}

// ---- handlers ----

func (a *API) handleGetPanelCert(w http.ResponseWriter, r *http.Request) {
	s := a.loadPanelCertSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "public_host": a.publicHost(), "https": strings.HasPrefix(a.Cfg.PublicURL, "https://")})
}

func (a *API) handleSetPanelCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode    string `json:"mode"`
		Domain  string `json:"domain"`
		Email   string `json:"email"`
		Staging bool   `json:"staging"`
		CertPEM string `json:"cert_pem"`
		KeyPEM  string `json:"key_pem"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Domain)), ".")
	s := a.loadPanelCertSettings(r.Context())
	switch req.Mode {
	case "internal":
		s = panelCertSettings{Mode: "internal", UpdatedAt: time.Now()}
		activePanelCert.Store(nil)
		_ = a.Store.SetSetting(r.Context(), panelCertPEMKey, "")
	case "custom":
		certPEM, keyPEM := []byte(strings.TrimSpace(req.CertPEM)), []byte(strings.TrimSpace(req.KeyPEM))
		c, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "certificate and key don't form a pair: "+err.Error())
			return
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if time.Now().After(leaf.NotAfter) {
			writeErr(w, http.StatusBadRequest, "this certificate has expired")
			return
		}
		if len(leaf.DNSNames) == 0 {
			writeErr(w, http.StatusBadRequest, "the certificate has no DNS names")
			return
		}
		for _, n := range leaf.DNSNames {
			if strings.EqualFold(n, a.publicHost()) {
				writeErr(w, http.StatusBadRequest, publicHostClash(a.publicHost()))
				return
			}
		}
		if err := a.savePanelCert(r.Context(), certPEM, keyPEM); err != nil {
			a.internal(w, err)
			return
		}
		if err := installPanelCert(certPEM, keyPEM); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s = panelCertSettings{Mode: "custom", Domain: strings.ToLower(leaf.DNSNames[0]), Expires: leaf.NotAfter, Issuer: leaf.Issuer.CommonName, UpdatedAt: time.Now()}
	case "acme":
		if !domainRe.MatchString(req.Domain) || strings.HasPrefix(req.Domain, "*.") {
			writeErr(w, http.StatusBadRequest, "enter the panel's host name, e.g. panel.example.com")
			return
		}
		if req.Domain == a.publicHost() {
			writeErr(w, http.StatusBadRequest, publicHostClash(req.Domain))
			return
		}
		if _, err := a.findZone(r.Context(), req.Domain); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error()+". Add the DNS provider that hosts it on the DNS page first.")
			return
		}
		s.Mode, s.Domain, s.Email, s.Staging = "acme", req.Domain, strings.TrimSpace(req.Email), req.Staging
		s.LastError, s.Issuing, s.UpdatedAt = "", true, time.Now()
		go a.issuePanelCert(req.Domain)
	default:
		writeErr(w, http.StatusBadRequest, "mode must be internal, acme or custom")
		return
	}
	if err := a.Store.SetSetting(r.Context(), panelCertKey, s); err != nil {
		a.internal(w, err)
		return
	}
	a.audit(r, "", "settings.panel_cert", s.Domain, map[string]string{"mode": s.Mode}, true)
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "public_host": a.publicHost()})
}

func publicHostClash(h string) string {
	return fmt.Sprintf("%s is the address agents use (SM_PUBLIC_HOST) and must keep the pinned panel certificate; use another name for the browser, e.g. panel.<your domain>", h)
}

// ---- Let's Encrypt over DNS-01 ----

// RunPanelCertRenewal renews an ACME certificate 30 days before it expires.
func (a *API) RunPanelCertRenewal(ctx context.Context) {
	a.LoadPanelCert(ctx)
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		s := a.loadPanelCertSettings(ctx)
		if s.Mode == "acme" && s.Domain != "" && (s.Expires.IsZero() || time.Until(s.Expires) < panelCertRenew) {
			a.issuePanelCert(s.Domain)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *API) issuePanelCert(domain string) {
	panelCertMu.Lock()
	defer panelCertMu.Unlock()
	ctx, cancel := context.WithTimeout(a.baseCtx(), 10*time.Minute)
	defer cancel()
	s := a.loadPanelCertSettings(ctx)
	if s.Mode != "acme" || s.Domain != domain {
		return
	}
	certPEM, keyPEM, leaf, err := a.obtainPanelCert(ctx, s)
	s = a.loadPanelCertSettings(ctx)
	s.Issuing = false
	if err != nil {
		s.LastError = err.Error()
		a.Log.Error("panel certificate", "domain", domain, "err", err)
		a.notifyEvent(ctx, Message{Title: fmt.Sprintf("[FAILED] Panel certificate for %s: %v", domain, err), Firing: true, Rule: "Panel certificate"})
	} else if err = a.savePanelCert(ctx, certPEM, keyPEM); err == nil {
		err = installPanelCert(certPEM, keyPEM)
		s.LastError, s.Expires, s.Issuer = "", leaf.NotAfter, leaf.Issuer.CommonName
		a.auditAs(ctx, Who{Actor: "panel"}, "settings.panel_cert_issued", domain, map[string]string{"expires": leaf.NotAfter.Format(time.DateOnly)}, true)
	}
	if err != nil && s.LastError == "" {
		s.LastError = err.Error()
	}
	s.UpdatedAt = time.Now()
	_ = a.Store.SetSetting(ctx, panelCertKey, s)
}

func (a *API) acmeAccountKey(ctx context.Context) (crypto.Signer, error) {
	var sealed string
	_ = a.Store.GetSetting(ctx, panelACMEAcctKey, &sealed)
	if sealed != "" {
		if plain, err := a.open(sealed); err == nil {
			if b, _ := pem.Decode(plain); b != nil {
				if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
					return k, nil
				}
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, _ := x509.MarshalECPrivateKey(k)
	s, err := a.seal(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	if err != nil {
		return nil, err
	}
	return k, a.Store.SetSetting(ctx, panelACMEAcctKey, s)
}

// acmeDirectory can be pointed at a test CA.
var acmeDirectory = func(staging bool) string {
	if staging {
		return "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	return acme.LetsEncryptURL
}

// dnsPropagationWait is how long to give a new TXT record before asking the
// CA to check it.
var dnsPropagationWait = 30 * time.Second

func (a *API) obtainPanelCert(ctx context.Context, s panelCertSettings) (certPEM, keyPEM []byte, leaf *x509.Certificate, err error) {
	acctKey, err := a.acmeAccountKey(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	cl := &acme.Client{Key: acctKey, DirectoryURL: acmeDirectory(s.Staging), UserAgent: "servermgr-panel"}
	acct := &acme.Account{}
	if s.Email != "" {
		acct.Contact = []string{"mailto:" + s.Email}
	}
	if _, err := cl.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, nil, nil, fmt.Errorf("ACME account: %w", err)
	}
	order, err := cl.AuthorizeOrder(ctx, acme.DomainIDs(s.Domain))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ACME order: %w", err)
	}
	m, err := a.findZone(ctx, s.Domain)
	if err != nil {
		return nil, nil, nil, err
	}
	var cleanup []dns.Record
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		for _, rec := range cleanup {
			_ = m.prov.Delete(cctx, m.Zone, rec)
		}
	}()
	for _, zurl := range order.AuthzURLs {
		z, err := cl.GetAuthorization(ctx, zurl)
		if err != nil {
			return nil, nil, nil, err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range z.Challenges {
			if c.Type == "dns-01" {
				chal = c
			}
		}
		if chal == nil {
			return nil, nil, nil, errors.New("the CA offered no dns-01 challenge")
		}
		val, err := cl.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, nil, nil, err
		}
		rec := dns.Record{Type: "TXT", Name: "_acme-challenge." + z.Identifier.Value, Content: val, TTL: 60}
		if err := dns.Validate(m.Zone, &rec); err != nil {
			return nil, nil, nil, err
		}
		if err := m.prov.Create(ctx, m.Zone, rec); err != nil {
			return nil, nil, nil, fmt.Errorf("create TXT %s at %s: %w", rec.Name, m.Provider.Name, err)
		}
		cleanup = append(cleanup, rec)
		select {
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		case <-time.After(dnsPropagationWait):
		}
		if _, err := cl.Accept(ctx, chal); err != nil {
			return nil, nil, nil, fmt.Errorf("ACME accept: %w", err)
		}
		if _, err := cl.WaitAuthorization(ctx, z.URI); err != nil {
			return nil, nil, nil, fmt.Errorf("the CA could not verify the TXT record: %w", err)
		}
	}
	if order, err = cl.WaitOrder(ctx, order.URI); err != nil {
		return nil, nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: s.Domain}, DNSNames: []string{s.Domain}}, key)
	if err != nil {
		return nil, nil, nil, err
	}
	ders, _, err := cl.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ACME finalize: %w", err)
	}
	for _, d := range ders {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	leaf, err = x509.ParseCertificate(ders[0])
	return certPEM, keyPEM, leaf, err
}
