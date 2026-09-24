package management

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// StartCollectionReselectionManager selects process-owned staging from the
// caller's opened store mode. dataDirectory is the native store's configured absolute
// directory. Memory stores use a separate private OS temporary directory.
func StartCollectionReselectionManager(ctx context.Context, catalog *Catalog, mode, dataDirectory string, now func() time.Time) (*CollectionReselectionManager, error) {
	if ctx == nil || catalog == nil || now == nil {
		return nil, ErrValidation
	}
	if err := catalog.readyContext(ctx); err != nil {
		return nil, err
	}
	switch mode {
	case "raft":
		if !filepath.IsAbs(dataDirectory) {
			return nil, ErrValidation
		}
		return NewCollectionReselectionManager(ctx, catalog, dataDirectory, now)
	case "memory":
		parent, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil || !filepath.IsAbs(parent) {
			return nil, errReselectionSpool
		}
		directory, err := os.MkdirTemp(parent, "cpra-encrypted-reselection-")
		if err != nil {
			return nil, errReselectionSpool
		}
		if err := protectNewStartupDirectory(directory); err != nil {
			_ = os.Remove(directory)
			return nil, errReselectionSpool
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() {
			return nil, errReselectionSpool
		}
		cleanup := func(root *reselectionSpoolRoot) error {
			return removeReselectionRuntimeDirectory(directory, info, root)
		}
		manager, err := newCollectionReselectionManager(ctx, catalog, directory, now, cleanup)
		if err != nil {
			// A constructor failure does not authorize removing an unknown or
			// incomplete ownership database. Remove only an empty private parent.
			if current, statErr := os.Lstat(directory); statErr == nil && os.SameFile(info, current) {
				_ = os.Remove(directory)
			}
			return nil, err
		}
		return manager, nil
	default:
		return nil, ErrValidation
	}
}

// Cleanup runs after every borrower and the spool OS lock have closed. Remove
// only the captured empty root and ownership file; unexpected entries or
// replacements remain untouched for stopped inspection.
func removeReselectionRuntimeDirectory(directory string, info os.FileInfo, root *reselectionSpoolRoot) error {
	current, err := os.Lstat(directory)
	if err != nil || info == nil || !current.IsDir() || !os.SameFile(info, current) || root == nil || root.directory != filepath.Join(directory, reselectionRootDirectory) {
		return errReselectionSpool
	}
	for _, check := range []struct {
		path string
		info os.FileInfo
	}{{root.directory, root.directoryInfo}, {filepath.Join(root.directory, reselectionOwnershipFile), root.ownershipInfo}} {
		current, err := os.Lstat(check.path)
		if err != nil || check.info == nil || !os.SameFile(check.info, current) {
			return errReselectionSpool
		}
	}
	for _, check := range []struct{ directory, child string }{{directory, reselectionRootDirectory}, {root.directory, reselectionOwnershipFile}} {
		file, err := os.Open(check.directory)
		if err != nil {
			return errReselectionSpool
		}
		entries, readErr := file.ReadDir(2)
		closeErr := file.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(entries) != 1 || entries[0].Name() != check.child {
			return errReselectionSpool
		}
	}
	for _, path := range []string{filepath.Join(root.directory, reselectionOwnershipFile), root.directory, directory} {
		if err := os.Remove(path); err != nil {
			return errReselectionSpool
		}
	}
	return nil
}
