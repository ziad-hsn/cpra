package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
	bolt "go.etcd.io/bbolt"
)

func ledgerTestOperation(n uint64) string {
	return operationHandle("11111111-1111-4111-8111-111111111111", n)
}

func ledgerTestItem(ordinal uint64) CollectionItem {
	return CollectionItem{Ordinal: ordinal, Key: CatalogKey{Kind: "Monitor", ID: fmt.Sprintf("monitor-%d", ordinal)},
		Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: ordinal, ContentDigest: strings.Repeat("1", 64),
		Payload: secureconfig.Envelope{Format: 1, KeyID: "test-key", WrappedKey: []byte("wrapped"), Nonce: make([]byte, 12), Ciphertext: bytes.Repeat([]byte{0x8b}, 32)}}
}

func newLedgerTest(t *testing.T, disk bool, quota int64) *collectionLedger {
	t.Helper()
	var l *collectionLedger
	var err error
	if disk {
		l, err = openCollectionLedger(filepath.Join(t.TempDir(), "collections"), quota)
	} else {
		l, err = newMemoryCollectionLedger(quota)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return l
}

func TestCollectionLedgerAppendIdentityAtomicityAndQuota(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			op := ledgerTestOperation(1)
			first, second := ledgerTestItem(1), ledgerTestItem(2)
			cost1, err := collectionItemCost(op, first)
			if err != nil {
				t.Fatal(err)
			}
			cost2, _ := collectionItemCost(op, second)
			l := newLedgerTest(t, disk, cost1+cost2)
			if err := l.Append(op, first); err != nil {
				t.Fatal(err)
			}
			first.Payload.Ciphertext[0] = 99
			got, found, err := l.Item(op, 1)
			if err != nil || !found || got.Payload.Ciphertext[0] != 0x8b {
				t.Fatalf("input ownership: %v %t", err, found)
			}
			got.Payload.Ciphertext[0] = 42
			original := ledgerTestItem(1)
			if err := l.AppendBatch(op, []CollectionItem{original, second}); err != nil {
				t.Fatal(err)
			}
			if err := l.Append(op, original); err != nil {
				t.Fatal("exact retry at quota:", err)
			}
			if size, _ := l.Bytes(); size != cost1+cost2 {
				t.Fatal("retry changed accounting:", size)
			}
			changed := original.Clone()
			changed.Payload.Ciphertext[0]++
			if err := l.Append(op, changed); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("re-encrypted retry:", err)
			}
			if err := l.Append(op, ledgerTestItem(3)); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("quota:", err)
			}
			if ordinal, ok, err := l.Find(op, second.Key); err != nil || !ok || ordinal != 2 {
				t.Fatal("index:", ordinal, ok, err)
			}
			if _, ok, err := l.Find(op, CatalogKey{Kind: "Monitor", ID: "absent"}); err != nil || ok {
				t.Fatal("absent index:", ok, err)
			}
			if page, err := l.Page(op, 1, 1); err != nil || len(page) != 1 || page[0].Ordinal != 2 {
				t.Fatal("page:", page, err)
			}
			if page, err := l.Page(op, math.MaxUint64, 1); err != nil || len(page) != 0 {
				t.Fatal("overflow page:", page, err)
			}
			for _, limit := range []int{0, -1, 501} {
				if _, err := l.Page(op, 0, limit); err == nil {
					t.Fatal("bad page limit accepted")
				}
			}

			atomic := newLedgerTest(t, disk, 1<<20)
			bad := ledgerTestItem(2)
			bad.Key = original.Key
			if err := atomic.AppendBatch(op, []CollectionItem{original, bad}); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("duplicate key:", err)
			}
			if n, _ := atomic.Bytes(); n != 0 {
				t.Fatal("rejected batch retained prefix")
			}
			if _, ok, _ := atomic.Item(op, 1); ok {
				t.Fatal("rejected batch retained row")
			}
			if err := atomic.Append(op, second); !errors.Is(err, errCollectionLedgerConflict) {
				t.Fatal("ordinal gap:", err)
			}

			quota := newLedgerTest(t, disk, cost1+cost2-1)
			if err := quota.AppendBatch(op, []CollectionItem{original, second}); !errors.Is(err, errCollectionLedgerQuota) {
				t.Fatal("batch quota:", err)
			}
			if n, _ := quota.Bytes(); n != 0 {
				t.Fatal("quota failure retained prefix")
			}
		})
	}
}

