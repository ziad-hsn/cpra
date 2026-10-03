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
	// Rename within the pinned directory without replacing a raced-in target
	// or leaving an extra hard link after process termination.
	return unix.RenameatxNp(int(dir.Fd()), temporary, int(dir.Fd()), target, unix.RENAME_EXCL)
}
