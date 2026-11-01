//go:build externaljobs

package worker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

// ConvertJournal upgrades a stopped format-2 journal to format 3. It acquires
// the same exclusive bbolt lock as the runner, authenticates all retained data,
// and saves a synchronized, encrypted format-2 backup before changing anything.
// No protocol client, credentials resolver or handler is called. Publication is
// one synchronous bbolt transaction: interruption retains either the complete
// old journal or the complete validated replacement, never mixed AAD versions.
//
// Config needs only identities, StateDir, WrappingKeyPath and optional Limits.
// Conversion is deliberately explicit; New never upgrades a journal.
func ConvertJournal(ctx context.Context, config Config) (backupPath string, err error) {
	if ctx == nil || !validID(config.ServerID) || !validID(config.WorkerID) || !validID(config.WorkerUID) {
		return "", ErrIdentity
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	config.Limits, err = config.Limits.defaults()
	if err != nil {
		return "", err
	}
	j, err := openJournalContext(ctx, config, 2, true)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, j.close()) }()
	if err = j.checkConversionPartials(); err != nil {
		return "", err
	}
	info, err := os.Stat(j.path)
	if err != nil {
		return "", err
	}
	if info.Size() > config.Limits.AllocatedBytes || j.accounting.records > config.Limits.Records || j.accounting.liveBytes+j.accounting.reservedBytes > config.Limits.LiveBytes {
		return "", ErrCapacity
	}
	backupPath = filepath.Join(config.StateDir, "worker.format2-"+j.meta.ID+".db")
	if err = rejectSymlinks(backupPath); err != nil {
		return "", err
	}
	if _, err = os.Lstat(backupPath); err == nil {
		return backupPath, errors.New("original journal backup already exists; retain it and verify the stopped conversion state before retrying")
	} else if !os.IsNotExist(err) {
		return backupPath, err
	}
	temporary := backupPath + ".partial-" + randomID()
	backup, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create original worker journal backup: %w", err)
	}
	defer os.Remove(temporary)
	err = j.db.View(func(tx *bolt.Tx) error {
		_, e := tx.WriteTo(&journalBackupWriter{ctx: ctx, file: backup, remaining: config.Limits.AllocatedBytes})
		return e
	})
	if err == nil {
		err = backup.Sync()
	}
	err = errors.Join(err, backup.Close())
	if err != nil {
		return backupPath, err
	}
	if err = publishJournalBackup(temporary, backupPath); err != nil {
		return backupPath, err
	}
	if err = j.checkIdentity(); err != nil {
		return backupPath, err
	}
	if err = checkPrivate(backupPath, false); err != nil {
		return backupPath, err
	}
	if err = ctx.Err(); err != nil {
		return backupPath, err
	}
	// Source keys remain unchanged in j for decoding. The replacement has fresh
	// data-key epochs and format-3 AAD; no plaintext outcome is written to disk.
	dst := &journal{meta: j.meta, wrapping: j.wrapping, keys: make(map[string]cipher.AEAD), limits: j.limits}
	dst.meta.Format = 3
	err = j.db.Update(func(tx *bolt.Tx) error {
		staged, err := tx.CreateBucket([]byte("conversion-epochs"))
		if err != nil {
			return ErrCorrupt
		}
		rotate := func() error {
			key := make([]byte, 32)
			if _, e := rand.Read(key); e != nil {
				return e
			}
			defer clear(key)
			block, e := aes.NewCipher(key)
			if e != nil {
				return e
			}
			a, e := cipher.NewGCM(block)
			if e != nil {
				return e
			}
			dst.epoch = randomID()
			dst.keys[dst.epoch] = a
			dst.encryptions = 0
			wrapped, e := seal(dst.wrapping, key, dst.aad("epoch", dst.epoch))
			if e != nil {
				return e
			}
			return staged.Put([]byte(dst.epoch), wrapped)
		}
		if err = rotate(); err != nil {
			return err
		}
		bucket := tx.Bucket(recordsBucket)
		cursor := bucket.Cursor()
		key, value := cursor.First()
		count := 0
		var accounted journalAccounting
		for key != nil {
			if err = ctx.Err(); err != nil {
				return err
			}
			id := string(key)
			record, e := j.decode(id, value)
			if e != nil {
				return e
			}
			count++
			accounted = accounted.replace(journalAccounting{}, recordAccounting(record, j.limits))
			if count > config.Limits.Records || accounted.liveBytes+accounted.reservedBytes > config.Limits.LiveBytes {
				return ErrCapacity
			}
			if dst.encryptions >= 65536 {
				if e = rotate(); e != nil {
					return e
				}
			}
			encoded, e := dst.encode(record)
			if e != nil {
				return e
			}
			if _, e = dst.decode(id, encoded); e != nil {
				return e
			}
			// Copy the continuation before Put invalidates cursor-owned slices.
			next, _ := cursor.Next()
			next = append([]byte(nil), next...)
			if e = bucket.Put([]byte(id), encoded); e != nil {
				return e
			}
			if next == nil {
				break
			}
			cursor = bucket.Cursor()
			key, value = cursor.Seek(next)
		}
		if count != j.accounting.records {
			return ErrCorrupt
		}
		if err = bucket.ForEach(func(k, v []byte) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			_, e := dst.decode(string(k), v)
			return e
		}); err != nil {
			return err
		}
		if err = tx.DeleteBucket(epochsBucket); err != nil {
			return err
		}
		epochs, err := tx.CreateBucket(epochsBucket)
		if err != nil {
			return err
		}
		if err = staged.ForEach(func(k, v []byte) error { return epochs.Put(k, v) }); err != nil {
			return err
		}
		if err = tx.DeleteBucket([]byte("conversion-epochs")); err != nil {
			return err
		}
		identity, err := json.Marshal(dst.meta)
		if err != nil {
			return err
		}
		proof, err := seal(dst.wrapping, []byte("cpra-worker-key-check"), dst.aad("wrapping-key", "identity"))
		if err != nil {
			return err
		}
		meta := tx.Bucket(metadataBucket)
		if err = meta.Put([]byte("key-check"), proof); err != nil {
			return err
		}
		if err = meta.Put([]byte("identity"), identity); err != nil {
			return err
		}
		// bbolt spills and grows after this callback. Allocation is not a hard
		// completion-write cap; never confuse tx.Size here with post-commit size.
		// Source allocation, record count and authenticated live/reserved bytes
		// bound the conversion. Operators must provide space for the synchronized
		// backup and bbolt copy-on-write growth as documented.
		if err = j.checkIdentity(); err != nil {
			return err
		}
		return ctx.Err()
	})
	return backupPath, err
}

type journalBackupWriter struct {
	ctx       context.Context
	file      *os.File
	remaining int64
}

func (w *journalBackupWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrCapacity
	}
	n, err := w.file.Write(p)
	w.remaining -= int64(n)
	return n, err
}
