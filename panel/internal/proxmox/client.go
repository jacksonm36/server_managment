// Package proxmox is a small Proxmox VE API client: API-token auth and a
// pinned TLS certificate (SHA-256 of the leaf), so self-signed PVE
// certificates are trusted exactly, never blindly.
package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type Client struct {
	BaseURL string // https://host:8006
	TokenID string // user@realm!name
	Secret  string
	http    *http.Client
}

// Fingerprint formats a certificate hash like Proxmox does (AA:BB:…).
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// Probe connects without verifying and returns the server certificate's
// fingerprint and subject, for the admin to confirm before pinning.
func Probe(ctx context.Context, baseURL string) (fp, subject string, err error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", errors.New("URL must look like https://pve.example:8006")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "8006")
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} // only to read the certificate
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", "", errors.New("no certificate presented")
	}
	return Fingerprint(certs[0].Raw), certs[0].Subject.String(), nil
}

// New returns a client that only accepts a server certificate whose
// fingerprint equals pinned.
func New(baseURL, tokenID, secret, pinned string) *Client {
	pinned = strings.ToUpper(strings.TrimSpace(pinned))
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin check below
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 || Fingerprint(raw[0]) != pinned {
					return errors.New("certificate does not match the pinned Proxmox fingerprint")
				}
				return nil
			},
		},
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     60 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), TokenID: tokenID, Secret: secret, http: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// WithToken is a client for the same server (and pinned certificate)
// that authenticates with another token.
func (c *Client) WithToken(tokenID, secret string) *Client {
	return &Client{BaseURL: c.BaseURL, TokenID: tokenID, Secret: secret, http: c.http}
}

// APIError is an error response from Proxmox.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return fmt.Sprintf("proxmox: %s (HTTP %d)", e.Msg, e.Status) }

// Do calls /api2/json<path>. form is sent url-encoded for POST/PUT and as
// the query for GET/DELETE; out receives the "data" field.
func (c *Client) Do(ctx context.Context, method, path string, form url.Values, out any) error {
	u := c.BaseURL + "/api2/json" + path
	var body io.Reader
	if len(form) > 0 {
		if method == http.MethodGet || method == http.MethodDelete {
			u += "?" + form.Encode()
		} else {
			body = strings.NewReader(form.Encode())
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.TokenID+"="+c.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	var env struct {
		Data    json.RawMessage   `json:"data"`
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(env.Message)
		for k, v := range env.Errors {
			msg = strings.TrimSpace(msg + " " + k + ": " + v)
		}
		if msg == "" {
			msg = resp.Status
		}
		return &APIError{resp.StatusCode, msg}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// ---- typed helpers ----

type Resource struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"` // node | qemu | lxc | storage
	Node     string  `json:"node"`
	VMID     int     `json:"vmid,omitempty"`
	Name     string  `json:"name,omitempty"`
	Status   string  `json:"status"`
	CPU      float64 `json:"cpu"`
	MaxCPU   float64 `json:"maxcpu"`
	Mem      int64   `json:"mem"`
	MaxMem   int64   `json:"maxmem"`
	Disk     int64   `json:"disk"`
	MaxDisk  int64   `json:"maxdisk"`
	Uptime   int64   `json:"uptime"`
	Template int     `json:"template,omitempty"`
	Tags     string  `json:"tags,omitempty"`
	Pool     string  `json:"pool,omitempty"`
	Storage  string  `json:"storage,omitempty"`
}

func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	return out, c.Do(ctx, http.MethodGet, "/cluster/resources", nil, &out)
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	return v.Version, c.Do(ctx, http.MethodGet, "/version", nil, &v)
}

type TaskStatus struct {
	Status     string `json:"status"` // running | stopped
	ExitStatus string `json:"exitstatus"`
}

func (c *Client) Task(ctx context.Context, node, upid string) (*TaskStatus, error) {
	var t TaskStatus
	return &t, c.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid)+"/status", nil, &t)
}

