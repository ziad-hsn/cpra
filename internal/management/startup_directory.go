package management

import (
	"errors"
	"os"
	"path/filepath"
)

func createStartupDirectory(path string) (bool, error) {
	if !filepath.IsAbs(path) {
		return false, ErrValidation
	}
	if _, err := os.Lstat(path); err == nil {
		return false, checkStartupDirectory(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, ErrStageUnavailable
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, checkStartupDirectory(path)
		}
		return false, ErrStageUnavailable
	}
	// No staging data is written until native permissions are established. A
	// failure leaves the empty directory for explicit inspection, never repair.
	if err := protectNewStartupDirectory(path); err != nil {
		return true, err
	}
	if err := syncStartupDirectoryEntry(path); err != nil {
		return true, err
	}
	return true, nil
}

func checkStartupDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return ErrValidation
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStageUnavailable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !sameStartupPath(path, resolved) {
		return ErrStageUnavailable
	}
	return checkStartupDirectoryAccess(path, info)
}
