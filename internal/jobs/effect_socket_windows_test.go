package jobs

import (
	"net"
	"strconv"
	"syscall"
	"testing"
)

func refusedTCPAddress(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Closesocket(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	address, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(address.(*syscall.SockaddrInet4).Port))
}
