//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"gopkg.in/yaml.v3"
)

func TestLocalBootstrapCLIInspectAndConditionalDiscard(t *testing.T) {
	state, _ := localAuthTestPaths(t)
	root := filepath.Dir(state)
	key := bytes.Repeat([]byte{43}, 32)
	defer clear(key)
	keyFile := filepath.Join(root, "keys", "wrapping.key")
	if err := secureconfig.WriteNewProtectedFile(t.Context(), secureconfig.ProtectedFileOptions{Path: keyFile, DataDirectory: state, MaxBytes: 32}, key); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = state
	cfg.Storage.Encryption = &runtimeconfig.ManagementEncryption{Backend: "local", Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: keyFile}}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(runtimeFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	monitor := persistence.Monitor{ID: "existing-file-monitor", Name: "Existing file monitor", Revision: "before-discard", Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Unhealthy: 2, Healthy: 2}}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: time.Now().UTC()}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("cannot commit preserved-state fixture", err, results)
	}
	preserved, found := store.Get(monitor.ID)
	if !found {
		t.Fatal("preserved-state fixture is missing")
	}
	preservedJSON, err := json.Marshal(preserved)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := store.Status().NodeID
	wrapper, err := secureconfig.NewLocalWrapper(key)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	stageDirectory := filepath.Join(state, "management-bootstrap")
	if err := os.Mkdir(stageDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	stage, err := management.CreateStage(t.Context(), management.StageOptions{Directory: stageDirectory, StoreID: store.Status().NodeID, StageID: "startup-v1", MaxResources: 20, MaxEncodedBytes: 1 << 20}, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(remote.Close)
	t.Setenv("CPRA_AUTH_TOKEN_FILE", filepath.Join(root, "absent-api-token"))
	invoke := func(args ...string) (string, error) {
		t.Helper()
		command := append([]string{"--server", remote.URL, "local", "bootstrap"}, args...)
		command = append(command, "--data-dir", state, "--runtime-config", runtimeFile, "--stage", "published")
		out, stderr, err := runLocalAuth(t, command...)
		if strings.Contains(out+stderr, string(key)) {
			t.Fatal("bootstrap command exposed encryption material")
		}
		return out, err
	}
	if _, err := invoke("inspect"); err == nil {
		t.Fatal("inspection opened a running owner's store")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(state, "raft.db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := invoke("inspect")
	var inspected management.StartupStageReport
	if err != nil || json.Unmarshal([]byte(out), &inspected) != nil || !inspected.Discardable || inspected.Admitted || inspected.Phase != "loading" || inspected.StoreID != nodeID {
		t.Fatal("CLI did not identify the unadmitted loading stage", err)
	}
	for _, args := range [][]string{
		{"discard", "--identity", inspected.Identity, "--reason", "Correct first-import input"},
		{"discard", "--identity", inspected.Identity, "--actor", "local/admin"},
		{"discard", "--identity", strings.Repeat("0", 64), "--actor", "local/admin", "--reason", "Correct first-import input"},
	} {
		if _, err := invoke(args...); err == nil {
			t.Fatal("discard accepted missing audit input or a different identity")
		}
		if _, err := os.Stat(stageDirectory); err != nil {
			t.Fatal("rejected discard changed the stage", err)
		}
	}
	out, err = invoke("discard", "--identity", inspected.Identity, "--actor", "local/admin", "--reason", "Correct first-import input")
	var discarded localadmin.BootstrapDiscardReport
	if err != nil || json.Unmarshal([]byte(out), &discarded) != nil || discarded.Outcome != "completed" || discarded.Stage.Identity != inspected.Identity {
		t.Fatal("CLI did not confirm the exact stage discard", err)
	}
	if _, err := os.Lstat(stageDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed discard left the loading stage", err)
	}
	for _, phase := range []string{"intent", "completed"} {
		path := filepath.Join(discarded.AuditDirectory, discarded.AttemptID+"-"+phase+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var audit struct {
			Phase, Actor, Reason string
			Stage                management.StartupStageReport
		}
		if json.Unmarshal(data, &audit) != nil || audit.Phase != phase || audit.Actor != "local/admin" || audit.Reason != "Correct first-import input" || audit.Stage.Identity != inspected.Identity {
			t.Fatal("CLI discard audit lost its identity or assertion")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("CLI discard audit is not protected", err)
		}
	}
	if _, err := invoke("inspect"); err == nil {
		t.Fatal("inspection recreated an absent stage")
	}
	if _, err := os.Lstat(stageDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected inspection recreated an absent stage", err)
	}
	after, err := os.Stat(filepath.Join(state, "raft.db"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("stopped bootstrap commands replaced or removed the Raft database", err)
	}
	// Administrative opening elects the single node and may advance Raft metadata.
	// Compare committed application data rather than requiring identical DB bytes.
	reopened, err := persistence.OpenAdministrative(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, found := reopened.Get(monitor.ID)
	recoveredJSON, err := json.Marshal(recovered)
	if err != nil || !found || !bytes.Equal(preservedJSON, recoveredJSON) || reopened.Status().NodeID != nodeID {
		t.Fatal("stopped bootstrap commands changed committed state or installation identity", err)
	}
	if _, admitted := reopened.Bootstrap(); admitted {
		t.Fatal("stopped bootstrap commands admitted input")
	}
	if catalog, err := reopened.HasCatalog(); err != nil || catalog {
		t.Fatal("stopped bootstrap commands activated a catalog", err)
	}
	if requests.Load() != 0 {
		t.Fatal("local bootstrap commands contacted the API")
	}
}
