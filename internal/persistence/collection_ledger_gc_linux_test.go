package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

type collectionGCFixture struct {
	dir        string
	old        string
	protection collectionGCProtection
	mu         sync.Mutex
}

func newCollectionGCFixture(t *testing.T) *collectionGCFixture {
	t.Helper()
	f := &collectionGCFixture{dir: t.TempDir()}
	if err := os.Chmod(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.old = "generation-" + uuid.NewString()
	f.protection = collectionGCProtection{StoreID: uuid.NewString(), NodeID: uuid.NewString(),
		Current: "generation-" + uuid.NewString(), Previous: "generation-" + uuid.NewString()}
	for i, name := range []string{f.old, f.protection.Previous, f.protection.Current} {
		directory := filepath.Join(f.dir, name)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		// Real bbolt files exercise native identities and apparent byte reporting;
		// the GC deliberately does not certify the contents of their database pages.
		db, err := bolt.Open(filepath.Join(directory, "ledger.db"), 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		if err = atomicJSON(filepath.Join(directory, "generation.json"), collectionLedgerSelection{Version: collectionLedgerFormat, Generation: name}); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(1_700_000_000+int64(i), 0)
		if err = os.Chtimes(filepath.Join(directory, "generation.json"), at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicJSON(filepath.Join(f.dir, "current.json"), collectionLedgerSelection{Version: collectionLedgerFormat, Generation: f.protection.Current}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *collectionGCFixture) guard(ctx context.Context, use func(collectionGCProtection) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(f.protection)
}

func (f *collectionGCFixture) open(t *testing.T) *collectionRetirement {
	t.Helper()
	r, err := newCollectionRetirement(f.dir, f.guard)
	if errors.Is(err, errCollectionGCUnavailable) {
		t.Skip("native filesystem does not provide required statx identity/mount evidence")
	}
	if err != nil {
		var stat unix.Statx_t
		_ = unix.Statx(unix.AT_FDCWD, f.dir, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME|unix.STATX_MNT_ID, &stat)
		t.Fatalf("open retirement: %v (mode=%o uid=%d euid=%d mask=%x)", err, stat.Mode, stat.Uid, os.Geteuid(), stat.Mask)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func gcTestStep(t *testing.T, r *collectionRetirement, name string) collectionGCStep {
	t.Helper()
	for i := 0; i < 20; i++ {
		result, err := r.Step(context.Background(), name)
		if errors.Is(err, errCollectionGCBudget) {
			continue // The filesystem calls are not a hard real-time contract.
		}
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Fatal("step repeatedly exhausted cooperative work budget")
	return collectionGCStep{}
}

func (f *collectionGCFixture) assertProtected(t *testing.T) {
	t.Helper()
	for _, name := range []string{f.protection.Current, f.protection.Previous} {
		for _, leaf := range []string{"ledger.db", "generation.json"} {
			if _, err := os.Stat(filepath.Join(f.dir, name, leaf)); err != nil {
				t.Fatalf("protected generation lost %s: %v", leaf, err)
			}
		}
	}
}

func TestCollectionGCRetirementOneTransitionAndRestart(t *testing.T) {
	f := newCollectionGCFixture(t)
	var unlinked int64
	for _, want := range []string{"intent", "removed-ledger.db", "removed-generation.json", "removed-directory", "complete"} {
		r := f.open(t)
		step := gcTestStep(t, r, f.old)
		if step.Phase != want || step.Generation != f.old {
			t.Fatalf("step=%+v, want %s", step, want)
		}
		unlinked += step.UnlinkedBytes
		if want != "complete" {
			if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
				t.Fatal("retirement lost its external intent", err)
			}
		}
		if want == "intent" {
			if _, err := os.Stat(filepath.Join(f.dir, f.old, "ledger.db")); err != nil {
				t.Fatal("intent publication unlinked candidate", err)
			}
		}
		f.assertProtected(t)
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if unlinked <= 0 {
		t.Fatal("apparent unlink bytes not recorded")
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate directory remains", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed intent remains", err)
	}
	if step := gcTestStep(t, f.open(t), ""); step.Phase != "idle" {
		t.Fatal("empty continuation not idle", step)
	}
}

func TestCollectionGCRejectsProtectedAndIncomplete(t *testing.T) {
	for _, name := range []string{"current", "previous", "pinned", "missing-marker", "missing-db", "extra", "name", "newer", "wide-directory", "wide-file", "marker-schema", "marker-duplicate", "marker-large", "marker-trailing", "marker-identity", "symlink-directory", "symlink-db", "symlink-marker", "hardlink-db"} {
		t.Run(name, func(t *testing.T) {
			f := newCollectionGCFixture(t)
			candidate := f.old
			path := filepath.Join(f.dir, candidate)
			mutate := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "current":
				candidate = f.protection.Current
			case "previous":
				candidate = f.protection.Previous
			case "pinned":
				f.protection.Pinned = []string{candidate}
			case "missing-marker":
				mutate(os.Remove(filepath.Join(path, "generation.json")))
			case "missing-db":
				mutate(os.Remove(filepath.Join(path, "ledger.db")))
			case "extra":
				mutate(os.WriteFile(filepath.Join(path, "unknown"), []byte("retain"), 0600))
			case "name":
				candidate = "../" + candidate
			case "newer":
				mutate(os.Chtimes(filepath.Join(path, "generation.json"), time.Now(), time.Now()))
			case "wide-directory":
				mutate(os.Chmod(path, 0750))
			case "wide-file":
				mutate(os.Chmod(filepath.Join(path, "ledger.db"), 0644))
			case "marker-schema":
				mutate(os.WriteFile(filepath.Join(path, "generation.json"), []byte(`{"version":2,"generation":"`+candidate+`"}`), 0600))
			case "marker-duplicate":
				mutate(os.WriteFile(filepath.Join(path, "generation.json"), []byte(`{"version":0,"version":1,"generation":"`+candidate+`"}`), 0600))
			case "marker-large":
				mutate(os.WriteFile(filepath.Join(path, "generation.json"), bytes.Repeat([]byte(" "), 4097), 0600))
			case "marker-trailing":
				mutate(os.WriteFile(filepath.Join(path, "generation.json"), []byte(`{"version":1,"generation":"`+candidate+`"} {}`), 0600))
			case "marker-identity":
				mutate(atomicJSON(filepath.Join(path, "generation.json"), collectionLedgerSelection{Version: 1, Generation: f.protection.Current}))
			case "symlink-directory":
				mutate(os.Rename(path, path+"-original"))
				mutate(os.Symlink(f.protection.Current, path))
			case "symlink-db", "symlink-marker":
				leaf := "ledger.db"
				if name == "symlink-marker" {
					leaf = "generation.json"
				}
				mutate(os.Remove(filepath.Join(path, leaf)))
				mutate(os.Symlink(filepath.Join("..", f.protection.Current, leaf), filepath.Join(path, leaf)))
			case "hardlink-db":
				mutate(os.Remove(filepath.Join(path, "ledger.db")))
				mutate(os.Link(filepath.Join(f.dir, f.protection.Current, "ledger.db"), filepath.Join(path, "ledger.db")))
			}
			r := f.open(t)
			step, err := r.Step(context.Background(), candidate)
			if !errors.Is(err, errCollectionGCBlocked) {
				t.Fatal("unsafe candidate accepted", err)
			}
			if name == "name" && step.Generation != "" {
				t.Fatal("invalid candidate path escaped through diagnostic metadata")
			}
			if _, err := os.Lstat(filepath.Join(f.dir, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ineligible candidate acquired an intent", err)
			}
			f.assertProtected(t)
		})
	}
}

func TestCollectionGCInterruptedIntentRejectsChangedIdentity(t *testing.T) {
	for _, name := range []string{"node", "store", "intent-version", "intent-field", "intent-zero-identity", "intent-duplicate", "intent-truncated", "intent-symlink", "database-swap", "directory-swap", "marker-content", "new-extra", "selection", "pin-after-admission", "candidate-name"} {
		t.Run(name, func(t *testing.T) {
			f := newCollectionGCFixture(t)
			r := f.open(t)
			gcTestStep(t, r, f.old)
			candidate := f.old
			path := filepath.Join(f.dir, candidate)
			intentPath := filepath.Join(f.dir, collectionGCIntentName)
			mutate := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "node":
				f.protection.NodeID = uuid.NewString()
			case "store":
				f.protection.StoreID = uuid.NewString()
			case "intent-version", "intent-field", "intent-zero-identity":
				raw, err := os.ReadFile(intentPath)
				mutate(err)
				var value map[string]any
				mutate(json.Unmarshal(raw, &value))
				if name == "intent-version" {
					value["version"] = 2
				} else if name == "intent-field" {
					value["arbitrary"] = "unknown"
				} else {
					value["directory"] = map[string]any{}
				}
				mutate(atomicJSON(intentPath, value))
			case "intent-duplicate":
				raw, err := os.ReadFile(intentPath)
				mutate(err)
				mutate(os.WriteFile(intentPath, bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":0,"version":1`), 1), 0600))
			case "intent-truncated":
				mutate(os.WriteFile(intentPath, []byte(`{"version":`), 0600))
			case "intent-symlink":
				mutate(os.Rename(intentPath, intentPath+"-original"))
				mutate(os.Symlink("gc-retirement.json-original", intentPath))
			case "database-swap":
				mutate(os.Rename(filepath.Join(path, "ledger.db"), filepath.Join(f.dir, "saved-ledger")))
				mutate(os.WriteFile(filepath.Join(path, "ledger.db"), []byte("replacement"), 0600))
			case "directory-swap":
				mutate(os.Rename(path, path+"-original"))
				mutate(os.Mkdir(path, 0700))
			case "marker-content":
				mutate(os.WriteFile(filepath.Join(path, "generation.json"), []byte(`{ "version":1,"generation":"`+candidate+`"}`), 0600))
			case "new-extra":
				mutate(os.WriteFile(filepath.Join(path, "surprise"), []byte("retain"), 0600))
			case "selection":
				mutate(atomicJSON(filepath.Join(f.dir, "current.json"), collectionLedgerSelection{Version: 1, Generation: f.protection.Previous}))
			case "pin-after-admission":
				f.protection.Pinned = []string{candidate}
			case "candidate-name":
				candidate = "generation-" + uuid.NewString()
			}
			if _, err := r.Step(context.Background(), candidate); !errors.Is(err, errCollectionGCBlocked) {
				t.Fatal("changed retirement admitted", err)
			}
			if _, err := os.Lstat(intentPath); err != nil {
				t.Fatal("failed retirement discarded its intent", err)
			}
			f.assertProtected(t)
		})
	}
}

func TestCollectionGCFailedIntentSyncAuthorizesNoUnlink(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	fd, err := unix.Open(f.dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	realDir := r.dir
	badDir := os.NewFile(uintptr(fd), "pinned-root-without-fsync-capability")
	r.dir = badDir
	defer func() { _ = badDir.Close(); r.dir = realDir }()
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("failed directory fsync was not visible", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.old, "ledger.db")); err != nil {
		t.Fatal("failed intent publication deleted database", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); err != nil {
		t.Fatal("uncertain intent evidence was removed", err)
	}
	_ = r.dir.Close()
	r.dir = realDir
	if step := gcTestStep(t, r, ""); step.Phase != "removed-ledger.db" {
		t.Fatal("durably revalidated intent did not resume", step)
	}
}

func TestCollectionGCOwnershipCancellationAndOpenReader(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Step(ctx, f.old); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled step admitted", err)
	}
	r.guard = func(context.Context, func(collectionGCProtection) error) error {
		return errors.New("private-owner-path")
	}
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) || strings.Contains(err.Error(), "private") {
		t.Fatal("ownership failure lost safe classification", err)
	}
	r.guard = f.guard
	db, err := bolt.Open(filepath.Join(f.dir, f.old, "ledger.db"), 0600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	f.protection.Pinned = []string{f.old}
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("open reader generation removed", err)
	}
	if tx.ID() < 1 {
		t.Fatal("reader transaction invalidated")
	}
	f.assertProtected(t)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCUnavailable) {
		t.Fatal("closed primitive remained usable", err)
	}
}

func TestCollectionGCRootReplacementAndStrictNames(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	oldPath := f.dir + "-moved"
	if err := os.Rename(f.dir, oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(oldPath, f.dir) })
	if err := os.Mkdir(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.dir) })
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("renamed root used through stale pathname", err)
	}
	for _, name := range []string{"", "generation-00000000-0000-0000-0000-000000000000", strings.ToUpper(f.old), f.old + "/", "generation-{" + strings.TrimPrefix(f.old, "generation-") + "}"} {
		if collectionGCName(name) {
			t.Fatalf("unsafe generation name accepted: %q", name)
		}
	}
	if _, err := newCollectionRetirement("relative", f.guard); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("relative root accepted", err)
	}
}

func TestCollectionGCConcurrentStepsKeepSingleIntent(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	var wg sync.WaitGroup
	errorsFound := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Step(context.Background(), f.old)
			if err != nil && !errors.Is(err, errCollectionGCBudget) {
				errorsFound <- err
			}
		}()
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		// Calls after retirement completion cannot admit a missing directory.
		if !errors.Is(err, errCollectionGCBlocked) {
			t.Fatal(fmt.Errorf("concurrent step: %w", err))
		}
	}
	f.assertProtected(t)
}

