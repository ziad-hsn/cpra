package persistence

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func bootstrapFixture(t *testing.T, s *Store) (BootstrapManifest, []CatalogRecord) {
	t.Helper()
	secret := catalogRecord(t, s, "Credential", "bootstrap-secret", "secret-uid", "secret-revision", "private secret")
	monitor := catalogRecord(t, s, "Monitor", "stable-one", "monitor-uid", "monitor-revision", "private monitor", secret.Key)
	records := []CatalogRecord{secret, monitor}
	for i := range records {
		// Payload identity excludes metadata times; keep fixture timestamps before
		// the process clock so this test also works across local/UTC date changes.
		records[i].CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		records[i].UpdatedAt = records[i].CreatedAt
	}
	digest := BootstrapInitialDigest()
	for _, record := range records {
		var err error
		digest, err = BootstrapDigest(digest, record)
		if err != nil {
			t.Fatal(err)
		}
	}
	return BootstrapManifest{StageID: "migration-stage", InventoryDigest: strings.Repeat("a", 64), CatalogDigest: digest, Count: uint64(len(records))}, records
}

func bootstrapSubmit(t *testing.T, s *Store, command BootstrapCommand) Result {
	t.Helper()
	return submit(t, s, Command{Kind: "bootstrap", At: time.Now().UTC(), Bootstrap: &command})[0]
}

func TestBootstrapKeepsPartialCatalogUnavailableAndPreservesIncident(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
	previous, _ := s.Get("stable-one")
	m, records := bootstrapFixture(t, s)
	begin := BootstrapCommand{Action: "begin", StageID: m.StageID, Manifest: &m}
	if result := bootstrapSubmit(t, s, begin); !result.Allowed || result.Err != nil {
		t.Fatal(result.Err)
	}
	if s.Status().Ready {
		t.Fatal("incomplete migration reported ready")
	}
	if _, err := s.CatalogSnapshot(); !errors.Is(err, ErrBootstrapPending) {
		t.Fatal("partial snapshot exposed", err)
	}
	if result := bootstrapSubmit(t, s, begin); !result.Allowed {
		t.Fatal("same begin identity was not resumable", result.Err)
	}
	for i := range records {
		if result := bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: m.StageID, Ordinal: uint64(i + 1), Record: &records[i]}); result.Err != nil || !result.Allowed {
			t.Fatal(result.Err)
		}
		if _, _, err := s.CatalogGet(records[i].Key); !errors.Is(err, ErrBootstrapPending) {
			t.Fatal("seed became visible before atomic activation", err)
		}
	}
	unrelated := catalogRecord(t, s, "Recipient", "other", "uid", "rv", "private")
	if result := catalogSubmit(t, s, CatalogMutation{Create: true, Record: unrelated}); !errors.Is(result.Err, ErrBootstrapPending) {
		t.Fatal("HTTP catalog admission interleaved with bootstrap", result.Err)
	}
	activate := BootstrapCommand{Action: "activate", StageID: m.StageID, Manifest: &m}
	if result := bootstrapSubmit(t, s, activate); !result.Allowed || result.Err != nil {
		t.Fatal(result.Err)
	}
	if !s.Status().Ready {
		t.Fatal("complete catalog never became available")
	}
	view, err := s.CatalogSnapshot()
	if err != nil || view.Len() != 2 {
		t.Fatal("activation did not publish entire input", err)
	}
	current, _ := s.Get("stable-one")
	if !current.Incident || current.Sequence != previous.Sequence || len(current.Actions) != len(previous.Actions) {
		t.Fatal("encrypted resource migration reset incident state")
	}
	before, _ := s.Bootstrap()
	if result := bootstrapSubmit(t, s, activate); !result.Allowed {
		t.Fatal("lost activation response was not idempotent")
	}
	after, _ := s.Bootstrap()
	if before != after {
		t.Fatal("repeated activation changed durable identity/timestamp")
	}
	old := requireCatalog(t, s, records[0].Key)
	if result := catalogSubmit(t, s, updateCatalogMutation(t, s, old, "api-edit", "new value")); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: m.StageID, Ordinal: 1, Record: &records[0]}); !errors.Is(result.Err, ErrBootstrapConflict) {
		t.Fatal("old startup seed could overwrite API edit", result.Err)
	}
}

func TestBootstrapRejectsDifferentInputMissingRecordsAndWrongDigest(t *testing.T) {
	for _, fault := range []string{"different stage", "out of order", "incomplete", "different ciphertext", "repeated seed"} {
		t.Run(fault, func(t *testing.T) {
			s := openCatalogMemory(t)
			manifest, records := bootstrapFixture(t, s)
			bootstrapSubmit(t, s, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest})
			var result Result
			switch fault {
			case "different stage":
				copy := manifest
				copy.StageID = "different"
				result = bootstrapSubmit(t, s, BootstrapCommand{Action: "begin", StageID: copy.StageID, Manifest: &copy})
			case "out of order":
				result = bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 2, Record: &records[1]})
			case "incomplete":
				result = bootstrapSubmit(t, s, BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest})
			case "repeated seed":
				seed := BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &records[0]}
				bootstrapSubmit(t, s, seed)
				result = bootstrapSubmit(t, s, seed)
			case "different ciphertext":
				changed := catalogRecord(t, s, "Credential", records[0].Key.ID, records[0].UID, records[0].Revision, "changed plaintext")
				changed.CreatedAt, changed.UpdatedAt = records[0].CreatedAt, records[0].UpdatedAt
				bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &changed})
				bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 2, Record: &records[1]})
				result = bootstrapSubmit(t, s, BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest})
			}
			if !errors.Is(result.Err, ErrBootstrapConflict) || s.Status().Ready {
				t.Fatal("unsafe migration became active", result.Err)
			}
		})
	}
}

