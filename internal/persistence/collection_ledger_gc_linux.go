package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const collectionGCIntentName = "gc-retirement.json"

type collectionGCIntent struct {
	Version      int                  `json:"version"`
	StoreID      string               `json:"storeID"`
	NodeID       string               `json:"nodeID"`
	Generation   string               `json:"generation"`
	Root         collectionGCIdentity `json:"root"`
	Directory    collectionGCIdentity `json:"directory"`
	Database     collectionGCIdentity `json:"database"`
	Marker       collectionGCIdentity `json:"marker"`
	MarkerDigest string               `json:"markerDigest"`
}

type collectionGCCandidate struct {
	root       *os.Root
	dir        *os.File
	identity   collectionGCIdentity
	database   collectionGCIdentity
	marker     collectionGCIdentity
	dbInfo     os.FileInfo
	markerInfo os.FileInfo
	markerHash string
}

func (c *collectionGCCandidate) close() { _ = c.root.Close(); _ = c.dir.Close() }

func collectionGCUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func collectionGCName(name string) bool {
	return strings.HasPrefix(name, "generation-") && collectionGCUUID(strings.TrimPrefix(name, "generation-"))
}

// statx supplies mount identity even for same-device bind mounts. Birth time
// guards persisted intents against ordinary inode reuse. Missing kernel/filesystem
// support fails closed, including when the other identity fields are available.
func collectionGCNative(file *os.File, directory bool) (collectionGCIdentity, error) {
	var stat unix.Statx_t
	mask := uint32(unix.STATX_BASIC_STATS | unix.STATX_BTIME | unix.STATX_MNT_ID)
	if unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, int(mask), &stat) != nil || stat.Mask&mask != mask {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	want := uint16(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if stat.Mode&unix.S_IFMT != want || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) ||
		stat.Ino == 0 || stat.Mnt_id == 0 || !directory && stat.Nlink != 1 || directory && stat.Mode&0700 != 0700 {
		return collectionGCIdentity{}, errCollectionGCBlocked
	}
	identity := collectionGCIdentity{DeviceMajor: stat.Dev_major, DeviceMinor: stat.Dev_minor, Mount: stat.Mnt_id,
		Inode: stat.Ino, BirthSec: stat.Btime.Sec, BirthNSec: stat.Btime.Nsec}
	if !collectionGCValidIdentity(identity) {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	return identity, nil
}

func collectionGCSameMount(a, b collectionGCIdentity) bool {
	return a.Mount == b.Mount && a.DeviceMajor == b.DeviceMajor && a.DeviceMinor == b.DeviceMinor
}

func collectionGCValidIdentity(identity collectionGCIdentity) bool {
	return identity.Mount != 0 && identity.Inode != 0 && identity.BirthNSec < 1_000_000_000 &&
		(identity.BirthSec != 0 || identity.BirthNSec != 0)
}

func (i collectionGCIntent) valid(root collectionGCIdentity, p collectionGCProtection) bool {
	if i.Version != 1 || i.StoreID != p.StoreID || i.NodeID != p.NodeID || i.Root != root ||
		!collectionGCName(i.Generation) || !bootstrapHash(i.MarkerDigest) {
		return false
	}
	for _, identity := range []collectionGCIdentity{i.Directory, i.Database, i.Marker} {
		if !collectionGCValidIdentity(identity) || !collectionGCSameMount(root, identity) {
			return false
		}
	}
	return i.Directory != root && i.Database != i.Directory && i.Marker != i.Database && i.Marker != i.Directory
}

func newCollectionRetirement(directory string, guard collectionGCGuard) (_ *collectionRetirement, err error) {
	if guard == nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errCollectionGCBlocked
	}
	before, err := os.Lstat(directory)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errCollectionGCBlocked
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errCollectionGCBlocked
	}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	dir, err := root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errCollectionGCBlocked
	}
	defer func() {
		if err != nil {
			_ = dir.Close()
		}
	}()
	after, err := dir.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errCollectionGCBlocked
	}
	identity, err := collectionGCNative(dir, true)
	if err != nil {
		return nil, err
	}
	return &collectionRetirement{root: root, dir: dir, identity: identity, guard: guard}, nil
}

