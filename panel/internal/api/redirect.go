package api

import (
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
)

// Plain HTTP only answers callers on the panel host itself (the deploy
// health check, OpenTofu's state backend); everyone else, agents included,
// is sent to HTTPS.

var redirectHost = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}\.?|\[[0-9A-Fa-f:.]+\])$`)

// HTTPSRedirect sends browsers on plain HTTP to the same page on the HTTPS
// port. A 308 keeps the method and body, so an API call lands as it was sent.
func HTTPSRedirect(next http.Handler, httpsPort string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keepHTTP(r) {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
			if strings.Contains(h, ":") {
				host = "[" + h + "]"
			}
		}
		if !redirectHost.MatchString(host) {
			http.Error(w, "use https", http.StatusBadRequest)
			return
		}
		if httpsPort != "443" {
			host += ":" + httpsPort
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

func keepHTTP(r *http.Request) bool {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && ap.Addr().Unmap().IsLoopback() {
		return true
	}
	return false
}