func TestBootstrapSnapshotFreezesProgressAndRejectsTampering(t *testing.T) {
	s := openCatalogMemory(t)
	manifest, records := bootstrapFixture(t, s)
	bootstrapSubmit(t, s, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest})
	bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &records[0]})
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 2, Record: &records[1]})
	i := frozen.(*frozenSnapshot).image
	if i.Bootstrap.Applied != 1 || len(i.Catalog) != 1 {
		t.Fatal("background snapshot observed later bootstrap updates")
	}
	for _, change := range []func(*image){
		func(i *image) { i.Bootstrap.Applied++ },
		func(i *image) { i.Bootstrap.ProgressDigest = strings.Repeat("f", 64) },
		func(i *image) { i.Bootstrap.LastKey.ID = "wrong" },
		func(i *image) { i.Bootstrap.Phase = "active" },
	} {
		copy := i
		state := *i.Bootstrap
		copy.Bootstrap = &state
		change(&copy)
		raw, _ := json.Marshal(copy)
		if _, err := decodeImage(strings.NewReader(string(raw))); err == nil {
			t.Fatal("corrupt migration snapshot accepted")
		}
	}
}

func TestBootstrapCrashHelper(t *testing.T) {
	dir := os.Getenv("CPRA_BOOTSTRAP_CRASH_DIR")
	if dir == "" {
		return
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = dir
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	manifest, records := bootstrapFixture(t, s)
	bootstrapSubmit(t, s, BootstrapCommand{Action: "begin", StageID: manifest.StageID, Manifest: &manifest})
	bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 1, Record: &records[0]})
	point := os.Getenv("CPRA_BOOTSTRAP_CRASH_POINT")
	if point == "snapshot-prefix" {
		if err := s.Snapshot(); err != nil {
			t.Fatal(err)
		}
	}
	if point == "all-seeded" || point == "active-edited" {
		if r := bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: manifest.StageID, Ordinal: 2, Record: &records[1]}); r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if point == "active-edited" {
		if r := bootstrapSubmit(t, s, BootstrapCommand{Action: "activate", StageID: manifest.StageID, Manifest: &manifest}); r.Err != nil {
			t.Fatal(r.Err)
		}
		old := requireCatalog(t, s, records[0].Key)
		if r := catalogSubmit(t, s, updateCatalogMutation(t, s, old, "api-edited-version", "new secret after activation")); r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	// Parent receives ciphertext-only fixture data; there is no regeneration
	// after restart, exactly like reading the original encrypted stage.
	raw, _ := json.Marshal(struct {
		Manifest BootstrapManifest
		Records  []CatalogRecord
	}{manifest, records})
	fmt.Println(string(raw))
	select {}
}

func TestBootstrapActualProcessCrashResumesOnlyOriginalEncryptedInput(t *testing.T) {
	for _, point := range []string{"log-prefix", "snapshot-prefix", "all-seeded", "active-edited"} {
		t.Run(point, func(t *testing.T) { bootstrapCrashScenario(t, point) })
	}
}

func bootstrapCrashScenario(t *testing.T, point string) {
	c := testConfig(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestBootstrapCrashHelper$")
	cmd.Env = append(os.Environ(), "CPRA_BOOTSTRAP_CRASH_DIR="+c.Storage.Directory, "CPRA_BOOTSTRAP_CRASH_POINT="+point)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	lines := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); lines <- line }()
	var fixture struct {
		Manifest BootstrapManifest
		Records  []CatalogRecord
	}
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(line), &fixture); err != nil {
			t.Fatal("child failed before committing staged prefix", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("child did not commit migration prefix")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, ok := s.Bootstrap()
	expected := uint64(1)
	if point == "all-seeded" || point == "active-edited" {
		expected = 2
	}
	if !ok || state.Applied != expected {
		t.Fatal("restart lost partial migration boundary")
	}
	if point == "active-edited" {
		if state.Phase != "active" || !s.Status().Ready {
			t.Fatal("committed activation lost on crash")
		}
		if r := bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: state.Manifest.StageID, Ordinal: 1, Record: &fixture.Records[0]}); !errors.Is(r.Err, ErrBootstrapConflict) {
			t.Fatal("recovered active catalog accepted old startup seed", r.Err)
		}
		if current := requireCatalog(t, s, fixture.Records[0].Key); current.Revision != "api-edited-version" {
			t.Fatal("restart replaced later management edit")
		}
		return
	}
	if state.Phase != "seeding" || s.Status().Ready {
		t.Fatal("unactivated migration was reported available")
	}
	if state.Applied == 1 {
		if r := bootstrapSubmit(t, s, BootstrapCommand{Action: "seed", StageID: state.Manifest.StageID, Ordinal: 2, Record: &fixture.Records[1]}); r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if r := bootstrapSubmit(t, s, BootstrapCommand{Action: "activate", StageID: state.Manifest.StageID, Manifest: &fixture.Manifest}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if !s.Status().Ready {
		t.Fatal("original stage failed to complete")
	}
}