func collectionGCRead(root *os.Root, name string, mount collectionGCIdentity) ([]byte, os.FileInfo, collectionGCIdentity, error) {
	file, info, identity, err := collectionGCOpenFile(root, name, mount)
	if err != nil {
		return nil, nil, collectionGCIdentity{}, err
	}
	defer file.Close()
	if info.Size() < 1 || info.Size() > 4096 {
		return nil, nil, collectionGCIdentity{}, errCollectionGCBlocked
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 || int64(len(raw)) != info.Size() {
		return nil, nil, collectionGCIdentity{}, errCollectionGCBlocked
	}
	return raw, info, identity, nil
}

func collectionGCOpenFile(root *os.Root, name string, mount collectionGCIdentity) (*os.File, os.FileInfo, collectionGCIdentity, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, collectionGCIdentity{}, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, collectionGCIdentity{}, errCollectionGCBlocked
	}
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, collectionGCIdentity{}, errCollectionGCBlocked
	}
	after, err := file.Stat()
	identity, nativeErr := collectionGCNative(file, false)
	if err != nil || nativeErr != nil || !os.SameFile(before, after) || !collectionGCSameMount(mount, identity) {
		_ = file.Close()
		if errors.Is(nativeErr, errCollectionGCUnavailable) {
			return nil, nil, collectionGCIdentity{}, nativeErr
		}
		return nil, nil, collectionGCIdentity{}, errCollectionGCBlocked
	}
	return file, after, identity, nil
}

func collectionGCDecode(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(new(any)) != io.EOF {
		return errCollectionGCBlocked
	}
	// Require each schema field exactly once. Decoder's unknown-field check alone
	// still accepts duplicate keys, missing zero-valued fields and scalar nulls.
	var required []string
	switch output.(type) {
	case *collectionLedgerSelection:
		required = []string{"version", "generation"}
	case *collectionGCIdentity:
		required = []string{"deviceMajor", "deviceMinor", "mount", "inode", "birthSec", "birthNSec"}
	case *collectionGCIntent:
		required = []string{"version", "storeID", "nodeID", "generation", "root", "directory", "database", "marker", "markerDigest"}
	default:
		return errCollectionGCBlocked
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errCollectionGCBlocked
	}
	seen := make(map[string]bool, len(required))
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return errCollectionGCBlocked
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return errCollectionGCBlocked
		}
		if len(value) > 0 && value[0] == '{' && collectionGCDecode(value, new(collectionGCIdentity)) != nil {
			return errCollectionGCBlocked
		}
	}
	for _, name := range required {
		if !seen[name] {
			return errCollectionGCBlocked
		}
	}
	if len(seen) != len(required) {
		return errCollectionGCBlocked // encoding/json otherwise accepts case aliases.
	}
	_, err = decoder.Token()
	return err
}

func (r *collectionRetirement) openCandidate(name string, interrupted bool) (_ *collectionGCCandidate, err error) {
	if !collectionGCName(name) {
		return nil, errCollectionGCBlocked
	}
	before, err := r.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errCollectionGCBlocked
	}
	root, err := r.root.OpenRoot(name)
	if err != nil {
		return nil, errCollectionGCBlocked
	}
	candidate := &collectionGCCandidate{root: root}
	defer func() {
		if err != nil {
			_ = root.Close()
			if candidate.dir != nil {
				_ = candidate.dir.Close()
			}
		}
	}()
	candidate.dir, err = root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errCollectionGCBlocked
	}
	after, err := candidate.dir.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errCollectionGCBlocked
	}
	candidate.identity, err = collectionGCNative(candidate.dir, true)
	if err != nil {
		return nil, err
	}
	if !collectionGCSameMount(r.identity, candidate.identity) {
		return nil, errCollectionGCBlocked
	}
	names, err := candidate.dir.Readdirnames(3)
	if err != nil && err != io.EOF || len(names) > 2 {
		return nil, errCollectionGCBlocked
	}
	for _, leaf := range names {
		switch leaf {
		case "ledger.db":
			var file *os.File
			file, candidate.dbInfo, candidate.database, err = collectionGCOpenFile(root, leaf, r.identity)
			if file != nil {
				_ = file.Close()
			}
		case "generation.json":
			var raw []byte
			raw, candidate.markerInfo, candidate.marker, err = collectionGCRead(root, leaf, r.identity)
			if err == nil {
				var marker collectionLedgerSelection
				err = collectionGCDecode(raw, &marker)
				if err == nil && (marker.Version != collectionLedgerFormat || marker.Generation != name) {
					err = errCollectionGCBlocked
				}
				hash := sha256.Sum256(raw)
				candidate.markerHash = hex.EncodeToString(hash[:])
			}
		default:
			err = errCollectionGCBlocked
		}
		if err != nil {
			return nil, err
		}
	}
	if !interrupted && (candidate.dbInfo == nil || candidate.markerInfo == nil) || candidate.dbInfo != nil && candidate.markerInfo == nil {
		return nil, errCollectionGCBlocked
	}
	return candidate, nil
}

