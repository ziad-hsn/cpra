package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	raftbolt "github.com/hashicorp/raft-boltdb/v2"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	bolt "go.etcd.io/bbolt"
)

func restoreBase(t *testing.T) (runtimeconfig.Config, string, string) {
	t.Helper()
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var commands []Command
	for n := 0; n < 300; n++ {
		m := testMonitor()
		m.ID = fmt.Sprintf("restore-monitor-%03d", n)
		m.Policy.Endpoints = map[string]int{"red": 1}
		commands = append(commands, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: now},
			Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: now, Outcome: "failure"})
	}
	for _, result := range submit(t, s, commands...) {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	m, _ := s.Get("restore-monitor-000")
	action := sortedActions(m.Actions)[0]
	if result := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action, At: now})[0]; !result.Allowed {
		t.Fatal("started fixture rejected")
	}
	commands = nil
	for n := 0; n < 260; n++ {
		r := catalogRecord(t, s, "Credential", fmt.Sprintf("receipt-%03d", n), uuid.NewString(), uuid.NewString(), "encrypted fixture")
		commands = append(commands, Command{Kind: "catalog", At: time.Now().UTC(), Catalog: &CatalogMutation{Create: true, Record: r, OperationID: r.Revision, Actor: "oncall"}})
	}
	for _, result := range submit(t, s, commands...) {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	command := authenticationBootstrap()
	command.LegacyTokenSHA256 = authenticationVerifier("old-read-token")
	if _, err := s.CommitAuthentication(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	nodeID := s.nodeID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return c, nodeID, action
}

func copyRestoreFixture(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
}

// openUnfinishedRestore is a test-only splice immediately after production
// openRaft's replay barrier and before finishRestore. It enables real-process
// interruption at each committed reset phase without a production fault hook.
func openUnfinishedRestore(t *testing.T, c runtimeconfig.Config) *Store {
	t.Helper()
	s := &Store{administrative: true, opening: true, config: c, executorSession: uuid.NewString(), requests: make(chan submission, 1024), stop: make(chan struct{}), done: make(chan struct{})}
	var err error
	s.log, err = raftbolt.New(raftbolt.Options{Path: filepath.Join(c.Storage.Directory, "raft.db"), BoltOptions: &bolt.Options{Timeout: time.Second}, NoSync: false})
	if err != nil {
		t.Fatal(err)
	}
	h, err := openHistoryWithCleanup(filepath.Join(c.Storage.Directory, "history"), false)
	if err != nil {
		t.Fatal(err)
	}
	s.fsm = &machine{image: image{Version: FormatVersion, Monitors: map[string]Monitor{}}, history: h}
	if err = s.openRaft(context.Background()); err != nil {
		t.Fatal(err)
	}
	go s.run()
	return s
}

func resetOnePage(t *testing.T, s *Store) {
	t.Helper()
	s.fsm.mu.RLock()
	state := s.fsm.image.Restore
	c := RestoreCommand{Marker: *s.restoreMarker, Phase: "begin"}
	if state != nil && state.Marker == *s.restoreMarker {
		c.Phase, c.After = state.Phase, state.After
	}
	s.fsm.mu.RUnlock()
	result := submit(t, s, Command{Kind: "restore_reset", At: c.Marker.At, Restore: &c})[0]
	if result.Err != nil || !result.Allowed {
		t.Fatal("reset page failed", result.Err)
	}
	if len(result.Events) > restorePageSize {
		t.Fatal("reset page exceeded bounded audit events", len(result.Events))
	}
}

func TestRestoreResetResumesEveryCommittedPhaseAfterProcessKill(t *testing.T) {
	base, nodeID, interrupted := restoreBase(t)
	const completeSteps = 1 + 300/restoreActionPageSize + 1 + 260/restorePageSize + 1
	for steps := 0; steps <= completeSteps; steps++ {
		t.Run(fmt.Sprintf("after-%d-pages", steps), func(t *testing.T) {
			c := testConfig(t)
			copyRestoreFixture(t, base.Storage.Directory, c.Storage.Directory)
			if err := MarkRestored(c.Storage.Directory, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			// Old releases check Version == 1 before creating Raft. The explicit
			// identity fence prevents them from ignoring RESTORE.json entirely.
			var node nodeIdentity
			if err := readOfflineJSON(filepath.Join(c.Storage.Directory, "identity.json"), &node); err != nil || node.Version != restoredIdentityVersion || node.Version == FormatVersion || node.ID != nodeID {
				t.Fatal("old-reader compatibility fence missing", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestoreResetProcessHelper$")
			child.Env = append(os.Environ(), "CPRA_RESTORE_PROCESS_HELPER="+c.Storage.Directory, "CPRA_RESTORE_PROCESS_STEPS="+strconv.Itoa(steps))
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			child.Stderr = &diagnostic
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill() })
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "RESET_PAGE_COMMITTED\n" {
				t.Fatal("reset fixture failed", line, err, diagnostic.String())
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			if steps < completeSteps {
				if lock, err := LockOffline(c.Storage.Directory); !errors.Is(err, ErrRestorePending) {
					if lock != nil {
						_ = lock.Close()
					}
					t.Fatal("partial restore was advertised as complete backup", err)
				}
			}
			if store, err := Open(context.Background(), c); !errors.Is(err, ErrAuthenticationResetRequired) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("normal startup bypassed reset", err)
			}
			admin := openAuthenticationAdmin(t, c)
			state, err := admin.Authentication()
			if err != nil || !state.ResetRequired || len(state.Principals) != 0 || state.LegacyTokenSHA256 != "" || state.AnonymousLoopback || admin.nodeID != nodeID {
				t.Fatal("restore resurrected authentication or changed node", err)
			}
			if epoch, err := admin.OperationEpoch(); err != nil || epoch != admin.restoreMarker.OperationEpoch || epoch == "" {
				t.Fatal("restore operation epoch not persisted", err)
			}
			for n := 0; n < 300; n++ {
				m, _ := admin.Get(fmt.Sprintf("restore-monitor-%03d", n))
				if !m.Incident || m.TotalChecks != 1 {
					t.Fatal("restore destroyed observation/incident facts")
				}
				for _, a := range m.Actions {
					want := Cancelled
					if a.ID == interrupted {
						want = Unknown
					}
					if a.State != want {
						t.Fatalf("restored external action can replay: %s", a.State)
					}
				}
			}
			if len(admin.fsm.image.Operations) != 0 || admin.fsm.image.Restore.Phase != "complete" {
				t.Fatal("old pending operations stayed executable")
			}
			page, err := admin.History().Page("resource/Credential/receipt-000", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range page.Events {
				if event.Operation != nil && event.Operation.InvalidatedByRestore == admin.restoreMarker.ID {
					found = true
					if event.Operation.State != "partial" || event.Operation.Outcome != "superseded" || event.Reason != "explicit_restore" {
						t.Fatal("restore misrepresented operation completion")
					}
				}
			}
			if !found {
				t.Fatal("old receipt evidence lost")
			}
			if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); !errors.Is(err, ErrAuthenticationConflict) {
				t.Fatal("restore permitted old bootstrap path", err)
			}
			provision := authenticationBootstrap()
			provision.Mode, provision.Epoch, provision.ExpectedEpoch, provision.ExpectedRevision = "provision", state.Epoch, state.Epoch, state.Revision
			provision.Principals[0].TokenSHA256 = authenticationVerifier("fresh-after-restore")
			if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
				t.Fatal(err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			resumed, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if resumed.nodeID != nodeID {
				t.Fatal("restore reprovision changed encryption binding")
			}
			if err := resumed.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(c.Storage.Directory)
			if err != nil {
				t.Fatal("completed restore cannot be backed up", err)
			}
			_ = lock.Close()
		})
	}
}

func TestRestoreResetProcessHelper(t *testing.T) {
	directory := os.Getenv("CPRA_RESTORE_PROCESS_HELPER")
	if directory == "" {
		t.Skip("subprocess fixture")
	}
	steps, err := strconv.Atoi(os.Getenv("CPRA_RESTORE_PROCESS_STEPS"))
	if err != nil {
		t.Fatal(err)
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = directory
	s := openUnfinishedRestore(t, c)
	for n := 0; n < steps; n++ {
		resetOnePage(t, s)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("RESET_PAGE_COMMITTED")
	select {}
}

func TestRestoreMarkerMissingCorruptOrMismatchedFailsClosed(t *testing.T) {
	base, _, _ := restoreBase(t)
	for _, fault := range []string{"missing", "truncated", "mismatch", "future"} {
		t.Run(fault, func(t *testing.T) {
			c := testConfig(t)
			copyRestoreFixture(t, base.Storage.Directory, c.Storage.Directory)
			if err := MarkRestored(c.Storage.Directory, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(c.Storage.Directory, restoreMarkerFile)
			switch fault {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				var marker RestoreMarker
				if err := readOfflineJSON(path, &marker); err != nil {
					t.Fatal(err)
				}
				if fault == "mismatch" {
					marker.ID = uuid.NewString()
				} else {
					marker.Version++
				}
				if err := atomicJSON(path, marker); err != nil {
					t.Fatal(err)
				}
			}
			if store, err := Open(context.Background(), c); !errors.Is(err, ErrRestoreInvalid) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("broken restore marker fell back to old authority", err)
			}
		})
	}
}

func TestRestoreSnapshotCannotClaimProvisionBeforeFencingComplete(t *testing.T) {
	c, _, _ := restoreBase(t)
	if err := MarkRestored(c.Storage.Directory, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s := openUnfinishedRestore(t, c)
	defer s.Close()
	resetOnePage(t, s)
	state, _ := s.Authentication()
	command := authenticationBootstrap()
	command.Mode, command.Epoch, command.ExpectedEpoch, command.ExpectedRevision = "provision", state.Epoch, state.Epoch, state.Revision
	if _, err := s.CommitAuthentication(context.Background(), command); !errors.Is(err, ErrAuthenticationResetRequired) {
		t.Fatal("provision admitted before action fencing", err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	i := snapshot.(*frozenSnapshot).image
	copy := i
	stateCopy := *i.Restore
	stateCopy.After = "zzzz-skipped-unfenced-work"
	copy.Restore = &stateCopy
	encoded, _ := json.Marshal(copy)
	if _, err := decodeImage(bytes.NewReader(encoded)); !errors.Is(err, ErrRestoreInvalid) {
		t.Fatal("snapshot cursor skipped unfenced actions", err)
	}
	i.Authentication.ResetRequired = false
	data, _ := json.Marshal(i)
	if _, err := decodeImage(bytes.NewReader(data)); !errors.Is(err, ErrRestoreInvalid) {
		t.Fatal("snapshot bypassed pending reset", err)
	}
}
