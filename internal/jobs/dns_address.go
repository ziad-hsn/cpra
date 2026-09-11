package jobs

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// dnsServerAddress accepts a host with an optional numeric port. Bare IPv6
// addresses use port 53; an IPv6 address with a port must be bracketed.
func dnsServerAddress(server string) (string, error) {
	if ip, err := netip.ParseAddr(server); err == nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}
	if strings.HasPrefix(server, "[") && strings.HasSuffix(server, "]") {
		if ip, err := netip.ParseAddr(server[1 : len(server)-1]); err == nil && ip.Is6() {
			return net.JoinHostPort(ip.String(), "53"), nil
		}
	}
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		if server != "" && !strings.ContainsAny(server, ":[]/\\ \t\r\n") {
			return net.JoinHostPort(server, "53"), nil
		}
		return "", fmt.Errorf("DNS server requires a host or host:port; bracket IPv6 when specifying a port")
	}
	n, err := strconv.Atoi(port)
	if host == "" || strings.ContainsAny(host, "/\\ \t\r\n") || err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("DNS server requires a nonempty host and a port between 1 and 65535")
	}
	return net.JoinHostPort(host, port), nil
}
