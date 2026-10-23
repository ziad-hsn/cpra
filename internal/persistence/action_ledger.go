package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/google/btree"
	bolt "go.etcd.io/bbolt"
)

// Older configure commands allowed revisions close to 2 MiB. Retained actions
// add identities, review evidence and execution metadata to that original value.
// Keep their row allowance separate from collection resource/plan limits.
const actionLedgerMaxFrame = maxCommitBytes

var (
	actionTerminalBucket  = []byte("action_terminal")
	errActionLedger       = errors.New("action ledger is corrupt or incompatible")
	actionRowsBucket      = []byte("action_versions")
	actionCurrentBucket   = []byte("action_current")
	actionMonitorBucket   = []byte("action_monitors")
	actionOwnerBucket     = []byte("action_owners")
	actionEpisodeBucket   = []byte("action_episodes")
	actionIncidentBucket  = []byte("action_incidents")
	actionReviewBucket    = []byte("action_reviews")
	actionCooldownBucket  = []byte("action_cooldowns")
	actionMetaBucket      = []byte("action_metadata")
	actionRelevantBucket  = []byte("action_relevant")
	actionReleasedBucket  = []byte("action_released")
	reviewFenceBucket     = []byte("action_review_fences")
	reviewConditionBucket = []byte("action_review_conditions")
)

// Position is a committed command, not an observation timestamp. Sequence is
// local to the snapshot/log materialization; neither changes the Action JSON
// used by conditional reviews or the provider's original execution identity.
type actionLedgerPosition struct {
	Index   uint64 `json:"index"`
	Ordinal uint32 `json:"ordinal"`
}

type actionLedgerRow struct {
	Deleted   bool   `json:"deleted,omitempty"`
	Sequence  uint64 `json:"sequence"`
	Previous  uint64 `json:"previous,omitempty"`
	MonitorID string `json:"monitor_id"`
	Action    Action `json:"action"`
}

type actionLedgerProgress struct {
	Position actionLedgerPosition `json:"position"`
	Sequence uint64               `json:"sequence"`
	Digest   string               `json:"digest"`
	Bytes    uint64               `json:"bytes"`
}

type actionCommandDigest struct {
	hash  hash.Hash
	count uint64
}

func newActionCommandDigest() *actionCommandDigest {
	d := &actionCommandDigest{hash: sha256.New()}
	_, _ = d.hash.Write([]byte("["))
	return d
}
func (d *actionCommandDigest) add(row actionLedgerRow) error {
	row.Sequence, row.Previous = 0, 0
	data, err := json.Marshal(row)
	if err != nil {
		return errActionLedger
	}
	if d.count != 0 {
		_, _ = d.hash.Write([]byte(","))
	}
	_, _ = d.hash.Write(data)
	d.count++
	return nil
}
func (d *actionCommandDigest) sum() string {
	_, _ = d.hash.Write([]byte("]"))
	return hex.EncodeToString(d.hash.Sum(nil))
}

// The disposable memory mode mirrors disk version selection. Raft mode keeps
// these indexes in bbolt; no all-history Go tree or payload cache is allocated.
type actionLedgerMemory struct {
	accounting       ActionStorageAccounting
	byTerminal       *btree.BTreeG[string]
	progress         actionLedgerProgress
	current          map[string][]byte
	versions         map[string][][]byte
	order            *btree.BTreeG[string]
	byMonitor        *btree.BTreeG[string]
	byCooldown       *btree.BTreeG[string]
	byRelevant       *btree.BTreeG[string]
	byReleased       *btree.BTreeG[string]
	reviewFences     *btree.BTreeG[string]
	reviewConditions *btree.BTreeG[string]
	reviews          map[string][]byte
	reviewOrder      *btree.BTreeG[string]
}

func newActionLedgerMemory() *actionLedgerMemory {
	return &actionLedgerMemory{byTerminal: newReviewOrder(), byRelevant: newReviewOrder(), byReleased: newReviewOrder(), reviewFences: newReviewOrder(), reviewConditions: newReviewOrder(), reviews: make(map[string][]byte), reviewOrder: newReviewOrder(), byCooldown: newReviewOrder(), current: make(map[string][]byte), versions: make(map[string][][]byte),
		order: btree.NewG(32, func(a, b string) bool { return a < b }), byMonitor: btree.NewG(32, func(a, b string) bool { return a < b })}
}

