//go:build externaljobs

package worker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	bolt "go.etcd.io/bbolt"
)

const journalFormat = 3

// At most sixteen 256-byte identity fields with worst-case JSON escaping, plus
// fixed fields/framing. This is separate from the already encoded outcome cap.
const journalMetadataReserve = 32 << 10

var metadataBucket = []byte("metadata")
var epochsBucket = []byte("epochs")
var recordsBucket = []byte("records")

type journalMetadata struct {
	Format                            int
	ID, ServerID, WorkerID, WorkerUID string
}
type encryptedRecord struct {
	Epoch             string
	Nonce, Ciphertext []byte
}
type journalRecord struct {
	ProtocolVersion string   `json:",omitempty"`
	JobTypeID       string   `json:",omitempty"`
	JobTypeVersion  string   `json:",omitempty"`
	FailureCodes    []string `json:",omitempty"`
	ExecutionID     string
	WorkerUID       string
	JobTypeUID      string
	TargetUID       string
	Start           api.StartRequest
	Kind            string
	GrantID         string
	State           string
	Deadline        time.Time
	Outcome         *api.Outcome
	ReceiptID       string
	Evidence        *api.LateEvidenceRequest
}

// journal.mu protects key epochs, closing, and every accounting transaction.
// bbolt's process lock stays held from openJournal until close.
type journal struct {
	mu          sync.Mutex
	db          *bolt.DB
	file        *os.File
	directory   os.FileInfo
	path        string
	meta        journalMetadata
	wrapping    cipher.AEAD
	keys        map[string]cipher.AEAD
	epoch       string
	encryptions uint64
	limits      Limits
	accounting  journalAccounting
	closed      bool
}

// journalAccounting is derived from authenticated records when the journal opens and
// changes only after a successful write transaction. It is never persisted as
// another source of truth or recomputed by status polling.
type journalAccounting struct {
	records, pendingOutcomes, unknownActions int
	liveBytes, reservedBytes                 int64
}

func (a journalAccounting) replace(previous, next journalAccounting) journalAccounting {
	return journalAccounting{
		records:         a.records - previous.records + next.records,
		pendingOutcomes: a.pendingOutcomes - previous.pendingOutcomes + next.pendingOutcomes,
		unknownActions:  a.unknownActions - previous.unknownActions + next.unknownActions,
		liveBytes:       a.liveBytes - previous.liveBytes + next.liveBytes,
		reservedBytes:   a.reservedBytes - previous.reservedBytes + next.reservedBytes,
	}
}

func openJournal(c Config) (*journal, error) {
	return openJournalFormat(c, journalFormat, false)
}

// A conversion opener never initializes or rotates a key in the old journal.
func openJournalFormat(c Config, format int, conversion bool) (*journal, error) {
	return openJournalContext(context.Background(), c, format, conversion)
}

