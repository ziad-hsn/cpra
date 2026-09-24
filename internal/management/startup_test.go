package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func startupTestOptions(directory string, resources ...api.Resource) StartupOptions {
	raw, _ := json.Marshal(resources)
	return StartupOptions{DataDirectory: directory, MaxResources: 2000, MaxEncodedBytes: 32 << 20,
		OpenSource: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }}
}

func noStartupSource(t *testing.T) func(context.Context) (io.ReadCloser, error) {
	t.Helper()
	return func(context.Context) (io.ReadCloser, error) {
		t.Error("authoritative restart reread source input")
		return nil, errors.New("unreadable obsolete input")
	}
}

func TestStartupCatalogRaftRestartDoesNotReplaceAPIEdit(t *testing.T) {
	ctx := context.Background()
	store, config := bootstrapTestStore(t, "raft", 2)
	sealer := stageTestSealer(t, 91)
	opts := startupTestOptions(config.Storage.Directory, stageMonitor("api"))
	result, err := StartupCatalog(ctx, store, sealer, opts)
	if err != nil || result.Catalog == nil || !result.Bootstrap.CatalogActivated || result.Bootstrap.SeededRecords != 1 {
		t.Fatalf("initial startup failed: %+v %v", result.Bootstrap, err)
	}
	original, err := result.Catalog.Get(ctx, "Monitor", "api")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := result.Catalog.PreparePatch(ctx, "Monitor", "api", original.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"Saved through the API"}}`))
	if err != nil {
		t.Fatal(err)
	}
	change, err := result.Catalog.Commit(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// An active catalog has no runtime dependency on either original input or
	// staging. Removing this completed fixture stage must not trigger import.
	if err := os.RemoveAll(filepath.Join(config.Storage.Directory, startupStageDirectory)); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before := reopened.Status().CommittedIndex
	result, err = StartupCatalog(ctx, reopened, sealer, StartupOptions{OpenSource: noStartupSource(t), DataDirectory: "an obsolete relative directory", MaxResources: 10_000_001})
	if err != nil || result.Catalog == nil || reopened.Status().CommittedIndex != before {
		t.Fatal("restart required obsolete input options or rewrote state", err)
	}
	got, err := result.Catalog.Get(ctx, "Monitor", "api")
	if err != nil || got.APIVersion != change.Resource.APIVersion || got.Kind != change.Resource.Kind || !reflect.DeepEqual(got.Metadata, change.Resource.Metadata) || !bytes.Equal(got.Spec, change.Resource.Spec) {
		t.Fatal("bootstrap overwrote the API edit", err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(got.Status, &status); err != nil || status.LastCheckLatencyMS.Available || status.LastCheckLatencyMS.Reason == "" {
		t.Fatal("restart fabricated an available check measurement before controller observation", err)
	}
}

func TestStartupCatalogExistingTombstonesRemainAuthoritative(t *testing.T) {
	ctx := context.Background()
	store, config := bootstrapTestStore(t, "raft", 1000)
	sealer := stageTestSealer(t, 92)
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	create, err := catalog.Prepare(ctx, stageMonitor("deleted"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	created, err := catalog.Commit(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := catalog.PrepareDelete(ctx, "Monitor", "deleted", created.Resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Commit(ctx, remove); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := StartupCatalog(ctx, reopened, sealer, StartupOptions{OpenSource: noStartupSource(t)})
	if err != nil || result.Catalog == nil || !result.Bootstrap.CatalogActivated {
		t.Fatal("deleted catalog treated as uninitialized", err)
	}
	view, err := reopened.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("deleted monitor recreated", err)
	}
	if _, exists := reopened.Bootstrap(); exists {
		t.Fatal("old catalog started a new bootstrap")
	}
}

func makeStartupStage(t *testing.T, store *persistence.Store, sealer *secureconfig.Sealer, options StartupOptions, freeze bool) *Stage {
	t.Helper()
	options, err := normalizeStartupOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(options.DataDirectory, startupStageDirectory)
	if _, err := createStartupDirectory(directory); err != nil {
		t.Fatal(err)
	}
	stage := createTestStage(t, startupStageOptions(options, directory, store.Status().NodeID), sealer)
	if freeze {
		if _, err := fillStartupStage(context.Background(), stage, options); err != nil {
			t.Fatal(err)
		}
	} else {
		addStage(t, stage, stageMonitor("incomplete-input"))
	}
	return stage
}

func TestStartupCatalogResumesFrozenStageWithoutReadingChangedSource(t *testing.T) {
	for _, begin := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed-begin-%t", begin), func(t *testing.T) {
			ctx := context.Background()
			store, config := bootstrapTestStore(t, "raft", 1)
			sealer := stageTestSealer(t, 93)
			opts := startupTestOptions(config.Storage.Directory, stageMonitor("original-a"), stageMonitor("original-b"))
			stage := makeStartupStage(t, store, sealer, opts, true)
			original, _, err := stage.Page(ctx, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			if begin {
				observer := &observingBootstrapStore{Store: store, loseAction: "seed"}
				if _, err := applyBootstrap(ctx, observer, stage); !errors.Is(err, persistence.ErrCommitUnconfirmed) {
					t.Fatal(err)
				}
			}
			if err := stage.Close(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := persistence.Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			opts.OpenSource = noStartupSource(t)
			result, err := StartupCatalog(ctx, reopened, sealer, opts)
			if err != nil || result.Catalog == nil || !result.Bootstrap.CatalogActivated {
				t.Fatal("original staged import could not resume", err)
			}
			view, err := reopened.CatalogSnapshot()
			if err != nil || view.Len() != len(original) {
				t.Fatal("resumed catalog size changed", err)
			}
			for _, expected := range original {
				actual, found := view.Get(expected.Key)
				actual.CommittedIndex, actual.DependentsVersion = 0, 0
				if !found || !reflect.DeepEqual(actual, expected) {
					t.Fatal("startup regenerated frozen identity or ciphertext")
				}
			}
		})
	}
}

func TestStartupCatalogIncompleteInputRequiresExplicitStoppedCleanup(t *testing.T) {
	ctx := context.Background()
	store, config := bootstrapTestStore(t, "raft", 1)
	sealer := stageTestSealer(t, 94)
	opts := startupTestOptions(config.Storage.Directory, stageMonitor("changed"))
	stage := makeStartupStage(t, store, sealer, opts, false)
	before, err := stage.Info()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	opts.OpenSource = noStartupSource(t)
	if _, err := StartupCatalog(ctx, reopened, sealer, opts); !errors.Is(err, ErrStartupIncomplete) || !strings.Contains(err.Error(), "stop CPRa") {
		t.Fatalf("loading stage implicitly resumed: %v", err)
	}
	if has, err := reopened.HasCatalog(); has || err != nil {
		t.Fatal("rejected loading stage changed active state", err)
	}
	normalized, _ := normalizeStartupOptions(opts)
	retained, err := ResumeStage(ctx, startupStageOptions(normalized, filepath.Join(config.Storage.Directory, startupStageDirectory), reopened.Status().NodeID), sealer)
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	after, err := retained.Info()
	if err != nil || before != after {
		t.Fatal("loading stage was altered", err)
	}
}

func TestStartupCatalogPendingMissingWrongOrChangedStageFailsClosed(t *testing.T) {
	for _, damage := range []string{"missing", "wrong-key", "changed-quota", "locked", "foreign-stage", "old-loading-stage"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			store, config := bootstrapTestStore(t, "raft", 1)
			sealer := stageTestSealer(t, 95)
			opts := startupTestOptions(config.Storage.Directory, stageMonitor("original"))
			stage := makeStartupStage(t, store, sealer, opts, true)
			observer := &observingBootstrapStore{Store: store, loseAction: "begin"}
			if _, err := applyBootstrap(ctx, observer, stage); !errors.Is(err, persistence.ErrCommitUnconfirmed) {
				t.Fatal(err)
			}
			before, _ := store.Bootstrap()
			if damage != "locked" {
				if err := stage.Close(); err != nil {
					t.Fatal(err)
				}
			}
			switch damage {
			case "missing":
				if err := os.RemoveAll(filepath.Join(config.Storage.Directory, startupStageDirectory)); err != nil {
					t.Fatal(err)
				}
			case "wrong-key":
				sealer = stageTestSealer(t, 96)
			case "changed-quota":
				opts.MaxResources++
			case "foreign-stage", "old-loading-stage":
				if err := os.Remove(filepath.Join(config.Storage.Directory, startupStageDirectory, stageFileName)); err != nil {
					t.Fatal(err)
				}
				o, _ := normalizeStartupOptions(opts)
				storeID := "wrong-store"
				if damage == "old-loading-stage" {
					storeID = store.Status().NodeID
				}
				foreign := startupStageOptions(o, filepath.Join(config.Storage.Directory, startupStageDirectory), storeID)
				s := createTestStage(t, foreign, sealer)
				addStage(t, s, stageMonitor("foreign"))
				if damage == "foreign-stage" {
					if _, err := s.Freeze(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			opts.OpenSource = noStartupSource(t)
			result, err := StartupCatalog(ctx, store, sealer, opts)
			if err == nil || !errors.Is(err, ErrStartupStage) || result.Catalog != nil || result.Bootstrap.Manifest != before.Manifest {
				t.Fatalf("invalid stage accepted or progress hidden: %+v %v", result.Bootstrap, err)
			}
			if errors.Is(err, ErrStartupIncomplete) {
				t.Fatal("pending committed migration recommended deleting its required stage")
			}
			after, _ := store.Bootstrap()
			if before != after {
				t.Fatal("failed stage recovery modified durable progress")
			}
		})
	}
}

type startupReader struct {
	io.Reader
	closed  *atomic.Int32
	err     error
	onClose func() error
}

func (r startupReader) Close() error {
	if r.closed != nil {
		r.closed.Add(1)
	}
	if r.onClose != nil {
		return r.onClose()
	}
	return r.err
}

func TestStartupCatalogSourceFailureNeverActivatesAndDoesNotLogInput(t *testing.T) {
	for _, failure := range []string{"malformed", "close", "missing-reference", "quota", "empty"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			store, config := bootstrapTestStore(t, "raft", 2)
			sealer := stageTestSealer(t, 97)
			marker := "protected-startup-input-never-log"
			opts := startupTestOptions(config.Storage.Directory, stageMonitor("first"))
			raw, _ := json.Marshal(stageMonitor("first"))
			input := string(raw)
			var closeErr error
			switch failure {
			case "malformed":
				input += "\n{" + marker
			case "close":
				closeErr = errors.New(marker)
			case "missing-reference":
				resources := stageFixture("https://example.test/" + marker)
				broken, _ := json.Marshal(resources[:4])
				input = string(broken)
			case "quota":
				opts.MaxResources = 1
				more, _ := json.Marshal([]api.Resource{stageMonitor("first"), stageMonitor("second")})
				input = string(more)
			case "empty":
				input = "[]"
			}
			var closed atomic.Int32
			opts.Decode.SourceName = marker
			opts.OpenSource = func(context.Context) (io.ReadCloser, error) {
				return startupReader{Reader: strings.NewReader(input), closed: &closed, err: closeErr}, nil
			}
			result, err := StartupCatalog(ctx, store, sealer, opts)
			if err == nil || !errors.Is(err, ErrStartupIncomplete) || strings.Contains(err.Error(), marker) || result.Catalog != nil || closed.Load() != 1 {
				t.Fatalf("failed input was admitted, leaked or left open: %+v %v", result.Bootstrap, err)
			}
			if has, err := store.HasCatalog(); has || err != nil {
				t.Fatal("invalid final input partially activated", err)
			}
		})
	}
}

func TestStartupCatalogMemoryAndValidationWriteOnlyEncryptedTemporaryStages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("temporary-directory environment isolation is covered by native Windows directory tests")
	}
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	marker := "encrypted-bootstrap-secret-value"
	check := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, target.URL+"/?token="+marker))}}}
	opts := startupTestOptions(filepath.Join(temporary, "must-not-be-created"), resource("Monitor", "private-http", check))
	open := opts.OpenSource
	opts.OpenSource = func(ctx context.Context) (io.ReadCloser, error) {
		entries, err := os.ReadDir(temporary)
		if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "cpra-encrypted-bootstrap-") {
			t.Fatal("temporary encrypted stage was not private and isolated", err)
		}
		path := filepath.Join(temporary, entries[0].Name())
		if err := checkStartupDirectory(path); err != nil {
			t.Fatal(err)
		}
		reader, err := open(ctx)
		if err != nil {
			return nil, err
		}
		return startupReader{Reader: reader, onClose: func() error {
			err := filepath.WalkDir(path, func(file string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				if bytes.Contains(data, []byte(marker)) {
					t.Error("plaintext credential entered temporary staging")
				}
				return nil
			})
			return errors.Join(err, reader.Close())
		}}, nil
	}
	validation, err := ValidateStartupSource(context.Background(), opts)
	if err != nil || validation.Monitors != 1 || validation.Resources != 2 {
		t.Fatalf("source-only validation failed: %+v %v", validation, err)
	}
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatal("validation left staging files", err)
	}
	store, _ := bootstrapTestStore(t, "memory", 1)
	result, err := StartupCatalog(context.Background(), store, stageTestSealer(t, 98), opts)
	if err != nil || result.Catalog == nil || result.Bootstrap.SeededRecords != 2 {
		t.Fatal("memory startup failed", err)
	}
	entries, err = os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatal("memory startup left staging files", err)
	}
	if calls.Load() != 0 {
		t.Fatal("startup validation executed a provider")
	}
	failedStore, _ := bootstrapTestStore(t, "memory", 1)
	_, err = StartupCatalog(context.Background(), failedStore, stageTestSealer(t, 101), StartupOptions{})
	if !errors.Is(err, ErrStartupMemory) || !errors.Is(err, ErrStartupEmpty) || errors.Is(err, ErrStartupIncomplete) {
		t.Fatalf("memory failure gave persistent-stage cleanup guidance: %v", err)
	}
	entries, err = os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed memory startup left encrypted temporary data", err)
	}
}

func TestStartupTemporaryRootMayUseOSAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native symlink creation requires a separately configured privilege")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "real-temp")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "temp-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	validation, err := ValidateStartupSource(context.Background(), startupTestOptions("", stageMonitor("through-os-temp-alias")))
	if err != nil || validation.Monitors != 1 {
		t.Fatal("OS temporary root alias prevented private staging", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary stage left behind", err)
	}
}

func TestStartupSourceValidationRejectsUnavailableDriverWithoutOpeningProviders(t *testing.T) {
	var unavailable jobs.Capability
	for _, capability := range jobs.Capabilities() {
		if capability.Kind == "check" && !capability.Available {
			unavailable = capability
			break
		}
	}
	if unavailable.Driver == "" {
		t.Skip("all built-in check drivers are compiled")
	}
	monitor := stageMonitor("optional")
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Check.Driver = api.DriverConfig{Type: unavailable.Driver, Config: json.RawMessage(`{}`)}
	monitor.Spec, _ = json.Marshal(spec)
	if _, err := ValidateStartupSource(context.Background(), startupTestOptions("", monitor)); !errors.Is(err, ErrStartupDriver) {
		t.Fatalf("missing compiled driver accepted or misreported: %v", err)
	}
}

func TestStartupEmptyAdmissionIsExplicitAndRestoredEmptyDoesNotRequireFlag(t *testing.T) {
	store, config := bootstrapTestStore(t, "raft", 1)
	ctx := context.Background()
	sealer := stageTestSealer(t, 99)
	result, err := StartupCatalog(ctx, store, sealer, StartupOptions{DataDirectory: config.Storage.Directory, AllowEmpty: true})
	if err != nil || result.Catalog == nil || result.Bootstrap.SeededRecords != 0 {
		t.Fatal("explicit empty startup failed", err)
	}
	result, err = StartupCatalog(ctx, store, sealer, StartupOptions{OpenSource: noStartupSource(t)})
	if err != nil || result.Catalog == nil {
		t.Fatal("authoritative empty catalog depended on initial flag", err)
	}
}

func TestStartupDirectoryRejectsUnsafeExistingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions and symlinks; native ACL tests cover Windows")
	}
	for _, kind := range []string{"permissions", "symlink", "file", "empty-existing"} {
		t.Run(kind, func(t *testing.T) {
			store, config := bootstrapTestStore(t, "raft", 1)
			directory := filepath.Join(config.Storage.Directory, startupStageDirectory)
			switch kind {
			case "permissions":
				if err := os.Mkdir(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), directory); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(directory, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-existing":
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			opts := startupTestOptions(config.Storage.Directory, stageMonitor("unread"))
			opts.OpenSource = noStartupSource(t)
			if _, err := StartupCatalog(context.Background(), store, stageTestSealer(t, 100), opts); !errors.Is(err, ErrStartupIncomplete) {
				t.Fatalf("unsafe stage path accepted or replaced: %v", err)
			}
		})
	}
}
