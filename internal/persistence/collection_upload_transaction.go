package persistence

import (
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	MaxCollectionApplyItems = 64
	MaxCollectionApplyBytes = 1 << 20
)

// collectionUploadTransaction belongs to one contiguous bounded group in Apply.
// The ledger lock covers the transaction and its uniqueness/accounting reads.
// Each command keeps its own validation result; only its accepted prefix is
// synchronized. No observation or snapshot can see an unfinished transaction.
type collectionUploadTransaction struct {
	ledger   *collectionLedger
	view     collectionLedgerView
	tx       *bolt.Tx
	accepted int
}

func (l *collectionLedger) beginUploadTransaction() (*collectionUploadTransaction, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, errCollectionLedgerClosed
	}
	t := &collectionUploadTransaction{ledger: l}
	if l.db != nil {
		var err error
		t.tx, err = l.db.Begin(true)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		t.view.tx = t.tx
	} else {
		t.view.rows, t.view.keys = l.rows, l.keys
	}
	return t, nil
}

func (t *collectionUploadTransaction) append(operation string, item CollectionItem) error {
	encoded, err := collectionItemEncoding(operation, item)
	if err != nil {
		return err
	}
	if t.tx != nil {
		err = t.ledger.appendDisk(t.tx, operation, []CollectionItem{item}, [][]byte{encoded})
	} else {
		err = t.ledger.appendMemory(operation, []CollectionItem{item}, [][]byte{encoded})
	}
	if err == nil {
		t.accepted++
	}
	return err
}

func (t *collectionUploadTransaction) close(commit bool) error {
	defer t.ledger.mu.Unlock()
	if t.tx == nil {
		return nil
	}
	if commit && t.accepted != 0 {
		return t.tx.Commit() // NoSync remains false: one synchronous prefix commit.
	}
	return t.tx.Rollback()
}

func collectionUploadGroup(commands []Command) int {
	operation := commands[0].Collection.OperationID
	count, size := 0, 0
	for _, c := range commands {
		if c.Kind != "collection" || c.Collection.Action != "upload" || c.Collection.OperationID != operation || count == MaxCollectionApplyItems {
			break
		}
		// The envelope uses AES-GCM with a 16-byte authentication tag. The
		// application budget counts resource bytes, independently of the public
		// wire-upload and Store's separately enforced encoded-command ceiling.
		bytes := len(c.Collection.Item.Payload.Ciphertext) - 16
		if count != 0 && bytes > MaxCollectionApplyBytes-size {
			break
		}
		count++
		size += bytes
	}
	return count
}

func (f *machine) applyCollectionUploadGroup(commands []Command, format int) ([]Result, []Event, time.Time) {
	var results []Result
	var events []Event
	var latest time.Time
	if f.collections == nil {
		result := f.applyCollection(*commands[0].Collection, commands[0].At, format)
		return []Result{result}, result.Events, commands[0].At
	}
	transaction, err := f.collections.beginUploadTransaction()
	if err != nil {
		f.collectionStorageFailure(err)
		return nil, nil, latest
	}
	f.collectionUpload = transaction
	for _, c := range commands {
		result := f.applyCollection(*c.Collection, c.At, format)
		results = append(results, result)
		events = append(events, result.Events...)
		if c.At.After(latest) {
			latest = c.At
		}
		if f.err != nil {
			break
		}
	}
	f.collectionUpload = nil
	if err := transaction.close(f.err == nil); err != nil {
		f.collectionStorageFailure(err)
	}
	return results, events, latest
}

func (f *machine) collectionUploadItem(operation string, ordinal uint64) (CollectionItem, bool, error) {
	if f.collectionUpload != nil {
		return f.collectionUpload.view.item(operation, ordinal)
	}
	return f.collections.Item(operation, ordinal)
}

func (f *machine) collectionUploadFind(operation string, key CatalogKey) (uint64, bool, error) {
	if f.collectionUpload != nil {
		return f.collectionUpload.view.Find(operation, key)
	}
	return f.collections.Find(operation, key)
}

func (f *machine) appendCollectionUpload(operation string, item CollectionItem) error {
	if f.collectionUpload != nil {
		return f.collectionUpload.append(operation, item)
	}
	return f.collections.Append(operation, item)
}
