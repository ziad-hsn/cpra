package secureconfig

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishKeyFile(root *os.Root, temporary, target string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := publishKeyName(dir, temporary, target); err != nil {
		return err
	}
	return dir.Sync()
}

func publishKeyName(dir *os.File, temporary, target string) error {
	// Unlike link/unlink, this does not leave an extra hard link if the process
	// dies after publication. Both names are relative to the pinned directory.
	// Kernels/filesystems without RENAME_NOREPLACE return an explicit error;
	// there is deliberately no racy existence-check/overwrite fallback.
	return unix.Renameat2(int(dir.Fd()), temporary, int(dir.Fd()), target, unix.RENAME_NOREPLACE)
}
