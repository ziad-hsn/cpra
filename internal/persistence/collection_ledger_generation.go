package persistence

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type collectionLedgerSelection struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
}

// openCollectionLedger always starts a fresh generation. The directory is the
// collection materialization directory, not the main Raft directory. Its caller
// must hold the main Raft ownership lock for this generation's entire lifetime.
// Prior generations remain untouched, including when reconstruction fails.
func openCollectionLedger(directory string, maxBytes int64) (_ *collectionLedger, err error) {
	if maxBytes < 1 || maxBytes > maxCollectionLedgerBytes {
		return nil, errCollectionLedgerQuota
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("collection ledger requires an absolute clean directory")
	}
	if err = os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("collection ledger requires a private real directory")
	}
	if err = validateCollectionLedgerSelection(directory); err != nil {
		return nil, err
	}
	generation := "generation-" + uuid.NewString()
	path := filepath.Join(directory, generation)
	if err = os.Mkdir(path, 0700); err != nil {
		return nil, err
	}
	// bbolt holds a read lock on the mmap throughout a snapshot transaction.
	// Reserve virtual address space for the bounded logical quota and indexes so
	// ordinary concurrent appends do not require remapping beneath that reader.
	// Windows may also preallocate this mapped size; it must be included in native
	// filesystem qualification. This is not a total RSS/physical disk limit.
	mmap := maxBytes*2 + 16<<20
	if mmap > int64(^uint(0)>>1) {
		mmap = int64(^uint(0) >> 1)
	}
	db, err := bolt.Open(filepath.Join(path, "ledger.db"), 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: int(mmap)})
	if err != nil {
		return nil, err
	}
	l := &collectionLedger{db: db, directory: directory, generation: generation, maxBytes: maxBytes}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{collectionLedgerRecords, collectionLedgerKeys, collectionLedgerMeta, collectionLedgerPlans, collectionLedgerPlanMeta, collectionLedgerValidation, collectionLedgerValidationMeta, collectionLedgerExecution, collectionLedgerExecutionMeta} {
			if _, e := tx.CreateBucket(name); e != nil {
				return e
			}
		}
		meta := tx.Bucket(collectionLedgerMeta)
		if e := meta.Put([]byte("format"), collectionOrdinal(collectionLedgerFormat)); e != nil {
			return e
		}
		if e := meta.Put([]byte("plan_bytes"), collectionOrdinal(0)); e != nil {
			return e
		}
		if e := meta.Put([]byte("validation_bytes"), collectionOrdinal(0)); e != nil {
			return e
		}
		if e := meta.Put([]byte("execution_bytes"), collectionOrdinal(0)); e != nil {
			return e
		}
		return meta.Put([]byte("bytes"), collectionOrdinal(0))
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

func validateCollectionLedgerSelection(directory string) error {
	path := filepath.Join(directory, "current.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return errCollectionLedgerCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	var selection collectionLedgerSelection
	if err = d.Decode(&selection); err != nil {
		return errCollectionLedgerCorrupt
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errCollectionLedgerCorrupt
	}
	if selection.Version != collectionLedgerFormat || len(selection.Generation) != len("generation-")+36 {
		return errCollectionLedgerCorrupt
	}
	id := selection.Generation[len("generation-"):]
	u, err := uuid.Parse(id)
	if selection.Generation != "generation-"+id || err != nil || u == uuid.Nil || u.String() != id {
		return errCollectionLedgerCorrupt
	}
	for _, target := range []string{filepath.Join(directory, selection.Generation), filepath.Join(directory, selection.Generation, "ledger.db")} {
		info, err = os.Lstat(target)
		// This selector is diagnostic metadata for a materialization, not a
		// source of truth. Missing old cache files cannot invalidate complete
		// authoritative snapshots/logs used to reconstruct the new generation.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errCollectionLedgerCorrupt
		}
		if target == filepath.Join(directory, selection.Generation) && !info.IsDir() || target != filepath.Join(directory, selection.Generation) && !info.Mode().IsRegular() {
			return errCollectionLedgerCorrupt
		}
	}
	return nil
}

// Publish selects the fully recovered generation. Call only after snapshot
// validation and committed log replay have both completed successfully. It never
// removes old generations or authoritative logs/snapshots. GC is separate work.
func (l *collectionLedger) Publish() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db == nil {
		return nil
	}
	if err := l.db.Sync(); err != nil {
		return err
	}
	selection := collectionLedgerSelection{Version: collectionLedgerFormat, Generation: l.generation}
	// Reuse the platform's durable replacement primitive to also synchronize
	// the generation directory containing ledger.db before selecting its name.
	if err := atomicJSON(filepath.Join(l.directory, l.generation, "generation.json"), selection); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(l.directory, "current.json"), selection)
}
