package jobs

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
)

// SSRFProtect is set at startup, before any jobs run. It protects outbound
// HTTP destinations; trusted manifests may intentionally monitor private IPs.
var SSRFProtect bool

func validateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid HTTP URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("HTTP URL requires an http/https scheme and host")
	}
	if SSRFProtect {
		if ip := net.ParseIP(u.Hostname()); ip != nil && isNonPublic(ip) {
			return fmt.Errorf("non-public HTTP destination blocked")
		}
	}
	return nil
}

// protectedDial resolves once, validates every answer and dials an IP literal.
// The URL hostname is retained by net/http for Host and TLS certificate checks.
func protectedDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("HTTP destination has no addresses")
	}
	for _, ip := range ips {
		if isNonPublic(ip.IP) {
			return nil, fmt.Errorf("non-public HTTP destination blocked")
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func isNonPublic(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return true
		}
	}
	return false
}
