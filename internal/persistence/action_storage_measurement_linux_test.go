//go:build linux

package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type actionFileUsage struct {
	Apparent  int64 `json:"apparent_bytes"`
	Allocated int64 `json:"allocated_bytes"`
	Files     int   `json:"files"`
}

func actionGenerationUsage(t *testing.T, directory string) actionFileUsage {
	t.Helper()
	var usage actionFileUsage
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return usage
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "generation-") {
			continue
		}
		for _, name := range []string{"ledger.db", "generation.json"} {
			info, err := os.Lstat(filepath.Join(directory, entry.Name(), name))
			if os.IsNotExist(err) && name == "generation.json" {
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				t.Fatal("unexpected measurement file", err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("native allocated-block accounting unavailable")
			}
			usage.Apparent += info.Size()
			usage.Allocated += stat.Blocks * 512
			usage.Files++
		}
	}
	return usage
}

func logActionStorageMeasurement(t *testing.T, phase string, s *Store, extra map[string]any) actionFileUsage {
	t.Helper()
	counts, _ := actionStorageFixtureInventory(t, s)
	usage := func() actionFileUsage {
		s.collectionGenerations.mu.Lock()
		defer s.collectionGenerations.mu.Unlock()
		return actionGenerationUsage(t, filepath.Join(s.config.Storage.Directory, "collections"))
	}()
	value := map[string]any{"phase": phase, "generation_files": usage, "action_rows": counts,
		"scope": "native Linux temporary fixture; allocated filesystem blocks, not Windows host free space"}
	for key, v := range extra {
		value[key] = v
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	return usage
}

func actionStorageFixtureInventory(t *testing.T, s *Store) (ActionStorageAccounting, uint64) {
	t.Helper()
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	counts, err := s.fsm.collections.actionAccounting()
	if err != nil {
		t.Fatal(err)
	}
	counts.Retired = s.fsm.image.ActionStorage.Retired
	counts.RetiredBytes = s.fsm.image.ActionStorage.RetiredBytes
	return counts, s.fsm.image.ActionStorage.Retired
}

func submitActionStorageFixture(t *testing.T, ctx context.Context, s *Store, commands []Command) {
	t.Helper()
	for len(commands) > 0 {
		count := min(64, len(commands))
		results, err := s.Submit(ctx, commands[:count])
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != count {
			t.Fatal("storage fixture result count", len(results), count)
		}
		for n, result := range results {
			grant := commands[n].Kind == "start" || commands[n].Kind == "action_retire"
			if result.Err != nil || grant && !result.Allowed {
				t.Fatal("storage fixture transition", commands[n].Kind, result.Err, "allowed", result.Allowed)
			}
		}
		commands = commands[count:]
	}
}

// Uses real committed configure/pulse/start/result transitions, without jobs,
// provider clients or external operations. The replay-only helper suppresses
// automatic maintenance so each measured publication boundary is explicit.
func actionStorageFixture(t *testing.T, ctx context.Context, config runtimeconfig.Config, ordinary int, at time.Time) (*Store, string) {
	t.Helper()
	s, err := openReplayStore(t, config)
	if err != nil {
		t.Fatal(err)
	}
	s.maintenanceContext = ctx
	commands := make([]Command, 0, ordinary+1)
	for n := 0; n <= ordinary; n++ {
		m := testMonitor()
		m.ID = fmt.Sprintf("storage-%04d", n)
		m.Policy = Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 1,
			RecoveryBypass: true, Endpoints: map[string]int{"red": 1}}
		commands = append(commands, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	}
	submitActionStorageFixture(t, ctx, s, commands)
	commands = commands[:0]
	for n := 0; n <= ordinary; n++ {
		commands = append(commands, Command{Kind: "pulse", MonitorID: fmt.Sprintf("storage-%04d", n), Revision: "r1", Generation: 1, Outcome: "failure", At: at.Add(time.Second)})
	}
	submitActionStorageFixture(t, ctx, s, commands)
	commands = commands[:0]
	actions := make([]Action, ordinary+1)
	for n := range actions {
		m, _ := s.Get(fmt.Sprintf("storage-%04d", n))
		actions[n] = notificationByColor(t, m, "red", Queued)
		commands = append(commands, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision,
			ActionID: actions[n].ID, NoticeRevision: ActionNoticeRevision(actions[n]), At: at.Add(2 * time.Second)})
	}
	submitActionStorageFixture(t, ctx, s, commands)
	commands = commands[:0]
	for n, action := range actions {
		outcome := "success"
		if n == ordinary {
			outcome = "failure"
		}
		commands = append(commands, Command{Kind: "result", MonitorID: fmt.Sprintf("storage-%04d", n), Revision: "r1",
			ActionID: action.ID, Outcome: outcome, Ambiguous: n == ordinary, At: at.Add(3 * time.Second)})
	}
	submitActionStorageFixture(t, ctx, s, commands)
	commands = commands[:0]
	for n := range actions {
		commands = append(commands, Command{Kind: "pulse", MonitorID: fmt.Sprintf("storage-%04d", n), Revision: "r1", Generation: 2, Outcome: "success", At: at.Add(4 * time.Second)})
	}
	submitActionStorageFixture(t, ctx, s, commands)
	if err := s.migrateActionProjection(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateNotificationConditions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.collectionGenerations.publish(s.fsm.collections); err != nil {
		t.Fatal(err)
	}
	s.collectionGenerations.track(s.fsm.collections)
	counts, err := s.fsm.collections.actionAccounting()
	if err != nil || counts.OrdinaryRetained != uint64(ordinary) || counts.Protected != 1 || counts.Active != 0 {
		t.Fatalf("unexpected committed fixture inventory: %+v %v", counts, err)
	}
	return s, actions[ordinary].ID
}

func retireActionStorageFixture(t *testing.T, ctx context.Context, s *Store, ordinary int, at time.Time) {
	t.Helper()
	after := ""
	for step := 0; step <= ordinary/64+2; step++ {
		selection, err := s.actionRetirementCandidates(at, after)
		if err != nil {
			t.Fatal(err)
		}
		if selection.command != nil {
			submitActionStorageFixture(t, ctx, s, []Command{{Kind: "action_retire", At: at, ActionRetirement: selection.command}})
		}
		after = selection.after
		if after == "" {
			break
		}
	}
	if s.fsm.image.ActionStorage.Retired != uint64(ordinary) {
		t.Fatalf("retired %d ordinary actions, expected %d", s.fsm.image.ActionStorage.Retired, ordinary)
	}
}

func copyActionStorageFixture(t *testing.T, ctx context.Context, s *Store) (*collectionLedger, image) {
	t.Helper()
	fence := actionCompactionFence(s.fsm.image)
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	candidate, err := openCollectionLedger(s.collectionGenerations.directory, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidate.Close() })
	s.collectionGenerations.live.Store(candidate.generation, candidate)
	if err := copyCollectionGeneration(ctx, fence, view, candidate); err != nil {
		t.Fatal(err)
	}
	return candidate, fence
}

