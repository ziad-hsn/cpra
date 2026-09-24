package management

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const (
	reselectionRootDirectory = "collection-reselection"
	reselectionOwnershipFile = "ownership.db"
	reselectionRecordsFile   = "records.bin"
	reselectionFrameHeader   = 48
	reselectionFrameLimit    = 1 << 20
	reselectionRecordLimit   = 65_536
	reselectionBytesLimit    = 2 << 30
	reselectionFrameMagic    = "CPRARS01"
)

var (
	errReselectionSpool       = errors.New("encrypted reselection spool unavailable")
	errReselectionSpoolLocked = errors.New("encrypted reselection spool root already owned")
	errReselectionSpoolQuota  = errors.New("encrypted reselection spool quota exceeded")
	errReselectionSpoolRecord = errors.New("encrypted reselection spool record mismatch")
)

type reselectionSpoolRootOptions struct {
	MaxSpools         int
	MaxCleanupEntries int
	MaxCleanupBytes   int64
}

// The caller supplies its trusted, already private runtime directory, never an
// HTTP path. One small bbolt file supplies the existing dependency's portable
// exclusive OS lock. Only root acquisition performs stale-attempt cleanup;
// there is no API that opens old attempt data or recovers an encryption key.
// A first-creation crash before the ownership format is committed leaves an
// unmarked database. Reopening refuses that state; stopped operator inspection
// is required. Unknown ownership contents are never repaired or removed here.
type reselectionSpoolRoot struct {
	mu               *sync.Mutex
	directory        string
	directoryInfo    os.FileInfo
	ownershipInfo    os.FileInfo
	db               *bolt.DB
	maxSpools        int
	spools           map[*reselectionSpool]struct{}
	admissionBlocked bool // protected by mu; cleanup failure must not free capacity
	closed           *atomic.Bool
	cleanupEntries   int
	cleanupFileBytes int64
	closeDone        chan struct{}
	closeErr         error
}

func (reselectionSpoolRoot) String() string               { return "private encrypted reselection spool root" }
func (r reselectionSpoolRoot) GoString() string           { return r.String() }
func (r reselectionSpoolRoot) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }
func (reselectionSpoolRoot) MarshalJSON() ([]byte, error) { return nil, errReselectionSpool }

func newReselectionSpoolRoot(ctx context.Context, parent string, options reselectionSpoolRootOptions) (*reselectionSpoolRoot, error) {
	if ctx == nil || options.MaxSpools < 1 || options.MaxSpools > 64 || options.MaxCleanupEntries < 1 || options.MaxCleanupEntries > 1024 ||
		options.MaxCleanupBytes < 1 || options.MaxCleanupBytes > 128*int64(reselectionBytesLimit) {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkStartupDirectory(parent); err != nil {
		return nil, errReselectionSpool
	}
	directory := filepath.Join(parent, reselectionRootDirectory)
	created, err := createStartupDirectory(directory)
	if err != nil {
		return nil, errReselectionSpool
	}
	directoryBefore, err := os.Lstat(directory)
	if err != nil || !directoryBefore.IsDir() {
		return nil, errReselectionSpool
	}
	path := filepath.Join(directory, reselectionOwnershipFile)
	var before, opened os.FileInfo
	if before, err = os.Lstat(path); err == nil {
		if !before.Mode().IsRegular() || runtime.GOOS != "windows" && before.Mode().Perm()&0077 != 0 || created {
			return nil, errReselectionSpool
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errReselectionSpool
	}
	// A missing ownership file never grants authority to clean preexisting data.
	if before == nil {
		f, err := os.Open(directory)
		if err != nil {
			return nil, errReselectionSpool
		}
		entries, readErr := f.ReadDir(1)
		closeErr := f.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(entries) != 0 {
			return nil, errReselectionSpool
		}
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 25 * time.Millisecond, NoSync: false, NoGrowSync: false,
		OpenFile: func(name string, flags int, mode os.FileMode) (*os.File, error) {
			if before == nil {
				flags |= os.O_CREATE | os.O_EXCL
			} else {
				flags &^= os.O_CREATE
			}
			f, err := os.OpenFile(name, flags, 0600)
			if err != nil {
				return nil, err
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || before != nil && !os.SameFile(before, info) {
				_ = f.Close()
				return nil, errReselectionSpool
			}
			opened = info
			return f, nil
		}})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, errReselectionSpoolLocked
		}
		return nil, errReselectionSpool
	}
	r := &reselectionSpoolRoot{mu: &sync.Mutex{}, closed: &atomic.Bool{}, closeDone: make(chan struct{}), directory: directory, db: db, maxSpools: options.MaxSpools, spools: make(map[*reselectionSpool]struct{})}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	r.directoryInfo, err = os.Lstat(directory)
	if err != nil || !r.directoryInfo.IsDir() || !os.SameFile(directoryBefore, r.directoryInfo) {
		return nil, errReselectionSpool
	}
	r.ownershipInfo, err = os.Lstat(path)
	if err != nil || opened == nil || !r.ownershipInfo.Mode().IsRegular() || !os.SameFile(opened, r.ownershipInfo) {
		return nil, errReselectionSpool
	}
	marker := []byte("cpra.reselection.spool-root.v1")
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("owner"))
		if b == nil && before == nil {
			var err error
			b, err = tx.CreateBucket([]byte("owner"))
			if err != nil {
				return err
			}
			return b.Put([]byte("format"), marker)
		}
		if b == nil || !bytes.Equal(b.Get([]byte("format")), marker) {
			return errReselectionSpool
		}
		return nil
	})
	if err != nil {
		return nil, errReselectionSpool
	}
	if err := r.cleanup(ctx, options); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	ok = true
	return r, nil
}

