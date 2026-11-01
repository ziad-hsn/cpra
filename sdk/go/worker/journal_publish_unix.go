//go:build externaljobs && !windows

package worker

import (
	"errors"
	"os"
	"path/filepath"
)

func publishJournalBackup(temporary, path string) error {
	// Link publishes without replacing an existing backup. Both names are in the
	// same private directory; the file contents were synchronized before linking.
	if err := os.Link(temporary, path); err != nil {
		return err
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