func TestCollectionLedgerFrozenViewsAndLogicalStream(t *testing.T) {
	var expected []byte
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 16<<20)
			for _, op := range []string{ledgerTestOperation(2), ledgerTestOperation(1)} {
				if err := l.AppendBatch(op, []CollectionItem{ledgerTestItem(1), ledgerTestItem(2)}); err != nil {
					t.Fatal(err)
				}
			}
			v, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if err := l.Append(ledgerTestOperation(1), ledgerTestItem(3)); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := v.Item(ledgerTestOperation(1), 3); err != nil || ok {
				t.Fatal("snapshot changed:", ok, err)
			}
			var order []string
			if err := v.Walk(func(op string, item CollectionItem) error {
				order = append(order, fmt.Sprintf("%s/%d", op, item.Ordinal))
				item.Payload.Ciphertext[0] = 3
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(order) != 4 || !strings.HasPrefix(order[0], ledgerTestOperation(1)) {
				t.Fatal("unstable walk:", order)
			}
			var stream bytes.Buffer
			written, err := v.WriteTo(&stream)
			if err != nil || written != int64(stream.Len()) {
				t.Fatal("export:", written, err)
			}
			if expected == nil {
				expected = bytes.Clone(stream.Bytes())
			} else if !bytes.Equal(expected, stream.Bytes()) {
				t.Fatal("memory/disk exports differ")
			}
			restored := newLedgerTest(t, disk, 16<<20)
			input := bytes.NewReader(append(bytes.Clone(stream.Bytes()), []byte("outer-footer")...))
			if err := importCollectionLedger(input, restored); err != nil {
				t.Fatal(err)
			}
			remaining, _ := io.ReadAll(input)
			if string(remaining) != "outer-footer" {
				t.Fatal("import consumed outer snapshot bytes")
			}
			if _, ok, err := restored.Item(ledgerTestOperation(1), 3); err != nil || ok {
				t.Fatal("restored future row")
			}
			got, ok, err := restored.Item(ledgerTestOperation(1), 1)
			if err != nil || !ok || got.Payload.Ciphertext[0] != 0x8b {
				t.Fatal("restored row mutated:", ok, err)
			}
			if err := importCollectionLedger(bytes.NewReader(stream.Bytes()), restored); err == nil {
				t.Fatal("import into nonempty generation")
			}
			if err := v.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := v.Item(ledgerTestOperation(1), 1); !errors.Is(err, errCollectionLedgerClosed) {
				t.Fatal("closed snapshot:", err)
			}
		})
	}
}

func ledgerTestStream(t *testing.T, rows ...collectionLedgerRow) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(collectionLedgerMagic)
	digest := sha256.New()
	var total uint64
	for _, row := range rows {
		data, err := collectionItemEncoding(row.OperationID, row.Item)
		if err != nil {
			t.Fatal(err)
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(data)))
		b.Write(size[:])
		b.Write(data)
		digest.Write(size[:])
		digest.Write(data)
		total += uint64(len(data))
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], uint64(len(rows)))
	binary.BigEndian.PutUint64(footer[12:20], total)
	copy(footer[20:], digest.Sum(nil))
	b.Write(footer[:])
	return b.Bytes()
}

func TestCollectionLedgerImportRejectsCorruptionAndBounds(t *testing.T) {
	one := collectionLedgerRow{OperationID: ledgerTestOperation(1), Item: ledgerTestItem(1)}
	two := collectionLedgerRow{OperationID: ledgerTestOperation(1), Item: ledgerTestItem(2)}
	good := ledgerTestStream(t, one, two)
	corruptFooter := bytes.Clone(good)
	corruptFooter[len(corruptFooter)-1] ^= 1
	oversized := append(bytes.Clone(collectionLedgerMagic), []byte{0, 0x20, 0, 1}...)
	wrongMagic := bytes.Clone(good)
	wrongMagic[0] = '!'
	duplicateID := two
	duplicateID.Item.Key = one.Item.Key
	cases := map[string][]byte{
		"wrong magic": wrongMagic, "truncated": good[:len(good)-1], "footer": corruptFooter,
		"frame bound": oversized, "duplicate row": ledgerTestStream(t, one, one), "gap": ledgerTestStream(t, two),
		"reorder": ledgerTestStream(t, two, one), "duplicate identity": ledgerTestStream(t, one, duplicateID),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			l := newLedgerTest(t, true, 1<<20)
			if err := importCollectionLedger(bytes.NewReader(data), l); err == nil {
				t.Fatal("corrupt stream accepted")
			}
			if _, err := os.Stat(filepath.Join(l.directory, "current.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial import selected a generation")
			}
		})
	}
	cost, _ := collectionItemCost(one.OperationID, one.Item)
	quota := newLedgerTest(t, false, cost-1)
	if err := importCollectionLedger(bytes.NewReader(good), quota); !errors.Is(err, errCollectionLedgerQuota) {
		t.Fatal("import quota:", err)
	}
	if used, _ := quota.Bytes(); used != 0 {
		t.Fatal("oversized first frame was retained")
	}
}

