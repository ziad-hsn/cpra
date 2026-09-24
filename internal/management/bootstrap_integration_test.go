package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func bootstrapTestStore(t *testing.T, mode string, batchSize int) (*persistence.Store, runtimeconfig.Config) {
	t.Helper()
	config := runtimeconfig.Default()
	config.Storage.Mode = mode
	config.Storage.Directory = t.TempDir()
	config.Storage.BatchSize = batchSize
	store, err := persistence.Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, config
}
func bootstrapTestStage(t *testing.T, store *persistence.Store, sealer *secureconfig.Sealer) (*Stage, StageOptions) {
	t.Helper()
	options := stageTestOptions(t)
	options.StoreID = store.Status().NodeID
	return createTestStage(t, options, sealer), options
}
func stageSource(t *testing.T, stage *Stage, source []byte) {
	t.Helper()
	ctx := context.Background()
	err := collection.Decode(ctx, bytes.NewReader(source), collection.DecodeOptions{SourceName: "bootstrap fixture"}, func(item collection.Item) error {
		normalized, credentials, err := ExtractInlineCredentials(item.Resource)
		if err != nil {
			return err
		}
		for _, credential := range credentials {
			if err := stage.Add(ctx, credential); err != nil {
				return err
			}
		}
		return stage.Add(ctx, normalized)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
}

// This wrapper forwards every command to a real persistence.Store, then can discard
// one response. It injects lost acknowledgments without replacing Raft/storage or
// any provider transport. The production entry point always receives *Store.
type observingBootstrapStore struct {
	*persistence.Store
	after        func([]persistence.Command)
	loseAction   string
	lost         bool
	submissions  int
	seedBatches  []int
	encodedBytes []int
	maxBytes     int
}

func (s *observingBootstrapStore) CommandLimits() (int, int) {
	count, size := s.Store.CommandLimits()
	if s.maxBytes != 0 {
		size = s.maxBytes
	}
	return count, size
}

func (s *observingBootstrapStore) Submit(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
	results, err := s.Store.Submit(ctx, commands)
	s.submissions++
	if err != nil {
		return results, err
	}
	if s.after != nil {
		s.after(commands)
	}
	if len(commands) > 0 && commands[0].Bootstrap.Action == "seed" {
		s.seedBatches = append(s.seedBatches, len(commands))
		size := 64
		for _, command := range commands {
			bound, err := persistence.CommandEncodedBound(command)
			if err != nil {
				return nil, err
			}
			size += bound + 1
		}
		s.encodedBytes = append(s.encodedBytes, size)
	}
	if !s.lost && len(commands) > 0 && commands[0].Bootstrap.Action == s.loseAction {
		s.lost = true
		return nil, errors.Join(persistence.ErrCommitUnconfirmed, context.Canceled)
	}
	return results, nil
}

func TestApplyBootstrapRaftEndToEndKeepsPartialCatalogUnavailable(t *testing.T) {
	ctx := context.Background()
	store, config := bootstrapTestStore(t, "raft", 2)
	sealer := stageTestSealer(t, 81)
	stage, _ := bootstrapTestStage(t, store, sealer)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { providerCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	secret := target.URL + "/notify?token=private-bootstrap-integration-secret"
	fixtures := stageFixture(secret)
	// Feed an inline endpoint through Decode -> ExtractInlineCredentials -> Add.
	// The generated private Credential is staged before its normalized endpoint.
	fixtures = fixtures[:4]
	fixtures[3] = resource("NotificationEndpoint", "hook", api.DriverConfig{Type: "webhook", Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, secret))})
	source, _ := json.Marshal(fixtures)
	stageSource(t, stage, source)
	info, _ := stage.Info()
	want, _, err := stage.Page(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	legacy := persistence.Monitor{ID: "api", Name: "old", Revision: "legacy-revision", Policy: persistence.Policy{Interval: time.Minute, Unhealthy: 1, Healthy: 1, Enabled: true, Endpoints: map[string]int{"red": 1}}}
	if _, err := store.Submit(ctx, []persistence.Command{{Kind: "configure", At: time.Now().UTC(), MonitorID: legacy.ID, Revision: legacy.Revision, Config: &legacy}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(ctx, []persistence.Command{{Kind: "pulse", At: time.Now().UTC(), MonitorID: legacy.ID, Revision: legacy.Revision, Generation: 1, Outcome: "failure"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Get("api")
	observer := &observingBootstrapStore{Store: store, after: func(commands []persistence.Command) {
		if commands[0].Bootstrap.Action != "activate" {
			if _, err := store.CatalogSnapshot(); !errors.Is(err, persistence.ErrBootstrapPending) {
				t.Fatalf("partial catalog exposed after %s: %v", commands[0].Bootstrap.Action, err)
			}
			if store.Status().Ready {
				t.Fatal("partial migration reported ready")
			}
		}
	}}
	progress, err := applyBootstrap(ctx, observer, stage)
	if err != nil || !progress.CatalogActivated || progress.SeededRecords != info.Count || progress.OutcomeUnconfirmed {
		t.Fatalf("bootstrap failed: %+v %v", progress, err)
	}
	for i, count := range observer.seedBatches {
		if count > config.Storage.BatchSize || observer.encodedBytes[i] > 4<<20 {
			t.Fatal("store admission limits exceeded")
		}
	}
	after, _ := store.Get("api")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("desired catalog activation changed owner incident projection")
	}
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Ready() {
		t.Fatal("bootstrap implicitly verified application catalog")
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := store.CatalogSnapshot()
	if err != nil || view.Len() != len(want) {
		t.Fatal("incomplete active catalog", err)
	}
	for _, expected := range want {
		got, ok := view.Get(expected.Key)
		if !ok {
			t.Fatal("missing seed", expected.Key)
		}
		got.CommittedIndex, got.DependentsVersion = 0, 0
		if !reflect.DeepEqual(got, expected) {
			t.Fatal("seed ciphertext or identity changed")
		}
		if expected.Key.Kind == "Credential" {
			public, err := catalog.Get(ctx, "Credential", expected.Key.ID)
			if err != nil || bytes.Contains(public.Spec, []byte("private-bootstrap-integration-secret")) || bytes.Contains(public.Spec, []byte(`"value"`)) {
				t.Fatal("credential readback leaked value", err)
			}
		}
	}
	readView, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := catalog.PrepareRuntime(ctx, readView, "api")
	if err != nil || len(prepared.NotificationTargets["red"]) != 1 {
		t.Fatal("shared routing or credentials not usable", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatal("bootstrap invoked provider")
	}
	err = filepath.WalkDir(config.Storage.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte("private-bootstrap-integration-secret")) {
			t.Fatal("plaintext provider value entered Raft storage")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApplyBootstrapResumesOriginalStageAfterLostReplies(t *testing.T) {
	for _, action := range []string{"begin", "seed", "activate"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store, config := bootstrapTestStore(t, "raft", 1)
			sealer := stageTestSealer(t, 82)
			stage, options := bootstrapTestStage(t, store, sealer)
			source, _ := json.Marshal(stageFixture("https://example.test/notify?token=retained-private-credential"))
			stageSource(t, stage, source)
			original, _, err := stage.Page(ctx, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			observer := &observingBootstrapStore{Store: store, loseAction: action}
			progress, err := applyBootstrap(ctx, observer, stage)
			if !errors.Is(err, persistence.ErrCommitUnconfirmed) || !errors.Is(err, ErrBootstrapOutcomeUnconfirmed) || !progress.OutcomeUnconfirmed {
				t.Fatalf("lost response hidden: %+v %v", progress, err)
			}
			if action == "begin" && observer.submissions != 1 {
				t.Fatal("begin was automatically retried")
			}
			if action == "seed" && (observer.submissions != 2 || progress.SeededRecords != 1) {
				t.Fatal("seed was automatically retried")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := stage.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := persistence.Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			resumed, err := ResumeStage(ctx, options, sealer)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			progress, err = ApplyBootstrap(ctx, reopened, resumed)
			if err != nil || !progress.CatalogActivated || progress.OutcomeUnconfirmed || progress.SeededRecords != uint64(len(original)) {
				t.Fatalf("original stage could not resume: %+v %v", progress, err)
			}
			view, err := reopened.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range original {
				got, ok := view.Get(expected.Key)
				got.CommittedIndex, got.DependentsVersion = 0, 0
				if !ok || !reflect.DeepEqual(got, expected) {
					t.Fatal("resume regenerated record or ciphertext")
				}
			}
		})
	}
}

func TestApplyBootstrapRejectsNonfrozenDifferentStageAndBadPrefix(t *testing.T) {
	ctx := context.Background()
	store, _ := bootstrapTestStore(t, "memory", 1)
	sealer := stageTestSealer(t, 83)
	stage, options := bootstrapTestStage(t, store, sealer)
	addStage(t, stage, stageMonitor("one"))
	if _, err := ApplyBootstrap(ctx, store, stage); !errors.Is(err, ErrStageNotFrozen) {
		t.Fatalf("loading stage admitted: %v", err)
	}
	if _, exists := store.Bootstrap(); exists {
		t.Fatal("nonfrozen input changed Raft")
	}
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	observer := &observingBootstrapStore{Store: store, loseAction: "begin"}
	if _, err := applyBootstrap(ctx, observer, stage); !errors.Is(err, persistence.ErrCommitUnconfirmed) {
		t.Fatal(err)
	}
	before, _ := store.Bootstrap()
	otherOptions := stageTestOptions(t)
	otherOptions.StoreID = options.StoreID
	otherOptions.StageID = "different-stage"
	other := createTestStage(t, otherOptions, sealer)
	addStage(t, other, stageMonitor("one"))
	if _, err := other.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBootstrap(ctx, store, other); !errors.Is(err, persistence.ErrBootstrapConflict) {
		t.Fatalf("changed stage accepted: %v", err)
	}
	foreignOptions := stageTestOptions(t)
	foreign := createTestStage(t, foreignOptions, sealer)
	if _, err := foreign.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBootstrap(ctx, store, foreign); !errors.Is(err, persistence.ErrBootstrapConflict) {
		t.Fatalf("wrong store accepted: %v", err)
	}
	after, _ := store.Bootstrap()
	if before != after {
		t.Fatal("rejected replacement changed progress")
	}
	// A caller-supplied progress record cannot justify skipping a different
	// prefix, even though the stage itself is fully authenticated.
	manifest := before.Manifest
	bad := before
	bad.ProgressDigest = strings.Repeat("a", 64)
	if err := preflightBootstrap(ctx, stage, manifest, bad, true, 4<<20); !errors.Is(err, persistence.ErrBootstrapConflict) {
		t.Fatalf("invalid prefix proof accepted: %v", err)
	}
	first, _, err := stage.Page(ctx, "", 1)
	if err != nil || len(first) != 1 {
		t.Fatal("missing staged record", err)
	}
	bad.Applied = 1
	bad.ProgressDigest, err = persistence.BootstrapDigest(persistence.BootstrapInitialDigest(), first[0])
	if err != nil {
		t.Fatal(err)
	}
	bad.LastKey = persistence.CatalogKey{Kind: "Monitor", ID: "another-monitor"}
	if err := preflightBootstrap(ctx, stage, manifest, bad, true, 4<<20); !errors.Is(err, persistence.ErrBootstrapConflict) {
		t.Fatalf("wrong resumed last key accepted: %v", err)
	}
	bad.LastKey = first[0].Key
	if err := preflightBootstrap(ctx, stage, manifest, bad, true, 4<<20); err != nil {
		t.Fatalf("matching authenticated prefix rejected: %v", err)
	}
	if _, err := ApplyBootstrap(ctx, store, stage); err != nil {
		t.Fatal("original stage no longer resumable", err)
	}
}

func TestApplyBootstrapActiveRetryPreservesLaterAPIEdits(t *testing.T) {
	ctx := context.Background()
	store, _ := bootstrapTestStore(t, "memory", 1000)
	sealer := stageTestSealer(t, 84)
	stage, _ := bootstrapTestStage(t, store, sealer)
	addStage(t, stage, stageMonitor("api"))
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBootstrap(ctx, store, stage); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := catalog.Get(ctx, "Monitor", "api")
	if err != nil {
		t.Fatal(err)
	}
	change, err := catalog.PreparePatch(ctx, "Monitor", "api", old.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"Edited after activation"}}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.Commit(ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Status().CommittedIndex
	progress, err := ApplyBootstrap(ctx, store, stage)
	if err != nil || !progress.CatalogActivated || progress.OutcomeUnconfirmed || store.Status().CommittedIndex != before {
		t.Fatalf("active import was replayed: %+v %v", progress, err)
	}
	got, err := catalog.Get(ctx, "Monitor", "api")
	if err != nil || got.Metadata.ResourceVersion != result.Resource.Metadata.ResourceVersion || *got.Metadata.Name != "Edited after activation" {
		t.Fatal("bootstrap replaced API edit", err)
	}
}

func TestApplyBootstrapHonorsEncodedAndCountLimits(t *testing.T) {
	ctx := context.Background()
	store, _ := bootstrapTestStore(t, "memory", 1000)
	sealer := stageTestSealer(t, 85)
	stage, _ := bootstrapTestStage(t, store, sealer)
	value := strings.Repeat("large-encrypted-fixture-", 32000)
	for i := range 7 {
		addStage(t, stage, resource("Credential", fmt.Sprintf("large-%d", i), api.CredentialSpec{Value: &value}))
	}
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	limited := &observingBootstrapStore{Store: store, maxBytes: 64 << 10}
	if _, err := applyBootstrap(ctx, limited, stage); !errors.Is(err, ErrStageQuota) {
		t.Fatalf("oversized seed accepted by admission preflight: %v", err)
	}
	if _, exists := store.Bootstrap(); exists || limited.submissions != 0 {
		t.Fatal("oversized input began a partial migration")
	}
	observer := &observingBootstrapStore{Store: store}
	if _, err := applyBootstrap(ctx, observer, stage); err != nil {
		t.Fatal(err)
	}
	if len(observer.seedBatches) < 2 {
		t.Fatal("all large records put in one oversized commit")
	}
	for _, size := range observer.encodedBytes {
		if size > 4<<20 {
			t.Fatal("encoded byte bound exceeded")
		}
	}
	if err := preflightBootstrap(context.Background(), stage, (func() persistence.BootstrapManifest {
		info, _ := stage.Info()
		return persistence.BootstrapManifest{StageID: info.StageID, InventoryDigest: info.Digest, CatalogDigest: info.CatalogDigest, Count: info.Count}
	})(), persistence.BootstrapState{}, false, 64<<10); !errors.Is(err, ErrStageQuota) {
		t.Fatalf("oversized seed not caught in preflight: %v", err)
	}
}

func TestApplyBootstrapCancellationAfterConfirmedSeedPreservesProgress(t *testing.T) {
	store, _ := bootstrapTestStore(t, "memory", 1)
	stage, _ := bootstrapTestStage(t, store, stageTestSealer(t, 87))
	for _, id := range []string{"one", "two", "three"} {
		addStage(t, stage, stageMonitor(id))
	}
	if _, err := stage.Freeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := &observingBootstrapStore{Store: store, after: func(commands []persistence.Command) {
		if commands[0].Bootstrap.Action == "seed" {
			cancel()
		}
	}}
	progress, err := applyBootstrap(ctx, observer, stage)
	if !errors.Is(err, context.Canceled) || progress.OutcomeUnconfirmed || progress.CatalogActivated || progress.SeededRecords != 1 || observer.submissions != 2 {
		t.Fatalf("confirmed cancellation lost import position: %+v %v", progress, err)
	}
	progress, err = ApplyBootstrap(context.Background(), store, stage)
	if err != nil || !progress.CatalogActivated || progress.SeededRecords != 3 || progress.OutcomeUnconfirmed {
		t.Fatalf("canceled import could not explicitly resume: %+v %v", progress, err)
	}
}

func TestApplyBootstrapCancellationAndEmptyInput(t *testing.T) {
	store, _ := bootstrapTestStore(t, "memory", 1)
	stage, _ := bootstrapTestStage(t, store, stageTestSealer(t, 86))
	ctx := context.Background()
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ApplyBootstrap(canceled, store, stage); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import accepted: %v", err)
	}
	if _, exists := store.Bootstrap(); exists {
		t.Fatal("canceled import changed catalog")
	}
	progress, err := ApplyBootstrap(ctx, store, stage)
	if err != nil || !progress.CatalogActivated || progress.SeededRecords != 0 {
		t.Fatalf("intentional empty import failed: %+v %v", progress, err)
	}
}
