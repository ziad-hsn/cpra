package httpserver

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func loopbackHost(host string) bool {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name = strings.Trim(host, "[]")
	} else if port == "" {
		return false
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false
	}
	return strings.EqualFold(name, "localhost") || name == "127.0.0.1" || name == "::1"
}

func loopbackRequest(r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(remote).IsLoopback() || !loopbackHost(r.Host) {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && origin.Scheme == scheme && origin.User == nil &&
		origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" && loopbackHost(origin.Host) && strings.EqualFold(origin.Host, r.Host)
}

// LoopbackHandler protects unauthenticated local diagnostics from Host/origin
// rebinding. Binding the listener to loopback alone does not prove browser origin.
func LoopbackHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "loopback request required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