func (m *actionLedgerMemory) validate() error {
	var total uint64
	if len(m.current) != len(m.versions) {
		return errActionLedger
	}
	for id, versions := range m.versions {
		if len(versions) == 0 || !bytes.Equal(m.current[id], versions[len(versions)-1]) {
			return errActionLedger
		}
		var previous uint64
		for n, data := range versions {
			row, err := decodeActionRow(data)
			if err != nil || row.Action.ID != id || row.Sequence > m.progress.Sequence || n != 0 && (row.Previous != previous || row.Sequence <= previous) {
				return errActionLedger
			}
			previous = row.Sequence
			if uint64(len(data)) > math.MaxUint64-total {
				return errActionLedger
			}
			total += uint64(len(data))
		}
	}
	for key, data := range m.reviews {
		r, err := decodeReviewRecord(data)
		if err != nil || r.Key != key || r.Sequence > m.progress.Sequence {
			return errActionLedger
		}
		total += uint64(len(data))
	}
	if total != m.progress.Bytes {
		return errActionLedger
	}
	return nil
}

func actionBuckets() [][]byte {
	return [][]byte{actionRowsBucket, actionCurrentBucket, actionMonitorBucket, actionOwnerBucket, actionEpisodeBucket, actionIncidentBucket, actionReviewBucket, actionMetaBucket, actionCooldownBucket, reviewRowsBucket, reviewReadyBucket, actionRelevantBucket, actionReleasedBucket, reviewFenceBucket, reviewConditionBucket, actionTerminalBucket}
}

func actionOwnerKey(monitorID string, a Action) string {
	return monitorID + "\x00" + a.CatalogUID + "\x00" + a.FileIncarnation + "\x00"
}

func actionRowKeys(row actionLedgerRow) map[string]string {
	a := row.Action
	if row.Deleted {
		return map[string]string{string(actionMonitorBucket): row.MonitorID + "\x00" + a.ID}
	}
	review := "complete"
	if a.Held() {
		review = "held"
	} else if a.State == Unknown {
		review = "reviewed"
	} else if a.State == Queued || a.State == Started {
		review = "active"
	}
	owner := actionOwnerKey(row.MonitorID, a)
	episode := a.NotificationEpisode
	if episode == "" {
		episode = a.IncidentID
	}
	keys := map[string]string{
		string(actionCooldownBucket): rejectionIndexKey(row),
		string(actionMonitorBucket):  row.MonitorID + "\x00" + a.ID,
		string(actionOwnerBucket):    owner + a.ID,
		string(actionEpisodeBucket):  owner + episode + "\x00" + a.ID,
		string(actionIncidentBucket): owner + a.IncidentID + "\x00" + a.ID,
		string(actionReviewBucket):   owner + review + "\x00" + a.ID,
	}
	if key := terminalActionKey(row); key != "" {
		keys[string(actionTerminalBucket)] = key
	}
	return keys
}

func encodeActionRow(row actionLedgerRow) ([]byte, error) {
	if row.Sequence == 0 || row.Previous >= row.Sequence || !catalogIdentifier(row.MonitorID, 256) || !catalogIdentifier(row.Action.ID, 256) ||
		(row.Action.CatalogUID != "" && !catalogIdentifier(row.Action.CatalogUID, 256)) ||
		(row.Action.FileIncarnation != "" && !catalogIdentifier(row.Action.FileIncarnation, 256)) ||
		(row.Action.IncidentID != "" && !catalogIdentifier(row.Action.IncidentID, 256)) {
		return nil, errActionLedger
	}
	if row.Deleted && !reflect.DeepEqual(row.Action, Action{ID: row.Action.ID}) {
		return nil, errActionLedger
	}
	data, err := json.Marshal(row)
	if err != nil || len(data) > actionLedgerMaxFrame {
		return nil, errActionLedger
	}
	return data, nil
}

func decodeActionRow(data []byte) (actionLedgerRow, error) {
	var row actionLedgerRow
	if len(data) == 0 || len(data) > actionLedgerMaxFrame {
		return row, errActionLedger
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&row); err != nil {
		return row, errActionLedger
	}
	canonical, err := encodeActionRow(row)
	if err != nil || !bytes.Equal(data, canonical) {
		return row, errActionLedger
	}
	return row, nil
}

func actionVersionKey(id string, sequence uint64) []byte {
	return append([]byte(id+"\x00"), collectionOrdinal(sequence)...)
}