func (r *reselectionSpoolRoot) check() error {
	if r == nil || r.closed == nil || r.closed.Load() {
		return errReselectionSpool
	}
	dir, err := os.Lstat(r.directory)
	if err != nil || !dir.IsDir() || !os.SameFile(dir, r.directoryInfo) {
		return errReselectionSpool
	}
	file, err := os.Lstat(filepath.Join(r.directory, reselectionOwnershipFile))
	if err != nil || !file.Mode().IsRegular() || !os.SameFile(file, r.ownershipInfo) {
		return errReselectionSpool
	}
	return nil
}

// Cleanup examines at most MaxCleanupEntries child directories before deleting
// anything. Byte accounting is exact regular-file length, not filesystem blocks,
// bbolt allocation, filesystem metadata, or an assurance of physical free space.
func (r *reselectionSpoolRoot) cleanup(ctx context.Context, options reselectionSpoolRootOptions) error {
	if err := r.check(); err != nil {
		return err
	}
	dir, err := os.Open(r.directory)
	if err != nil {
		return errReselectionSpool
	}
	entries, readErr := dir.ReadDir(options.MaxCleanupEntries + 2)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return errReselectionSpool
	}
	if len(entries) > options.MaxCleanupEntries+1 {
		return errReselectionSpoolQuota
	}
	var stale []reselectionSpoolFiles
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == reselectionOwnershipFile {
			continue
		}
		name := strings.TrimPrefix(entry.Name(), "attempt-")
		if name == entry.Name() || !canonicalUUID(name) {
			return errReselectionSpool
		}
		child := filepath.Join(r.directory, entry.Name())
		if err := checkStartupDirectory(child); err != nil {
			return errReselectionSpool
		}
		f, err := os.Open(child)
		if err != nil {
			return errReselectionSpool
		}
		childInfo, statErr := f.Stat()
		files, readErr := f.ReadDir(2)
		closeErr := f.Close()
		if statErr != nil || readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(files) > 1 {
			return errReselectionSpool
		}
		owned := reselectionSpoolFiles{directory: child, directoryInfo: childInfo}
		if len(files) == 1 {
			if files[0].Name() != reselectionRecordsFile {
				return errReselectionSpool
			}
			info, err := os.Lstat(filepath.Join(child, reselectionRecordsFile))
			if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
				return errReselectionSpool
			}
			if info.Size() > options.MaxCleanupBytes-total {
				return errReselectionSpoolQuota
			}
			total += info.Size()
			owned.fileInfo = info
		}
		stale = append(stale, owned)
	}
	for _, child := range stale {
		if err := r.check(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := child.remove(); err != nil {
			return errReselectionSpool
		}
	}
	r.cleanupEntries, r.cleanupFileBytes = len(stale), total
	return nil
}

