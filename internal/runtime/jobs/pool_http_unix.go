//go:build !windows

package jobs

import (
	"context"
	"net"
	"syscall"
	"time"
)

// UnixDialTimeout is the connection timeout for Unix systems.
// Unlike Windows (which has a 3-second kernel SYN timeout that blocks even
// when targets are unreachable), Unix/Linux fail fast on unreachable targets
// via immediate ICMP responses or RST packets. We can use a longer timeout.
const UnixDialTimeout = 10 * time.Second

// customFasthttpDialer is a shared dialer with SO_REUSEADDR for faster socket recycling.
// Uses DialContext for consistency with Windows implementation and proper cancellation.
var customFasthttpDialer = func(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), UnixDialTimeout)
	defer cancel()

	dialer := &net.Dialer{
		KeepAlive:     -1,    // Disable keep-alive for health checks
		DualStack:     false, // IPv4 only
		FallbackDelay: -1,    // Disable Happy Eyeballs
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
		},
	}
	return dialer.DialContext(ctx, "tcp", addr)
}
