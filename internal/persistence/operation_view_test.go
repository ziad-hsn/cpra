package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func operationViewAll(t *testing.T, v OperationView, monitor string, limit int) []OperationReceipt {
	t.Helper()
	var all []OperationReceipt
	after := ""
	for range 10000 {
		page, next, err := v.Page(context.Background(), monitor, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if next == "" {
			return all
		}
		if next == after {
			t.Fatal("operation cursor made no progress")
		}
		after = next
	}
	t.Fatal("operation pagination did not terminate")
	return nil
}
func TestOperationViewFrozenLiveAndTerminalAcrossCompletion(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk-%t", disk), func(t *testing.T) {
			config := testConfig(t)
			if !disk {
				config.Storage.Mode = "memory"
			}
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			at := time.Now().UTC()
			reserved, _ := allocatedCommand(t, s, reservationCommand(t, s, "reserved-view", at))
			live, c := allocatedCommand(t, s, reservationCommand(t, s, "live-view", at))
			active := submit(t, s, c)[0]
			if active.Err != nil {
				t.Fatal(active.Err)
			}
			done, c := allocatedCommand(t, s, reservationCommand(t, s, "done-view", at))
			completed := submit(t, s, c)[0]
			if completed.Err != nil {
				t.Fatal(completed.Err)
			}
			completeReceipt(t, s, *completed.Operation, true)
			v, err := s.OperationSnapshot(time.Now().UTC(), 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			first, next, err := v.Page(context.Background(), "", "", 1)
			if err != nil || len(first) != 1 || next == "" {
				t.Fatal(first, next, err)
			}
			frozen := operationViewAll(t, v, "", 1)
			if len(frozen) != 3 {
				t.Fatal("initial membership", frozen)
			}
			completeReceipt(t, s, *active.Operation, true)
			added, c := allocatedCommand(t, s, reservationCommand(t, s, "after-view", time.Now().UTC()))
			r := submit(t, s, c)[0]
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			completeReceipt(t, s, *r.Operation, true)
			again := operationViewAll(t, v, "", 1)
			if !reflect.DeepEqual(again, frozen) {
				t.Fatal("frozen operation view changed")
			}
			seen := map[string]bool{}
			for _, item := range again {
				if seen[item.ID] {
					t.Fatal("duplicate operation", item.ID)
				}
				seen[item.ID] = true
				if item.ID == live.ID && item.State != "committed" {
					t.Fatal("live state drifted")
				}
				if item.ID == reserved.ID && item.CommittedIndex != 0 {
					t.Fatal("reservation fabricated commit")
				}
			}
			if !seen[done.ID] || seen[added.ID] {
				t.Fatal("terminal upper bound failed", seen)
			}
			filtered := operationViewAll(t, v, "done-view", 1)
			if len(filtered) != 1 || filtered[0].ID != done.ID {
				t.Fatal("monitor index filter", filtered)
			}
			first[0].Actor = "caller-mutated"
			retry, _, err := v.Page(context.Background(), "", "", 1)
			if err != nil || retry[0].Actor == "caller-mutated" {
				t.Fatal("returned page aliases snapshot")
			}
		})
	}
}
func TestOperationViewQuotaRetentionCloseAndLegacyFences(t *testing.T) {
	s := openCatalogMemory(t)
	c := reservationCommand(t, s, "quota-view", time.Now().UTC())
	_, _ = allocatedCommand(t, s, c)
	if _, err := s.OperationSnapshot(time.Now().UTC(), 0); !errors.Is(err, ErrOperationSnapshotQuota) {
		t.Fatal(err)
	}
	v, err := s.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if v.EstimatedBytes() < 512 {
		t.Fatal("snapshot omitted accounting")
	}
	if _, err := s.OperationSnapshot(time.Now().UTC(), v.EstimatedBytes()-1); !errors.Is(err, ErrOperationSnapshotQuota) {
		t.Fatal("byte quota ignored", err)
	}
	if _, err := s.OperationSnapshot(time.Now().UTC(), v.EstimatedBytes()); err != nil {
		t.Fatal("exact byte budget rejected", err)
	}
	createOperationResource(t, s, "legacy-view", "legacy-view-operation")
	if _, _, err := v.Page(context.Background(), "", "", 10); !errors.Is(err, ErrOperationCursorExpired) {
		t.Fatal("legacy progress silently changed view", err)
	}
	v, err = s.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.History().Expire(time.Now().UTC().Add(32 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Page(context.Background(), "", "", 10); !errors.Is(err, ErrOperationCursorExpired) {
		t.Fatal("retention did not fence view", err)
	}
	v, err = s.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Page(context.Background(), "", "", 10); !errors.Is(err, ErrOperationCursorExpired) {
		t.Fatal("closed view returned empty success", err)
	}
}

// appendOperationViewHistory creates real indexed terminal history, without
// provider work. It deliberately does not claim to be a controller throughput test.
func appendOperationViewHistory(t *testing.T, s *Store, receipts []OperationReceipt) {
	t.Helper()
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	index := s.fsm.image.Index + 1
	events := make([]Event, len(receipts))
	at := time.Now().UTC()
	for i, r := range receipts {
		e := receiptEvent(r)
		e.ID = fmt.Sprintf("%020d:%08d", index, i)
		events[i] = e
		if r.At.After(at) {
			at = r.At
		}
	}
	if err := s.fsm.history.append(index, events, at); err != nil {
		t.Fatal(err)
	}
	s.fsm.image.Index = index
}
func viewTerminal(id, monitor string, at time.Time) OperationReceipt {
	revision := id
	if !isLegacyOperation(id) {
		revision = uuid.NewString()
	}
	return OperationReceipt{ID: id, Key: CatalogKey{Kind: "Monitor", ID: monitor}, UID: uuid.NewString(), NewVersion: revision, Generation: 1, CommittedIndex: 1, Actor: "operator", At: at, UpdatedAt: at, State: "completed", Outcome: "applied"}
}
func TestOperationViewBoundedEmptyFrontierAndLegacyDedup(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	epoch, foreign := uuid.NewString(), uuid.NewString()
	s.fsm.mu.Lock()
	s.fsm.image.OperationEpoch = epoch
	s.fsm.image.OperationHighWater = 1
	s.fsm.mu.Unlock()
	rows := make([]OperationReceipt, 12000)
	for i := range rows {
		rows[i] = viewTerminal(operationHandle(foreign, uint64(i+1)), "old-epoch", at)
	}
	rows = append(rows, viewTerminal(operationHandle(epoch, 1), "current", at))
	appendOperationViewHistory(t, s, rows)
	v, err := s.OperationSnapshot(at, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := v.Page(context.Background(), "", "", 100)
	if err != nil || len(page) != 0 || next == "" {
		t.Fatal("bounded empty frontier absent", len(page), next, err)
	}
	retry, retryNext, err := v.Page(context.Background(), "", "", 100)
	if err != nil || !reflect.DeepEqual(page, retry) || retryNext != next {
		t.Fatal("bounded frontier was unstable", err)
	}
	all := operationViewAll(t, v, "", 100)
	if len(all) != 1 || all[0].Key.ID != "current" {
		t.Fatal("frontier lost retained item", all)
	}
	old := viewTerminal("legacy-reused", "legacy", at)
	newer := old
	newer.UpdatedAt = at.Add(time.Second)
	newer.State, newer.Outcome = "failed", "projection_failed"
	appendOperationViewHistory(t, s, []OperationReceipt{old, newer})
	v, err = s.OperationSnapshot(at.Add(time.Second), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	all = operationViewAll(t, v, "legacy", 1)
	if len(all) != 1 || all[0].Outcome != "projection_failed" {
		t.Fatal("legacy terminal duplicated", all)
	}
}
func createLegacyOperationIndexFixture(t *testing.T, s *Store, count int) {
	t.Helper()
	for start := 0; start < count; start += 128 {
		var commands []Command
		for i := start; i < min(count, start+128); i++ {
			id := fmt.Sprintf("indexed-%04d", i)
			record := catalogRecord(t, s, "Monitor", id, "uid-"+id, "legacy-"+id, "private")
			record.CreatedAt, record.UpdatedAt = time.Now().UTC(), time.Now().UTC()
			mutation := operationMutation(CatalogMutation{Record: record, Create: true})
			commands = append(commands, Command{Kind: "catalog", At: record.UpdatedAt, Catalog: &mutation})
		}
		results := submit(t, s, commands...)
		commands = nil
		for _, r := range results {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			op := r.Operation
			commands = append(commands, Command{Kind: "operation", At: time.Now().UTC(), Operation: &OperationUpdate{ID: op.ID, Key: op.Key, UID: op.UID, Revision: op.NewVersion, Applied: true}})
		}
		for _, r := range submit(t, s, commands...) {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
	}
}
func removeOperationIndexes(t *testing.T, path string) {
	t.Helper()
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{operationTerminalBucket, operationMonitorBucket, operationIndexMeta} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestOperationIndexMigrationResumesAndAdministrativeOpenDoesNotMigrate(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	createLegacyOperationIndexFixture(t, s, 300)
	var path string
	for day := range s.History().databases {
		path = filepath.Join(config.Storage.Directory, "history", day+".db")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeOperationIndexes(t, path)
	admin := openAuthenticationAdmin(t, config)
	v, err := admin.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Page(context.Background(), "indexed-0000", "", 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing derived index reported empty", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(operationIndexMeta) != nil {
			return errors.New("administrative open migrated history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done, err := migrateOperationIndexPage(context.Background(), db)
	if err != nil || done {
		t.Fatal("migration did not stop at bounded page", done, err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(operationIndexMeta)
		if len(meta.Get([]byte("after"))) == 0 || !bytes.Equal(meta.Get([]byte("complete")), []byte{0}) {
			return errors.New("migration progress not durable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err = s.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := operationViewAll(t, v, "", 37); len(got) != 300 {
		t.Fatal("resumed migration lost operations", len(got))
	}
	if got := operationViewAll(t, v, "indexed-0123", 2); len(got) != 1 {
		t.Fatal("monitor index mismatch", got)
	}
}
func TestOperationViewMissingOrCorruptIndexFails(t *testing.T) {
	for _, damage := range []string{"bucket", "primary", "segment"} {
		t.Run(damage, func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			createLegacyOperationIndexFixture(t, s, 1)
			v, err := s.OperationSnapshot(time.Now().UTC(), 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			for day, db := range s.History().databases {
				if damage == "segment" {
					if err := os.Remove(filepath.Join(config.Storage.Directory, "history", day+".db")); err != nil {
						t.Skip("cannot unlink open segment", err)
					}
				} else if err := db.Update(func(tx *bolt.Tx) error {
					if damage == "bucket" {
						return tx.DeleteBucket(operationTerminalBucket)
					}
					b := tx.Bucket(operationTerminalBucket)
					_, primary := b.Cursor().First()
					if primary == nil {
						return errors.New("terminal fixture missing")
					}
					return tx.Bucket([]byte("events")).Put(primary, []byte(`{"broken":true}`))
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := v.Page(context.Background(), "", "", 10); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("corruption became empty/success", err)
			}
		})
	}
}
func TestOperationViewRejectsMalformedCursorAndCancelledRead(t *testing.T) {
	s := openCatalogMemory(t)
	v, err := s.OperationSnapshot(time.Now().UTC(), 512)
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{"invalid", "h:future", "l:", "x:000", strings.Repeat("x", 300)} {
		if _, _, err := v.Page(context.Background(), "", after, 1); err == nil {
			t.Fatal("invalid cursor accepted", after)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := v.Page(ctx, "", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if bytes.Contains(raw, []byte("private")) {
		t.Fatal("private view serialized")
	}
}

func TestOperationIndexMigrationSurvivesProcessTermination(t *testing.T) {
	const envName = "CPRA_OPERATION_INDEX_MIGRATION_HELPER"
	if path := os.Getenv(envName); path != "" {
		db, err := bolt.Open(path, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		done, err := migrateOperationIndexPage(context.Background(), db)
		if err != nil || done {
			t.Fatal("partial migration", done, err)
		}
		fmt.Println("migration-page-committed")
		select {}
	}
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	createLegacyOperationIndexFixture(t, s, 300)
	var path string
	for day := range s.History().databases {
		path = filepath.Join(config.Storage.Directory, "history", day+".db")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeOperationIndexes(t, path)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOperationIndexMigrationSurvivesProcessTermination$")
	cmd.Env = append(os.Environ(), envName+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if line != "migration-page-committed" {
			t.Fatal("helper failed", line, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("migration helper timed out")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.OperationSnapshot(time.Now().UTC(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if rows := operationViewAll(t, v, "", 100); len(rows) != 300 {
		t.Fatal("crash migration lost terminal operations", len(rows))
	}
}
func TestOperationIndexMigrationRejectsCorruptProgress(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	createLegacyOperationIndexFixture(t, s, 300)
	var path string
	for day := range s.History().databases {
		path = filepath.Join(config.Storage.Directory, "history", day+".db")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeOperationIndexes(t, path)
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := migrateOperationIndexPage(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(operationIndexMeta).Put([]byte("after"), []byte("nonexistent-primary-position"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateOperationIndexPage(context.Background(), db); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("corrupt migration progress advanced", err)
	}
}

func TestOperationIndexMigrationRejectsForgedExistingProgress(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	createLegacyOperationIndexFixture(t, s, 300)
	var path string
	for day := range s.History().databases {
		path = filepath.Join(config.Storage.Directory, "history", day+".db")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeOperationIndexes(t, path)
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrateOperationIndexPage(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// A valid-looking primary key is insufficient proof that preceding events
	// were indexed. The completion pass must reject the omitted middle range.
	if err := db.Update(func(tx *bolt.Tx) error {
		last, _ := tx.Bucket([]byte("events")).Cursor().Last()
		return tx.Bucket(operationIndexMeta).Put([]byte("after"), last)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(context.Background(), config); !errors.Is(err, ErrHistoryUnavailable) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("forged progress hid earlier terminal records", err)
	}
}

// This context deterministically cancels during an application validation scan,
// rather than depending on scheduler timing or a large physical database.
type operationValidationContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *operationValidationContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestOperationIndexValidationHonorsCancellationDuringScan(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	createLegacyOperationIndexFixture(t, s, 10)
	for _, db := range s.History().databases {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		checking := &operationValidationContext{Context: ctx, cancel: cancel, remaining: 5}
		if err := db.View(func(tx *bolt.Tx) error {
			return validateOperationMonitorIndexContext(checking, tx)
		}); !errors.Is(err, context.Canceled) {
			t.Fatal("final validation ignored cancellation during its scan", err)
		}
		if err := validateHistoryOperationsContext(ctx, db); !errors.Is(err, context.Canceled) {
			t.Fatal("startup validation ignored cancellation", err)
		}
	}
}

func TestOperationViewRejectsTooManyRetainedSegments(t *testing.T) {
	s := openCatalogMemory(t)
	h := s.History()
	h.mu.Lock()
	for i := range maxOperationSegments + 1 {
		h.catalog.Segments[fmt.Sprintf("segment-%d", i)] = true
	}
	h.mu.Unlock()
	if _, err := s.OperationSnapshot(time.Now().UTC(), 16<<20); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("unbounded segment catalog accepted", err)
	}
}

func TestOperationValidationRejectsOversizedRetainedValues(t *testing.T) {
	for _, damaged := range []string{"terminal-primary", "legacy-latest", "general-primary"} {
		t.Run(damaged, func(t *testing.T) {
			db, err := bolt.Open(filepath.Join(t.TempDir(), "history.db"), 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			event := receiptEvent(viewTerminal("legacy-size", "size", time.Now().UTC()))
			event.ID = fmt.Sprintf("%020d:%08d", 1, 0)
			if err := validateOperationEvent(event); err != nil {
				t.Fatal("invalid size-test fixture", err)
			}
			if damaged == "general-primary" {
				event.Operation = nil
			}
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			max := maxOperationEventBytes
			if damaged == "general-primary" {
				max = maxHistoryEventBytes
			}
			// Valid JSON plus padding isolates the byte cap from JSON parse errors.
			padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), max+1-len(raw))...)
			if err := db.Update(func(tx *bolt.Tx) error {
				if damaged != "legacy-latest" {
					if err := ensureOperationIndex(tx); err != nil {
						return err
					}
				}
				primary, err := tx.CreateBucketIfNotExists([]byte("events"))
				if err != nil {
					return err
				}
				if err := primary.Put([]byte(event.MonitorID+"\x00"+event.ID), padded); err != nil {
					return err
				}
				if event.Operation == nil {
					return nil
				}
				latest, err := tx.CreateBucketIfNotExists([]byte("operations"))
				if err != nil {
					return err
				}
				if err := latest.Put([]byte(event.Operation.ID), padded); err != nil {
					return err
				}
				if damaged == "terminal-primary" {
					return writeOperationMonitorIndex(tx, event)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := validateHistoryOperations(db); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("startup accepted oversized retained value", err)
			}
			if damaged != "legacy-latest" {
				if err := db.View(validateOperationMonitorIndex); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("completed index validation accepted oversized value", err)
				}
			} else if _, err := migrateOperationIndexPage(context.Background(), db); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("migration accepted oversized operation", err)
			}
		})
	}
}
