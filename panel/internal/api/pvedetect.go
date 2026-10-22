package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Detecting what a guest runs from the outside. The panel never runs
// anything inside a guest (no guest-exec, no file access) and never hands
// guest data to Proxmox: it only opens TCP connections to a few well-known
// ports on the guest's own private addresses and reads at most a few KiB
// of what answers (SSH and MySQL greetings, an HTTP page's title and
// Server header). Everything read is guest-controlled, so it is only
// matched against the fixed patterns below and reduced to one of their
// keys; no guest text is stored, logged or sent to the browser.
//
// A guest can therefore change its own icon and nothing else. To keep a
// guest from steering the panel at other machines, an address is probed
// only if it is private (RFC 1918, CGNAT or IPv6 ULA), not one of the
// panel host's own addresses and not in a container or VM network that
// lives on the panel host (docker, incus, libvirt…). Only platform users'
// requests probe; tenants get name and tag guesses.

const (
	probeTimeout = 1500 * time.Millisecond
	probeTTL     = 30 * time.Minute
	probeMaxBody = 64 << 10
)

// probePorts are tried on every address: web UIs and two greetings that
// name software and distribution.
var (
	probeHTTPSPorts = []int{443, 8443, 9443, 8006, 8007, 10443}
	probeHTTPPorts  = []int{80, 8080, 3000, 5000, 5380, 8000, 8096, 8123, 9000, 3001, 81}
)

type appPattern struct {
	key string
	re  *regexp.Regexp
}

// appPatterns map what a web page, SSH or MySQL greeting says to an app
// key the UI has an icon for. Specific products come before the generic
// web servers; the first match wins.
var appPatterns = func() []appPattern {
	raw := [][2]string{
		{"truenas", `truenas`},
		{"pfsense", `pfsense`},
		{"opnsense", `opnsense`},
		{"proxmox", `proxmox`},
		{"unifi", `unifi`},
		{"mikrotik", `routeros|mikrotik|webfig`},
		{"nextcloud", `nextcloud`},
		{"owncloud", `owncloud`},
		{"vaultwarden", `vaultwarden|bitwarden web vault`},
		{"openbao", `openbao`},
		{"vault", `hashicorp vault|vault ui`},
		{"authentik", `authentik`},
		{"keycloak", `keycloak`},
		{"forgejo", `forgejo`},
		{"gitea", `gitea`},
		{"gitlab", `gitlab`},
		{"technitium", `technitium`},
		{"pangolin", `pangolin`},
		{"pihole", `pi-hole`},
		{"adguard", `adguard`},
		{"homeassistant", `home assistant`},
		{"jellyfin", `jellyfin`},
		{"plex", `plex`},
		{"immich", `immich`},
		{"paperless", `paperless`},
		{"portainer", `portainer`},
		{"grafana", `grafana`},
		{"uptimekuma", `uptime kuma`},
		{"mailcow", `mailcow`},
		{"roundcube", `roundcube`},
		{"iventoy", `iventoy`},
		{"netboot", `netboot\.xyz`},
		{"openarchiver", `open ?archiver`},
		{"termix", `termix`},
		{"pterodactyl", `pterodactyl|jexactyl|pelican panel`},
		{"rustdesk", `rustdesk|betterdesk`},
		{"deluge", `deluge`},
		{"qbittorrent", `qbittorrent`},
		{"semaphore", `semaphore`},
		{"ansible", `ansible|awx`},
		{"wordpress", `wp-content|wordpress`},
		{"nginxproxymanager", `nginx proxy manager`},
		{"traefik", `traefik`},
		{"caddy", `server: caddy`},
		{"mariadb", `mariadb`},
		{"mysql", `mysql`},
		{"iis", `microsoft-iis`},
		{"nginx", `server: nginx`},
		{"apache", `server: apache`},
	}
	out := make([]appPattern, len(raw))
	for i, r := range raw {
		out[i] = appPattern{r[0], regexp.MustCompile(`(?i)` + r[1])}
	}
	return out
}()

// sshOS maps an SSH greeting to a distribution.
var sshOS = []appPattern{
	{"windows", regexp.MustCompile(`(?i)for_windows`)},
	{"ubuntu", regexp.MustCompile(`(?i)ubuntu`)},
	{"raspbian", regexp.MustCompile(`(?i)raspbian`)},
	{"debian", regexp.MustCompile(`(?i)debian`)},
	{"freebsd", regexp.MustCompile(`(?i)freebsd`)},
}

func matchKey(list []appPattern, text string) string {
	for _, p := range list {
		if p.re.MatchString(text) {
			return p.key
		}
	}
	return ""
}

// probeResult is what the outside view of a guest says.
type probeResult struct {
	App, AppVia string
	OS, OSVia   string
	at          time.Time
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]probeResult{}
)