func publishActionStorageFixture(t *testing.T, s *Store, candidate *collectionLedger, fence image) {
	t.Helper()
	_, cancel := context.WithCancel(t.Context())
	s.actionCompaction = &actionCompaction{source: s.fsm.collections, fence: fence, done: make(chan actionCompactionResult, 1), cancel: cancel}
	s.actionCompaction.done <- actionCompactionResult{ledger: candidate}
	if condition, err := s.finishActionCompaction(); err != nil || condition != "" {
		t.Fatal("replacement publication", condition, err)
	}
}

func TestActionCompactionMeasuresPhysicalReclamation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	config := testConfig(t)
	collectionGCTestPrivate(t, config.Storage.Directory)
	const ordinary = 256
	s, heldID := actionStorageFixture(t, ctx, config, ordinary, time.Now().UTC().Add(-100*24*time.Hour))
	defer s.Close()
	before := logActionStorageMeasurement(t, "before-retirement", s, nil)
	old := s.fsm.collections
	pin, err := old.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	retireActionStorageFixture(t, ctx, s, ordinary, time.Now().UTC())
	logical := logActionStorageMeasurement(t, "after-logical-retirement", s, nil)
	counts, err := s.fsm.collections.actionAccounting()
	if err != nil || counts.OrdinaryRetained != 0 || counts.Protected != 1 || logical.Allocated < before.Allocated {
		t.Fatal("logical deletion unexpectedly claimed physical reclamation", counts, before, logical, err)
	}
	for range 2 {
		candidate, fence := copyActionStorageFixture(t, ctx, s)
		publishActionStorageFixture(t, s, candidate, fence)
	}
	retained := logActionStorageMeasurement(t, "after-replacement-with-old-pin", s, nil)
	for range 4 {
		if err := s.collectionGenerations.maintain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(old.directory, old.generation, "ledger.db")); err != nil {
		t.Fatal("pinned generation was reclaimed", err)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	retireGenerationFixtures(t, s.collectionGenerations, 2)
	after := logActionStorageMeasurement(t, "after-old-generation-retirement", s, nil)
	// Keeping the current and previous generation can retain two sparse files
	// with a larger combined apparent size than the original single file. The
	// native allocated blocks must shrink, and removing the old pinned file
	// must reduce apparent size relative to that three-generation boundary.
	if after.Allocated >= logical.Allocated || after.Allocated >= retained.Allocated || after.Apparent >= retained.Apparent {
		t.Fatal("replacement did not reclaim fixture file allocation", logical, retained, after)
	}
	if got := snapshotAction(t, s.fsm.collections, heldID); got.State != Unknown || !got.Held() {
		t.Fatal("compaction lost the protected unknown action")
	}
	persistPausedReviewSnapshot(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reopened, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	elapsed := time.Since(started)
	held, found, err := reopened.Action(heldID)
	if err != nil || !found || !reopened.Status().Ready || held.Action.State != Unknown {
		t.Fatal("compacted state did not recover its protected action")
	}
	logActionStorageMeasurement(t, "reopened-after-compaction", reopened, map[string]any{"open_to_ready_ns": elapsed.Nanoseconds()})
}

func generationNames(t *testing.T, owner *collectionGenerations) map[string]bool {
	t.Helper()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	entries, err := os.ReadDir(owner.directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "generation-") {
			names[entry.Name()] = true
		}
	}
	return names
}

