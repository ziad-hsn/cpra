package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCollectionSnapshotRestartWithoutOperations(t *testing.T) {
	for _, name := range []string{"empty", "authenticated", "monitor"} {
		t.Run(name, func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if s != nil {
					_ = s.Close()
				}
			})
			if name == "authenticated" {
				if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
					t.Fatal(err)
				}
			}
			if name == "monitor" {
				configure(t, s)
				submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: time.Now().UTC(), Outcome: "failure"})
			}
			monitor, found := s.Get("stable-one")
			history, err := s.History().Page("stable-one", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			assertNoLedger := func() {
				t.Helper()
				if name == "monitor" {
					// Retained actions now share the Store-owned ledger even when
					// the installation has never uploaded a collection.
					if s.fsm.collections == nil || s.fsm.image.ActionStorage == nil || s.fsm.image.ActionStorage.Sequence == 0 {
						t.Fatal("action-only ledger missing")
					}
					return
				}
				if _, err := os.Lstat(filepath.Join(config.Storage.Directory, "collections")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("store without collection operations created a disk ledger: %v", err)
				}
			}
			for cycle := 0; cycle < 3; cycle++ {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
				assertNoLedger()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				assertNoLedger()
				if err := s.collectionGenerations.maintain(context.Background()); err != nil {
					t.Fatal(err)
				}
				assertNoLedger()
				got, exists := s.Get("stable-one")
				if exists != found || !reflect.DeepEqual(got, monitor) {
					t.Fatal("restart changed monitor state")
				}
				page, err := s.History().Page("stable-one", "", 100)
				if err != nil || !reflect.DeepEqual(page.Events, history.Events) {
					t.Fatal("restart changed retained events", err)
				}
			}
			// A later import must allocate a disk ledger and remain recoverable.
			head := createCollectionFixture(t, s, 1)
			item := collectionItemFixture(t, s, head, 1, "after-restart")
			if result := uploadCollectionFixture(t, s, head, item, head.CreatedAt); result.Err != nil || !result.Allowed {
				t.Fatal("first collection upload failed", result.Err)
			}
			if s.fsm.collections == nil || s.fsm.collections.db == nil {
				t.Fatal("first collection did not create disk storage")
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			items, err := s.CollectionPage(head.ID, 0, 100)
			if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], item) {
				t.Fatal("first collection did not survive restart", err)
			}
		})
	}
}

func TestEmptyCollectionSnapshotRequiresCompleteFraming(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(dispatchSnapshotMagic)) {
		t.Fatal("fixture did not use the current monitor format")
	}
	for _, memory := range []bool{false, true} {
		name := "disk"
		if memory {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			directory := ""
			if !memory {
				directory = filepath.Join(t.TempDir(), "collections")
			}
			decoded, ledger, err := decodeSnapshot(bytes.NewReader(raw), directory)
			if ledger != nil {
				defer ledger.Close()
			}
			if err != nil || decoded.Version != DispatchFormatVersion || (ledger != nil) != memory {
				t.Fatal("empty snapshot did not preserve its storage mode", err)
			}
			if !memory {
				if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("empty snapshot allocated a disk generation", err)
				}
			}
		})
	}
	for _, namespace := range []struct {
		name  string
		magic []byte
	}{
		{"input", collectionLedgerMagic},
		{"plan", collectionPlanLedgerMagic},
		{"validation", collectionValidationLedgerMagic},
		{"execution", collectionExecutionLedgerMagic},
	} {
		t.Run(namespace.name, func(t *testing.T) {
			offset := bytes.Index(raw, namespace.magic)
			if offset < 0 {
				t.Fatal("mandatory namespace absent from fixture")
			}
			changed := bytes.Clone(raw)
			changed[offset+len(namespace.magic)+4] ^= 1
			row := bytes.Clone(raw)
			binary.BigEndian.PutUint32(row[offset+len(namespace.magic):], collectionLedgerMaxFrame)
			for name, broken := range map[string][]byte{
				"missing": raw[:offset], "truncated": raw[:offset+len(namespace.magic)],
				"footer": changed, "unexpected-row": row,
				"duplicate": append(bytes.Clone(raw), raw[offset:]...),
			} {
				t.Run(name, func(t *testing.T) {
					directory := filepath.Join(t.TempDir(), "collections")
					_, ledger, err := decodeSnapshot(bytes.NewReader(broken), directory)
					if ledger != nil {
						_ = ledger.Close()
					}
					if err == nil {
						t.Fatal("corrupt empty collection stream accepted")
					}
					if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("invalid empty snapshot allocated a disk generation", err)
					}
				})
			}
		})
	}
}
