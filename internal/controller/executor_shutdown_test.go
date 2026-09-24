package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	bolt "go.etcd.io/bbolt"
)

// This test-only Job deliberately ignores the worker's context while making a
// real local HTTP call. It models uncooperative compiled Go code, not a provider
// certification fixture. Durable authorization and completion remain unmodified.
type uncooperativeRecoveryJob struct {
	jobs.Job
	jobs.Execution
	client   *http.Client
	url      string
	contexts chan<- context.Context
}

func (j *uncooperativeRecoveryJob) Copy() jobs.Job {
	copy := *j
	copy.Job = j.Job.Copy()
	return &copy
}

func (j *uncooperativeRecoveryJob) Execute() jobs.Result {
	select {
	case j.contexts <- j.Context():
	default:
	}
	// An independent context is intentional: cancellation alone must not be
	// treated as evidence that this executor has returned.
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, j.url, nil)
	if err != nil {
		return jobs.Result{Err: err}
	}
	response, err := j.client.Do(request)
	if err != nil {
		return jobs.Result{Err: err}
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	return jobs.Result{Err: errors.Join(readErr, closeErr)}
}

func TestUncooperativeRecoveryRetainsOwnerAndProcessLockUntilReturn(t *testing.T) {
	InitializeLoggers(false)
	t.Cleanup(CloseLoggers)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	var effects atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recovery" {
			effects.Add(1)
			enteredOnce.Do(func() { close(entered) })
			<-release
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(target.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	httpClient := &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	settings := runtimeconfig.Default()
	settings.Storage.Directory = filepath.Join(t.TempDir(), "state")
	catalog, store := managedStore(t, settings)
	var c *Controller
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		if c != nil {
			c.Stop()
		}
		if err := store.Close(); err != nil {
			t.Error("close joined fixture storage", err)
		}
	})
	commitManaged(t, catalog, managedResource("Credential", "uncooperative-target", api.CredentialSpec{Value: api.Pointer(target.URL + "/recovery")}), "")
	monitor := managedMonitor("uncooperative", target.URL+"/health", true)
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Recovery = &api.RecoverySpec{MaxAttempts: api.Pointer(int64(3)), Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"timeout":"1s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "uncooperative-target"})}}
	monitor.Spec, _ = json.Marshal(spec)
	commitManaged(t, catalog, monitor, "")
	config := DefaultConfig()
	config.Store, config.Catalog, config.Runtime = store, catalog, settings
	config.WorkerConfig.MinWorkers, config.WorkerConfig.MaxWorkers, config.WorkerConfig.NumShards = 1, 1, 1
	config.WorkerConfig.ResultBatchTimeout = time.Millisecond
	config.WorkerConfig.DrainTimeout = 25 * time.Millisecond
	c = NewController(config)
	if err := c.LoadCatalog(t.Context()); err != nil {
		t.Fatal(err)
	}
	entity, ok := c.mapper.Lookup("uncooperative")
	if !ok {
		t.Fatal("loaded monitor missing")
	}
	storage := ecs.NewMap1[components.JobStorage](c.world).Get(entity)
	contexts := make(chan context.Context, 1)
	storage.InterventionJob = &uncooperativeRecoveryJob{Job: storage.InterventionJob, client: httpClient, url: target.URL + "/recovery", contexts: contexts}
	// No ECS component is accessed outside its owner after Start.
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("real dispatched recovery did not enter the controlled target")
	}
	var workerContext context.Context
	select {
	case workerContext = <-contexts:
	case <-time.After(time.Second):
		t.Fatal("recovery did not receive the production worker context")
	}
	state, ok := store.Get("uncooperative")
	if !ok {
		t.Fatal("durable monitor missing")
	}
	actionID := ""
	for id, action := range state.Actions {
		if action.Kind == "intervention" && action.State == persistence.Started && action.ExecutorKind == "local" && action.ExecutorSession != "" && action.ExecutorFinishedAt.IsZero() {
			actionID = id
		}
	}
	if actionID == "" || effects.Load() != 1 {
		t.Fatal("recovery ran without a committed local start or ran more than once")
	}
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	err := c.StopContext(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || c.Ready() {
		t.Fatal("uncooperative shutdown did not report deadline expiry and unavailable readiness", err)
	}
	select {
	case <-workerContext.Done():
	default:
		t.Fatal("deadline did not cancel the worker context")
	}
	c.lifecycleMu.Lock()
	done := c.doneCh
	c.lifecycleMu.Unlock()
	select {
	case <-done:
		t.Fatal("controller reported stopped while recovery Go code remained active")
	default:
	}
	if err := store.Close(); !errors.Is(err, persistence.ErrLocalExecutorActive) {
		t.Fatal("storage close accepted an active executor", err)
	}
	probeExecutorProcessLock(t, settings.Storage.Directory, "locked")
	select {
	case <-done:
		t.Fatal("controller joined merely because a competing process failed to open storage")
	default:
	}
	state, _ = store.Get("uncooperative")
	if !state.Actions[actionID].ExecutorFinishedAt.IsZero() {
		t.Fatal("cancellation falsely recorded executor completion")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("controller did not join after recovery actually returned")
	}
	state, _ = store.Get("uncooperative")
	action := state.Actions[actionID]
	if action.State != persistence.Succeeded || action.ExecutorFinishedAt.IsZero() || effects.Load() != 1 {
		t.Fatal("shutdown lost known recovery success/completion or repeated the provider")
	}
	if err := store.Close(); err != nil {
		t.Fatal("completed executor did not permit storage close", err)
	}
	probeExecutorProcessLock(t, settings.Storage.Directory, "open")
}

func probeExecutorProcessLock(t *testing.T, directory, expected string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecutorProcessLockProbe$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CPRA_EXECUTOR_PROBE_DIRECTORY="+directory, "CPRA_EXECUTOR_PROBE_EXPECT="+expected)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("separate process ownership probe failed (%s): %v\n%s", expected, err, output)
	}
}

func TestExecutorProcessLockProbe(t *testing.T) {
	directory, expected := os.Getenv("CPRA_EXECUTOR_PROBE_DIRECTORY"), os.Getenv("CPRA_EXECUTOR_PROBE_EXPECT")
	if directory == "" {
		t.Skip("invoked only as a separate ownership-probe process")
	}
	if !filepath.IsAbs(directory) || (expected != "locked" && expected != "open") {
		t.Fatal("invalid process probe configuration")
	}
	settings := runtimeconfig.Default()
	settings.Storage.Directory = directory
	store, err := persistence.Open(t.Context(), settings)
	if expected == "locked" {
		if err == nil {
			store.Close()
			t.Fatal("another process opened state while the original recovery executor remained active")
		}
		if !errors.Is(err, bolt.ErrTimeout) {
			t.Fatal("competing process failed for an unrelated reason", err)
		}
		return
	}
	if err != nil {
		t.Fatal("released state could not be reopened", err)
	}
	defer store.Close()
	m, ok := store.Get("uncooperative")
	if !ok || len(m.Actions) == 0 {
		t.Fatal("reopened state lost the recovery action")
	}
	for _, action := range m.Actions {
		if action.Kind == "intervention" && (action.State != persistence.Succeeded || action.ExecutorFinishedAt.IsZero()) {
			t.Fatal("new process did not restore known provider success and executor completion")
		}
	}
}