func TestActionSnapshotMeasuresSelectedReopen(t *testing.T) {
	for _, ordinary := range []int{0, 256, 1024} {
		t.Run(fmt.Sprint(ordinary), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			config := testConfig(t)
			collectionGCTestPrivate(t, config.Storage.Directory)
			s, heldID := actionStorageFixture(t, ctx, config, ordinary, time.Now().UTC().Add(-time.Minute))
			for range 3 {
				submitActionStorageFixture(t, ctx, s, []Command{{Kind: "barrier", At: time.Now().UTC()}})
				persistPausedReviewSnapshot(t, s)
			}
			snapshots, err := newSnapshotStore(config.Storage.Directory, 3, true)
			if err != nil {
				t.Fatal(err)
			}
			all, err := snapshots.List()
			if err != nil || len(all) != 3 {
				t.Fatal("fixture must contain three completed snapshots", err, len(all))
			}
			var snapshotBytes int64
			for _, meta := range all {
				snapshotBytes += meta.Size
			}
			before := generationNames(t, s.collectionGenerations)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			reopened, err := Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			elapsed := time.Since(started)
			if !reopened.Status().Ready {
				t.Fatal("reopen returned without ready storage")
			}
			created := 0
			for name := range generationNames(t, reopened.collectionGenerations) {
				if !before[name] {
					created++
				}
			}
			// Each full disk import allocates a fresh generation. Streaming checks
			// of the other snapshots must not construct two discarded ledgers.
			if created != 1 {
				t.Fatalf("selected restore constructed %d ledger generations, expected one", created)
			}
			counts, _ := actionStorageFixtureInventory(t, reopened)
			held, found, err := reopened.Action(heldID)
			if err != nil || !found || counts.OrdinaryRetained != uint64(ordinary) || counts.Protected != 1 || held.Action.State != Unknown {
				t.Fatal("reopen changed the committed action inventory", counts, err)
			}
			logActionStorageMeasurement(t, "three-snapshot-reopen", reopened, map[string]any{
				"ordinary_fixture_actions": ordinary, "retained_snapshots": len(all), "retained_snapshot_bytes": snapshotBytes,
				"selected_snapshot_bytes": all[0].Size, "constructed_generations": created, "open_to_ready_ns": elapsed.Nanoseconds(),
			})
		})
	}
}

