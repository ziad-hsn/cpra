//go:build externaljobs && !windows

package worker

import (
	"errors"
	"os"
	"syscall"
)

func makePrivateDir(path string) error { return os.MkdirAll(path, 0700) }

func checkPrivate(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return errors.New("worker key/journal path has wrong file type")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("worker key/journal must deny group and other access")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("worker key/journal must belong to the service identity")
	}
	return nil
}
