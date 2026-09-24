package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func validationTestStream(t *testing.T, rows []collectionValidationLedgerRow) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(collectionValidationLedgerMagic)
	digest := sha256.New()
	var used uint64
	for _, row := range rows {
		raw, err := collectionValidationLedgerEncoding(row.OperationID, row.Item)
		if err != nil {
			t.Fatal(err)
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
		b.Write(size[:])
		b.Write(raw)
		digest.Write(size[:])
		digest.Write(raw)
		used += uint64(len(raw))
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], uint64(len(rows)))
	binary.BigEndian.PutUint64(footer[12:20], used)
	copy(footer[20:], digest.Sum(nil))
	b.Write(footer[:])
	return b.Bytes()
}
func TestCollectionValidationStreamFrozenRoundTripAndEmpty(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			op := ledgerTestOperation(1)
			items := validationLedgerItems(600)
			l := newLedgerTest(t, disk, 4<<20)
			appendValidationLedger(t, l, op, items[:300])
			v, err := l.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			appendValidationLedger(t, l, op, items[300:])
			var frozen bytes.Buffer
			n, err := writeCollectionValidationLedger(&frozen, v)
			if err != nil || n != int64(frozen.Len()) {
				t.Fatal(n, err)
			}
			target := newLedgerTest(t, disk, 4<<20)
			input := ledgerTestItem(1)
			if err := target.Append(op, input); err != nil {
				t.Fatal(err)
			}
			plans, _ := planLedgerSmall(t, op)
			appendPlanLedgerParts(t, target, op, plans)
			originalInput, originalPlan, _ := validationLedgerStreams(t, target)
			trailing := []byte("outer-snapshot-next-field")
			r := bytes.NewReader(append(bytes.Clone(frozen.Bytes()), trailing...))
			var txID int
			if disk {
				txID = ledgerTestTransactionID(t, target)
			}
			if err := importCollectionValidationLedger(r, target); err != nil {
				t.Fatal(err)
			}
			if disk && ledgerTestTransactionID(t, target)-txID != 2 {
				t.Fatal("import did not use bounded batched transactions")
			}
			remaining, _ := io.ReadAll(r)
			if !bytes.Equal(remaining, trailing) {
				t.Fatal("namespace importer consumed outer snapshot bytes")
			}
			page, err := target.ValidationPage(op, 0, 500)
			if err != nil || !reflect.DeepEqual(page, items[:300]) {
				t.Fatal("frozen snapshot changed", err)
			}
			afterInput, afterPlan, _ := validationLedgerStreams(t, target)
			if !bytes.Equal(originalInput, afterInput) || !bytes.Equal(originalPlan, afterPlan) {
				t.Fatal("import changed other namespaces")
			}
			if err := importCollectionValidationLedger(bytes.NewReader(frozen.Bytes()), target); !errors.Is(err, errCollectionLedgerCorrupt) {
				t.Fatal("merged into existing result namespace", err)
			}
			empty := newLedgerTest(t, disk, 1<<20)
			ev, err := empty.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer ev.Close()
			var encoded bytes.Buffer
			if _, err := writeCollectionValidationLedger(&encoded, ev); err != nil {
				t.Fatal(err)
			}
			fresh := newLedgerTest(t, disk, 1<<20)
			if err := importCollectionValidationLedger(&encoded, fresh); err != nil {
				t.Fatal("mandatory empty stream failed", err)
			}
			if count, size, err := fresh.ValidationStats(op); err != nil || count != 0 || size != 0 {
				t.Fatal(count, size, err)
			}
		})
	}
}
func TestCollectionValidationStreamLargeInventory(t *testing.T) {
	const count = 6000
	op := ledgerTestOperation(1)
	items := validationLedgerItems(count)
	for i := range items {
		items[i].Key.ID = strings.Repeat("m", 120) + fmt.Sprint(i)
		items[i].Change = "update"
		items[i].UID = strings.Repeat("u", 256)
		items[i].ResourceVersion = strings.Repeat("r", 256)
	}
	source := newLedgerTest(t, true, 16<<20)
	appendValidationLedger(t, source, op, items)
	v, err := source.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var encoded bytes.Buffer
	if _, err := writeCollectionValidationLedger(&encoded, v); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= 4<<20 {
		t.Fatal("fixture did not cross import byte ceiling")
	}
	target := newLedgerTest(t, true, 16<<20)
	before := ledgerTestTransactionID(t, target)
	if err := importCollectionValidationLedger(&encoded, target); err != nil {
		t.Fatal(err)
	}
	if got := ledgerTestTransactionID(t, target) - before; got != (count+255)/256 {
		t.Fatal("unexpected import transaction count", got)
	}
	if got, size, err := target.ValidationStats(op); err != nil || got != count || size != validationLedgerCost(t, op, items) {
		t.Fatal(got, size, err)
	}
	for after := uint64(0); after < count; {
		page, err := target.ValidationPage(op, after, 500)
		if err != nil || len(page) == 0 {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(page, items[after:after+uint64(len(page))]) {
			t.Fatal("large stream reordered or duplicated")
		}
		after = page[len(page)-1].Ordinal
	}
}
func TestCollectionValidationStreamRejectsCorruptionAndQuota(t *testing.T) {
	op := ledgerTestOperation(1)
	items := validationLedgerItems(600)
	rows := make([]collectionValidationLedgerRow, len(items))
	for i, item := range items {
		rows[i] = collectionValidationLedgerRow{OperationID: op, Item: item}
	}
	valid := validationTestStream(t, rows)
	for _, disk := range []bool{false, true} {
		for _, mode := range []string{"footer", "truncated", "magic", "oversize", "order", "noncanonical", "quota"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, mode), func(t *testing.T) {
				stream := bytes.Clone(valid)
				quota := int64(4 << 20)
				wantPrefix := uint64(0)
				switch mode {
				case "footer":
					stream[len(stream)-1] ^= 1
					wantPrefix = 512
				case "truncated":
					stream = stream[:len(stream)-1]
					wantPrefix = 512
				case "magic":
					stream[0] ^= 1
				case "oversize":
					binary.BigEndian.PutUint32(stream[len(collectionValidationLedgerMagic):], collectionValidationLedgerMaxFrame+1)
				case "order":
					changed := append([]collectionValidationLedgerRow(nil), rows...)
					changed[300].Item.Ordinal++
					stream = validationTestStream(t, changed)
					wantPrefix = 256
				case "noncanonical":
					offset := len(collectionValidationLedgerMagic) + 4
					stream[offset] = ' '
				case "quota":
					quota = validationLedgerCost(t, op, items[:300])
					wantPrefix = 256
				}
				target := newLedgerTest(t, disk, quota)
				err := importCollectionValidationLedger(bytes.NewReader(stream), target)
				if err == nil {
					t.Fatal("invalid stream passed")
				}
				if mode == "quota" && !errors.Is(err, errCollectionLedgerQuota) {
					t.Fatal(err)
				}
				count, _, statsErr := target.ValidationStats(op)
				if statsErr != nil || count != wantPrefix {
					t.Fatal("bad stream escaped bounded unpublished prefix", count, statsErr)
				}
			})
		}
	}
	// The stream integrity check is independent of the item allowlist and the
	// canonical wrapper check. Duplicate JSON fields must never be normalized.
	raw, _ := collectionValidationLedgerEncoding(op, items[0])
	raw = append([]byte(`{"operation_id":"duplicate",`), raw[1:]...)
	if _, err := decodeCollectionValidationLedgerRow(raw); !errors.Is(err, errCollectionLedgerCorrupt) {
		t.Fatal("duplicate wrapper accepted", err)
	}
}

type validationShortWriter struct{}

func (validationShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestCollectionValidationStreamWriterErrors(t *testing.T) {
	l := newLedgerTest(t, false, 1<<20)
	appendValidationLedger(t, l, ledgerTestOperation(1), validationLedgerItems(1))
	v, err := l.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionValidationLedger(validationShortWriter{}, v); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCollectionValidationLedger(io.Discard, v); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal(err)
	}
	if err := v.WalkValidation(context.Background(), func(string, CollectionValidationItem) error { return nil }); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal(err)
	}
}
