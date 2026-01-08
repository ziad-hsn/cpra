//go:build windows

package jobs

import (
	"context"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// WindowsDialTimeout is the connection timeout for Windows.
// Set to 2 seconds to beat Windows kernel's 3-second SYN timeout.
// This allows Go to cancel the connection attempt BEFORE the kernel
// starts its retry cycle (which can take 3-21 seconds for unreachable hosts).
//
// Research confirms: Go's DialContext with context cancellation will close
// the socket, which aborts pending SYN attempts in the Windows kernel.
const WindowsDialTimeout = 2 * time.Second

// customFasthttpDialer for Windows with context-aware fast-fail timeout.
// Uses DialContext internally to enable proper cancellation before kernel SYN timeout.
var customFasthttpDialer = func(addr string) (net.Conn, error) {
	// Create a context with timeout shorter than Windows kernel SYN timeout (3s)
	// This allows the dial to be cancelled and socket closed before kernel retries
	ctx, cancel := context.WithTimeout(context.Background(), WindowsDialTimeout)
	defer cancel()

	dialer := &net.Dialer{
		// Note: Don't set Timeout here - use context deadline instead
		// Go issue #70751 documents that Dialer.Timeout causes slowdowns on Windows
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
	return dialer.DialContext(ctx, "tcp", addr)
}