func actionDiskHead(tx *bolt.Tx, id string) (actionLedgerRow, []byte, bool, error) {
	return actionDiskHeadDecoded(tx, id, decodeActionRow)
}
func actionDiskHeadDecoded(tx *bolt.Tx, id string, decode func([]byte) (actionLedgerRow, error)) (actionLedgerRow, []byte, bool, error) {
	versions, current := tx.Bucket(actionRowsBucket), tx.Bucket(actionCurrentBucket)
	if versions == nil || current == nil {
		return actionLedgerRow{}, nil, false, errActionLedger
	}
	target := actionVersionKey(id, math.MaxUint64)
	cursor := versions.Cursor()
	key, data := cursor.Seek(target)
	if key == nil {
		key, data = cursor.Last()
	} else if !bytes.Equal(key, target) {
		key, data = cursor.Prev()
	}
	head := current.Get([]byte(id))
	if !bytes.HasPrefix(key, []byte(id+"\x00")) {
		if head != nil {
			return actionLedgerRow{}, nil, false, errActionLedger
		}
		return actionLedgerRow{}, nil, false, nil
	}
	row, err := decode(data)
	if err != nil || row.Action.ID != id || !bytes.Equal(key, actionVersionKey(id, row.Sequence)) || !bytes.Equal(head, data) {
		return actionLedgerRow{}, nil, false, errActionLedger
	}
	return row, data, true, nil
}

func actionDiskProgress(tx *bolt.Tx) (actionLedgerProgress, error) {
	var p actionLedgerProgress
	meta := tx.Bucket(actionMetaBucket)
	if meta == nil {
		return p, errActionLedger
	}
	raw := meta.Get([]byte("progress"))
	if raw == nil {
		for _, name := range actionBuckets() {
			bucket := tx.Bucket(name)
			if bucket == nil {
				return p, errActionLedger
			}
			key, _ := bucket.Cursor().First()
			if key != nil {
				return p, errActionLedger
			}
		}
		return p, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || p.Sequence == 0 || p.Position.Index == 0 || len(p.Digest) != 64 {
		return p, errActionLedger
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return p, errActionLedger
	}
	if _, err := hex.DecodeString(p.Digest); err != nil {
		return p, errActionLedger
	}
	return p, nil
}

func actionBucketCount(tx *bolt.Tx) int {
	count := 0
	for _, name := range actionBuckets() {
		if tx.Bucket(name) != nil {
			count++
		}
	}
	return count
}

// CommitActions writes the final changed rows for one committed command in one
// synchronous transaction, including indexes and replay progress. Its caller
// consolidates multiple edits of a row before calling. Eviction from a monitor
// projection is deliberately not a deletion from this evidence ledger.
func (l *collectionLedger) CommitActions(position actionLedgerPosition, rows []actionLedgerRow) (uint64, error) {
	return l.CommitActionState(position, rows, nil)
}

func (l *collectionLedger) CommitActionState(position actionLedgerPosition, rows []actionLedgerRow, reviews []reviewRecord) (uint64, error) {
	if position.Index == 0 || len(rows)+len(reviews) == 0 {
		return 0, errActionLedger
	}
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b actionLedgerRow) int { return strings.Compare(a.Action.ID, b.Action.ID) })
	for i := range rows {
		if i > 0 && rows[i-1].Action.ID == rows[i].Action.ID {
			return 0, errActionLedger
		}
		rows[i].Sequence = 0 // The caller cannot assign a ledger version.
		rows[i].Previous = 0
	}
	digest := newActionCommandDigest()
	for _, row := range rows {
		if err := digest.add(row); err != nil {
			return 0, err
		}
	}
	reviews = slices.Clone(reviews)
	slices.SortFunc(reviews, func(a, b reviewRecord) int { return strings.Compare(a.Key, b.Key) })
	for n, r := range reviews {
		if n > 0 && reviews[n-1].Key == r.Key {
			return 0, errActionLedger
		}
		if err := digest.addReview(r); err != nil {
			return 0, err
		}
	}
	hash := digest.sum()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, errCollectionLedgerClosed
	}
	if l.db == nil {
		if l.actions == nil {
			l.actions = newActionLedgerMemory()
		}
		return l.commitMemoryActions(position, rows, reviews, hash)
	}
	var sequence uint64
	err := l.db.Update(func(tx *bolt.Tx) error {
		switch actionBucketCount(tx) {
		case 0:
			for _, name := range actionBuckets() {
				if _, err := tx.CreateBucket(name); err != nil {
					return err
				}
			}
		case len(actionBuckets()):
		default:
			return errActionLedger
		}
		previous, err := actionDiskProgress(tx)
		if err != nil {
			return err
		}
		sequence, err = nextActionSequence(previous, position, hash)
		if err != nil || sequence == previous.Sequence {
			return err
		}
		used := previous.Bytes
		for _, row := range rows {
			row.Sequence = sequence
			current := tx.Bucket(actionCurrentBucket)
			decoded, _, exists, err := actionDiskHead(tx, row.Action.ID)
			if err != nil {
				return err
			}
			if exists {
				if decoded.MonitorID != row.MonitorID || decoded.Sequence > previous.Sequence {
					return errActionLedger
				}
				row.Previous = decoded.Sequence
				for bucket, key := range actionRowKeys(decoded) {
					if err := tx.Bucket([]byte(bucket)).Delete([]byte(key)); err != nil {
						return err
					}
				}
			}
			data, err := encodeActionRow(row)
			if err != nil {
				return err
			}
			if uint64(len(data)) > math.MaxUint64-used {
				return errActionLedger
			}
			used += uint64(len(data))
			key := actionVersionKey(row.Action.ID, sequence)
			if tx.Bucket(actionRowsBucket).Get(key) != nil {
				return errActionLedger
			}
			if err := tx.Bucket(actionRowsBucket).Put(key, data); err != nil {
				return err
			}
			if err := updateActionAccounting(tx, decoded, exists, row); err != nil {
				return err
			}
			if err := current.Put([]byte(row.Action.ID), data); err != nil {
				return err
			}
			if err := putActionRelevance(tx, decoded, exists, row); err != nil {
				return err
			}
			for bucket, key := range actionRowKeys(row) {
				if err := tx.Bucket([]byte(bucket)).Put([]byte(key), []byte(row.Action.ID)); err != nil {
					return err
				}
			}
		}
		for _, r := range reviews {
			r.Sequence = sequence
			data, err := encodeReviewUpdate(tx.Bucket(reviewRowsBucket).Get([]byte(r.Key)), r)
			if err != nil {
				return err
			}
			if old := tx.Bucket(reviewRowsBucket).Get([]byte(r.Key)); old != nil {
				used -= uint64(len(old))
			}
			used += uint64(len(data))
			if err := putReviewRecord(tx, r, data); err != nil {
				return err
			}
		}
		if err := pruneActionRelevance(tx, nil, 64); err != nil {
			return err
		}
		progress, err := json.Marshal(actionLedgerProgress{Position: position, Sequence: sequence, Digest: hash, Bytes: used})
		if err != nil {
			return errActionLedger
		}
		return tx.Bucket(actionMetaBucket).Put([]byte("progress"), progress)
	})
	return sequence, err
}