func TestCollectionGCStrictSchemaAndInspectionBound(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	for _, raw := range []string{
		`{"version":0,"Version":1,"generation":"` + f.old + `"}`,
		`{"Version":1,"generation":"` + f.old + `"}`,
		`{"version":1,"generation":null}`,
		`{"version":1,"generation":"` + f.old + `","extra":0}`,
	} {
		var selection collectionLedgerSelection
		if err := collectionGCDecode([]byte(raw), &selection); !errors.Is(err, errCollectionGCBlocked) {
			t.Fatal("noncanonical private schema accepted")
		}
	}
	for _, raw := range []string{
		`{"deviceMajor":0,"deviceMinor":1,"mount":1,"inode":1,"birthSec":1}`,
		`{"deviceMajor":null,"deviceMinor":1,"mount":1,"inode":1,"birthSec":1,"birthNSec":0}`,
		`{"deviceMajor":0,"DeviceMajor":2,"deviceMinor":1,"mount":1,"inode":1,"birthSec":1,"birthNSec":0}`,
	} {
		var identity collectionGCIdentity
		if err := collectionGCDecode([]byte(raw), &identity); !errors.Is(err, errCollectionGCBlocked) {
			t.Fatal("incomplete or ambiguous native identity accepted")
		}
	}
	f.protection.Pinned = make([]string, 14)
	for i := range f.protection.Pinned {
		f.protection.Pinned[i] = f.protection.Current
	}
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("too many directory inspections admitted", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("over-budget protection set created intent", err)
	}
}

func TestCollectionGCRetentionTimestampTie(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	previousPath := filepath.Join(f.dir, f.protection.Previous, "generation.json")
	oldPath := filepath.Join(f.dir, f.old, "generation.json")
	at := time.Unix(1_700_000_000, 0)
	for _, path := range []string{previousPath, oldPath} {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if f.old > f.protection.Previous {
		if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
			t.Fatal("newer lexical tie was selected for retirement", err)
		}
	} else if step := gcTestStep(t, r, f.old); step.Phase != "intent" {
		t.Fatal("older lexical tie was not eligible", step)
	}
}
