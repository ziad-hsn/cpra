package durable

import (
	"io/fs"
	"path/filepath"
	"strings"
)

type DiskUsage struct {
	Bytes         int64 `json:"bytes"`
	HistoryBytes  int64 `json:"history_bytes"`
	SnapshotBytes int64 `json:"snapshot_bytes"`
	SnapshotFiles int   `json:"snapshot_files"`
	Available     bool  `json:"available"`
}

// Usage walks only the store's bounded file catalog on an HTTP goroutine.
// It does not hold the FSM or controller lock while obtaining file metadata.
func (s *Store) Usage() DiskUsage {
	u := DiskUsage{Available: true}
	if s.config.Storage.Mode == "memory" {
		return u
	}
	err := filepath.WalkDir(s.config.Storage.Directory, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		u.Bytes += info.Size()
		rel, _ := filepath.Rel(s.config.Storage.Directory, path)
		if strings.HasPrefix(rel, "history"+string(filepath.Separator)) {
			u.HistoryBytes += info.Size()
		}
		if strings.HasPrefix(rel, "snapshots"+string(filepath.Separator)) {
			u.SnapshotBytes += info.Size()
			if e.Name() == "state.bin" {
				u.SnapshotFiles++
			}
		}
		return nil
	})
	u.Available = err == nil
	return u
}
