package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	bolt "go.etcd.io/bbolt"
)

const (
	StageFormatVersion  = 1
	stageFileName       = "bootstrap.db"
	stageMetadataBudget = 128 << 10
	stageMaxEntryBytes  = 4 << 20
	stageMaxPageBytes   = 4 << 20
)

var (
	ErrStageUnavailable = errors.New("encrypted bootstrap stage is unavailable or failed integrity verification")
	ErrStageExists      = errors.New("bootstrap stage already exists; resume it explicitly")
	ErrStageMissing     = errors.New("bootstrap stage does not exist; resume never creates a store")
	ErrStageLocked      = errors.New("bootstrap stage is already open")
	ErrStageQuota       = errors.New("bootstrap staging quota exceeded")
	ErrStageFrozen      = errors.New("bootstrap stage is frozen")
	ErrStageDuplicate   = errors.New("duplicate bootstrap resource identity")
	ErrStageNotFrozen   = errors.New("validate and freeze bootstrap staging before reading activation records")
)

var stageMetaBucket = []byte("metadata")
var stageRecordsBucket = []byte("records")
var stageOrderBucket = []byte("append-order")
var stageMetaKey = []byte("authenticated-info")

// StageOptions identifies a caller-provisioned private staging directory. Limits
// are explicit, persistent and cannot change on resume. Encoded-byte accounting
// includes ciphertext, authentication stamps, both indexes, reserved metadata and
// per-row overhead; it is not a bound on bbolt allocation or filesystem usage.
// The caller must provision the directory's ACL on Windows before opening it.
type StageOptions struct {
	Directory       string
	StoreID         string
	StageID         string
	MaxResources    uint64
	MaxEncodedBytes uint64
}

// StageInfo contains no source paths, provider parameters or secret digests.
// Digest commits to the append sequence of exact encrypted and stamped records.
type StageInfo struct {
	Format          int    `json:"format"`
	StoreID         string `json:"storeID"`
	StageID         string `json:"stageID"`
	Phase           string `json:"phase"`
	Count           uint64 `json:"count"`
	EncodedBytes    uint64 `json:"encodedBytes"`
	MaxResources    uint64 `json:"maxResources"`
	MaxEncodedBytes uint64 `json:"maxEncodedBytes"`
	Digest          string `json:"digest"`
	CatalogDigest   string `json:"catalogDigest,omitempty"`
}

// Stage is isolated from the active catalog. It cannot activate resources or
// change management readiness. Public iteration returns ciphertext only.
type Stage struct {
	mu       sync.Mutex
	db       *bolt.DB
	path     string
	fileInfo os.FileInfo
	sealer   *secureconfig.Sealer
	info     StageInfo
	failed   bool
}

func (s *Stage) String() string   { return "encrypted bootstrap staging (private configuration omitted)" }
func (s *Stage) GoString() string { return s.String() }
func (s *Stage) MarshalJSON() ([]byte, error) {
	return nil, errors.New("bootstrap stage must not be serialized")
}

type stageEntry struct {
	Sequence uint64                    `json:"sequence"`
	Record   persistence.CatalogRecord `json:"record"`
	Stamp    secureconfig.Envelope     `json:"stamp"`
}

// CreateStage exclusively creates a new store. A failed creation is retained for
// explicit inspection/removal; it is never silently repaired or overwritten.
func CreateStage(ctx context.Context, options StageOptions, sealer *secureconfig.Sealer) (*Stage, error) {
	return openStage(ctx, options, sealer, true)
}

// ResumeStage requires an existing database, matching identity/limits, available
// keys and an authenticated complete record inventory. It never creates a file.
func ResumeStage(ctx context.Context, options StageOptions, sealer *secureconfig.Sealer) (*Stage, error) {
	return openStage(ctx, options, sealer, false)
}