func nextActionSequence(previous actionLedgerProgress, position actionLedgerPosition, digest string) (uint64, error) {
	if previous.Sequence != 0 && previous.Position == position {
		if previous.Digest != digest {
			return 0, errCollectionLedgerConflict
		}
		return previous.Sequence, nil
	}
	if previous.Position.Index > position.Index || previous.Position.Index == position.Index && previous.Position.Ordinal >= position.Ordinal || previous.Sequence == math.MaxUint64 {
		return 0, errCollectionLedgerConflict
	}
	return previous.Sequence + 1, nil
}

func (l *collectionLedger) commitMemoryActions(position actionLedgerPosition, rows []actionLedgerRow, reviews []reviewRecord, digest string) (uint64, error) {
	m := l.actions
	sequence, err := nextActionSequence(m.progress, position, digest)
	if err != nil || sequence == m.progress.Sequence {
		return sequence, err
	}
	encoded := make([][]byte, len(rows))
	used := m.progress.Bytes
	for i := range rows {
		rows[i].Sequence = sequence
		versions := m.versions[rows[i].Action.ID]
		if len(versions) != 0 && !bytes.Equal(versions[len(versions)-1], m.current[rows[i].Action.ID]) || len(versions) == 0 && m.current[rows[i].Action.ID] != nil {
			return 0, errActionLedger
		}
		if old := m.current[rows[i].Action.ID]; old != nil {
			decoded, err := decodeActionRow(old)
			if err != nil || decoded.MonitorID != rows[i].MonitorID {
				return 0, errActionLedger
			}
			rows[i].Previous = decoded.Sequence
		}
		data, err := encodeActionRow(rows[i])
		if err != nil {
			return 0, err
		}
		if uint64(len(data)) > math.MaxUint64-used {
			return 0, errActionLedger
		}
		used += uint64(len(data))
		encoded[i] = data
	}
	reviewData := make([][]byte, len(reviews))
	for i, r := range reviews {
		r.Sequence = sequence
		data, err := encodeReviewUpdate(m.reviews[r.Key], r)
		if err != nil {
			return 0, err
		}
		used -= uint64(len(m.reviews[r.Key]))
		used += uint64(len(data))
		reviewData[i] = data
	}
	for i, r := range reviews {
		m.updateReviewIndexes(r)
		m.reviews[r.Key] = reviewData[i]
		if r.Deleted {
			m.reviewOrder.Delete(r.Key)
		} else {
			m.reviewOrder.ReplaceOrInsert(r.Key)
		}
	}
	for i, row := range rows {
		if old := m.current[row.Action.ID]; old != nil {
			prior, _ := decodeActionRow(old)
			if err := m.accounting.adjust(prior, false); err != nil {
				return 0, err
			}
			m.byCooldown.Delete(rejectionIndexKey(prior))
			m.byTerminal.Delete(terminalActionKey(prior))
			m.byReleased.Delete(releasedActionKey(prior))
		}
		if err := m.accounting.adjust(row, true); err != nil {
			return 0, err
		}
		m.putActionRelevance(row)
		if !row.Deleted {
			m.byCooldown.ReplaceOrInsert(rejectionIndexKey(row))
		}
		if key := terminalActionKey(row); key != "" {
			m.byTerminal.ReplaceOrInsert(key)
		}
		m.current[row.Action.ID] = encoded[i]
		m.versions[row.Action.ID] = append(m.versions[row.Action.ID], encoded[i])
		m.order.ReplaceOrInsert(row.Action.ID)
		m.byMonitor.ReplaceOrInsert(row.MonitorID + "\x00" + row.Action.ID)
	}
	if err := pruneActionRelevance(nil, m, 64); err != nil {
		return 0, err
	}
	m.progress = actionLedgerProgress{Position: position, Sequence: sequence, Digest: digest, Bytes: used}
	return sequence, nil
}

