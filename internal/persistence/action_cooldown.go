package persistence

import (
	"bytes"
	"context"
	bolt "go.etcd.io/bbolt"
	"strings"
	"time"
)

func rejectedAt(a Action) time.Time {
	if a.Kind != "intervention" || a.State != Unknown || a.Held() || a.Review == nil || a.Review.Resolution != "rejected" {
		return time.Time{}
	}
	at := a.Review.At
	for _, v := range []time.Time{a.FinishedAt, a.ExecutorFinishedAt} {
		if v.After(at) {
			at = v
		}
	}
	return at
}
func rejectionIndexKey(row actionLedgerRow) string {
	a := row.Action
	at := rejectedAt(a)
	kind := "other"
	stamp := ""
	if !at.IsZero() {
		kind = "rejected"
		stamp = at.UTC().Format("20060102150405.000000000")
	}
	return actionOwnerKey(row.MonitorID, a) + kind + "\x00" + a.IncidentID + "\x00" + stamp + "\x00" + a.ID
}

// Review changes are infrequent, but still use a bounded index lookup instead
// of scanning retained history to rebuild a cooldown maximum.
func (f *machine) currentRejectedAt(m Monitor) (time.Time, error) {
	result := time.Time{}
	for _, row := range f.actionWrites {
		if row.MonitorID == m.ID && m.OwnsAction(row.Action) && row.Action.IncidentID == m.IncidentID {
			if at := rejectedAt(row.Action); at.After(result) {
				result = at
			}
		}
	}
	if f.collections == nil {
		return result, nil
	}
	pin, err := f.collections.pinActions()
	if err != nil {
		return result, err
	}
	defer pin.Close()
	if pin.sequence == 0 {
		return result, nil
	}
	owners := []Action{{CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation}}
	if m.AdoptedFileIncarnation != nil {
		owners = append(owners, Action{FileIncarnation: *m.AdoptedFileIncarnation})
	}
	err = pin.read(context.Background(), func(tx *bolt.Tx, memory *actionLedgerMemory) error {
		for _, owner := range owners {
			prefix := actionOwnerKey(m.ID, owner) + "rejected\x00" + m.IncidentID + "\x00"
			var queryErr error
			visit := func(key string, data []byte) bool {
				if !strings.HasPrefix(key, prefix) {
					return false
				}
				row, e := decodeActionRow(data)
				if e != nil || row.MonitorID != m.ID || rejectionIndexKey(row) != key {
					queryErr = errActionLedger
					return false
				}
				if _, changed := f.actionWrites[row.Action.ID]; changed {
					return true
				}
				if at := rejectedAt(row.Action); at.After(result) {
					result = at
				}
				return false
			}
			if tx != nil {
				cursor := tx.Bucket(actionCooldownBucket).Cursor()
				upper := []byte(prefix + "\xff")
				key, id := cursor.Seek(upper)
				if key == nil {
					key, id = cursor.Last()
				} else if !bytes.Equal(key, upper) {
					key, id = cursor.Prev()
				}
				for key != nil {
					if !visit(string(key), tx.Bucket(actionCurrentBucket).Get(id)) {
						break
					}
					key, id = cursor.Prev()
				}
			} else if memory != nil {
				memory.byCooldown.DescendLessOrEqual(prefix+"\xff", func(key string) bool {
					n := strings.LastIndexByte(key, 0)
					if n < 0 {
						queryErr = errActionLedger
						return false
					}
					return visit(key, memory.current[key[n+1:]])
				})
			}
			if queryErr != nil {
				return queryErr
			}
		}
		return nil
	})
	return result, err
}
