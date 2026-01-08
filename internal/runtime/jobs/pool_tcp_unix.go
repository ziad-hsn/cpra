//go:build !windows

package jobs

import (
	"net"
	"sync"
	"syscall"
)

var (
	tcpDialer     *net.Dialer
	tcpDialerOnce sync.Once
)

// getTCPDialer returns a Unix-optimized TCP dialer.
// Unix/Linux don't have Windows' 3-second kernel SYN timeout issue -
// unreachable targets fail fast via ICMP or RST. We use 10s timeout.
func getTCPDialer() *net.Dialer {
	tcpDialerOnce.Do(func() {
		tcpDialer = &net.Dialer{
			Timeout:       UnixDialTimeout, // 10s - Unix fails fast on unreachable
			KeepAlive:     -1,              // Disable keep-alive for health checks
			DualStack:     false,           // IPv4 only
			FallbackDelay: -1,              // Disable Happy Eyeballs
			Control: func(network, address string, c syscall.RawConn) error {
				return c.Control(func(fd uintptr) {
					_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				})
			},
		}
	})
	return tcpDialer
}
