package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/btree"
	bolt "go.etcd.io/bbolt"
)

var (
	operationTerminalBucket = []byte("operation-terminals")
	operationMonitorBucket  = []byte("operation-terminals-by-monitor")
	operationIndexMeta      = []byte("operation-index")
)

const operationIndexBatch = 256

func terminalOperation(r OperationReceipt) bool {
	return r.State == "completed" || r.State == "failed" || r.State == "partial"
}

type operationMemoryItem struct {
	key   string
	event Event
}

func newOperationMemoryTree() *btree.BTreeG[operationMemoryItem] {
	return btree.NewG(32, func(a, b operationMemoryItem) bool { return a.key < b.key })
}
func terminalMonitorKey(e Event) string {
	if e.Operation.Key.Kind != "Monitor" {
		return ""
	}
	return e.Operation.Key.ID + "\x00" + e.ID
}
func (h *HistoryStore) ensureOperationMemoryIndexes() {
	if h.memoryOperationTree != nil {
		return
	}
	h.memoryOperationTree, h.memoryMonitorOperations = newOperationMemoryTree(), newOperationMemoryTree()
	for _, e := range h.memoryOperations {
		if terminalOperation(*e.Operation) {
			h.insertMemoryTerminal(e)
		}
	}
}
func (h *HistoryStore) insertMemoryTerminal(e Event) {
	h.memoryOperationTree.ReplaceOrInsert(operationMemoryItem{key: e.ID, event: e})
	if key := terminalMonitorKey(e); key != "" {
		h.memoryMonitorOperations.ReplaceOrInsert(operationMemoryItem{key: key, event: e})
	}
}
func (h *HistoryStore) indexMemoryOperation(e Event) {
	h.ensureOperationMemoryIndexes()
	if old, ok := h.memoryOperations[e.Operation.ID]; ok {
		h.unindexMemoryOperation(e.Operation.ID, old)
	}
	if terminalOperation(*e.Operation) {
		h.insertMemoryTerminal(e)
	}
}
func (h *HistoryStore) unindexMemoryOperation(_ string, e Event) {
	if h.memoryOperationTree == nil || !terminalOperation(*e.Operation) {
		return
	}
	h.memoryOperationTree.Delete(operationMemoryItem{key: e.ID})
	if key := terminalMonitorKey(e); key != "" {
		h.memoryMonitorOperations.Delete(operationMemoryItem{key: key})
	}
}
func operationIndexReady(tx *bolt.Tx) (bool, error) {
	meta := tx.Bucket(operationIndexMeta)
	if meta == nil {
		return false, nil
	}
	if !bytes.Equal(meta.Get([]byte("version")), []byte{1}) || tx.Bucket(operationTerminalBucket) == nil || tx.Bucket(operationMonitorBucket) == nil {
		return false, ErrHistoryUnavailable
	}
	done := meta.Get([]byte("complete"))
	after := meta.Get([]byte("after"))
	if len(done) != 1 || done[0] > 1 || len(after) > 1024 || done[0] == 1 && len(after) != 0 {
		return false, ErrHistoryUnavailable
	}
	return done[0] == 1, nil
}
func ensureOperationIndex(tx *bolt.Tx) error {
	if tx.Bucket(operationIndexMeta) != nil {
		_, err := operationIndexReady(tx)
		return err
	}
	if _, err := tx.CreateBucket(operationTerminalBucket); err != nil {
		return err
	}
	if _, err := tx.CreateBucket(operationMonitorBucket); err != nil {
		return err
	}
	meta, err := tx.CreateBucket(operationIndexMeta)
	if err != nil {
		return err
	}
	if err := meta.Put([]byte("version"), []byte{1}); err != nil {
		return err
	}
	done := byte(0)
	if events := tx.Bucket([]byte("events")); events == nil || events.Stats().KeyN == 0 {
		done = 1
	}
	return meta.Put([]byte("complete"), []byte{done})
}
func writeOperationMonitorIndex(tx *bolt.Tx, e Event) error {
	if err := ensureOperationIndex(tx); err != nil {
		return err
	}
	if !terminalOperation(*e.Operation) {
		return nil
	}
	primary := []byte(e.MonitorID + "\x00" + e.ID)
	if err := tx.Bucket(operationTerminalBucket).Put([]byte(e.ID), primary); err != nil {
		return err
	}
	if key := terminalMonitorKey(e); key != "" {
		return tx.Bucket(operationMonitorBucket).Put([]byte(key), primary)
	}
	return nil
}
func migrateOperationIndexPage(ctx context.Context, db *bolt.DB) (bool, error) {
	done := false
	err := db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ensureOperationIndex(tx); err != nil {
			return err
		}
		ready, err := operationIndexReady(tx)
		if err != nil {
			return err
		}
		if ready {
			done = true
			return nil
		}
		meta, events := tx.Bucket(operationIndexMeta), tx.Bucket([]byte("events"))
		if events == nil {
			return ErrHistoryUnavailable
		}
		after := bytes.Clone(meta.Get([]byte("after")))
		if len(after) > 0 && events.Get(after) == nil {
			return ErrHistoryUnavailable
		}
		cur := events.Cursor()
		k, raw := cur.Seek(after)
		if bytes.Equal(k, after) {
			k, raw = cur.Next()
		}
		count := 0
		for ; k != nil && count < operationIndexBatch; k, raw = cur.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(k) > 1024 || len(raw) > maxHistoryEventBytes {
				return ErrHistoryUnavailable
			}
			var e Event
			if json.Unmarshal(raw, &e) != nil || !validHistoryPosition(e.ID) || string(k) != e.MonitorID+"\x00"+e.ID {
				return ErrHistoryUnavailable
			}
			if e.Operation != nil {
				if len(raw) > maxOperationEventBytes || validateOperationEvent(e) != nil {
					return ErrHistoryUnavailable
				}
				if err := writeOperationMonitorIndex(tx, e); err != nil {
					return err
				}
			}
			if err := meta.Put([]byte("after"), k); err != nil {
				return err
			}
			count++
		}
		if k == nil {
			if err := meta.Delete([]byte("after")); err != nil {
				return err
			}
			if err := meta.Put([]byte("complete"), []byte{1}); err != nil {
				return err
			}
			done = true
		}
		return nil
	})
	return done, err
}
func (h *HistoryStore) migrateOperationIndexes(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.err != nil {
		return ErrHistoryUnavailable
	}
	if h.dir == "" {
		h.ensureOperationMemoryIndexes()
		return nil
	}
	days := make([]string, 0, len(h.databases))
	for day := range h.databases {
		days = append(days, day)
	}
	slices.Sort(days)
	for _, day := range days {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready := false
		if err := h.databases[day].View(func(tx *bolt.Tx) error { var err error; ready, err = operationIndexReady(tx); return err }); err != nil {
			return err
		}
		if ready {
			continue
		}
		for {
			done, err := migrateOperationIndexPage(ctx, h.databases[day])
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
		if err := h.databases[day].View(func(tx *bolt.Tx) error { return validateOperationMonitorIndexContext(ctx, tx) }); err != nil {
			return err
		}
	}
	return nil
}
func validateOperationMonitorIndex(tx *bolt.Tx) error {
	return validateOperationMonitorIndexContext(context.Background(), tx)
}

func validateOperationMonitorIndexContext(ctx context.Context, tx *bolt.Tx) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ready, err := operationIndexReady(tx)
	if err != nil || !ready {
		return err
	}
	events := tx.Bucket([]byte("events"))
	for _, name := range [][]byte{operationTerminalBucket, operationMonitorBucket} {
		if err := tx.Bucket(name).ForEach(func(k, primary []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if events == nil || len(k) > 1024 || len(primary) > 1024 {
				return ErrHistoryUnavailable
			}
			raw := events.Get(primary)
			if len(raw) > maxOperationEventBytes {
				return ErrHistoryUnavailable
			}
			var e Event
			if json.Unmarshal(raw, &e) != nil || validateOperationEvent(e) != nil || !terminalOperation(*e.Operation) || string(primary) != e.MonitorID+"\x00"+e.ID {
				return ErrHistoryUnavailable
			}
			want := e.ID
			if bytes.Equal(name, operationMonitorBucket) {
				want = terminalMonitorKey(e)
			}
			if want == "" || string(k) != want {
				return ErrHistoryUnavailable
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if events == nil {
		return nil
	}
	return events.ForEach(func(primary, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(primary) > 1024 || len(raw) > maxHistoryEventBytes {
			return ErrHistoryUnavailable
		}
		var e Event
		if json.Unmarshal(raw, &e) != nil {
			return ErrHistoryUnavailable
		}
		if e.Operation == nil || !terminalOperation(*e.Operation) {
			return nil
		}
		if len(raw) > maxOperationEventBytes {
			return ErrHistoryUnavailable
		}
		if !bytes.Equal(tx.Bucket(operationTerminalBucket).Get([]byte(e.ID)), primary) {
			return ErrHistoryUnavailable
		}
		if key := terminalMonitorKey(e); key != "" && !bytes.Equal(tx.Bucket(operationMonitorBucket).Get([]byte(key)), primary) {
			return ErrHistoryUnavailable
		}
		return nil
	})
}
func isLegacyOperation(id string) bool { return !strings.HasPrefix(id, "op.") }