func openJournalContext(ctx context.Context, c Config, format int, conversion bool) (*journal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.WrappingKeyPath) {
		return nil, errors.New("worker state and key paths must be absolute")
	}
	state := filepath.Clean(c.StateDir)
	keyPath := filepath.Clean(c.WrappingKeyPath)
	rel, err := filepath.Rel(state, keyPath)
	if err != nil {
		return nil, err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, errors.New("worker wrapping key must be outside the journal directory")
	}
	if err = rejectSymlinks(keyPath); err != nil {
		return nil, err
	}
	if err = checkPrivate(keyPath, false); err != nil {
		return nil, err
	}
	keyFile, err := os.Open(keyPath)
	if err != nil {
		return nil, fmt.Errorf("open worker wrapping key: %w", err)
	}
	key, err := io.ReadAll(io.LimitReader(keyFile, 33))
	closeErr := keyFile.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	defer clear(key)
	if len(key) != 32 {
		return nil, errors.New("worker wrapping key must contain exactly 32 raw bytes")
	}
	if err = rejectSymlinks(state); err != nil {
		return nil, err
	}
	if !conversion {
		if err = makePrivateDir(state); err != nil {
			return nil, err
		}
	}
	if err = checkPrivate(state, true); err != nil {
		return nil, err
	}
	path := filepath.Join(state, "worker.db")
	if err = rejectSymlinks(path); err != nil {
		return nil, err
	}
	existing := true
	if info, statErr := os.Stat(path); statErr == nil {
		if conversion && info.Size() > c.Limits.AllocatedBytes {
			return nil, ErrCapacity
		}
		if err = checkPrivate(path, false); err != nil {
			return nil, err
		}
		if info.Size() == 0 {
			return nil, fmt.Errorf("%w: existing worker database is empty", ErrCorrupt)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	} else {
		if conversion {
			return nil, ErrJournalVersion
		}
		// Own first creation explicitly. A crash before initialization leaves an
		// existing empty file which is rejected, never silently bootstrapped.
		file, createErr := createPrivateFile(path)
		if createErr != nil {
			return nil, fmt.Errorf("create worker journal: %w", createErr)
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		existing = false
	}
	directory, err := os.Stat(state)
	if err != nil {
		return nil, err
	}
	var opened *os.File
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond, OpenFile: func(name string, flag int, mode os.FileMode) (*os.File, error) {
		if conversion {
			flag &^= os.O_CREATE
		}
		f, e := os.OpenFile(name, flag, mode)
		if e == nil {
			opened = f
		}
		return f, e
	}})
	if err != nil {
		return nil, fmt.Errorf("open exclusively locked worker journal: %w", err)
	}
	if err = checkPrivate(path, false); err != nil {
		_ = db.Close()
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	wrap, err := cipher.NewGCM(block)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	j := &journal{db: db, file: opened, directory: directory, path: path, wrapping: wrap, keys: make(map[string]cipher.AEAD), limits: c.Limits}
	if err = j.checkIdentity(); err != nil {
		_ = db.Close()
		return nil, err
	}
	transaction := db.Update
	if conversion {
		transaction = db.View
	}
	err = transaction(func(tx *bolt.Tx) error {
		var e error
		meta := tx.Bucket(metadataBucket)
		if existing {
			if meta == nil || tx.Bucket(epochsBucket) == nil || tx.Bucket(recordsBucket) == nil {
				return fmt.Errorf("%w: required bucket is missing", ErrCorrupt)
			}
			if meta.Get([]byte("identity")) == nil || meta.Get([]byte("key-check")) == nil {
				return fmt.Errorf("%w: required identity or key check is missing", ErrCorrupt)
			}
		} else {
			if meta, e = tx.CreateBucket(metadataBucket); e != nil {
				return e
			}
			if _, e = tx.CreateBucket(epochsBucket); e != nil {
				return e
			}
			if _, e = tx.CreateBucket(recordsBucket); e != nil {
				return e
			}
		}
		raw := meta.Get([]byte("identity"))
		if len(raw) > 4096 {
			return ErrCorrupt
		}
		if raw == nil {
			j.meta = journalMetadata{Format: format, ID: randomID(), ServerID: c.ServerID, WorkerID: c.WorkerID, WorkerUID: c.WorkerUID}
			identity, e := json.Marshal(j.meta)
			if e != nil {
				return e
			}
			if e = meta.Put([]byte("identity"), identity); e != nil {
				return e
			}
			proof, e := seal(j.wrapping, []byte("cpra-worker-key-check"), j.aad("wrapping-key", "identity"))
			if e != nil {
				return e
			}
			return meta.Put([]byte("key-check"), proof)
		}
		if e = json.Unmarshal(raw, &j.meta); e != nil {
			return fmt.Errorf("%w: metadata", ErrCorrupt)
		}
		if j.meta.Format != format {
			return fmt.Errorf("%w: %d; existing records require operator reconciliation", ErrJournalVersion, j.meta.Format)
		}
		if j.meta.ID == "" || j.meta.ServerID != c.ServerID || j.meta.WorkerID != c.WorkerID || j.meta.WorkerUID != c.WorkerUID {
			return ErrIdentity
		}
		proof, e := unseal(j.wrapping, meta.Get([]byte("key-check")), j.aad("wrapping-key", "identity"))
		if e != nil || string(proof) != "cpra-worker-key-check" {
			return fmt.Errorf("%w: wrong wrapping key or altered identity", ErrCorrupt)
		}
		epochs := 0
		return tx.Bucket(epochsBucket).ForEach(func(k, v []byte) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			epochs++
			if conversion && epochs > c.Limits.Records+1 {
				return ErrCapacity
			}
			if len(k) > 64 || len(v) > 512 {
				return ErrCorrupt
			}
			plain, e := unseal(j.wrapping, v, j.aad("epoch", string(k)))
			if e != nil {
				return fmt.Errorf("%w: epoch", ErrCorrupt)
			}
			defer clear(plain)
			b, e := aes.NewCipher(plain)
			if e != nil {
				return fmt.Errorf("%w: epoch key", ErrCorrupt)
			}
			a, e := cipher.NewGCM(b)
			if e != nil {
				return e
			}
			j.keys[string(k)] = a
			return nil
		})
	})
	if err == nil {
		err = db.View(func(tx *bolt.Tx) error {
			return tx.Bucket(recordsBucket).ForEach(func(k, v []byte) error {
				if e := ctx.Err(); e != nil {
					return e
				}
				if conversion && (j.accounting.records >= c.Limits.Records || len(v) > encodedJournalRecordLimit(c.Limits)) {
					return ErrCapacity
				}
				r, err := j.decode(string(k), v)
				if err != nil {
					return err
				}
				j.accounting = j.accounting.replace(journalAccounting{}, recordAccounting(r, j.limits))
				if conversion && j.accounting.liveBytes+j.accounting.reservedBytes > c.Limits.LiveBytes {
					return ErrCapacity
				}
				return nil
			})
		})
	}
	if err == nil && !conversion {
		err = j.rotateEpoch()
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize worker journal: %w", err)
	}
	return j, nil
}