func collectionGCBefore(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().After(deadline) {
		return errCollectionGCBudget
	}
	return nil
}

// Step admits one retirement intent or unlinks at most one expected entry. It
// never scans the collection root or recursively deletes. candidate is required
// for admission and may be empty to resume an existing intent. Filesystem calls
// (especially Sync/Remove) cannot be forcibly canceled; context/budget checks are
// cooperative boundaries before work and immediately before each mutation.
func (r *collectionRetirement) Step(ctx context.Context, candidate string) (result collectionGCStep, err error) {
	if r == nil || ctx == nil {
		return result, errCollectionGCBlocked
	}
	deadline := time.Now().Add(25 * time.Millisecond)
	if !r.mu.TryLock() {
		return result, errCollectionGCBudget
	}
	defer r.mu.Unlock()
	if r.closed {
		return result, errCollectionGCUnavailable
	}
	if err = collectionGCBefore(ctx, deadline); err != nil {
		return result, err
	}
	called := false
	err = r.guard(ctx, func(protection collectionGCProtection) error {
		if called {
			return errCollectionGCBlocked
		}
		called = true
		var inside error
		result, inside = r.stepProtected(ctx, deadline, candidate, protection)
		return inside
	})
	if !called && err == nil {
		err = errCollectionGCBlocked
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, errCollectionGCUnavailable) && !errors.Is(err, errCollectionGCBudget) {
		err = errCollectionGCBlocked // Filesystem paths never become diagnostics.
	}
	return result, err
}

