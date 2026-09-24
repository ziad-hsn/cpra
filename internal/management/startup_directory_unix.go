//go:build !windows

package management

import (
	"os"
	"path/filepath"
)

func sameStartupPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func checkStartupDirectoryAccess(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return ErrStageUnavailable
	}
	return nil
}

func protectNewStartupDirectory(path string) error { return checkStartupDirectory(path) }

func syncStartupDirectoryEntry(path string) error {
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrStageUnavailable
	}
	err = parent.Sync()
	closeErr := parent.Close()
	if err != nil || closeErr != nil {
		return ErrStageUnavailable
	}
	return nil
}