// Unknown actions may retain both an outcome and late evidence. Both envelope
// limits apply before AEAD, then encoding/json base64-encodes the ciphertext.
func encodedJournalRecordLimit(l Limits) int {
	return base64.StdEncoding.EncodedLen(2*l.OutcomeBytes+journalMetadataReserve+16) + 256
}

// Verify the opened database and its private parent still have the identities
// owned by this runner. Do not continue conversion against a substituted path.
func (j *journal) checkIdentity() error {
	if err := rejectSymlinks(j.path); err != nil {
		return err
	}
	current, err := os.Stat(j.path)
	if err != nil {
		return err
	}
	held, err := j.file.Stat()
	if err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) || !os.SameFile(j.directory, parent) {
		return ErrIdentity
	}
	return checkPrivate(j.path, false)
}

func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (j *journal) aad(purpose, id string) []byte {
	b, _ := json.Marshal([]any{"cpra-worker", j.meta.Format, j.meta.ID, j.meta.ServerID, j.meta.WorkerID, j.meta.WorkerUID, purpose, id})
	return b
}

func seal(a cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plaintext, aad), nil
}

func unseal(a cipher.AEAD, b, aad []byte) ([]byte, error) {
	if len(b) < a.NonceSize()+a.Overhead() {
		return nil, ErrCorrupt
	}
	return a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], aad)
}

