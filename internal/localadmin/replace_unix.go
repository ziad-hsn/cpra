//go:build !windows

package localadmin

import (
	"errors"
	"os"
	"path/filepath"
)

func replacePath(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
