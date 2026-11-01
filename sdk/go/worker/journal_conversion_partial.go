//go:build externaljobs

package worker

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrConversionIncomplete means an encrypted partial backup needs stopped
// operator inspection. Conversion preserves it and the original journal and
// refuses to allocate another backup. A matching filename is not deletion authority.
var ErrConversionIncomplete = errors.New("encrypted partial journal backup exists; preserve the original journal and inspect the stopped conversion before retrying")

func (j *journal) checkConversionPartials() error {
	if err := j.checkIdentity(); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	defer directory.Close()
	seen := 0
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			seen++
			if seen > 1024 {
				return ErrCapacity
			}
			if !strings.HasPrefix(entry.Name(), "worker.format2-") || !strings.Contains(entry.Name(), ".db.partial-") {
				continue
			}
			prefix := "worker.format2-" + j.meta.ID + ".db.partial-"
			suffix := strings.TrimPrefix(entry.Name(), prefix)
			if !strings.HasPrefix(entry.Name(), prefix) || len(suffix) != 32 {
				return ErrIdentity
			}
			if _, err := hex.DecodeString(suffix); err != nil {
				return ErrIdentity
			}
			path := filepath.Join(filepath.Dir(j.path), entry.Name())
			if err := rejectSymlinks(path); err != nil {
				return err
			}
			before, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !before.Mode().IsRegular() {
				return ErrIdentity
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			opened, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil || closeErr != nil {
				return errors.Join(statErr, closeErr)
			}
			after, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !os.SameFile(before, opened) || !os.SameFile(opened, after) {
				return ErrIdentity
			}
			if err := checkPrivate(path, false); err != nil {
				return err
			}
			if err := j.checkIdentity(); err != nil {
				return err
			}
			// Truncation prevents authenticating arbitrary partial content. Do not
			// publish, delete, or copy it merely because its name matches this journal.
			return ErrConversionIncomplete
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