// hostNets are the panel host's own addresses and the networks of its
// container and VM bridges; probing them could reach the host itself.
func hostNets() (addrs []netip.Addr, nets []netip.Prefix) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	virtual := regexp.MustCompile(`^(docker|br-|veth|incusbr|lxdbr|lxcbr|virbr|cni|flannel|podman|vnet|tap|fwbr|fwpr)`)
	for _, ifc := range ifs {
		as, _ := ifc.Addrs()
		for _, a := range as {
			p, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			addrs = append(addrs, p.Addr().Unmap())
			if virtual.MatchString(ifc.Name) {
				nets = append(nets, p.Masked())
			}
		}
	}
	return addrs, nets
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// probeable picks up to two guest addresses the panel may connect to.
func probeable(ips []string) []netip.Addr {
	own, nets := hostNets()
	var out []netip.Addr
	for _, s := range ips {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if !(a.IsPrivate() || cgnat.Contains(a)) || a.IsLoopback() || a.IsLinkLocalUnicast() {
			continue
		}
		skip := false
		for _, o := range own {
			skip = skip || o == a
		}
		for _, n := range nets {
			skip = skip || n.Contains(a)
		}
		if !skip {
			out = append(out, a)
		}
		if len(out) == 2 {
			break
		}
	}
	return out
}

// probeGuest looks at a guest from the outside, cached per guest and
// address set.
func probeGuest(ctx context.Context, key string, addrs []netip.Addr) probeResult {
	if len(addrs) == 0 {
		return probeResult{}
	}
	ck := key + "|" + fmt.Sprint(addrs)
	probeMu.Lock()
	if r, ok := probeCache[ck]; ok && time.Since(r.at) < probeTTL {
		probeMu.Unlock()
		return r
	}
	probeMu.Unlock()

	var (
		mu  sync.Mutex
		res probeResult
		wg  sync.WaitGroup
		web = map[string]string{} // via -> app key, by port order
	)
	for _, a := range addrs {
		host := a.String()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if line := readGreeting(ctx, host, 22); line != "" {
				if os := matchKey(sshOS, line); os != "" {
					mu.Lock()
					if res.OS == "" {
						res.OS, res.OSVia = os, "SSH greeting"
					}
					mu.Unlock()
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g := readGreeting(ctx, host, 3306); g != "" {
				if k := matchKey(appPatterns, g); k == "mariadb" || k == "mysql" {
					mu.Lock()
					web["mysql:3306"] = k
					mu.Unlock()
				}
			}
		}()
		for _, p := range probeHTTPSPorts {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				if k := probeHTTP(ctx, "https", host, p); k != "" {
					mu.Lock()
					web[fmt.Sprintf("https:%d", p)] = k
					mu.Unlock()
				}
			}(p)
		}
		for _, p := range probeHTTPPorts {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				if k := probeHTTP(ctx, "http", host, p); k != "" {
					mu.Lock()
					web[fmt.Sprintf("http:%d", p)] = k
					mu.Unlock()
				}
			}(p)
		}
	}
	wg.Wait()
	// The most specific product wins: the pattern list's order.
	best := len(appPatterns)
	for via, k := range web {
		for i, p := range appPatterns {
			if p.key == k && i < best {
				best, res.App, res.AppVia = i, k, via
			}
		}
	}
	res.at = time.Now()
	probeMu.Lock()
	probeCache[ck] = res
	probeMu.Unlock()
	return res
}

// readGreeting returns the first line a server sends on connect (SSH,
// MySQL), at most 256 bytes.
func readGreeting(ctx context.Context, host string, port int) string {
	d := net.Dialer{Timeout: probeTimeout}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return ""
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(probeTimeout))
	b := make([]byte, 256)
	n, _ := io.ReadAtLeast(c, b, 1)
	line, _, _ := strings.Cut(string(b[:n]), "\n")
	return line
}

// probeHTTP fetches / and matches the Server header, the page title and
// the start of the page against appPatterns. Redirects are followed only
// on the same address.
func probeHTTP(ctx context.Context, scheme, host string, port int) string {
	base := fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, fmt.Sprint(port)))
	cl := &http.Client{
		Timeout: 2 * probeTimeout,
		Transport: &http.Transport{
			// Only a banner is read and nothing is sent, so the guest's
			// (usually self-signed) certificate is not checked.
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			DialContext:         (&net.Dialer{Timeout: probeTimeout}).DialContext,
			TLSHandshakeTimeout: probeTimeout,
			DisableKeepAlives:   true,
			Proxy:               nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 2 || req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "servermgr-detect/1")
	resp, err := cl.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	var sb strings.Builder
	sb.WriteString("server: " + resp.Header.Get("Server") + "\n")
	sb.WriteString(resp.Header.Get("X-Powered-By") + "\n")
	sb.WriteString(resp.Request.URL.Path + "\n")
	sb.Write(body)
	return matchKey(appPatterns, sb.String())
}
