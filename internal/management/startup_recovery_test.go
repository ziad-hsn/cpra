package management

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestStartupInvalidInputCanBeCorrectedWithoutDeletingRaft(t *testing.T) {
	store, cfg := bootstrapTestStore(t, "raft", 1000)
	sealer := stageTestSealer(t, 107)
	opts := startupTestOptions(cfg.Storage.Directory)
	opts.Decode.SourceName = "payments.yaml"
	opts.OpenSource = func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("monitors:\n- name: payments\n  pulse_check: {type: http, interval: 60s, timeout: 5s, config: {url: https://example.test, methd: GET}}\n")), nil
	}
	before := store.Status().CommittedIndex
	_, err := StartupCatalog(t.Context(), store, sealer, opts)
	var fields *collection.InputErrors
	if err == nil || !errors.As(err, &fields) || !strings.Contains(err.Error(), "payments.yaml") {
		t.Fatalf("missing input diagnostic: %v", err)
	}
	if errors.Is(err, ErrStartupIncomplete) || errors.Is(err, ErrStartupCleanup) {
		t.Fatalf("ordinary typo retained a stage: %v", err)
	}
	for _, name := range []string{startupStageDirectory, startupLoadingDirectory} {
		if _, err := os.Lstat(filepath.Join(cfg.Storage.Directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed input left resumable staging", name, err)
		}
	}
	if store.Status().CommittedIndex != before {
		t.Fatal("invalid input submitted durable bootstrap")
	}
	opens := 0
	opts = startupTestOptions(cfg.Storage.Directory, stageMonitor("payments"))
	original := opts.OpenSource
	opts.OpenSource = func(ctx context.Context) (io.ReadCloser, error) { opens++; return original(ctx) }
	result, err := StartupCatalog(t.Context(), store, sealer, opts)
	if err != nil || !result.Bootstrap.CatalogActivated || opens != 1 {
		t.Fatal("corrected one-pass import failed", err, opens)
	}
}

func TestStoppedStartupDiscardRequiresExactUnadmittedLoadingStage(t *testing.T) {
	store, cfg := bootstrapTestStore(t, "raft", 1000)
	sealer := stageTestSealer(t, 108)
	opts := startupTestOptions(cfg.Storage.Directory, stageMonitor("first"))
	stage := makeStartupStage(t, store, sealer, opts, false)
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	inspect, err := InspectStartupStage(t.Context(), store, sealer, cfg.Storage.Directory, "published", CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := inspect.Report()
	if !first.Discardable || first.Phase != "loading" || first.Identity == "" {
		t.Fatal("loading stage not identified", first)
	}
	if err := inspect.Discard(t.Context(), strings.Repeat("0", 64)); !errors.Is(err, ErrStartupDiscard) {
		t.Fatal("wrong identity allowed", err)
	}
	if err := inspect.Discard(t.Context(), first.Identity); err != nil {
		t.Fatal(err)
	}
	if err := inspect.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.Directory, "raft.db")); err != nil {
		t.Fatal("Raft removed", err)
	}
	stage = makeStartupStage(t, store, sealer, opts, false)
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	inspect, err = InspectStartupStage(t.Context(), store, sealer, cfg.Storage.Directory, "published", CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close()
	if inspect.Report().Identity == first.Identity {
		t.Fatal("new stage reused old approval")
	}
	if err := inspect.Discard(t.Context(), first.Identity); !errors.Is(err, ErrStartupDiscard) {
		t.Fatal("stale approval allowed", err)
	}
}

func TestStartupInspectionRefusesUnsafeOrFrozenStage(t *testing.T) {
	for _, scenario := range []string{"frozen", "wrong-key", "extra-entry", "hard-link"} {
		t.Run(scenario, func(t *testing.T) {
			store, cfg := bootstrapTestStore(t, "raft", 1000)
			sealer := stageTestSealer(t, 109)
			opts := startupTestOptions(cfg.Storage.Directory, stageMonitor("first"))
			stage := makeStartupStage(t, store, sealer, opts, scenario == "frozen")
			if err := stage.Close(); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(cfg.Storage.Directory, startupStageDirectory)
			switch scenario {
			case "wrong-key":
				sealer = stageTestSealer(t, 110)
			case "extra-entry":
				if err := os.WriteFile(filepath.Join(directory, "unexpected"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(filepath.Join(directory, stageFileName), filepath.Join(cfg.Storage.Directory, "outside.db")); err != nil {
					t.Fatal(err)
				}
			}
			inspect, err := InspectStartupStage(t.Context(), store, sealer, cfg.Storage.Directory, "published", CatalogOptions{})
			if scenario != "frozen" {
				if err == nil {
					_ = inspect.Close()
					t.Fatal("unsafe stage accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer inspect.Close()
			if inspect.Report().Discardable || !errors.Is(inspect.Discard(t.Context(), inspect.Report().Identity), ErrStartupDiscard) {
				t.Fatal("frozen work was disposable")
			}
		})
	}
}

func TestFrozenTemporaryStartupPublishesWithoutReopeningInput(t *testing.T) {
	store, cfg := bootstrapTestStore(t, "raft", 1000)
	sealer := stageTestSealer(t, 111)
	opts := startupTestOptions(cfg.Storage.Directory, stageMonitor("first"))
	stage := makeStartupStage(t, store, sealer, opts, true)
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(cfg.Storage.Directory, startupStageDirectory), filepath.Join(cfg.Storage.Directory, startupLoadingDirectory)); err != nil {
		t.Fatal(err)
	}
	opts.OpenSource = noStartupSource(t)
	result, err := StartupCatalog(t.Context(), store, sealer, opts)
	if err != nil || !result.Bootstrap.CatalogActivated {
		t.Fatal("frozen temporary stage failed resume", err)
	}
}