// rotateEpoch is called with exclusive access, at startup and every 65,536
// record encryptions. Unreferenced epochs are pruned in the same transaction.
func (j *journal) rotateEpoch() error {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return err
	}
	defer clear(key)
	b, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return err
	}
	id := randomID()
	wrapped, err := seal(j.wrapping, key, j.aad("epoch", id))
	if err != nil {
		return err
	}
	used := map[string]bool{id: true}
	err = j.db.Update(func(tx *bolt.Tx) error {
		if e := tx.Bucket(recordsBucket).ForEach(func(_, v []byte) error {
			var r encryptedRecord
			if json.Unmarshal(v, &r) != nil {
				return ErrCorrupt
			}
			used[r.Epoch] = true
			return nil
		}); e != nil {
			return e
		}
		bucket := tx.Bucket(epochsBucket)
		var dead [][]byte
		if e := bucket.ForEach(func(k, _ []byte) error {
			if !used[string(k)] {
				dead = append(dead, append([]byte(nil), k...))
			}
			return nil
		}); e != nil {
			return e
		}
		for _, k := range dead {
			if e := bucket.Delete(k); e != nil {
				return e
			}
		}
		return bucket.Put([]byte(id), wrapped)
	})
	if err != nil {
		return err
	}
	for k := range j.keys {
		if !used[k] {
			delete(j.keys, k)
		}
	}
	j.keys[id] = a
	j.epoch = id
	j.encryptions = 0
	return nil
}

func (j *journal) decode(id string, b []byte) (journalRecord, error) {
	var envelope encryptedRecord
	var r journalRecord
	if json.Unmarshal(b, &envelope) != nil || j.keys[envelope.Epoch] == nil {
		return r, ErrCorrupt
	}
	a := j.keys[envelope.Epoch]
	if len(envelope.Nonce) != a.NonceSize() {
		return r, ErrCorrupt
	}
	plain, err := a.Open(nil, envelope.Nonce, envelope.Ciphertext, j.aad("execution", id))
	if err != nil {
		return r, ErrCorrupt
	}
	defer clear(plain)
	if json.Unmarshal(plain, &r) != nil || r.ExecutionID != id || !validKind(r.Kind) || r.Start.ExecutionID != id {
		return r, ErrCorrupt
	}
	switch r.State {
	case "reserved", "started", "outcome", "unknown":
	default:
		return r, ErrCorrupt
	}
	if !validID(r.Start.ExecutionRevision) || !validID(r.Start.LeaseID) || !validID(r.Start.SessionID) || r.Start.ServerID != j.meta.ServerID || r.Start.Mode != api.StartModeReconcile || r.WorkerUID != j.meta.WorkerUID || !validID(r.JobTypeUID) || !validID(r.TargetUID) || r.Deadline.IsZero() {
		return r, ErrCorrupt
	}
	if r.State == "started" && r.GrantID == "" {
		return r, ErrCorrupt
	}
	if r.State == "outcome" || r.State == "unknown" {
		if r.Outcome == nil || r.Outcome.ServerID != j.meta.ServerID || r.Outcome.WorkerUID != j.meta.WorkerUID || r.Outcome.ExecutionID != id || r.Outcome.Kind != r.Kind || r.Outcome.GrantID != r.GrantID || !validOutcome(r.Kind, r.Outcome.Status) {
			return r, ErrCorrupt
		}
		if !recordFailureValid(r, *r.Outcome) {
			return r, ErrCorrupt
		}
	}
	if r.ProtocolVersion != "" && r.ProtocolVersion != "1" && r.ProtocolVersion != "2" || r.ProtocolVersion == "2" && (j.meta.Format != 3 || r.Kind != "check" || !validID(r.JobTypeID) || !validID(r.JobTypeVersion) || api.ValidateJobTypeFailures(api.JobTypeSpec{Kind: r.Kind, ProtocolVersion: r.ProtocolVersion, FailureCodes: r.FailureCodes}) != nil) {
		return r, ErrCorrupt
	}
	if r.State == "unknown" && r.ReceiptID == "" {
		return r, ErrCorrupt
	}
	if r.Evidence != nil && (r.Evidence.ServerID != j.meta.ServerID || r.Evidence.WorkerUID != j.meta.WorkerUID || r.State != "unknown" || r.Evidence.ExecutionID != id || r.Evidence.OriginalReceiptID != r.ReceiptID || r.Evidence.EvidenceID == "") {
		return r, ErrCorrupt
	}
	return r, nil
}