// A pin owns one generation, not a long-lived bbolt read transaction. Page
// reads finish before releasing their lock so mmap growth can proceed between
// pages. Value copies share the same idempotent lifetime handle.
type actionLedgerPin struct {
	mu       sync.RWMutex
	ledger   *collectionLedger
	sequence uint64
	upperID  string
	closed   bool
}

func (l *collectionLedger) pinActions() (*actionLedgerPin, error) {
	return l.pinActionsContext(context.Background())
}

func (l *collectionLedger) pinActionsContext(ctx context.Context) (*actionLedgerPin, error) {
	if err := lockActionRead(ctx, &l.mu); err != nil {
		return nil, err
	}
	defer l.mu.RUnlock()
	if l.closed {
		return nil, errCollectionLedgerClosed
	}
	var sequence uint64
	var upperID string
	if l.db != nil {
		if err := l.db.View(func(tx *bolt.Tx) error {
			if actionBucketCount(tx) == 0 {
				return nil
			}
			if actionBucketCount(tx) != len(actionBuckets()) {
				return errActionLedger
			}
			p, err := actionDiskProgress(tx)
			sequence = p.Sequence
			if bucket := tx.Bucket(actionCurrentBucket); bucket != nil {
				key, _ := bucket.Cursor().Last()
				upperID = string(key)
			}
			return err
		}); err != nil {
			return nil, err
		}
	} else if l.actions != nil {
		sequence = l.actions.progress.Sequence
		upperID, _ = l.actions.order.Max()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.pinsMu.Lock()
	defer l.pinsMu.Unlock()
	l.viewPins.Add(1)
	pin := &actionLedgerPin{ledger: l, sequence: sequence, upperID: upperID}
	if l.actionPins == nil {
		l.actionPins = make(map[*actionLedgerPin]struct{})
	}
	l.actionPins[pin] = struct{}{}
	return pin, nil
}

func (v *actionLedgerPin) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	l := v.ledger
	l.pinsMu.Lock()
	delete(l.actionPins, v)
	l.pinsMu.Unlock()
	return l.releasePin()
}

