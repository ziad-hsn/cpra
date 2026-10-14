//go:build linux || darwin || windows

package persistence

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestRestoreCollectionRetirementPhases(t *testing.T) {
	for _, phase := range []string{"intent", "removed-ledger.db", "removed-generation.json", "removed-directory", "recorded"} {
		t.Run(phase, func(t *testing.T) {
			f, original := collectionRestoreFixture(t, phase)
			before := collectionRestoreInventory(t, f.dir)
			directory := copyCollectionRestoreFixture(t, f.dir)
			copied := filepath.Join(directory, "collections")
			// The runtime path must still reject copied filesystem identities.
			r, err := newCollectionRetirement(copied, f.guard)
			if err != nil {
				t.Fatal(err)
			}
			_, stepErr := gcTestAttempt(t, r, "")
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(stepErr, errCollectionGCBlocked) {
				t.Fatal("ordinary startup accepted copied native identities", stepErr)
			}
			if err := restoreCollectionRetirement(directory, f.protection.NodeID); err != nil {
				t.Fatal("restore did not finish admitted retirement", err)
			}
			for _, name := range []string{f.old, collectionGCIntentName} {
				if _, err := os.Lstat(filepath.Join(copied, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("retirement was not finalized", name, err)
				}
			}
			root, err := os.OpenRoot(copied)
			if err != nil {
				t.Fatal(err)
			}
			var got collectionGenerationCatalog
			err = readCollectionGenerationJSON(root, collectionGenerationCatalogName, &got)
			_ = root.Close()
			if err != nil || got.validate(f.protection.NodeID) != nil || got.Retired != 8 || len(got.Completed) != 2 ||
				got.Current != f.protection.Current || got.Previous != f.protection.Previous || slices.Contains(got.Completed, f.old) ||
				got.UnlinkedBytes != original {
				t.Fatal("retirement catalog or accounting changed incorrectly", got, original, err)
			}
			after := collectionRestoreInventory(t, copied)
			for path, digest := range before {
				if path == collectionGCIntentName || path == collectionGenerationCatalogName || filepath.Dir(path) == f.old {
					continue
				}
				if after[path] != digest {
					t.Fatalf("restore changed protected file %s", path)
				}
			}
			if err := restoreCollectionRetirement(directory, f.protection.NodeID); err != nil ||
				!reflect.DeepEqual(after, collectionRestoreInventory(t, copied)) {
				t.Fatal("repeat preparation changed completed retirement", err)
			}
			if !reflect.DeepEqual(before, collectionRestoreInventory(t, f.dir)) {
				t.Fatal("preparing restore changed the source")
			}
		})
	}
}

func TestRestoreCollectionRetirementRejectsInvalidProof(t *testing.T) {
	for _, variant := range []string{"node", "store", "identity", "current", "previous", "selector", "catalog", "unlisted", "extra-file", "missing-marker", "marker-digest", "hardlink", "invalid-intent", "absent-unrecorded", "temporary", "temporary-only"} {
		t.Run(variant, func(t *testing.T) {
			f, _ := collectionRestoreFixture(t, "intent")
			directory := copyCollectionRestoreFixture(t, f.dir)
			copied := filepath.Join(directory, "collections")
			mutate := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			intentPath := filepath.Join(copied, collectionGCIntentName)
			catalogPath := filepath.Join(copied, collectionGenerationCatalogName)
			read := func(path string, target any) {
				t.Helper()
				raw, err := os.ReadFile(path)
				mutate(err)
				mutate(json.Unmarshal(raw, target))
			}
			var intent collectionGCIntent
			var catalog collectionGenerationCatalog
			read(intentPath, &intent)
			read(catalogPath, &catalog)
			switch variant {
			case "node":
				intent.NodeID = intent.StoreID
				mutate(atomicJSON(intentPath, intent))
			case "store":
				intent.StoreID = intent.NodeID
				mutate(atomicJSON(intentPath, intent))
			case "identity":
				intent.Root = collectionGCIdentity{}
				mutate(atomicJSON(intentPath, intent))
			case "current", "previous":
				intent.Generation = catalog.Current
				if variant == "previous" {
					intent.Generation = catalog.Previous
				}
				mutate(atomicJSON(intentPath, intent))
			case "selector":
				mutate(atomicJSON(filepath.Join(copied, "current.json"), collectionLedgerSelection{Version: 1, Generation: catalog.Previous}))
			case "catalog":
				mutate(os.Remove(catalogPath))
			case "unlisted", "absent-unrecorded":
				catalog.Completed = []string{catalog.Current, catalog.Previous}
				if variant == "absent-unrecorded" {
					catalog.Retired = 0
					for _, leaf := range []string{"ledger.db", "generation.json"} {
						mutate(os.Remove(filepath.Join(copied, f.old, leaf)))
					}
					mutate(os.Remove(filepath.Join(copied, f.old)))
				}
				mutate(atomicJSON(catalogPath, catalog))
			case "extra-file":
				mutate(os.WriteFile(filepath.Join(copied, f.old, "unrelated"), []byte("preserve"), 0600))
			case "missing-marker":
				mutate(os.Remove(filepath.Join(copied, f.old, "generation.json")))
			case "marker-digest":
				intent.MarkerDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				mutate(atomicJSON(intentPath, intent))
			case "hardlink":
				mutate(os.Remove(filepath.Join(copied, f.old, "ledger.db")))
				mutate(os.Link(filepath.Join(copied, catalog.Current, "ledger.db"), filepath.Join(copied, f.old, "ledger.db")))
			case "invalid-intent":
				mutate(os.WriteFile(intentPath, []byte(`{"version":1,"version":1}`), 0600))
			case "temporary", "temporary-only":
				mutate(os.WriteFile(filepath.Join(copied, collectionGCIntentTemporary), []byte("partial"), 0600))
				if variant == "temporary-only" {
					mutate(os.Remove(intentPath))
				}
			}
			before := collectionRestoreInventory(t, copied)
			if err := restoreCollectionRetirement(directory, f.protection.NodeID); err == nil {
				t.Fatal("invalid copied retirement was accepted")
			}
			if !reflect.DeepEqual(before, collectionRestoreInventory(t, copied)) {
				t.Fatal("invalid copied retirement changed files")
			}
		})
	}
}

func collectionRestoreFixture(t *testing.T, phase string) (*collectionGCFixture, int64) {
	t.Helper()
	f := newCollectionGCFixture(t)
	catalog := collectionGenerationCatalog{Version: 1, StoreID: f.protection.StoreID, NodeID: f.protection.NodeID,
		Current: f.protection.Current, Previous: f.protection.Previous, Completed: []string{f.old, f.protection.Previous, f.protection.Current}, Retired: 7}
	var original int64
	for _, leaf := range []string{"ledger.db", "generation.json"} {
		info, err := os.Stat(filepath.Join(f.dir, f.old, leaf))
		if err != nil {
			t.Fatal(err)
		}
		original += info.Size()
	}
	r := f.open(t)
	for _, want := range []string{"intent", "removed-ledger.db", "removed-generation.json", "removed-directory"} {
		step := gcTestStep(t, r, f.old)
		if step.Phase != want {
			t.Fatal("fixture retirement phase", step, want)
		}
		catalog.UnlinkedBytes += step.UnlinkedBytes
		if want == phase {
			break
		}
	}
	if phase == "recorded" {
		catalog.Completed = []string{f.protection.Previous, f.protection.Current}
		catalog.Retired++
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(f.dir, collectionGenerationCatalogName), catalog); err != nil {
		t.Fatal(err)
	}
	return f, original
}

func copyCollectionRestoreFixture(t *testing.T, source string) string {
	t.Helper()
	directory := t.TempDir()
	collectionGCTestPrivate(t, directory)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(directory, "collections", relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func collectionRestoreInventory(t *testing.T, directory string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[relative] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
