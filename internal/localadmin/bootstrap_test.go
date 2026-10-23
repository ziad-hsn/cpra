//go:build linux

package localadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"gopkg.in/yaml.v3"
)

func stoppedBootstrapFixture(t *testing.T) BootstrapRequest {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "state")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	key := bytes.Repeat([]byte{41}, 32)
	keyFile := filepath.Join(root, "key", "wrapping.key")
	if err := secureconfig.WriteNewProtectedFile(t.Context(), secureconfig.ProtectedFileOptions{Path: keyFile, DataDirectory: directory, MaxBytes: 32}, key); err != nil {
		t.Fatal(err)
	}
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
	defer store.Close()
	wrapper, err := secureconfig.NewLocalWrapper(key)
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	stageDirectory := filepath.Join(directory, "management-bootstrap")
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return BootstrapRequest{DataDirectory: directory, RuntimeConfig: runtimeFile, Stage: "published", Actor: "local/admin", Reason: "Replace invalid first-import input"}
}

func TestBootstrapDiscardRecordsIntentAndCompletionOutsideState(t *testing.T) {
	request := stoppedBootstrapFixture(t)
	original, err := InspectBootstrap(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !original.Discardable || original.Admitted {
		t.Fatal("incorrect inspection", original)
	}
	request.Identity = original.Identity
	report, err := DiscardBootstrap(t.Context(), request)
	if err != nil || report.Outcome != "completed" {
		t.Fatal("discard not confirmed", err, report)
	}
	if _, err := os.Lstat(filepath.Join(request.DataDirectory, "management-bootstrap")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stage not removed", err)
	}
	if _, err := os.Stat(filepath.Join(request.DataDirectory, "raft.db")); err != nil {
		t.Fatal("Raft was touched", err)
	}
	var records [2]bootstrapAudit
	for i, phase := range []string{"intent", "completed"} {
		path := filepath.Join(report.AuditDirectory, report.AttemptID+"-"+phase+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &records[i]); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("audit was not protected", err)
		}
		if records[i].Phase != phase || records[i].Actor != request.Actor || records[i].Reason != request.Reason || records[i].Stage.Identity != original.Identity {
			t.Fatal("audit identity or assertion missing")
		}
	}
	if records[1].At.Before(records[0].At) {
		t.Fatal("completion preceded intent")
	}
}

func TestBootstrapDiscardCannotProceedWithoutProtectedAudit(t *testing.T) {
	request := stoppedBootstrapFixture(t)
	original, err := InspectBootstrap(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Identity = original.Identity
	request.AuditDirectory = filepath.Join(request.DataDirectory, "audit")
	report, err := DiscardBootstrap(t.Context(), request)
	if err == nil || report.Outcome != "not_started" {
		t.Fatal("missing audit permitted removal", err, report)
	}
	after, err := InspectBootstrap(t.Context(), request)
	if err != nil || after.Identity != original.Identity {
		t.Fatal("rejected audit changed stage", err)
	}
	request.Actor = "operator\nspoof"
	if _, err := DiscardBootstrap(t.Context(), request); err == nil || strings.Contains(err.Error(), request.Actor) {
		t.Fatal("unsafe audit actor accepted or echoed", err)
	}
}