func openStage(ctx context.Context, o StageOptions, sealer *secureconfig.Sealer, create bool) (*Stage, error) {
	if ctx == nil || sealer == nil || o.MaxResources == 0 || o.MaxResources > 10_000_000 ||
		o.MaxEncodedBytes < stageMetadataBudget || o.MaxEncodedBytes > 1<<40 ||
		!stageIdentity(o.StoreID) || !stageIdentity(o.StageID) || !filepath.IsAbs(o.Directory) {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := os.Lstat(o.Directory)
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && directory.Mode().Perm()&0077 != 0) {
		return nil, ErrStageUnavailable
	}
	path := filepath.Join(o.Directory, stageFileName)
	options := &bolt.Options{Timeout: 250 * time.Millisecond, NoSync: false, NoGrowSync: false,
		OpenFile: func(name string, flags int, mode os.FileMode) (*os.File, error) {
			if create {
				flags |= os.O_CREATE | os.O_EXCL
			} else {
				flags &^= os.O_CREATE
			}
			if !create {
				info, err := os.Lstat(name)
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() || info.Size() < 8192 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
					return nil, ErrStageUnavailable
				}
			}
			var before os.FileInfo
			if !create {
				before, _ = os.Lstat(name)
			}
			f, err := os.OpenFile(name, flags, 0600)
			if err != nil {
				return nil, err
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || (before != nil && !os.SameFile(before, info)) {
				_ = f.Close()
				return nil, ErrStageUnavailable
			}
			return f, nil
		}}
	db, err := bolt.Open(path, 0600, options)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrExist):
			return nil, ErrStageExists
		case errors.Is(err, os.ErrNotExist):
			return nil, ErrStageMissing
		case errors.Is(err, bolt.ErrTimeout):
			return nil, ErrStageLocked
		default:
			return nil, ErrStageUnavailable
		}
	}
	s := &Stage{db: db, path: path, sealer: sealer}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	s.fileInfo, err = os.Lstat(path)
	if err != nil {
		return nil, ErrStageUnavailable
	}
	if create {
		s.info = StageInfo{Format: StageFormatVersion, StoreID: o.StoreID, StageID: o.StageID, Phase: "loading",
			EncodedBytes: stageMetadataBudget, MaxResources: o.MaxResources, MaxEncodedBytes: o.MaxEncodedBytes, Digest: stageInitialDigest()}
		meta, err := s.encodeInfo(ctx, s.info)
		if err != nil {
			return nil, err
		}
		err = db.Update(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{stageMetaBucket, stageRecordsBucket, stageOrderBucket} {
				if _, err := tx.CreateBucket(name); err != nil {
					return err
				}
			}
			return tx.Bucket(stageMetaBucket).Put(stageMetaKey, meta)
		})
		if err != nil {
			return nil, ErrStageUnavailable
		}
		// bbolt synchronizes its file. Persist its newly created directory entry
		// on Unix too; directory FlushFileBuffers is unsupported on Windows.
		if runtime.GOOS != "windows" {
			dir, err := os.Open(o.Directory)
			if err != nil {
				return nil, ErrStageUnavailable
			}
			err = dir.Sync()
			_ = dir.Close()
			if err != nil {
				return nil, ErrStageUnavailable
			}
		}
	} else {
		s.info = StageInfo{StoreID: o.StoreID, StageID: o.StageID}
		err = db.View(func(tx *bolt.Tx) error {
			if tx.Bucket(stageMetaBucket) == nil || tx.Bucket(stageRecordsBucket) == nil || tx.Bucket(stageOrderBucket) == nil {
				return ErrStageUnavailable
			}
			raw := tx.Bucket(stageMetaBucket).Get(stageMetaKey)
			if len(raw) == 0 || len(raw) > stageMetadataBudget {
				return ErrStageUnavailable
			}
			var envelope secureconfig.Envelope
			if api.StrictDecode(raw, &envelope) != nil {
				return ErrStageUnavailable
			}
			plain, err := sealer.Open(ctx, s.metaBinding(), envelope)
			if err != nil {
				return stageContextError(ctx, ErrStageUnavailable)
			}
			defer clear(plain)
			var info StageInfo
			if api.StrictDecode(plain, &info) != nil || info.Format != StageFormatVersion || info.StoreID != o.StoreID || info.StageID != o.StageID ||
				info.MaxResources != o.MaxResources || info.MaxEncodedBytes != o.MaxEncodedBytes || info.Count > info.MaxResources ||
				info.EncodedBytes < stageMetadataBudget || info.EncodedBytes > info.MaxEncodedBytes || (info.Phase != "loading" && info.Phase != "frozen") {
				return ErrStageUnavailable
			}
			s.info = info
			if err := s.verifyInventory(ctx, tx); err != nil {
				return err
			}
			if info.Phase == "loading" {
				if info.CatalogDigest != "" {
					return ErrStageUnavailable
				}
			} else {
				digest, err := s.catalogDigest(ctx, tx)
				if err != nil || digest != info.CatalogDigest {
					return stageContextError(ctx, ErrStageUnavailable)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}

func stageIdentity(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}
func stageInitialDigest() string {
	sum := sha256.Sum256([]byte("cpra-encrypted-bootstrap-stage-v1"))
	return hex.EncodeToString(sum[:])
}
func stageNextDigest(previous string, key, entry []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(previous))
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(key)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(key)
	binary.BigEndian.PutUint64(size[:], uint64(len(entry)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(entry)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Stage) metaBinding() secureconfig.Binding {
	return secureconfig.Binding{StoreID: s.info.StoreID, Kind: "BootstrapStage", ID: s.info.StageID, UID: s.info.StageID, Revision: "1", Purpose: "bootstrap-stage-metadata"}
}
func (s *Stage) entryBinding(record persistence.CatalogRecord) secureconfig.Binding {
	binding := record.Binding(s.info.StoreID)
	binding.Purpose = "bootstrap-stage/" + s.info.StageID
	return binding
}
func (s *Stage) encodeInfo(ctx context.Context, info StageInfo) ([]byte, error) {
	plain, _ := json.Marshal(info)
	defer clear(plain)
	envelope, err := s.sealer.Seal(ctx, s.metaBinding(), plain)
	if err != nil {
		return nil, stageContextError(ctx, ErrStageUnavailable)
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > stageMetadataBudget {
		return nil, ErrStageUnavailable
	}
	return raw, nil
}
func stageKey(key persistence.CatalogKey) []byte {
	for index, kind := range ResourceKinds() {
		if key.Kind == kind {
			return []byte(fmt.Sprintf("%d:%s", index, key.ID))
		}
	}
	return nil
}
func stageEntryCost(key, entry []byte) uint64 { return uint64(len(entry) + 2*len(key) + 8 + 128) }
func stageStampPlain(sequence uint64, record []byte) []byte {
	sum := sha256.Sum256(record)
	out := make([]byte, 8+len(sum))
	binary.BigEndian.PutUint64(out, sequence)
	copy(out[8:], sum[:])
	return out
}
func stageContextError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
func (s *Stage) check() error {
	if s == nil || s.db == nil || s.failed {
		return ErrStageUnavailable
	}
	info, err := os.Lstat(s.path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, s.fileInfo) {
		s.failed = true
		return ErrStageUnavailable
	}
	return nil
}

// Add accepts a normalized create resource. Secret extraction belongs to the
// caller. Each successful append durably stores ciphertext and updates the
// authenticated inventory in the same transaction. Repeated IDs never overwrite.
func (s *Stage) Add(ctx context.Context, input api.Resource) error {
	if s == nil || ctx == nil {
		return ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.info.Phase != "loading" {
		return ErrStageFrozen
	}
	if s.info.Count == s.info.MaxResources {
		return ErrStageQuota
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > api.MaxResourceBytes {
		return ErrValidation
	}
	defer clear(raw)
	resource, err := api.DecodeResource(raw)
	if err != nil || !supportedKind(resource.Kind) || !validID(resource.Metadata.ID) || validateMetadata(resource.Metadata) != nil ||
		resource.Metadata.UID != "" || resource.Metadata.ResourceVersion != "" || resource.Metadata.Generation != 0 || len(resource.Status) != 0 {
		return ErrValidation
	}
	if resource.Kind == "Monitor" {
		if _, err := (manifest.Monitor{ID: resource.Metadata.ID}).EffectiveID(); err != nil {
			return ErrValidation
		}
	}
	if err := validateDesired(&resource); err != nil {
		return ErrValidation
	}
	key := persistence.CatalogKey{Kind: resource.Kind, ID: resource.Metadata.ID}
	encodedKey := stageKey(key)
	if err := s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(stageRecordsBucket).Get(encodedKey) != nil {
			return ErrStageDuplicate
		}
		return nil
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := persistence.CatalogRecord{Key: key, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: now, UpdatedAt: now}
	resource.Metadata.UID, resource.Metadata.ResourceVersion, resource.Metadata.Generation = record.UID, record.Revision, 1
	record.References, err = directReferences(resource)
	if err != nil {
		return ErrValidation
	}
	slices.SortFunc(record.References, func(a, b persistence.CatalogKey) int { return strings.Compare(a.Kind+"\x00"+a.ID, b.Kind+"\x00"+b.ID) })
	plain, err := json.Marshal(resource)
	if err != nil || len(plain) > secureconfig.MaxPlaintext {
		return ErrValidation
	}
	defer clear(plain)
	record.Payload, err = s.sealer.Seal(ctx, record.Binding(s.info.StoreID), plain)
	if err != nil {
		return stageContextError(ctx, ErrStageUnavailable)
	}
	recordJSON, _ := json.Marshal(record)
	sequence := s.info.Count + 1
	stampPlain := stageStampPlain(sequence, recordJSON)
	stamp, err := s.sealer.Seal(ctx, s.entryBinding(record), stampPlain)
	clear(stampPlain)
	if err != nil {
		return stageContextError(ctx, ErrStageUnavailable)
	}
	entry, err := json.Marshal(stageEntry{Sequence: sequence, Record: record, Stamp: stamp})
	if err != nil || len(entry) > stageMaxEntryBytes {
		return ErrStageQuota
	}
	cost := stageEntryCost(encodedKey, entry)
	if cost > s.info.MaxEncodedBytes-s.info.EncodedBytes {
		return ErrStageQuota
	}
	next := s.info
	next.Count++
	next.EncodedBytes += cost
	next.Digest = stageNextDigest(next.Digest, encodedKey, entry)
	meta, err := s.encodeInfo(ctx, next)
	if err != nil {
		return err
	}
	var orderKey [8]byte
	binary.BigEndian.PutUint64(orderKey[:], sequence)
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tx.Bucket(stageRecordsBucket).Put(encodedKey, entry); err != nil {
			return err
		}
		if err := tx.Bucket(stageOrderBucket).Put(orderKey[:], encodedKey); err != nil {
			return err
		}
		return tx.Bucket(stageMetaBucket).Put(stageMetaKey, meta)
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.failed = true
		}
		return stageContextError(ctx, ErrStageUnavailable)
	}
	s.info = next
	return nil
}

func (s *Stage) readEntry(ctx context.Context, key, raw []byte) (stageEntry, api.Resource, error) {
	var entry stageEntry
	if len(raw) == 0 || len(raw) > stageMaxEntryBytes || api.StrictDecode(raw, &entry) != nil {
		return entry, api.Resource{}, ErrStageUnavailable
	}
	record := entry.Record
	if !supportedKind(record.Key.Kind) || !validID(record.Key.ID) || !bytes.Equal(stageKey(record.Key), key) ||
		entry.Sequence == 0 || entry.Sequence > s.info.Count || record.Generation != 1 || record.Purpose != "desired-resource" ||
		record.Removed || record.CommittedIndex != 0 || record.DependentsVersion != 0 || record.CreatedAt.IsZero() || !record.UpdatedAt.Equal(record.CreatedAt) {
		return entry, api.Resource{}, ErrStageUnavailable
	}
	recordJSON, _ := json.Marshal(record)
	stamp, err := s.sealer.Open(ctx, s.entryBinding(record), entry.Stamp)
	if err != nil {
		return entry, api.Resource{}, stageContextError(ctx, ErrStageUnavailable)
	}
	wantStamp := stageStampPlain(entry.Sequence, recordJSON)
	validStamp := bytes.Equal(stamp, wantStamp)
	clear(stamp)
	clear(wantStamp)
	if !validStamp {
		return entry, api.Resource{}, ErrStageUnavailable
	}
	plain, err := s.sealer.Open(ctx, record.Binding(s.info.StoreID), record.Payload)
	if err != nil {
		return entry, api.Resource{}, stageContextError(ctx, ErrStageUnavailable)
	}
	defer clear(plain)
	var resource api.Resource
	if api.StrictDecode(plain, &resource) != nil || api.ValidateResource(resource) != nil || resource.Kind != record.Key.Kind || resource.Metadata.ID != record.Key.ID ||
		resource.Metadata.UID != record.UID || resource.Metadata.ResourceVersion != record.Revision || resource.Metadata.Generation != 1 || len(resource.Status) != 0 {
		return entry, api.Resource{}, ErrStageUnavailable
	}
	refs, err := directReferences(resource)
	slices.SortFunc(refs, func(a, b persistence.CatalogKey) int { return strings.Compare(a.Kind+"\x00"+a.ID, b.Kind+"\x00"+b.ID) })
	if err != nil || !slices.Equal(refs, record.References) {
		return entry, api.Resource{}, ErrStageUnavailable
	}
	return entry, resource, nil
}

// verifyInventory authenticates every row, index, append position and the record
// set digest. Row stamps alone cannot detect deletion or omission.
func (s *Stage) verifyInventory(ctx context.Context, tx *bolt.Tx) error {
	records, order := tx.Bucket(stageRecordsBucket), tx.Bucket(stageOrderBucket)
	if records == nil || order == nil {
		return ErrStageUnavailable
	}
	count, encoded := uint64(0), uint64(stageMetadataBudget)
	digest := stageInitialDigest()
	cursor := order.Cursor()
	for sequence, key := cursor.First(); sequence != nil; sequence, key = cursor.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > s.info.Count || len(sequence) != 8 || binary.BigEndian.Uint64(sequence) != count || key == nil {
			return ErrStageUnavailable
		}
		raw := records.Get(key)
		entry, resource, err := s.readEntry(ctx, key, raw)
		clear(resource.Spec)
		if err != nil || entry.Sequence != count {
			return stageContextError(ctx, ErrStageUnavailable)
		}
		cost := stageEntryCost(key, raw)
		if cost > s.info.MaxEncodedBytes-encoded {
			return ErrStageUnavailable
		}
		encoded += cost
		digest = stageNextDigest(digest, key, raw)
	}
	var rows uint64
	if err := records.ForEach(func(key, value []byte) error {
		rows++
		if rows > s.info.Count || value == nil {
			return ErrStageUnavailable
		}
		return nil
	}); err != nil {
		return err
	}
	if rows != count || count != s.info.Count || encoded != s.info.EncodedBytes || digest != s.info.Digest {
		return ErrStageUnavailable
	}
	return nil
}

// Freeze validates every resource and its bounded dependency closure. A failure
// leaves the entire stage inactive and loading. Successful freezing is immutable.
func (s *Stage) Freeze(ctx context.Context) (StageInfo, error) {
	if s == nil || ctx == nil {
		return StageInfo{}, ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return StageInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return StageInfo{}, err
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := s.verifyInventory(ctx, tx); err != nil {
			return err
		}
		cursor := tx.Bucket(stageRecordsBucket).Cursor()
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			if err := s.validateClosure(ctx, tx, key); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrStageUnavailable) {
			s.failed = true
		}
		return StageInfo{}, err
	}
	if s.info.Phase == "frozen" {
		return s.info, nil
	}
	next := s.info
	next.Phase = "frozen"
	err = s.db.View(func(tx *bolt.Tx) error {
		var err error
		next.CatalogDigest, err = s.catalogDigest(ctx, tx)
		return err
	})
	if err != nil {
		return StageInfo{}, err
	}
	meta, err := s.encodeInfo(ctx, next)
	if err != nil {
		return StageInfo{}, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return tx.Bucket(stageMetaBucket).Put(stageMetaKey, meta)
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.failed = true
		}
		return StageInfo{}, stageContextError(ctx, ErrStageUnavailable)
	}
	s.info = next
	return s.info, nil
}

func (s *Stage) validateClosure(ctx context.Context, tx *bolt.Tx, root []byte) error {
	resources := make(map[persistence.CatalogKey]api.Resource)
	defer func() {
		for _, r := range resources {
			clear(r.Spec)
		}
	}()
	queue := [][]byte{bytes.Clone(root)}
	seen := map[string]bool{string(root): true}
	total := 0
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := queue[0]
		queue = queue[1:]
		raw := tx.Bucket(stageRecordsBucket).Get(key)
		if raw == nil {
			return errors.Join(ErrValidation, persistence.ErrCatalogDependency)
		}
		total += len(raw)
		if total > 32<<20 {
			return ErrGraphLimit
		}
		entry, resource, err := s.readEntry(ctx, key, raw)
		if err != nil {
			return err
		}
		resources[entry.Record.Key] = resource
		for _, ref := range entry.Record.References {
			child := stageKey(ref)
			if seen[string(child)] {
				continue
			}
			if len(seen) >= maxValidationGraph {
				return ErrGraphLimit
			}
			seen[string(child)] = true
			queue = append(queue, child)
		}
	}
	notifications := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	var monitors []api.Monitor
	for _, r := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if validateMetadata(r.Metadata) != nil || validateDesired(&r) != nil {
			return ErrValidation
		}
		switch r.Kind {
		case "Monitor":
			var spec api.MonitorSpec
			if api.StrictDecode(r.Spec, &spec) != nil || ValidateMonitorSettings(spec) != nil {
				return ErrValidation
			}
			monitors = append(monitors, api.Monitor{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec})
		case "NotificationEndpoint":
			var spec api.DriverConfig
			if api.StrictDecode(r.Spec, &spec) != nil {
				return ErrValidation
			}
			notifications.Endpoints[r.Metadata.ID] = api.NotificationEndpoint{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		case "Recipient":
			var spec api.RecipientSpec
			if api.StrictDecode(r.Spec, &spec) != nil {
				return ErrValidation
			}
			notifications.Recipients[r.Metadata.ID] = api.Recipient{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		case "NotificationGroup":
			var spec api.NotificationGroupSpec
			if api.StrictDecode(r.Spec, &spec) != nil {
				return ErrValidation
			}
			notifications.Groups[r.Metadata.ID] = api.NotificationGroup{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		}
		if err := visitDrivers(&r, func(category string, driver *api.DriverConfig) error {
			if err := resolveDriver(driver, category, resources); err != nil {
				return ErrValidation
			}
			return ValidateResolvedDriver(category, *driver)
		}); err != nil {
			return ErrValidation
		}
	}
	if ValidateNotificationGraph(notifications, monitors) != nil {
		return ErrValidation
	}
	return nil
}

type stageCursor struct {
	StageID string `json:"stage"`
	Digest  string `json:"digest"`
	After   string `json:"after"`
}

// Page returns original encrypted catalog records in dependency order, then ID
// order within each kind. Reusing a cursor yields the same page after restart.
// Pages also stop at 4 MiB of encoded entries. The stage must be frozen;
// records are never decrypted into the public result.
func (s *Stage) Page(ctx context.Context, after string, limit int) ([]persistence.CatalogRecord, string, error) {
	if s == nil || ctx == nil {
		return nil, "", ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.info.Phase != "frozen" {
		return nil, "", ErrStageNotFrozen
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 || len(after) > 2048 {
		return nil, "", ErrValidation
	}
	var position stageCursor
	if after != "" {
		raw, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || api.StrictDecode(raw, &position) != nil || position.StageID != s.info.StageID || position.Digest != s.info.Digest || len(position.After) == 0 {
			return nil, "", ErrValidation
		}
	}
	var records []persistence.CatalogRecord
	pageBytes := 0
	next := ""
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(stageRecordsBucket)
		cursor := bucket.Cursor()
		key, value := cursor.First()
		if position.After != "" {
			key, value = cursor.Seek([]byte(position.After))
			if !bytes.Equal(key, []byte(position.After)) {
				return ErrValidation
			}
			key, value = cursor.Next()
		}
		for key != nil && len(records) < limit {
			if len(records) > 0 && pageBytes+len(value) > stageMaxPageBytes {
				break
			}
			pageBytes += len(value)
			if err := ctx.Err(); err != nil {
				return err
			}
			entry, resource, err := s.readEntry(ctx, key, value)
			clear(resource.Spec)
			if err != nil {
				return err
			}
			records = append(records, entry.Record)
			last := string(key)
			key, value = cursor.Next()
			if key != nil {
				raw, _ := json.Marshal(stageCursor{StageID: s.info.StageID, Digest: s.info.Digest, After: last})
				next = base64.RawURLEncoding.EncodeToString(raw)
			} else {
				next = ""
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrStageUnavailable) {
			s.failed = true
		}
		return nil, "", err
	}
	return records, next, nil
}

func (s *Stage) Info() (StageInfo, error) {
	if s == nil {
		return StageInfo{}, ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return StageInfo{}, err
	}
	return s.info, nil
}
func (s *Stage) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return ErrStageUnavailable
	}
	return nil
}

// catalogDigest follows the identical canonical ordering and digest algorithm
// used by deterministic Raft bootstrap admission. The caller authenticates the
// inventory first; this helper never exposes resource plaintext.
func (s *Stage) catalogDigest(ctx context.Context, tx *bolt.Tx) (string, error) {
	digest := persistence.BootstrapInitialDigest()
	err := tx.Bucket(stageRecordsBucket).ForEach(func(key, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > stageMaxEntryBytes {
			return ErrStageUnavailable
		}
		var entry stageEntry
		if api.StrictDecode(raw, &entry) != nil {
			return ErrStageUnavailable
		}
		next, err := persistence.BootstrapDigest(digest, entry.Record)
		if err != nil {
			return ErrStageUnavailable
		}
		digest = next
		return nil
	})
	return digest, err
}