func (j *journal) encode(r journalRecord) ([]byte, error) {
	plain, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	a := j.keys[j.epoch]
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	e := encryptedRecord{Epoch: j.epoch, Nonce: nonce, Ciphertext: a.Seal(nil, nonce, plain, j.aad("execution", r.ExecutionID))}
	j.encryptions++
	return json.Marshal(e)
}

func (j *journal) records() ([]journalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, ErrClosed
	}
	var records []journalRecord
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(recordsBucket).ForEach(func(k, v []byte) error {
			r, e := j.decode(string(k), v)
			if e != nil {
				return e
			}
			records = append(records, r)
			return nil
		})
	})
	return records, err
}

func (j *journal) record(id string) (journalRecord, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return journalRecord{}, false, ErrClosed
	}
	var record journalRecord
	var found bool
	err := j.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(recordsBucket).Get([]byte(id))
		if raw == nil {
			return nil
		}
		var err error
		record, err = j.decode(id, raw)
		found = err == nil
		return err
	})
	return record, found, err
}

func recordBytes(r journalRecord, limits Limits) (live, reserved int64) {
	if r.State == "reserved" || r.State == "started" {
		return 0, int64(limits.OutcomeBytes + journalMetadataReserve)
	}
	b, _ := json.Marshal(r)
	return int64(len(b)), 0
}

func recordAccounting(r journalRecord, limits Limits) journalAccounting {
	live, reserved := recordBytes(r, limits)
	a := journalAccounting{records: 1, liveBytes: live, reservedBytes: reserved}
	if r.State == "outcome" || r.Evidence != nil {
		a.pendingOutcomes = 1
	}
	if r.State == "unknown" {
		a.unknownActions = 1
	}
	return a
}

func (j *journal) statsLocked() (Status, error) {
	s := Status{
		Records:         j.accounting.records,
		PendingOutcomes: j.accounting.pendingOutcomes,
		UnknownActions:  j.accounting.unknownActions,
		LiveBytes:       j.accounting.liveBytes,
		ReservedBytes:   j.accounting.reservedBytes,
	}
	info, err := os.Stat(j.path)
	if err != nil {
		return s, err
	}
	s.AllocatedBytes = info.Size()
	s.AdmissionCapacity = j.limits.Records - s.Records
	byteCapacity := (j.limits.LiveBytes - s.LiveBytes - s.ReservedBytes) / int64(j.limits.OutcomeBytes+journalMetadataReserve)
	if byteCapacity < int64(s.AdmissionCapacity) {
		s.AdmissionCapacity = int(byteCapacity)
	}
	if s.AdmissionCapacity < 0 || s.AllocatedBytes >= j.limits.AllocatedBytes {
		s.AdmissionCapacity = 0
	}
	s.AdmissionAvailable = s.AdmissionCapacity > 0
	return s, nil
}

func (j *journal) stats() (Status, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Status{}, ErrClosed
	}
	return j.statsLocked()
}

