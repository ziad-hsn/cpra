//go:build windows

package jobs

import (
	"net"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	tcpDialer     *net.Dialer
	tcpDialerOnce sync.Once
)

// getTCPDialer returns a Windows-optimized TCP dialer.
// The timeout is set to 2 seconds to beat Windows kernel's 3-second SYN timeout.
// When used with DialContext (which DialTCP does), context cancellation will
// close the socket and abort pending kernel SYN attempts.
func getTCPDialer() *net.Dialer {
	tcpDialerOnce.Do(func() {
		tcpDialer = &net.Dialer{
			// 2s timeout beats Windows kernel's 3s SYN timeout
			// This ensures DialContext can cancel before kernel retry cycle
			Timeout:       WindowsDialTimeout,
			KeepAlive:     -1,    // Disable keep-alive for health checks
			DualStack:     false, // IPv4 only
			FallbackDelay: -1,    // Disable Happy Eyeballs
			Control: func(network, address string, c syscall.RawConn) error {
				var sockErr error
				ctrlErr := c.Control(func(fd uintptr) {
					sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
				})
				if ctrlErr != nil {
					return ctrlErr
				}
				return sockErr
			},
		}
	})
	return tcpDialer
}
