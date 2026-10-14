//go:build !linux && !darwin && !windows

package persistence

import (
	"errors"
	"os"
	"path/filepath"
)

func restoreCollectionRetirement(directory, _ string) error {
	for _, name := range []string{"gc-retirement.json", collectionGCIntentTemporary} {
		if _, err := os.Lstat(filepath.Join(directory, "collections", name)); !errors.Is(err, os.ErrNotExist) {
			return errCollectionGCUnavailable
		}
	}
	return nil
}
