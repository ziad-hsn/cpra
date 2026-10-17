//go:build !windows

package jobs

import (
	"golang.org/x/sys/unix"
	"os"
)

func openCheckCertificate(path string) (*os.File, error) {
	// Certificate files may be read-only projections/symlinks. A FIFO must not
	// block before the regular-file check made on the opened handle.
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
