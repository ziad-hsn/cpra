package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/go-msgpack/v2/codec"
	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

// OfflineLock holds the runtime's exclusive database lock without constructing
// Raft, replaying a state machine, expiring history, or invoking a provider.
type OfflineLock struct {
	db     *bolt.DB
	NodeID string
}

func (l *OfflineLock) Close() error { return l.db.Close() }

// LockOffline validates an existing complete store and prevents the runtime
// opening it while a backup is copied. The caller must retain it until copying
// finishes. This never creates an absent raft.db or bootstraps a store.
func LockOffline(dir string) (_ *OfflineLock, err error) {
	for _, path := range []string{dir, filepath.Join(dir, "history")} {
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("state requires real directories: %s", path)
		}
	}
	for _, name := range []string{"identity.json", "raft.db", "history/catalog.json"} {
		info, e := os.Lstat(filepath.Join(dir, name))
		if e != nil {
			return nil, fmt.Errorf("incomplete state (%s): %w", name, e)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("state file must be regular: %s", name)
		}
	}
	db, err := bolt.Open(filepath.Join(dir, "raft.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("stop CPRa before offline state operations (lock/corruption): %w", err)
	}
	l := &OfflineLock{db: db}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	var identity nodeIdentity
	if err = readOfflineJSON(filepath.Join(dir, "identity.json"), &identity); err != nil {
		return nil, err
	}
	if identity.Version != FormatVersion || !identity.Initialized {
		return nil, fmt.Errorf("incompatible or uninitialized node identity")
	}
	if _, err = uuid.Parse(identity.ID); err != nil {
		return nil, fmt.Errorf("invalid node identity: %w", err)
	}
	l.NodeID = identity.ID
	if err = checkOfflineDB(db); err != nil {
		return nil, err
	}
	err = db.View(func(tx *bolt.Tx) error {
		logs := tx.Bucket([]byte("logs"))
		if logs == nil || tx.Bucket([]byte("conf")) == nil {
			return fmt.Errorf("missing Raft buckets")
		}
		return logs.ForEach(func(_, value []byte) error {
			var entry raft.Log
			if e := codec.NewDecoderBytes(value, &codec.MsgpackHandle{}).Decode(&entry); e != nil {
				return fmt.Errorf("corrupt Raft log: %w", e)
			}
			if entry.Type != raft.LogCommand {
				return nil
			}
			var batch envelope
			if e := json.Unmarshal(entry.Data, &batch); e != nil {
				return e
			}
			if batch.Version != FormatVersion {
				return fmt.Errorf("incompatible log format at %d", entry.Index)
			}
			for _, command := range batch.Commands {
				if e := validateCommand(command); e != nil {
					return e
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	var catalog historyCatalog
	if err = readOfflineJSON(filepath.Join(dir, "history", "catalog.json"), &catalog); err != nil {
		return nil, err
	}
	if catalog.Version != FormatVersion || catalog.Segments == nil {
		return nil, fmt.Errorf("incompatible history catalog")
	}
	for day, active := range catalog.Segments {
		if _, err = time.Parse("2006-01-02", day); err != nil {
			return nil, fmt.Errorf("invalid history segment: %w", err)
		}
		if !active {
			continue
		}
		segment := filepath.Join(dir, "history", day+".db")
		info, e := os.Lstat(segment)
		if e != nil {
			return nil, fmt.Errorf("history segment missing: %w", e)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("history segment is not regular")
		}
		h, e := bolt.Open(segment, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
		if e != nil {
			return nil, e
		}
		err = errors.Join(checkOfflineDB(h), h.Close())
		if err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(filepath.Join(dir, "snapshots"))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("snapshots must be a real directory")
	}
	snapshots, err := raft.NewFileSnapshotStore(dir, 3, io.Discard)
	if err != nil {
		return nil, err
	}
	list, err := snapshots.List()
	if err != nil {
		return nil, err
	}
	for _, meta := range list {
		_, reader, e := snapshots.Open(meta.ID)
		if e != nil {
			return nil, e
		}
		state, e := decodeImage(reader)
		err = errors.Join(e, reader.Close())
		if err != nil {
			return nil, err
		}
		if catalog.Index < state.Index {
			return nil, fmt.Errorf("history catalog precedes snapshot; restore a complete backup")
		}
	}
	return l, nil
}

func checkOfflineDB(db *bolt.DB) error {
	return db.View(func(tx *bolt.Tx) error {
		var result error
		// Drain Check's channel even after the first error; its producer uses tx.
		for err := range tx.Check() {
			result = errors.Join(result, err)
		}
		return result
	})
}

func readOfflineJSON(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON in %s", filepath.Base(path))
	}
	return nil
}