func (r *collectionRetirement) stepProtected(ctx context.Context, deadline time.Time, name string, p collectionGCProtection) (result collectionGCStep, err error) {
	// Two retained generations, thirteen pins, and one candidate stay within the
	// sixteen-directory inspection budget. Larger protection sets fail closed.
	if !collectionGCUUID(p.StoreID) || !collectionGCUUID(p.NodeID) || !collectionGCName(p.Current) || !collectionGCName(p.Previous) || p.Current == p.Previous || len(p.Pinned) > 13 {
		return result, errCollectionGCBlocked
	}
	rootIdentity, err := collectionGCNative(r.dir, true)
	if err != nil || rootIdentity != r.identity {
		return result, errCollectionGCBlocked
	}
	visible, err := os.Lstat(r.root.Name())
	anchoredRoot, statErr := r.dir.Stat()
	if err != nil || statErr != nil || !visible.IsDir() || !os.SameFile(visible, anchoredRoot) {
		return result, errCollectionGCBlocked
	}
	raw, _, _, err := collectionGCRead(r.root, "current.json", r.identity)
	var selected collectionLedgerSelection
	if err != nil || collectionGCDecode(raw, &selected) != nil || selected.Version != collectionLedgerFormat || selected.Generation != p.Current {
		return result, errCollectionGCBlocked
	}
	var intent *collectionGCIntent
	intentRaw, _, _, err := collectionGCRead(r.root, collectionGCIntentName, r.identity)
	if err == nil {
		intent = new(collectionGCIntent)
		if collectionGCDecode(intentRaw, intent) != nil || !intent.valid(r.identity, p) || name != "" && name != intent.Generation {
			return result, errCollectionGCBlocked
		}
		name = intent.Generation
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, errCollectionGCBlocked
	}
	if name == "" {
		result.Phase = "idle"
		return result, nil
	}
	if name == p.Current || name == p.Previous || !collectionGCName(name) {
		return result, errCollectionGCBlocked
	}
	result.Generation = name
	protected := make(map[collectionGCIdentity]bool, 2*(2+len(p.Pinned)))
	var previousTime time.Time
	for _, keep := range append([]string{p.Current, p.Previous}, p.Pinned...) {
		if keep == name {
			return result, errCollectionGCBlocked
		}
		if err := collectionGCBefore(ctx, deadline); err != nil {
			return result, err
		}
		known, err := r.openCandidate(keep, false)
		if err != nil {
			return result, err
		}
		protected[known.identity], protected[known.database] = true, true
		if keep == p.Previous {
			previousTime = known.markerInfo.ModTime()
		}
		known.close()
	}
	entry, err := r.openCandidate(name, intent != nil)
	if errors.Is(err, os.ErrNotExist) && intent != nil {
		return r.finishIntent(ctx, deadline, result, intentRaw)
	}
	if err != nil {
		return result, err
	}
	defer entry.close()
	if protected[entry.identity] || entry.dbInfo != nil && protected[entry.database] {
		return result, errCollectionGCBlocked
	}
	if intent == nil {
		// Selection of the latest completed previous generation is the scanner's
		// job. At minimum, reject a proposed candidate newer than its selected
		// retention boundary, including the deterministic equal-time tie breaker.
		if entry.markerInfo.ModTime().After(previousTime) || entry.markerInfo.ModTime().Equal(previousTime) && name > p.Previous {
			return result, errCollectionGCBlocked
		}
		intent = &collectionGCIntent{Version: 1, StoreID: p.StoreID, NodeID: p.NodeID, Generation: name, Root: r.identity,
			Directory: entry.identity, Database: entry.database, Marker: entry.marker, MarkerDigest: entry.markerHash}
		if err := collectionGCBefore(ctx, deadline); err != nil {
			return result, err
		}
		if err := r.publishIntent(intent); err != nil {
			return result, err
		}
		result.Phase = "intent"
		return result, nil
	}
	if entry.identity != intent.Directory || entry.dbInfo != nil && entry.database != intent.Database ||
		entry.markerInfo != nil && (entry.marker != intent.Marker || entry.markerHash != intent.MarkerDigest) {
		return result, errCollectionGCBlocked
	}
	// An earlier call may have written all bytes then failed its final sync.
	// Re-establish the external intent durably before every destructive transition.
	if err := r.syncIntent(intentRaw); err != nil {
		return result, err
	}
	if err := entry.dir.Sync(); err != nil {
		return result, err
	}
	if err := collectionGCBefore(ctx, deadline); err != nil {
		return result, err
	}
	leaf := "ledger.db"
	info := entry.dbInfo
	if info == nil {
		leaf, info = "generation.json", entry.markerInfo
	}
	if info != nil {
		current, err := entry.root.Lstat(leaf)
		if err != nil || !os.SameFile(info, current) {
			return result, errCollectionGCBlocked
		}
		if err := entry.root.Remove(leaf); err != nil {
			return result, err
		}
		result.UnlinkedBytes, result.Phase = info.Size(), "removed-"+leaf
		return result, entry.dir.Sync()
	}
	current, err := r.root.Lstat(name)
	anchored, statErr := entry.dir.Stat()
	if err != nil || statErr != nil || !os.SameFile(current, anchored) {
		return result, errCollectionGCBlocked
	}
	if err = r.root.Remove(name); err != nil {
		return result, err
	}
	result.Phase = "removed-directory"
	return result, r.dir.Sync()
}

func (r *collectionRetirement) publishIntent(intent *collectionGCIntent) error {
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > 4096 {
		return errCollectionGCBlocked
	}
	file, err := r.root.OpenFile(collectionGCIntentName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errCollectionGCBlocked
	}
	// Never delete an incomplete intent automatically. It cannot have authorized
	// an unlink and requires attention; silently replacing it would lose evidence.
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return r.dir.Sync()
}

func (r *collectionRetirement) syncIntent(expected []byte) error {
	file, info, _, err := collectionGCOpenFile(r.root, collectionGCIntentName, r.identity)
	if err != nil {
		return err
	}
	if info.Size() != int64(len(expected)) || len(expected) > 4096 {
		_ = file.Close()
		return errCollectionGCBlocked
	}
	actual, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || !bytes.Equal(actual, expected) {
		_ = file.Close()
		return errCollectionGCBlocked
	}
	return errors.Join(file.Sync(), file.Close(), r.dir.Sync())
}

func (r *collectionRetirement) finishIntent(ctx context.Context, deadline time.Time, result collectionGCStep, intentRaw []byte) (collectionGCStep, error) {
	if err := r.syncIntent(intentRaw); err != nil {
		return result, err
	}
	if err := collectionGCBefore(ctx, deadline); err != nil {
		return result, err
	}
	if err := r.root.Remove(collectionGCIntentName); err != nil {
		return result, err
	}
	result.Phase = "complete"
	return result, r.dir.Sync()
}