func (r *reselectionSpoolRoot) Close() error {
	if r == nil || r.mu == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed.Swap(true) {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		return r.closeErr
	}
	spools := make([]*reselectionSpool, 0, len(r.spools))
	for s := range r.spools {
		spools = append(spools, s)
	}
	r.mu.Unlock()
	var failure bool
	for _, s := range spools {
		failure = s.Close() != nil || failure
	}
	failure = r.db.Close() != nil || failure
	r.mu.Lock()
	if failure {
		r.closeErr = errReselectionSpool
	}
	close(r.closeDone)
	r.mu.Unlock()
	return r.closeErr
}

type reselectionSpoolOptions struct {
	AttemptID         string
	MaxPlaintextBytes int64
	MaxEncodedBytes   int64
	MaxRecords        int
}

type reselectionSpoolPurpose byte

const (
	reselectionSource reselectionSpoolPurpose = 1
	reselectionSuffix reselectionSpoolPurpose = 2
)

// References contain opaque coordinates only and are valid for one open spool.
type reselectionSpoolRecord struct {
	ordinal, source, position uint64
	offset                    int64
	length                    int
	purpose                   reselectionSpoolPurpose
}

type reselectionSpoolAccounting struct {
	Records                      int
	PlaintextBytes, EncodedBytes int64
}

// Captured identities prevent cleanup from treating a later replacement as an
// owned artifact. Parent/child directories are private and the root OS lock
// excludes cooperative writers; a same-account adversary controlling directory
// entries concurrently is outside that ownership boundary.
type reselectionSpoolFiles struct {
	directory               string
	directoryInfo, fileInfo os.FileInfo
}

func (p reselectionSpoolFiles) remove() error {
	dir, err := os.Lstat(p.directory)
	if err != nil || p.directoryInfo == nil || !dir.IsDir() || !os.SameFile(dir, p.directoryInfo) {
		return errReselectionSpool
	}
	path := filepath.Join(p.directory, reselectionRecordsFile)
	file, err := os.Lstat(path)
	if p.fileInfo == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errReselectionSpool
		}
	} else {
		if err != nil || !file.Mode().IsRegular() || !os.SameFile(file, p.fileInfo) {
			return errReselectionSpool
		}
		if err := os.Remove(path); err != nil {
			return errReselectionSpool
		}
	}
	if err := os.Remove(p.directory); err != nil {
		return errReselectionSpool
	}
	return nil
}

// Disposable source and suffix data never uses the server's durable wrapping
// key. Closing clears explicit key bytes and drops cipher references; Go cannot
// guarantee erasure of cipher schedules or copies from process memory.
type reselectionSpool struct {
	mu             *sync.Mutex
	root           *reselectionSpoolRoot
	options        reselectionSpoolOptions
	directory      string
	directoryInfo  os.FileInfo
	file           *os.File
	fileInfo       os.FileInfo
	key            [32]byte
	aead           cipher.AEAD
	records        []reselectionSpoolRecord
	counts         reselectionSpoolAccounting
	failed, closed bool
	writeAt        func([]byte, int64) (int, error)
	syncFile       func() error
}

func (reselectionSpool) String() string {
	return "private encrypted reselection spool (contents omitted)"
}
func (s reselectionSpool) GoString() string           { return s.String() }
func (s reselectionSpool) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(s.String())) }
func (reselectionSpool) MarshalJSON() ([]byte, error) { return nil, errReselectionSpool }

