//go:build externaljobs

package worker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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

const journalFormat = 2

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
	ExecutionID string
	WorkerUID   string
	JobTypeUID  string
	TargetUID   string
	Start       api.StartRequest
	Kind        string
	GrantID     string
	State       string
	Deadline    time.Time
	Outcome     *api.Outcome
	ReceiptID   string
	Evidence    *api.LateEvidenceRequest
}

// journal.mu protects key epochs, closing, and every accounting transaction.
// bbolt's process lock stays held from openJournal until close.
type journal struct {
	mu          sync.Mutex
	db          *bolt.DB
	path        string
	meta        journalMetadata
	wrapping    cipher.AEAD
	keys        map[string]cipher.AEAD
	epoch       string
	encryptions uint64
	limits      Limits
	closed      bool
}

func openJournal(c Config) (*journal, error) {
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
	if err = makePrivateDir(state); err != nil {
		return nil, err
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
		if err = checkPrivate(path, false); err != nil {
			return nil, err
		}
		if info.Size() == 0 {
			return nil, fmt.Errorf("%w: existing worker database is empty", ErrCorrupt)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	} else {
		// Own first creation explicitly. A crash before initialization leaves an
		// existing empty file which is rejected, never silently bootstrapped.
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, fmt.Errorf("create worker journal: %w", createErr)
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		existing = false
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond})
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
	j := &journal{db: db, path: path, wrapping: wrap, keys: make(map[string]cipher.AEAD), limits: c.Limits}
	err = db.Update(func(tx *bolt.Tx) error {
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
		if raw == nil {
			j.meta = journalMetadata{Format: journalFormat, ID: randomID(), ServerID: c.ServerID, WorkerID: c.WorkerID, WorkerUID: c.WorkerUID}
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
		if j.meta.Format != journalFormat {
			return fmt.Errorf("%w: %d; existing records require operator reconciliation", ErrJournalVersion, j.meta.Format)
		}
		if j.meta.ID == "" || j.meta.ServerID != c.ServerID || j.meta.WorkerID != c.WorkerID || j.meta.WorkerUID != c.WorkerUID {
			return ErrIdentity
		}
		proof, e := unseal(j.wrapping, meta.Get([]byte("key-check")), j.aad("wrapping-key", "identity"))
		if e != nil || string(proof) != "cpra-worker-key-check" {
			return fmt.Errorf("%w: wrong wrapping key or altered identity", ErrCorrupt)
		}
		return tx.Bucket(epochsBucket).ForEach(func(k, v []byte) error {
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
		_, err = j.records()
	}
	if err == nil {
		err = j.rotateEpoch()
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize worker journal: %w", err)
	}
	return j, nil
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

func (j *journal) statsLocked(tx *bolt.Tx) (Status, error) {
	s := Status{}
	info, err := os.Stat(j.path)
	if err != nil {
		return s, err
	}
	s.AllocatedBytes = info.Size()
	err = tx.Bucket(recordsBucket).ForEach(func(k, v []byte) error {
		r, e := j.decode(string(k), v)
		if e != nil {
			return e
		}
		s.Records++
		live, reserved := recordBytes(r, j.limits)
		s.LiveBytes += live
		s.ReservedBytes += reserved
		if r.State == "outcome" || r.Evidence != nil {
			s.PendingOutcomes++
		}
		if r.State == "unknown" {
			s.UnknownActions++
		}
		return nil
	})
	s.AdmissionCapacity = j.limits.Records - s.Records
	byteCapacity := (j.limits.LiveBytes - s.LiveBytes - s.ReservedBytes) / int64(j.limits.OutcomeBytes+journalMetadataReserve)
	if byteCapacity < int64(s.AdmissionCapacity) {
		s.AdmissionCapacity = int(byteCapacity)
	}
	if s.AdmissionCapacity < 0 || s.AllocatedBytes >= j.limits.AllocatedBytes {
		s.AdmissionCapacity = 0
	}
	s.AdmissionAvailable = s.AdmissionCapacity > 0
	return s, err
}

func (j *journal) stats() (Status, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Status{}, ErrClosed
	}
	var s Status
	err := j.db.View(func(tx *bolt.Tx) error { var e error; s, e = j.statsLocked(tx); return e })
	return s, err
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
	return j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(recordsBucket)
		if b.Get([]byte(a.ExecutionID)) != nil {
			return ErrDuplicate
		}
		s, err := j.statsLocked(tx)
		if err != nil {
			return err
		}
		if !s.AdmissionAvailable {
			return ErrCapacity
		}
		r := journalRecord{ExecutionID: a.ExecutionID, WorkerUID: a.WorkerUID, JobTypeUID: a.JobTypeUID, TargetUID: a.IncarnationUID, Start: api.StartRequest{ServerID: a.ServerID, SessionID: a.SessionID, Mode: api.StartModeReconcile, ExecutionID: a.ExecutionID, LeaseID: a.LeaseID, ExecutionRevision: a.ExecutionRevision}, Kind: a.Kind, State: "reserved", Deadline: a.Deadline}
		encoded, err := j.encode(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(r.ExecutionID), encoded)
	})
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
		beforeLive, beforeReserved := recordBytes(r, j.limits)
		if err = fn(&r); err != nil {
			callbackErr = err
			return err
		}
		afterLive, afterReserved := recordBytes(r, j.limits)
		// Completion consumes capacity already reserved before permission to start.
		// Evidence requires extra capacity and cannot displace pending outcomes.
		if afterLive+afterReserved > beforeLive+beforeReserved {
			s, e := j.statsLocked(tx)
			if e != nil {
				return e
			}
			if s.LiveBytes+s.ReservedBytes-beforeLive-beforeReserved+afterLive+afterReserved > j.limits.LiveBytes {
				return ErrCapacity
			}
		}
		encoded, err := j.encode(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), encoded)
	})
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
		return bucket.Delete([]byte(id))
	})
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