// WaitTask polls a task until it stops; a non-OK exit status is an error.
func (c *Client) WaitTask(ctx context.Context, node, upid string) error {
	delay := 500 * time.Millisecond
	for {
		t, err := c.Task(ctx, node, upid)
		if err != nil {
			return err
		}
		if t.Status == "stopped" {
			if t.ExitStatus != "OK" && !strings.HasPrefix(t.ExitStatus, "WARNINGS") {
				return fmt.Errorf("task failed: %s", t.ExitStatus)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 3*time.Second {
			delay += 500 * time.Millisecond
		}
	}
}

// GuestPath is /nodes/<node>/<qemu|lxc>/<vmid>.
func GuestPath(node, typ string, vmid int) string {
	return fmt.Sprintf("/nodes/%s/%s/%d", url.PathEscape(node), typ, vmid)
}

// GuestIPv4 asks the QEMU guest agent for the first non-loopback IPv4.
func (c *Client) GuestIPv4(ctx context.Context, node string, vmid int) (string, error) {
	var res struct {
		Result []struct {
			Name string `json:"name"`
			IPs  []struct {
				Type string `json:"ip-address-type"`
				Addr string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if err := c.Do(ctx, http.MethodGet, GuestPath(node, "qemu", vmid)+"/agent/network-get-interfaces", nil, &res); err != nil {
		return "", err
	}
	for _, i := range res.Result {
		for _, a := range i.IPs {
			if ip := net.ParseIP(a.Addr); a.Type == "ipv4" && ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return a.Addr, nil
			}
		}
	}
	return "", errors.New("the guest agent reports no IPv4 address yet")
}

// GuestIPs lists a running guest's addresses: from the QEMU guest agent, or
// for a container from its interfaces. Loopback and link-local are left out.
func (c *Client) GuestIPs(ctx context.Context, node, typ string, vmid int) ([]string, error) {
	var addrs []string
	if typ == "lxc" {
		var ifs []struct {
			Name  string `json:"name"`
			Inet  string `json:"inet"`
			Inet6 string `json:"inet6"`
		}
		if err := c.Do(ctx, http.MethodGet, GuestPath(node, typ, vmid)+"/interfaces", nil, &ifs); err != nil {
			return nil, err
		}
		for _, i := range ifs {
			for _, a := range []string{i.Inet, i.Inet6} {
				addrs = append(addrs, strings.Split(a, " ")...)
			}
		}
	} else {
		var res struct {
			Result []struct {
				IPs []struct {
					Addr string `json:"ip-address"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		if err := c.Do(ctx, http.MethodGet, GuestPath(node, typ, vmid)+"/agent/network-get-interfaces", nil, &res); err != nil {
			return nil, err
		}
		for _, i := range res.Result {
			for _, a := range i.IPs {
				addrs = append(addrs, a.Addr)
			}
		}
	}
	out := []string{}
	for _, a := range addrs {
		host, _, _ := strings.Cut(a, "/")
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out, nil
}

// ConsoleProxy is a console session Proxmox opened with vncproxy or
// termproxy: a port on the node and a short-lived ticket for it.
type ConsoleProxy struct {
	Port   int
	Ticket string
	User   string // termproxy: the user the ticket was issued to
	Base   string // API path the console belongs to: a guest or /nodes/<node>
}

// OpenConsole starts vncproxy (serial == "") or termproxy on serial
// ("serial0"…; empty for a container's console when term is true).
func (c *Client) OpenConsole(ctx context.Context, node, typ string, vmid int, term bool, serial string) (*ConsoleProxy, error) {
	var res struct {
		Port   json.Number `json:"port"`
		Ticket string      `json:"ticket"`
		User   string      `json:"user"`
	}
	form := url.Values{}
	sub := "/vncproxy"
	if term {
		sub = "/termproxy"
		if serial != "" {
			form.Set("serial", serial)
		}
	} else {
		form.Set("websocket", "1")
	}
	if err := c.Do(ctx, http.MethodPost, GuestPath(node, typ, vmid)+sub, form, &res); err != nil {
		return nil, err
	}
	port, err := res.Port.Int64()
	if err != nil || port <= 0 || res.Ticket == "" {
		return nil, errors.New("proxmox returned no console port or ticket")
	}
	if res.User == "" {
		res.User, _, _ = strings.Cut(c.TokenID, "!")
	}
	return &ConsoleProxy{Port: int(port), Ticket: res.Ticket, User: res.User, Base: GuestPath(node, typ, vmid)}, nil
}

// OpenNodeShell starts termproxy for a node's own shell. cmd is "" for a
// login shell or "upgrade" for Proxmox's package upgrade.
func (c *Client) OpenNodeShell(ctx context.Context, node, cmd string) (*ConsoleProxy, error) {
	var res struct {
		Port   json.Number `json:"port"`
		Ticket string      `json:"ticket"`
		User   string      `json:"user"`
	}
	form := url.Values{}
	if cmd != "" {
		form.Set("cmd", cmd)
	}
	base := "/nodes/" + url.PathEscape(node)
	if err := c.Do(ctx, http.MethodPost, base+"/termproxy", form, &res); err != nil {
		return nil, err
	}
	port, err := res.Port.Int64()
	if err != nil || port <= 0 || res.Ticket == "" {
		return nil, errors.New("proxmox returned no console port or ticket")
	}
	if res.User == "" {
		res.User, _, _ = strings.Cut(c.TokenID, "!")
	}
	return &ConsoleProxy{Port: int(port), Ticket: res.Ticket, User: res.User, Base: base}, nil
}

// Upload streams a file to path (a storage's /upload) as multipart form
// data, without holding it in memory, and returns the task's UPID.
func (c *Client) Upload(ctx context.Context, path string, fields map[string]string, filename string, file io.Reader) (string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() { pw.CloseWithError(err) }()
		for k, v := range fields {
			if err = mw.WriteField(k, v); err != nil {
				return
			}
		}
		var part io.Writer
		if part, err = mw.CreateFormFile("filename", filename); err != nil {
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			return
		}
		err = mw.Close()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api2/json"+path, pr)
	if err != nil {
		pr.Close()
		return "", err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.TokenID+"="+c.Secret)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	hc := *c.http
	hc.Timeout = 0 // the context bounds an upload, which can take a while
	resp, err := hc.Do(req)
	if err != nil {
		pr.Close()
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = resp.Status
		}
		return "", &APIError{resp.StatusCode, msg}
	}
	var upid string
	_ = json.Unmarshal(env.Data, &upid)
	return upid, nil
}

// DialConsole opens Proxmox's vncwebsocket for a console session, over the
// pinned TLS connection and authenticated with the client's token.
func (c *Client) DialConsole(ctx context.Context, node, typ string, vmid int, p *ConsoleProxy) (*websocket.Conn, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	base := p.Base
	if base == "" {
		base = GuestPath(node, typ, vmid)
	}
	u.Path = "/api2/json" + base + "/vncwebsocket"
	u.RawQuery = url.Values{"port": {strconv.Itoa(p.Port)}, "vncticket": {p.Ticket}}.Encode()
	hc := *c.http
	hc.Timeout = 0 // the context bounds the dial; the socket lives on
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:   &hc,
		HTTPHeader:   http.Header{"Authorization": {"PVEAPIToken=" + c.TokenID + "=" + c.Secret}},
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("proxmox console: %s", resp.Status)
		}
		return nil, fmt.Errorf("proxmox console: %w", err)
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

// Forwarder is a reverse proxy to the API (/api2/...) that authenticates
// every request with the client's token over the pinned connection. The
// caller decides who may use it; the client's secret never leaves the
// panel.
func (c *Client) Forwarder() http.Handler {
	target, _ := url.Parse(c.BaseURL)
	return &httputil.ReverseProxy{
		Transport: c.http.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Set("Authorization", "PVEAPIToken="+c.TokenID+"="+c.Secret)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("CSRFPreventionToken")
		},
	}
}