const actionPublicationMarker = "action replacement ready:"

func TestActionCompactionPublicationProcessCrash(t *testing.T) {
	if directory := os.Getenv("CPRA_TEST_ACTION_PUBLICATION_DIR"); directory != "" {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		config := testConfig(t)
		config.Storage.Directory = directory
		s, heldID := actionStorageFixture(t, ctx, config, 8, time.Now().UTC().Add(-100*24*time.Hour))
		retireActionStorageFixture(t, ctx, s, 8, time.Now().UTC())
		persistPausedReviewSnapshot(t, s)
		candidate, fence := copyActionStorageFixture(t, ctx, s)
		switch os.Getenv("CPRA_TEST_ACTION_PUBLICATION_STAGE") {
		case "copy-complete":
		case "selector-published":
			// The exact production primitive between the synced candidate and
			// generations.json publication; no replacement hook is added.
			if err := candidate.Publish(); err != nil {
				t.Fatal(err)
			}
		case "catalog-published":
			publishActionStorageFixture(t, s, candidate, fence)
		default:
			t.Fatal("unknown process boundary")
		}
		fmt.Println(actionPublicationMarker + heldID)
		select {}
	}
	for _, stage := range []string{"copy-complete", "selector-published", "catalog-published"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			config := testConfig(t)
			collectionGCTestPrivate(t, config.Storage.Directory)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, executable, "-test.run=^TestActionCompactionPublicationProcessCrash$")
			command.Env = append(os.Environ(), "CPRA_TEST_ACTION_PUBLICATION_DIR="+config.Storage.Directory, "CPRA_TEST_ACTION_PUBLICATION_STAGE="+stage)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill() }()
			type childOutcome struct{ heldID, diagnostic string }
			ready := make(chan childOutcome, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				var lines []string
				for scanner.Scan() {
					if line := scanner.Text(); strings.HasPrefix(line, actionPublicationMarker) {
						ready <- childOutcome{heldID: strings.TrimPrefix(line, actionPublicationMarker)}
						return
					} else {
						lines = append(lines, line[:min(len(line), 4096)])
						if len(lines) > 8 {
							lines = lines[1:]
						}
					}
				}
				ready <- childOutcome{diagnostic: strings.Join(lines, "\n")}
			}()
			var heldID string
			select {
			case outcome := <-ready:
				heldID = outcome.heldID
				if heldID == "" {
					_ = command.Wait()
					t.Fatal("child did not reach publication boundary", outcome.diagnostic, stderr.String())
				}
			case <-ctx.Done():
				_ = command.Wait()
				t.Fatal("publication fixture exceeded its deadline")
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			reopened, err := Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			counts, retired := actionStorageFixtureInventory(t, reopened)
			if counts.OrdinaryRetained != 0 || counts.Protected != 1 || retired != 8 {
				t.Fatal("crash recovery changed retired/protected inventory", counts, retired)
			}
			if action, found, err := reopened.Action(heldID); err != nil || !found || action.Action.State != Unknown || !action.Held {
				t.Fatal("crash recovery lost the original unknown outcome")
			}
		})
	}
}
