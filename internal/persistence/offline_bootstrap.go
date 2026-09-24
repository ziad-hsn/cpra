package persistence

import (
	"encoding/binary"
	"fmt"

	"github.com/hashicorp/go-msgpack/v2/codec"
	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

// validateOfflineBootstrap reconstructs committed application state without
// opening Raft or a persistent HistoryStore. Snapshot metadata, rather than the
// application's last command index, identifies the first log to replay: Raft
// may have included subsequent configuration or no-op entries in its snapshot.
// The caller has already validated every snapshot and retained history segment.
func validateOfflineBootstrap(db *bolt.DB, snapshot *image, snapshotIndex, committed uint64) error {
	return validateOfflineBootstrapWithRestore(db, snapshot, snapshotIndex, committed, nil)
}

func validateOfflineBootstrapWithRestore(db *bolt.DB, snapshot *image, snapshotIndex, committed uint64, marker *RestoreMarker) error {
	return validateOfflineBootstrapWithCollections(db, snapshot, snapshotIndex, committed, marker, nil, "")
}

func validateOfflineBootstrapWithCollections(db *bolt.DB, snapshot *image, snapshotIndex, committed uint64, marker *RestoreMarker, collections *collectionLedger, collectionDirectory string) error {
	history := &HistoryStore{catalog: historyCatalog{Version: FormatVersion, Index: committed}}
	f := &machine{image: image{Version: FormatVersion, Monitors: make(map[string]Monitor)}, history: history, collections: collections, collectionDirectory: collectionDirectory}
	defer func() {
		if f.collections != nil && f.collections != collections {
			_ = f.collections.Close()
		}
	}()
	if snapshot != nil {
		if snapshot.Index > snapshotIndex || snapshot.Index > committed {
			return fmt.Errorf("snapshot exceeds committed application state; restore a complete backup")
		}
		f.image = *snapshot
		recovery, err := recoverCollectionExecution(f.image, collections)
		if err != nil {
			return err
		}
		f.installCollectionExecutionRecovery(recovery)
		f.rebuildOperationIndex()
		f.rebuildCatalogIndexes()
	}
	if committed > snapshotIndex {
		err := db.View(func(tx *bolt.Tx) error {
			logs := tx.Bucket([]byte("logs"))
			if logs == nil {
				return fmt.Errorf("missing Raft log bucket")
			}
			cursor := logs.Cursor()
			next := snapshotIndex + 1
			var first [8]byte
			binary.BigEndian.PutUint64(first[:], next)
			key, value := cursor.Seek(first[:])
			for {
				if len(key) != 8 || binary.BigEndian.Uint64(key) != next {
					return fmt.Errorf("committed Raft log index %d is missing; restore a complete backup", next)
				}
				var entry raft.Log
				if err := codec.NewDecoderBytes(value, &codec.MsgpackHandle{}).Decode(&entry); err != nil || entry.Index != next {
					return fmt.Errorf("corrupt committed Raft log at index %d", next)
				}
				if entry.Type == raft.LogCommand {
					// Apply preserves rejected command results. In particular an
					// attempted activation is not proof that activation succeeded.
					// history.append skips every replayed index at this fixed
					// watermark, so no replayed history is written or retained.
					if err, failed := f.Apply(&entry).(error); failed {
						return fmt.Errorf("offline committed-state validation: %w", err)
					}
				}
				if next == committed {
					return nil
				}
				next++
				key, value = cursor.Next()
			}
		})
		if err != nil {
			return err
		}
	}
	if f.image.Index != committed {
		return fmt.Errorf("committed application state does not reach history watermark; restore a complete backup")
	}
	if collectionFormat(f.image.Version) {
		view, err := f.collectionSnapshotView()
		if err != nil {
			return err
		}
		err = validateCollectionRows(f.image, view)
		closeErr := view.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if f.bootstrapPending() {
		return ErrBootstrapPending
	}
	if marker != nil {
		if f.image.Restore == nil || f.image.Restore.Marker != *marker || f.restorePending() {
			return ErrRestorePending
		}
	} else if f.image.Restore != nil {
		return ErrRestoreInvalid
	}
	return nil
}