func (v *actionLedgerPin) read(ctx context.Context, read func(*bolt.Tx, *actionLedgerMemory) error) error {
	if v == nil || ctx == nil {
		return errCollectionLedgerClosed
	}
	if err := lockActionRead(ctx, &v.mu); err != nil {
		return err
	}
	defer v.mu.RUnlock()
	if v.closed {
		return errCollectionLedgerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l := v.ledger
	if err := lockActionRead(ctx, &l.mu); err != nil {
		return err
	}
	defer l.mu.RUnlock()
	if l.closedNative.Load() {
		return errCollectionLedgerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if l.db != nil {
		err = l.db.View(func(tx *bolt.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return read(tx, nil)
		})
	} else {
		err = read(nil, l.actions)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func readActionVersion(tx *bolt.Tx, memory *actionLedgerMemory, id string, sequence uint64) (actionLedgerRow, bool, error) {
	return readActionVersionDecoded(tx, memory, id, sequence, decodeActionRow)
}
func readActionVersionDecoded(tx *bolt.Tx, memory *actionLedgerMemory, id string, sequence uint64, decode func([]byte) (actionLedgerRow, error)) (actionLedgerRow, bool, error) {
	var data []byte
	if sequence == 0 {
		return actionLedgerRow{}, false, nil
	}
	if tx != nil {
		bucket := tx.Bucket(actionRowsBucket)
		current := tx.Bucket(actionCurrentBucket)
		if bucket == nil || current == nil {
			return actionLedgerRow{}, false, errActionLedger
		}
		latest, _, found, err := actionDiskHeadDecoded(tx, id, decode)
		if err != nil || !found {
			return actionLedgerRow{}, false, err
		}
		if latest.Sequence <= sequence {
			return latest, !latest.Deleted, nil
		}
		cursor := bucket.Cursor()
		target := actionVersionKey(id, sequence)
		key, value := cursor.Seek(target)
		var successor actionLedgerRow
		if bytes.Equal(key, target) {
			data = value
			key, value = cursor.Next()
		}
		if !bytes.HasPrefix(key, []byte(id+"\x00")) {
			return actionLedgerRow{}, false, errActionLedger
		}
		successor, err = decode(value)
		if err != nil || successor.Action.ID != id || !bytes.Equal(key, actionVersionKey(id, successor.Sequence)) || successor.Sequence <= sequence {
			return actionLedgerRow{}, false, errActionLedger
		}
		if data != nil {
			row, err := decode(data)
			if err != nil || row.Sequence != sequence || row.Action.ID != id || successor.Previous != row.Sequence {
				return actionLedgerRow{}, false, errActionLedger
			}
			return row, !row.Deleted, nil
		}
		key, value = cursor.Prev()
		prefix := []byte(id + "\x00")
		if len(key) != len(prefix)+8 || !bytes.HasPrefix(key, prefix) {
			if successor.Previous != 0 {
				return actionLedgerRow{}, false, errActionLedger
			}
			return actionLedgerRow{}, false, nil
		}
		row, err := decode(value)
		if err != nil || binary.BigEndian.Uint64(key[len(prefix):]) != row.Sequence || row.Sequence > sequence || row.Action.ID != id || successor.Previous != row.Sequence {
			return actionLedgerRow{}, false, errActionLedger
		}
		return row, !row.Deleted, nil
	} else if memory != nil {
		versions := memory.versions[id]
		if len(versions) == 0 {
			if memory.current[id] != nil {
				return actionLedgerRow{}, false, errActionLedger
			}
			return actionLedgerRow{}, false, nil
		}
		if !bytes.Equal(versions[len(versions)-1], memory.current[id]) {
			return actionLedgerRow{}, false, errActionLedger
		}
		index, err := actionMemoryVersion(versions, sequence, decode)
		if err != nil {
			return actionLedgerRow{}, false, err
		}
		if index >= 0 {
			data = versions[index]
		}
		if index+1 < len(versions) {
			successor, err := decode(versions[index+1])
			if err != nil {
				return actionLedgerRow{}, false, err
			}
			var selected uint64
			if index >= 0 {
				row, err := decode(data)
				if err != nil {
					return actionLedgerRow{}, false, err
				}
				selected = row.Sequence
			}
			if successor.Previous != selected {
				return actionLedgerRow{}, false, errActionLedger
			}
		}
	}
	if data == nil {
		return actionLedgerRow{}, false, nil
	}
	row, err := decode(data)
	if err != nil || row.Sequence > sequence || row.Action.ID != id {
		return actionLedgerRow{}, false, errActionLedger
	}
	return row, !row.Deleted, nil
}

func actionMemoryVersion(versions [][]byte, sequence uint64, decode func([]byte) (actionLedgerRow, error)) (int, error) {
	lo, hi := 0, len(versions)
	for lo < hi {
		mid := lo + (hi-lo)/2
		row, err := decode(versions[mid])
		if err != nil {
			return -1, err
		}
		if row.Sequence <= sequence {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1, nil
}

func (v *actionLedgerPin) Get(ctx context.Context, id string) (actionLedgerRow, bool, error) {
	if !catalogIdentifier(id, 256) {
		return actionLedgerRow{}, false, errActionLedger
	}
	var row actionLedgerRow
	var found bool
	err := v.read(ctx, func(tx *bolt.Tx, memory *actionLedgerMemory) error {
		var err error
		row, found, err = readActionVersion(tx, memory, id, v.sequence)
		return err
	})
	if err != nil {
		return actionLedgerRow{}, false, err
	}
	return row, found, err
}

func (v *actionLedgerPin) Page(ctx context.Context, monitorID, after string, limit int) ([]actionLedgerRow, string, error) {
	return v.pageBounded(ctx, monitorID, after, limit, collectionLedgerPageLimit, collectionLedgerPageBytes)
}
func (v *actionLedgerPin) pageBounded(ctx context.Context, monitorID, after string, limit, visits, maxBytes int) ([]actionLedgerRow, string, error) {
	if limit < 1 || limit > 500 || after != "" && !catalogIdentifier(after, 256) || monitorID != "" && !catalogIdentifier(monitorID, 256) {
		return nil, "", errActionLedger
	}
	rows := make([]actionLedgerRow, 0, limit)
	next, size := "", 0
	err := v.read(ctx, func(tx *bolt.Tx, memory *actionLedgerMemory) error {
		if v.sequence == 0 {
			return nil
		}
		visited, last, decoded := 0, "", 0
		decode := func(data []byte) (actionLedgerRow, error) { decoded += len(data); return decodeActionRow(data) }
		visit := func(id string) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if id > v.upperID {
				return false, nil
			}
			// Stop between complete identity lookups. One existing large record
			// and its bounded version-proof reads may exceed the page budget;
			// it must remain readable without repeatedly making no progress.
			if visited == visits || visited > 0 && decoded >= maxBytes {
				next = last
				return false, nil
			}
			visited++
			last = id
			row, found, err := readActionVersionDecoded(tx, memory, id, v.sequence, decode)
			if err != nil || !found {
				return err == nil, err
			}
			if monitorID != "" && row.MonitorID != monitorID {
				return false, errActionLedger
			}
			encoded, _ := encodeActionRow(row)
			if len(rows) == limit || len(rows) > 0 && size+len(encoded) > maxBytes {
				next = rows[len(rows)-1].Action.ID
				return false, nil
			}
			rows = append(rows, row)
			size += len(encoded)
			return true, nil
		}
		if tx == nil {
			if memory == nil {
				return nil
			}
			tree, prefix := memory.order, ""
			if monitorID != "" {
				tree, prefix = memory.byMonitor, monitorID+"\x00"
			}
			var readErr error
			tree.AscendGreaterOrEqual(prefix+after, func(key string) bool {
				if !strings.HasPrefix(key, prefix) {
					return false
				}
				id := key[len(prefix):]
				if id <= after {
					return true
				}
				var more bool
				more, readErr = visit(id)
				return more && readErr == nil
			})
			return readErr
		}
		bucket, prefix := tx.Bucket(actionCurrentBucket), ""
		if monitorID != "" {
			bucket, prefix = tx.Bucket(actionMonitorBucket), monitorID+"\x00"
		}
		if bucket == nil {
			if v.sequence == 0 {
				return nil
			}
			return errActionLedger
		}
		cursor := bucket.Cursor()
		for key, _ := cursor.Seek([]byte(prefix + after)); key != nil; key, _ = cursor.Next() {
			if !strings.HasPrefix(string(key), prefix) {
				break
			}
			id := string(key[len(prefix):])
			if id <= after {
				continue
			}
			more, err := visit(id)
			if err != nil || !more {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return rows, next, nil
}
