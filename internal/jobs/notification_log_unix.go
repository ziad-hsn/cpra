//go:build !windows

package jobs

import (
	"os"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"golang.org/x/sys/unix"
)

func openNotificationLogFile(root *os.Root, name string) (*os.File, error) {
	// Nonblocking open prevents a FIFO from hanging the worker before fstat.
	return openNotificationLogEntry(root, name, unix.O_CREAT|unix.O_APPEND|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
}

func checkNotificationLogFile(f *os.File) error {
	if err := secureconfig.CheckPrivateFile(f); err != nil {
		return errNotificationLog
	}
	return nil
}

func openNotificationLogDirectory(root *os.Root, name string) (*os.File, error) {
	return openNotificationLogEntry(root, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

// name is one component. os.Root.OpenFile resolves ELOOP symlinks even when
// O_NOFOLLOW is supplied, so acquire the child directly from the held parent.
func openNotificationLogEntry(root *os.Root, name string, flags int, mode uint32) (*os.File, error) {
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func checkNotificationLogDirectory(file *os.File, private bool) error {
	var err error
	if private {
		err = secureconfig.CheckPrivateDirectory(file)
	} else {
		err = secureconfig.CheckTrustedDirectory(file)
	}
	if err != nil {
		if private {
			return errNotificationLogDirectoryAccess
		}
		return errNotificationLog
	}
	return nil
}
