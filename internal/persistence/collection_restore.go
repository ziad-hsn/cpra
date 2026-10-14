//go:build linux || darwin || windows

package persistence

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
)

// restoreCollectionRetirement runs only during explicit restore, with offline
// ownership of the authenticated private copy. Native identities cannot travel
// with a backup. The original intent proves admission; destination handles prove
// which remaining objects can be removed. Ordinary startup never uses this path.
func restoreCollectionRetirement(directory, nodeID string) error {
	directory = filepath.Join(directory, "collections")
	if _, err := os.Lstat(filepath.Join(directory, collectionGCIntentTemporary)); !errors.Is(err, os.ErrNotExist) {
		return errCollectionGCBlocked
	}
	if _, err := os.Lstat(filepath.Join(directory, collectionGCIntentName)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	g := &collectionGenerations{directory: directory}
	if err := g.openRetirement(); err != nil {
		return err
	}
	defer g.close()
	r := g.retirement
	// Retain exact bytes and destination identities until the catalog commit.
	// Replacing any of these proofs during preparation aborts publication.
	proofs := make(map[string]collectionRestoreFile, 3)
	for _, name := range []string{collectionGCIntentName, collectionGenerationCatalogName, "current.json"} {
		file, err := readCollectionRestoreFile(r, name)
		if err != nil {
			return err
		}
		proofs[name] = file
	}
	if err := decodeCollectionGenerationJSON(proofs[collectionGenerationCatalogName].raw, &g.catalog); err != nil {
		return err
	}
	if err := g.catalog.validate(nodeID); err != nil {
		return err
	}
	p := g.protection()
	var selected collectionLedgerSelection
	var intent collectionGCIntent
	if !collectionGCName(p.Current) || !collectionGCName(p.Previous) || p.Current == p.Previous ||
		collectionGCDecode(proofs["current.json"].raw, &selected) != nil || selected.Version != collectionLedgerFormat || selected.Generation != p.Current ||
		collectionGCDecode(proofs[collectionGCIntentName].raw, &intent) != nil ||
		!collectionGCValidIdentity(intent.Root) || !intent.valid(intent.Root, p) || intent.Database == intent.Root || intent.Marker == intent.Root ||
		intent.Generation == p.Current || intent.Generation == p.Previous {
		return errCollectionGCBlocked
	}
	protected := make(map[collectionGCIdentity]bool, 6)
	for _, name := range []string{p.Current, p.Previous} {
		entry, err := r.openCandidate(name, false)
		if err != nil {
			return err
		}
		protected[entry.identity], protected[entry.database], protected[entry.marker] = true, true, true
		entry.close()
	}
	entry, err := r.openCandidate(intent.Generation, true)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listed := slices.Contains(g.catalog.Completed, intent.Generation)
	if !listed && (entry != nil || g.catalog.Retired == 0) || listed && g.catalog.Retired == math.MaxUint64 {
		if entry != nil {
			entry.close()
		}
		return errCollectionGCBlocked
	}
	checkpoint := func() error {
		identity, err := collectionGCNative(r.dir, true)
		visible, visibleErr := os.Lstat(directory)
		anchored, statErr := r.dir.Stat()
		if err != nil || identity != r.identity || visibleErr != nil || statErr != nil || !os.SameFile(visible, anchored) {
			return errCollectionGCBlocked
		}
		for name, expected := range proofs {
			actual, err := readCollectionRestoreFile(r, name)
			if err != nil || actual.identity != expected.identity || !bytes.Equal(actual.raw, expected.raw) {
				return errCollectionGCBlocked
			}
		}
		return nil
	}
	if entry != nil {
		defer entry.close()
		if protected[entry.identity] || protected[entry.database] || protected[entry.marker] ||
			entry.markerInfo != nil && entry.markerHash != intent.MarkerDigest {
			return errCollectionGCBlocked
		}
		for _, leaf := range []string{"ledger.db", "generation.json"} {
			info, identity := entry.dbInfo, entry.database
			if leaf == "generation.json" {
				info, identity = entry.markerInfo, entry.marker
			}
			if info == nil {
				continue // An admitted retirement removes database, marker, then directory.
			}
			if info.Size() > math.MaxInt64-g.catalog.UnlinkedBytes {
				return errCollectionGCBlocked
			}
			if err := checkpoint(); err != nil {
				return err
			}
			file, current, actual, err := collectionGCOpenFile(entry.root, leaf, r.identity)
			if file != nil {
				_ = file.Close()
			}
			if err != nil || actual != identity || !os.SameFile(info, current) {
				return errCollectionGCBlocked
			}
			if err := collectionGCRemove(entry.root, entry.dir, leaf, identity, false); err != nil {
				return err
			}
			if err := collectionGCDirectoryCheckpoint(entry.dir); err != nil {
				return err
			}
			g.catalog.UnlinkedBytes += info.Size()
		}
		if err := checkpoint(); err != nil {
			return err
		}
		visible, err := r.root.Lstat(intent.Generation)
		anchored, statErr := entry.dir.Stat()
		if err != nil || statErr != nil || !os.SameFile(visible, anchored) {
			return errCollectionGCBlocked
		}
		if err := collectionGCRemove(r.root, r.dir, intent.Generation, entry.identity, true); err != nil {
			return err
		}
		if err := collectionGCDirectoryCheckpoint(r.dir); err != nil {
			return err
		}
	}
	if err := checkpoint(); err != nil {
		return err
	}
	// This is idempotent if the source stopped after recording retirement but
	// before removing the intent. An absent candidate is justified only by the
	// authenticated original intent, never by a missing directory alone.
	if err := p.RecordRetired(intent.Generation); err != nil {
		return err
	}
	delete(proofs, collectionGenerationCatalogName)
	if err := checkpoint(); err != nil {
		return err
	}
	if err := collectionGCRemove(r.root, r.dir, collectionGCIntentName, proofs[collectionGCIntentName].identity, false); err != nil {
		return err
	}
	return collectionGCDirectoryCheckpoint(r.dir)
}

type collectionRestoreFile struct {
	raw      []byte
	identity collectionGCIdentity
}

func readCollectionRestoreFile(r *collectionRetirement, name string) (collectionRestoreFile, error) {
	file, info, identity, err := collectionGCOpenFile(r.root, name, r.identity)
	if err != nil {
		return collectionRestoreFile{}, err
	}
	defer file.Close()
	limit := int64(4096)
	if name == collectionGenerationCatalogName {
		limit = 64 << 10
	}
	if info.Size() < 1 || info.Size() > limit {
		return collectionRestoreFile{}, errCollectionGCBlocked
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) != info.Size() {
		return collectionRestoreFile{}, errCollectionGCBlocked
	}
	return collectionRestoreFile{raw: raw, identity: identity}, nil
}