func newReselectionSpool(ctx context.Context, root *reselectionSpoolRoot, options reselectionSpoolOptions) (*reselectionSpool, error) {
	if ctx == nil || root == nil || !canonicalUUID(options.AttemptID) || options.MaxRecords < 1 || options.MaxRecords > reselectionRecordLimit ||
		options.MaxPlaintextBytes < 1 || options.MaxPlaintextBytes > reselectionBytesLimit || options.MaxEncodedBytes < reselectionFrameHeader+28 || options.MaxEncodedBytes > reselectionBytesLimit {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if err := root.check(); err != nil {
		return nil, err
	}
	if root.admissionBlocked {
		return nil, errReselectionSpool
	}
	if len(root.spools) >= root.maxSpools {
		return nil, errReselectionSpoolQuota
	}
	s := &reselectionSpool{mu: &sync.Mutex{}, root: root, options: options, directory: filepath.Join(root.directory, "attempt-"+uuid.NewString())}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, errReselectionSpool
	}
	ok, ownsDirectory := false, false
	defer func() {
		if !ok {
			clear(s.key[:])
			s.aead = nil
			if s.file != nil {
				_ = s.file.Close()
			}
			if ownsDirectory && s.directoryInfo != nil {
				owned := reselectionSpoolFiles{directory: s.directory, directoryInfo: s.directoryInfo, fileInfo: s.fileInfo}
				if err := owned.remove(); err != nil {
					root.admissionBlocked = true
				}
			} else if ownsDirectory {
				root.admissionBlocked = true
			}
		}
	}()
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, errReselectionSpool
	}
	s.aead, err = cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, errReselectionSpool
	}
	created, err := createStartupDirectory(s.directory)
	ownsDirectory = created
	if err != nil || !created {
		return nil, errReselectionSpool
	}
	s.directoryInfo, err = os.Lstat(s.directory)
	if err != nil {
		return nil, errReselectionSpool
	}
	s.file, err = os.OpenFile(filepath.Join(s.directory, reselectionRecordsFile), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, errReselectionSpool
	}
	s.fileInfo, err = s.file.Stat()
	if err != nil || !s.fileInfo.Mode().IsRegular() {
		return nil, errReselectionSpool
	}
	s.writeAt, s.syncFile = s.file.WriteAt, s.file.Sync
	root.spools[s] = struct{}{}
	ok = true
	return s, nil
}

func (s *reselectionSpool) check() error {
	if s == nil || s.closed || s.failed || s.file == nil || s.aead == nil {
		return errReselectionSpool
	}
	if err := s.root.check(); err != nil {
		s.failed = true
		return err
	}
	dir, err := os.Lstat(s.directory)
	if err != nil || !dir.IsDir() || !os.SameFile(dir, s.directoryInfo) {
		s.failed = true
		return errReselectionSpool
	}
	info, err := os.Lstat(filepath.Join(s.directory, reselectionRecordsFile))
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(s.fileInfo, info) || info.Size() != s.counts.EncodedBytes {
		s.failed = true
		return errReselectionSpool
	}
	return nil
}

func (s *reselectionSpool) appendSource(ctx context.Context, source, offset uint64, plain []byte) (reselectionSpoolRecord, error) {
	return s.append(ctx, reselectionSource, source, offset, plain)
}
func (s *reselectionSpool) appendSuffix(ctx context.Context, ordinal uint64, plain []byte) (reselectionSpoolRecord, error) {
	return s.append(ctx, reselectionSuffix, 0, ordinal, plain)
}

func (s *reselectionSpool) append(ctx context.Context, purpose reselectionSpoolPurpose, source, position uint64, plain []byte) (reselectionSpoolRecord, error) {
	empty := reselectionSpoolRecord{}
	if ctx == nil || s == nil || len(plain) > reselectionFrameLimit || purpose == reselectionSource && (source == 0 || source > 1000 || position > reselectionBytesLimit) ||
		purpose == reselectionSuffix && (source != 0 || position == 0 || position > 10_000) || purpose != reselectionSource && purpose != reselectionSuffix {
		return empty, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return empty, err
	}
	cost := int64(reselectionFrameHeader + len(plain) + s.aead.Overhead())
	if len(s.records) >= s.options.MaxRecords || int64(len(plain)) > s.options.MaxPlaintextBytes-s.counts.PlaintextBytes || cost > s.options.MaxEncodedBytes-s.counts.EncodedBytes {
		return empty, errReselectionSpoolQuota
	}
	ref := reselectionSpoolRecord{ordinal: uint64(len(s.records) + 1), source: source, position: position, offset: s.counts.EncodedBytes, length: len(plain), purpose: purpose}
	header := ref.header()
	frame := make([]byte, reselectionFrameHeader, cost)
	copy(frame, header[:])
	frame = s.aead.Seal(frame, nil, plain, s.additionalData(ref, header))
	if err := ctx.Err(); err != nil {
		// A sealed but uncommitted frame still consumed this key's nonce
		// budget. Retire the spool instead of permitting uncounted seals.
		s.failed = true
		return empty, err
	}
	n, err := s.writeAt(frame, ref.offset)
	if err != nil || n != len(frame) {
		s.failed = true
		return empty, errReselectionSpool
	}
	if err := s.syncFile(); err != nil {
		s.failed = true
		return empty, errReselectionSpool
	}
	// Cancellation after I/O poisons the private spool; the caller cannot assume
	// rejection left no bytes or attempt a changed write at the same position.
	if err := ctx.Err(); err != nil {
		s.failed = true
		return empty, err
	}
	s.records = append(s.records, ref)
	s.counts.Records++
	s.counts.PlaintextBytes += int64(len(plain))
	s.counts.EncodedBytes += cost
	return ref, nil
}