func TestCollectionLedgerGenerationNeverReusesAheadState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "collections")
	old, err := openCollectionLedger(directory, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = old.AppendBatch(ledgerTestOperation(1), []CollectionItem{ledgerTestItem(1), ledgerTestItem(2)}); err != nil {
		t.Fatal(err)
	}
	if err = old.Publish(); err != nil {
		t.Fatal(err)
	}
	priorSelection, _ := os.ReadFile(filepath.Join(directory, "current.json"))
	priorPath := filepath.Join(directory, old.generation, "ledger.db")
	priorBytes, _ := os.ReadFile(priorPath)
	recovered, err := openCollectionLedger(directory, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, found, err := recovered.Item(ledgerTestOperation(1), 1); err != nil || found {
		t.Fatal("replay consulted prior generation")
	}
	if err = recovered.Append(ledgerTestOperation(1), ledgerTestItem(1)); err != nil {
		t.Fatal(err)
	}
	selection, _ := os.ReadFile(filepath.Join(directory, "current.json"))
	if !bytes.Equal(selection, priorSelection) {
		t.Fatal("unpublished recovery replaced selection")
	}
	if err = recovered.Publish(); err != nil {
		t.Fatal(err)
	}
	selection, _ = os.ReadFile(filepath.Join(directory, "current.json"))
	var got collectionLedgerSelection
	if json.Unmarshal(selection, &got) != nil || got.Generation != recovered.generation {
		t.Fatal("recovered generation was not selected")
	}
	currentPriorBytes, _ := os.ReadFile(priorPath)
	if !bytes.Equal(priorBytes, currentPriorBytes) {
		t.Fatal("previous generation changed during reconstruction")
	}
	if _, found, _ := recovered.Item(ledgerTestOperation(1), 2); found {
		t.Fatal("ahead row survived fresh replay")
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Publish(); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("closed publish:", err)
	}
}

func TestCollectionLedgerRejectsIndexAndMetadataCorruption(t *testing.T) {
	for _, kind := range []string{"missing-key", "orphan-key", "byte-accounting", "row-sequence", "format", "noncanonical-row"} {
		t.Run(kind, func(t *testing.T) {
			l := newLedgerTest(t, true, 16<<20)
			op, item := ledgerTestOperation(1), ledgerTestItem(1)
			if err := l.Append(op, item); err != nil {
				t.Fatal(err)
			}
			err := l.db.Update(func(tx *bolt.Tx) error {
				rows := tx.Bucket(collectionLedgerRecords).Bucket([]byte(op))
				keys := tx.Bucket(collectionLedgerKeys).Bucket([]byte(op))
				meta := tx.Bucket(collectionLedgerMeta)
				switch kind {
				case "missing-key":
					return keys.Delete([]byte(item.Key.indexKey()))
				case "orphan-key":
					return keys.Put([]byte("Monitor\x00orphan"), collectionOrdinal(1))
				case "byte-accounting":
					return meta.Put([]byte("bytes"), collectionOrdinal(1))
				case "row-sequence":
					return rows.SetSequence(2)
				case "format":
					return meta.Put([]byte("format"), collectionOrdinal(2))
				case "noncanonical-row":
					data := append(bytes.Clone(rows.Get(collectionOrdinal(1))), ' ')
					return rows.Put(collectionOrdinal(1), data)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			v, err := l.Freeze()
			if err == nil {
				defer v.Close()
				err = v.Walk(func(string, CollectionItem) error { return nil })
			}
			if !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("corruption accepted:", err)
			}
		})
	}
}

func TestCollectionLedgerConcurrentFrozenReads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			l := newLedgerTest(t, disk, 16<<20)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := uint64(1); i <= 100; i++ {
					if err := l.Append(ledgerTestOperation(1), ledgerTestItem(i)); err != nil {
						t.Error(err)
						return
					}
				}
			}()
			for i := 0; i < 20; i++ {
				v, err := l.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				var first, second bytes.Buffer
				_, err = v.WriteTo(&first)
				if err == nil {
					_, err = v.WriteTo(&second)
				}
				if err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
					t.Error("concurrent snapshot changed:", err)
				}
				if err := v.Close(); err != nil {
					t.Error(err)
				}
			}
			wg.Wait()
		})
	}
}

type boundedLedgerWriter struct {
	w   io.Writer
	max int
}

func (w *boundedLedgerWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		w.max = len(p)
	}
	return w.w.Write(p)
}

