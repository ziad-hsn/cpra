//go:build !windows

package persistence

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errSnapshotPermissions = errors.New("snapshot path must be a private, owned directory or single-link regular file")

func createSnapshotDirectory(path string) error {
	err := os.Mkdir(path, 0700)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func checkSnapshotDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func protectSnapshotPath(path string, directory bool) error {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	mode := os.FileMode(0600)
	if directory {
		flags |= unix.O_DIRECTORY
		mode = 0700
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return err
	}
	if info.Uid != uint32(os.Geteuid()) || directory && info.Mode&unix.S_IFMT != unix.S_IFDIR ||
		!directory && (info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1) {
		return errSnapshotPermissions
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return snapshotAccessACL(file)
}