func (r reselectionSpoolRecord) header() [reselectionFrameHeader]byte {
	var header [reselectionFrameHeader]byte
	copy(header[:8], reselectionFrameMagic)
	header[8] = byte(r.purpose)
	binary.BigEndian.PutUint64(header[16:24], r.ordinal)
	binary.BigEndian.PutUint64(header[24:32], r.source)
	binary.BigEndian.PutUint64(header[32:40], r.position)
	binary.BigEndian.PutUint32(header[40:44], uint32(r.length))
	return header
}

func (s *reselectionSpool) additionalData(ref reselectionSpoolRecord, header [reselectionFrameHeader]byte) []byte {
	data := make([]byte, 0, 128)
	data = append(data, "cpra.reselection.spool.v1\x00"...)
	data = append(data, s.options.AttemptID...)
	data = append(data, header[:]...)
	return binary.BigEndian.AppendUint64(data, uint64(ref.offset))
}

// withRecord borrows one authenticated plaintext frame. The callback must not
// retain the slice or reenter this spool/root. The buffer is cleared on every
// return, including panic. No full-attempt concatenation is provided.
func (s *reselectionSpool) withRecord(ctx context.Context, ref reselectionSpoolRecord, purpose reselectionSpoolPurpose, visit func([]byte) error) error {
	if ctx == nil || s == nil || visit == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if ref.purpose != purpose || ref.ordinal == 0 || ref.ordinal > uint64(len(s.records)) || ref != s.records[ref.ordinal-1] {
		return errReselectionSpoolRecord
	}
	frame := make([]byte, reselectionFrameHeader+ref.length+s.aead.Overhead())
	if _, err := s.file.ReadAt(frame, ref.offset); err != nil {
		s.failed = true
		return errReselectionSpool
	}
	header := ref.header()
	if !bytes.Equal(frame[:reselectionFrameHeader], header[:]) {
		s.failed = true
		return errReselectionSpool
	}
	plain, err := s.aead.Open(nil, nil, frame[reselectionFrameHeader:], s.additionalData(ref, header))
	if err != nil {
		s.failed = true
		return errReselectionSpool
	}
	defer clear(plain)
	if len(plain) != ref.length {
		s.failed = true
		return errReselectionSpool
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := visit(plain); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *reselectionSpool) accounting() (reselectionSpoolAccounting, error) {
	if s == nil {
		return reselectionSpoolAccounting{}, errReselectionSpool
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return reselectionSpoolAccounting{}, err
	}
	return s.counts, nil
}

func (s *reselectionSpool) Close() error {
	if s == nil || s.mu == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	clear(s.key[:])
	s.aead = nil
	s.records = nil
	failure := false
	if s.file != nil {
		failure = s.file.Close() != nil
		s.file = nil
	}
	s.writeAt, s.syncFile = nil, nil
	owned := reselectionSpoolFiles{directory: s.directory, directoryInfo: s.directoryInfo, fileInfo: s.fileInfo}
	if err := owned.remove(); err != nil {
		failure = true
	}
	s.mu.Unlock()
	s.root.mu.Lock()
	if failure {
		// Closed attempts have lost their key, but failed removal may leave
		// allocated ciphertext. Never treat that as newly available capacity.
		s.root.admissionBlocked = true
	}
	delete(s.root.spools, s)
	s.root.mu.Unlock()
	if failure {
		return errReselectionSpool
	}
	return nil
}