func (j *journal) reserve(a api.Assignment) error {
	if err := validateAssignment(a, j.meta.ServerID, j.meta.WorkerUID, a.SessionID); err != nil {
		return err
	}
	// Ensure even a bounded interrupted outcome with a maximally escaped grant
	// fits before requesting permission. Handler payloads can then fall back safely.
	completion := unknownOutcome(a.Kind, strings.Repeat("x", 128))
	completion.ServerID, completion.WorkerUID, completion.ExecutionID = a.ServerID, a.WorkerUID, a.ExecutionID
	completion.GrantID = strings.Repeat("\x01", 256)
	encodedCompletion, err := json.Marshal(completion)
	if err != nil || len(encodedCompletion) > j.limits.OutcomeBytes {
		return ErrCapacity
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrClosed
	}
	if j.encryptions >= 65536 {
		if err := j.rotateEpoch(); err != nil {
			return err
		}
	}
	var committed journalAccounting
	err = j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(recordsBucket)
		if b.Get([]byte(a.ExecutionID)) != nil {
			return ErrDuplicate
		}
		s, err := j.statsLocked()
		if err != nil {
			return err
		}
		if !s.AdmissionAvailable {
			return ErrCapacity
		}
		r := journalRecord{ExecutionID: a.ExecutionID, WorkerUID: a.WorkerUID, JobTypeUID: a.JobTypeUID, TargetUID: a.IncarnationUID, Start: api.StartRequest{ServerID: a.ServerID, SessionID: a.SessionID, Mode: api.StartModeReconcile, ExecutionID: a.ExecutionID, LeaseID: a.LeaseID, ExecutionRevision: a.ExecutionRevision}, Kind: a.Kind, State: "reserved", Deadline: a.Deadline}
		if a.ProtocolVersion == "2" {
			r.ProtocolVersion, r.JobTypeID, r.JobTypeVersion, r.FailureCodes = a.ProtocolVersion, a.JobTypeID, a.JobTypeVersion, append([]string(nil), a.FailureCodes...)
		}
		encoded, err := j.encode(r)
		if err != nil {
			return err
		}
		committed = recordAccounting(r, j.limits)
		return b.Put([]byte(r.ExecutionID), encoded)
	})
	if err == nil {
		j.accounting = j.accounting.replace(journalAccounting{}, committed)
	}
	return err
}

func (j *journal) update(id string, fn func(*journalRecord) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrClosed
	}
	if j.encryptions >= 65536 {
		if err := j.rotateEpoch(); err != nil {
			return fmt.Errorf("%w: %w", ErrStorage, err)
		}
	}
	var callbackErr error
	var previous, committed journalAccounting
	err := j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(recordsBucket)
		raw := b.Get([]byte(id))
		if raw == nil {
			return errors.New("execution is not in the worker journal")
		}
		r, err := j.decode(id, raw)
		if err != nil {
			return err
		}
		previous = recordAccounting(r, j.limits)
		if err = fn(&r); err != nil {
			callbackErr = err
			return err
		}
		committed = recordAccounting(r, j.limits)
		// Completion consumes capacity already reserved before permission to start.
		// Evidence requires extra capacity and cannot displace pending outcomes.
		if committed.liveBytes+committed.reservedBytes > previous.liveBytes+previous.reservedBytes {
			s, e := j.statsLocked()
			if e != nil {
				return e
			}
			if s.LiveBytes+s.ReservedBytes-previous.liveBytes-previous.reservedBytes+committed.liveBytes+committed.reservedBytes > j.limits.LiveBytes {
				return ErrCapacity
			}
		}
		encoded, err := j.encode(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), encoded)
	})
	if err == nil {
		j.accounting = j.accounting.replace(previous, committed)
	}
	if err != nil && callbackErr == nil && !errors.Is(err, ErrCapacity) && !errors.Is(err, ErrCorrupt) {
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return err
}

func (j *journal) remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrClosed
	}
	var removed journalAccounting
	err := j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recordsBucket)
		raw := bucket.Get([]byte(id))
		if raw == nil {
			return nil
		}
		record, err := j.decode(id, raw)
		if err != nil {
			return err
		}
		// A concurrent late-evidence submission remains durable even if the
		// original action becomes terminal while its status request is in flight.
		if record.Evidence != nil {
			return nil
		}
		removed = recordAccounting(record, j.limits)
		return bucket.Delete([]byte(id))
	})
	if err == nil {
		j.accounting = j.accounting.replace(removed, journalAccounting{})
	}
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}
	return err
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return j.db.Close()
}

func rejectSymlinks(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("worker storage/key paths cannot contain symbolic links")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
	}
}