// This qualifies the ledger stream above the browser's 64 MiB source budget.
// It does not claim collection graph validation or activation at that size.
func TestCollectionLedgerLargeStream(t *testing.T) {
	if testing.Short() {
		t.Skip("large encrypted ledger stream fixture")
	}
	l := newLedgerTest(t, true, 256<<20)
	op := ledgerTestOperation(1)
	for ordinal := uint64(1); ordinal <= 64; ordinal++ {
		item := ledgerTestItem(ordinal)
		item.Payload.Ciphertext = bytes.Repeat([]byte{byte(ordinal)}, 1<<20)
		if err := l.Append(op, item); err != nil {
			t.Fatal(err)
		}
	}
	view, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	f, err := os.CreateTemp(t.TempDir(), "snapshot-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &boundedLedgerWriter{w: f}
	written, err := view.WriteTo(w)
	if err != nil || written <= 64<<20 || w.max > collectionLedgerMaxFrame {
		t.Fatal("unbounded or incomplete large stream:", written, w.max, err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	restored := newLedgerTest(t, true, 256<<20)
	beforeImport := ledgerTestTransactionID(t, restored)
	if err = importCollectionLedger(f, restored); err != nil {
		t.Fatal(err)
	}
	// Each canonical row is slightly larger than 4 MiB/3, so the byte limit
	// permits two rows per transaction regardless of the 256-row count limit.
	if transactions := ledgerTestTransactionID(t, restored) - beforeImport; transactions != 32 {
		t.Fatal("large-byte import did not use bounded two-row transactions:", transactions)
	}
	wantBytes, _ := l.Bytes()
	gotBytes, _ := restored.Bytes()
	if gotBytes != wantBytes {
		t.Fatal("large stream accounting mismatch:", gotBytes, wantBytes)
	}
	page, err := restored.Page(op, 63, 1)
	if err != nil || len(page) != 1 || page[0].Ordinal != 64 || len(page[0].Payload.Ciphertext) != 1<<20 {
		t.Fatal("large stream did not recover final row:", err)
	}
}

func TestCollectionLedgerRejectsUnsafeSelectionsAndInputs(t *testing.T) {
	for _, value := range []string{
		`{"version":2,"generation":"generation-11111111-1111-4111-8111-111111111111"}`,
		`{"version":1,"generation":"../escape"}`,
		`{"version":1,"generation":"generation-11111111-1111-4111-8111-111111111111","future":true}`,
		`{"version":1,"generation":"generation-11111111-1111-4111-8111-111111111111"} {}`,
	} {
		directory := filepath.Join(t.TempDir(), "collections")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "current.json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if l, err := openCollectionLedger(directory, 1<<20); err == nil {
			l.Close()
			t.Fatal("invalid generation selector accepted")
		}
		files, _ := os.ReadDir(directory)
		if len(files) != 1 {
			t.Fatal("rejected selector created a recovery generation")
		}
	}
	for _, disk := range []bool{false, true} {
		l := newLedgerTest(t, disk, 1<<20)
		invalid := ledgerTestItem(1)
		invalid.Source = "https://example.invalid/?secret=never-store"
		if err := l.Append(ledgerTestOperation(1), invalid); err == nil {
			t.Fatal("private URL accepted as opaque source token")
		}
		if err := l.Append("foreign-operation", ledgerTestItem(1)); err == nil {
			t.Fatal("invalid operation accepted")
		}
		if n, _ := l.Bytes(); n != 0 {
			t.Fatal("invalid input stored")
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if err := l.Append(ledgerTestOperation(1), ledgerTestItem(1)); !errors.Is(err, errCollectionLedgerClosed) {
			t.Fatal("closed append:", err)
		}
	}
}

func TestCollectionLedgerMissingSelectedMaterializationCanRebuild(t *testing.T) {
	for _, removeGeneration := range []bool{false, true} {
		t.Run(fmt.Sprintf("generation=%t", removeGeneration), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "collections")
			old, err := openCollectionLedger(directory, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err = old.Append(ledgerTestOperation(1), ledgerTestItem(1)); err != nil {
				t.Fatal(err)
			}
			if err = old.Publish(); err != nil {
				t.Fatal(err)
			}
			if err = old.Close(); err != nil {
				t.Fatal(err)
			}
			priorSelection, err := os.ReadFile(filepath.Join(directory, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			if removeGeneration {
				err = os.RemoveAll(filepath.Join(directory, old.generation))
			} else {
				err = os.Remove(filepath.Join(directory, old.generation, "ledger.db"))
			}
			if err != nil {
				t.Fatal(err)
			}
			rebuilt, err := openCollectionLedger(directory, 1<<20)
			if err != nil {
				t.Fatal("missing cache prevented fresh authoritative replay:", err)
			}
			defer rebuilt.Close()
			selection, _ := os.ReadFile(filepath.Join(directory, "current.json"))
			if !bytes.Equal(priorSelection, selection) {
				t.Fatal("opening fresh generation replaced selector prematurely")
			}
			if err = rebuilt.Append(ledgerTestOperation(1), ledgerTestItem(1)); err != nil {
				t.Fatal(err)
			}
			if err = rebuilt.Publish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
