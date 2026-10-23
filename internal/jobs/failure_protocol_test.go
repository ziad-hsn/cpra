//go:build mysql || postgres

package jobs

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// This receiver returns real protocol errors to the production connection path.
// It neither replaces a driver transport nor claims database-account coverage.
func checkFailureReceiver(t *testing.T, handle func(net.Conn) error) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			err = handle(conn)
			_ = conn.Close()
			if err != nil {
				t.Errorf("protocol fixture: %v", err)
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String(), calls
}
