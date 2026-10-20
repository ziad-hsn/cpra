//go:build externaljobs

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMainWorkerServerProcessHelper(t *testing.T) {
	runtimeFile := os.Getenv("CPRA_WORKER_PROCESS_RUNTIME")
	if runtimeFile == "" {
		t.Skip("subprocess helper")
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	options := runOptions{runtimeFile: runtimeFile, manifest: os.Getenv("CPRA_WORKER_PROCESS_MANIFEST"),
		webAddr: "127.0.0.1:0", web: true, allowEmpty: true, shutdownTimeout: 5 * time.Second}
	var address string
	options.webStarted = func(bound string) { address = bound }
	if err := runCPRa(ctx, options, func() {
		if address == "" {
			panic("server ready without a bound listener")
		}
		ready := os.Getenv("CPRA_WORKER_PROCESS_READY")
		if err := os.WriteFile(ready+".tmp", []byte(address), 0600); err != nil {
			panic(err)
		}
		if err := os.Rename(ready+".tmp", ready); err != nil {
			panic(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

type workerTestProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	logPath string
}

func startWorkerTestProcess(t *testing.T, command *exec.Cmd, logPath string) *workerTestProcess {
	t.Helper()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	p := &workerTestProcess{command: command, done: make(chan struct{}), logPath: logPath}
	go func() {
		p.err = command.Wait()
		_ = log.Close()
		close(p.done)
	}()
	t.Cleanup(func() { p.stop(t, true) })
	return p
}

func (p *workerTestProcess) output() string {
	file, err := os.Open(p.logPath)
	if err != nil {
		return "process log unavailable"
	}
	defer file.Close()
	raw, _ := io.ReadAll(io.LimitReader(file, 64<<10))
	return string(raw)
}

func (p *workerTestProcess) stop(t *testing.T, force bool) {
	t.Helper()
	select {
	case <-p.done:
		if !force && p.err != nil {
			t.Fatalf("child exited before shutdown: %v\n%s", p.err, p.output())
		}
		return
	default:
	}
	if force {
		_ = p.command.Process.Kill()
	} else {
		_ = p.command.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		_ = p.command.Process.Kill()
		<-p.done
		t.Fatalf("child missed shutdown deadline: %s", p.output())
	}
	if bytes.Contains([]byte(p.output()), []byte("WARNING: DATA RACE")) {
		t.Fatalf("child reported a race: %s", p.output())
	}
	if !force && p.err != nil {
		t.Fatalf("child shutdown failed: %v\n%s", p.err, p.output())
	}
}

func (p *workerTestProcess) kill(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatalf("child exited before forced termination: %v\n%s", p.err, p.output())
	default:
	}
	if err := p.command.Process.Kill(); err != nil {
		t.Fatal("force process termination", err)
	}
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		t.Fatal("killed process did not exit")
	}
	status, ok := p.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("crash boundary did not end with SIGKILL", p.command.ProcessState)
	}
	if bytes.Contains([]byte(p.output()), []byte("WARNING: DATA RACE")) {
		t.Fatalf("terminated child reported a race: %s", p.output())
	}
	t.Logf("confirmed SIGKILL of %s", filepath.Base(p.logPath))
}

func waitWorkerProcessFile(t *testing.T, process *workerTestProcess, path string) []byte {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if raw, err := os.ReadFile(path); err == nil {
			return raw
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-process.done:
			t.Fatalf("child exited before observation: %v\n%s", process.err, process.output())
		case <-deadline.C:
			process.stop(t, true)
			t.Fatalf("child observation deadline: %s", process.output())
		case <-tick.C:
		}
	}
}

func startWorkerServerProcess(t *testing.T, fixture *mainManagementFixture) (*cpra.Client, *workerTestProcess) {
	t.Helper()
	base := t.TempDir()
	ready := filepath.Join(base, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestMainWorkerServerProcessHelper$")
	command.Env = append(os.Environ(), "CPRA_WORKER_PROCESS_RUNTIME="+fixture.options.runtimeFile,
		"CPRA_WORKER_PROCESS_MANIFEST="+fixture.options.manifest,
		"CPRA_WORKER_PROCESS_READY="+ready)
	p := startWorkerTestProcess(t, command, filepath.Join(base, "server.log"))
	address := string(waitWorkerProcessFile(t, p, ready))
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatal("child did not report its bound loopback address", address, err)
	}
	fixture.options.webAddr = address
	client, err := cpra.New(cpra.Config{BaseURL: "https://" + fixture.options.webAddr, AuthToken: startupOperatorToken, HTTPClient: fixture.client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client, p
}

func workerProcessMonitor(t *testing.T, job api.JobType, targetURL, id string) api.Monitor {
	t.Helper()
	remote, err := json.Marshal(map[string]any{"jobTypeID": job.Metadata.ID, "version": "v1", "parameters": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	driver := api.DriverConfig{Type: "external", Config: remote}
	local, _ := json.Marshal(map[string]string{"url": targetURL + "/health"})
	spec := api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{
		Driver: api.DriverConfig{Type: "http", Config: local}, Interval: "1s", Timeout: "15s", UnhealthyThreshold: api.Pointer(int64(1))}}
	switch job.Spec.Kind {
	case "check":
		spec.Check.Driver = driver
	case "recovery":
		spec.Recovery = &api.RecoverySpec{Driver: driver, MaxAttempts: api.Pointer(int64(1))}
	case "notification":
		spec.Notifications = api.Pointer(map[string]api.AlertRule{"yellow": {Driver: &driver}})
	}
	return api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id}, Spec: spec}
}

func seedWorkerProcessMonitor(t *testing.T, fixture *mainManagementFixture, job api.JobType, targetURL string, monitorIDs ...string) {
	t.Helper()
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	keys, err := encryptionsetup.Open(t.Context(), encryptionsetup.Options{StorageMode: "raft", DataDirectory: fixture.settings.Storage.Directory, Encryption: fixture.settings.Management.Encryption})
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	monitor := workerProcessMonitor(t, job, targetURL, "process-monitor")
	rawSpec, err := json.Marshal(monitor.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitorIDs) == 0 {
		monitorIDs = []string{"process-monitor"}
	}
	for _, monitorID := range monitorIDs {
		now := time.Now().UTC()
		record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "Monitor", ID: monitorID}, UID: uuid.NewString(),
			Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: now, UpdatedAt: now}
		record.JobTypeReferences = []persistence.JobTypeReference{{JobTypeID: job.Metadata.ID, JobTypeUID: job.Metadata.UID,
			Version: "v1", Revision: job.Metadata.ResourceVersion, Category: job.Spec.Kind}}
		resource := api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: record.Key.ID,
			UID: record.UID, ResourceVersion: record.Revision, Generation: 1}, Spec: rawSpec}
		if err := api.ValidateResource(resource); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(raw)
		record.Payload, err = keys.Sealer().Seal(t.Context(), record.Binding(store.Status().NodeID), raw)
		if err != nil {
			t.Fatal(err)
		}
		// Stopped fixtures seed load/capacity state directly. The process crash
		// campaign below creates its monitor through the public SDK.
		results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "catalog", At: now,
			Catalog: &persistence.CatalogMutation{Create: true, Record: record}}})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("seed encrypted worker configuration", err, results)
		}
	}
}

func buildWorkerProcessHelper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native Linux process campaign; other platform qualification remains separate")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workerBinary := filepath.Join(t.TempDir(), "worker.test")
	arguments := []string{"test", "-c", "-tags=externaljobs", "-o", workerBinary}
	workerRace := os.Getenv("CPRA_WORKER_PROCESS_RACE") == "1"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			workerRace = workerRace || (setting.Key == "-race" && setting.Value == "true")
		}
	}
	if workerRace {
		arguments = append(arguments, "-race")
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOROOT() == "" {
		goBinary, err = exec.LookPath("go")
		if err != nil {
			t.Fatalf("locate compiler for independent worker helper: %v", err)
		}
	}
	inspect := exec.CommandContext(t.Context(), goBinary, "env", "GOVERSION")
	inspect.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	version, err := inspect.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect compiler for independent worker helper: %v\n%s", err, version)
	}
	if strings.TrimSpace(string(version)) != runtime.Version() {
		t.Fatalf("independent worker helper compiler %q differs from parent %s", strings.TrimSpace(string(version)), runtime.Version())
	}
	build := exec.CommandContext(t.Context(), goBinary, arguments...)
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	build.Dir = filepath.Join(root, "sdk/go/worker")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build independent worker helper: %v\n%s", err, output)
	}
	t.Logf("SDK worker helper uses %s; race instrumentation: %t", strings.TrimSpace(string(version)), workerRace)
	return workerBinary
}

func TestMainWorkerProcesses(t *testing.T) {
	workerBinary := buildWorkerProcessHelper(t)
	for _, category := range []string{"check", "recovery", "notification"} {
		modes := []string{"normal", "start-reply", "handler", "result-request", "result-reply", "server-restart", "uncooperative", "late-evidence", "revoked"}
		if category == "check" {
			modes = append(modes, "outbox")
		}
		for _, mode := range modes {
			if category == "check" && mode == "late-evidence" {
				continue // This case exercises retained unknown action evidence.
			}
			t.Run(category+"/"+mode, func(t *testing.T) { runWorkerProcessCase(t, workerBinary, category, mode) })
		}
		limits := []string{"live-bytes", "allocated-bytes"}
		if category != "check" {
			limits = append(limits, "unknown-bytes", "unknown-records")
		}
		for _, limit := range limits {
			t.Run("capacity/"+category+"/"+limit, func(t *testing.T) { runWorkerCapacityCase(t, workerBinary, category, limit) })
		}
	}
}

func runWorkerProcessCase(t *testing.T, binary, category, mode string) {
	t.Helper()
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	var effectsMu sync.Mutex
	effects := make(map[string]int)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var event struct{ ExecutionID, Kind string }
		if r.Method != http.MethodPost || r.URL.Path != "/invoke" || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&event) != nil || event.ExecutionID == "" || event.Kind != category {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		effectsMu.Lock()
		effects[event.ExecutionID]++
		effectsMu.Unlock()
		_ = json.NewEncoder(w).Encode(api.Outcome{Status: map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category], Data: json.RawMessage(`{}`)})
	}))
	defer target.Close()
	client, bootstrap := startWorkerServerProcess(t, fixture)
	job, err := client.JobTypes().Create(t.Context(), api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "process-" + category},
		Spec: api.JobTypeSpec{Kind: category, Version: "v1", Handler: "process-" + category, ProtocolVersion: "1", Timeout: "15s",
			ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), ResultSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.stop(t, false)
	private := filepath.Dir(fixture.options.runtimeFile)
	grants, err := json.Marshal(map[string]any{"grants": []persistence.WorkerGrant{{JobTypeID: job.Data.Metadata.ID, JobTypeUID: job.Data.Metadata.UID,
		Version: "v1", Category: category, ResourceKind: "Monitor", ResourceIDs: []string{"process-monitor"}}}})
	if err != nil {
		t.Fatal(err)
	}
	grantsFile, tokenFile := filepath.Join(private, "grants.json"), filepath.Join(private, "worker.token")
	if err := os.WriteFile(grantsFile, grants, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
		Action: "issue", Actor: "team/oncall", DataDirectory: fixture.settings.Storage.Directory, WorkerID: "process-worker", GrantsFile: grantsFile, TokenOutput: tokenFile})
	if err != nil {
		t.Fatal(err)
	}
	client, server := startWorkerServerProcess(t, fixture)
	keyFile := filepath.Join(private, "worker.key")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	clear(key)
	configFile, receiptFile, statusFile := filepath.Join(private, "worker.json"), filepath.Join(private, "receipt.json"), filepath.Join(private, "worker-status.json")
	config := map[string]string{"ServerURL": "https://" + fixture.options.webAddr, "ServerID": identity.ProtocolServerID,
		"WorkerID": "process-worker", "WorkerUID": identity.Workers[0].UID, "JobTypeID": job.Data.Metadata.ID, "Category": category,
		"StateDir": filepath.Join(private, "worker-state"), "WrappingKeyPath": keyFile, "TokenFile": tokenFile,
		"CAFile": fixture.settings.Management.TLS.CertFile, "TargetURL": target.URL, "Mode": mode,
		"BoundaryFile": filepath.Join(private, "boundary.json"), "ReceiptFile": receiptFile, "StatusFile": statusFile}
	if mode == "server-restart" || mode == "late-evidence" {
		config["Mode"] = "handler"
	}
	if mode == "revoked" {
		config["Mode"] = "result-request"
	}
	startWorker := func(logName string) *workerTestProcess {
		config["ServerURL"] = "https://" + fixture.options.webAddr
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "-test.run=^TestServerProcessHelper$")
		command.Env = append(os.Environ(), "CPRA_WORKER_SERVER_PROCESS_CONFIG="+configFile)
		return startWorkerTestProcess(t, command, filepath.Join(private, logName))
	}
	if _, err := client.Monitors.Create(t.Context(), workerProcessMonitor(t, job.Data, target.URL, "process-monitor")); err != nil {
		t.Fatal("public process configuration", err)
	}
	worker := startWorker("worker.log")
	var boundary struct{ ExecutionID, ReceiptID string }
	if mode != "normal" {
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, config["BoundaryFile"]), &boundary); err != nil || boundary.ExecutionID == "" {
			t.Fatal("missing original execution at crash boundary", err)
		}
		if mode == "outbox" {
			var capacity struct {
				Records, PendingOutcomes, ActiveHandlers, AdmissionCapacity int
				AdmissionAvailable                                          bool
			}
			if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".capacity"), &capacity); err != nil {
				t.Fatal(err)
			}
			if capacity.Records != 1 || capacity.PendingOutcomes != 1 || capacity.ActiveHandlers != 0 || capacity.AdmissionCapacity != 0 || capacity.AdmissionAvailable {
				t.Fatal("full outbox did not stop admission after its handler exited", capacity)
			}
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for pending := false; !pending; {
				observed, err := client.SLO.Get(t.Context())
				if err == nil {
					for _, row := range observed.Data.Reports {
						if row.Driver == "external" && row.Expected+row.Pending >= 3 && row.Samples == 1 && row.Pending+row.Overdue > 0 {
							pending = true
						}
					}
				}
				if pending {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("full outbox did not retain later check obligations in SLO accounting", err)
				case <-tick.C:
				}
			}
		}
		if mode == "uncooperative" {
			if err := worker.command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal("request worker drain", err)
			}
			var drain struct {
				Draining, DeadlineExpired, AdmissionAvailable, DeadlineError bool
				ActiveHandlers, AdmissionCapacity, Records                   int
			}
			if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".draining"), &drain); err != nil {
				t.Fatal(err)
			}
			if !drain.Draining || !drain.DeadlineExpired || !drain.DeadlineError || drain.AdmissionAvailable || drain.AdmissionCapacity != 0 || drain.ActiveHandlers != 1 || drain.Records != 1 {
				t.Fatal("drain did not stop admission and retain the active execution", drain)
			}
			probeFile := filepath.Join(private, "worker-lock.json")
			config["Mode"], config["StatusFile"] = "lock-probe", probeFile
			probe := startWorker("worker-lock.log")
			select {
			case <-probe.done:
				probe.stop(t, false)
			case <-time.After(8 * time.Second):
				t.Fatal("journal lock probe did not finish")
			}
			raw, err := os.ReadFile(probeFile)
			if err != nil {
				t.Fatal("journal lock probe did not publish its observation", err)
			}
			var locked struct{ Locked bool }
			if err := json.Unmarshal(raw, &locked); err != nil || !locked.Locked {
				t.Fatal("second process opened a journal still owned by a handler", err)
			}
			config["StatusFile"] = statusFile
		}
		worker.kill(t)
		if mode == "outbox" {
			server.stop(t, false)
			verifyWorkerProcessQueue(t, fixture, boundary.ExecutionID)
			client, server = startWorkerServerProcess(t, fixture)
		}
		if mode == "server-restart" || mode == "revoked" {
			if mode == "revoked" {
				server.stop(t, false)
				revoked, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
					Action: "revoke", Actor: "team/oncall", DataDirectory: fixture.settings.Storage.Directory, WorkerID: "process-worker"})
				if err != nil || revoked.Outcome != "committed" || len(revoked.Workers) != 1 || !revoked.Workers[0].Revoked || revoked.Workers[0].UID != identity.Workers[0].UID {
					t.Fatal("worker revocation did not commit for its original identity", err)
				}
			} else {
				server.kill(t)
			}
			client, server = startWorkerServerProcess(t, fixture)
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				observed, err := client.Workers(t.Context(), cpra.ListOptions{Limit: 100})
				if err == nil && len(observed.Data.Items) == 1 && observed.Data.Items[0].Active == 0 &&
					(mode != "revoked" || observed.Data.Items[0].SessionStatus == "revoked") &&
					(category == "check" || observed.Data.Items[0].UnknownActions == 1) {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("restarted server did not finalize its interrupted grant", err)
				case <-tick.C:
				}
			}
		}
		config["Mode"] = "normal"
		if mode == "revoked" {
			config["Mode"] = "revoked"
		}
		if mode == "outbox" {
			config["Mode"] = "outbox-recover"
		}
		worker = startWorker("worker-restarted.log")
	}
	var receipt api.Receipt
	if mode == "revoked" {
		for _, operation := range []string{"poll", "result"} {
			var denial struct {
				Unauthorized bool
				ExecutionID  string
			}
			if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".denied-"+operation), &denial); err != nil || !denial.Unauthorized || operation == "result" && denial.ExecutionID != boundary.ExecutionID {
				t.Fatal("revoked credential was not explicitly rejected", operation, err)
			}
		}
	} else {
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, receiptFile), &receipt); err != nil {
			t.Fatal(err)
		}
		if boundary.ExecutionID != "" && receipt.ExecutionID != boundary.ExecutionID {
			t.Fatal("restart reported a different execution instead of reconciling the original")
		}
		if boundary.ReceiptID != "" && receipt.ReceiptID != boundary.ReceiptID {
			t.Fatal("lost result reply changed the original committed receipt")
		}
	}
	var evidenceReceipt api.Receipt
	if mode == "late-evidence" {
		worker.stop(t, false)
		config["EvidenceFile"] = filepath.Join(private, "late-evidence.json")
		config["BoundaryFile"] = filepath.Join(private, "evidence-boundary.json")
		request := api.LateEvidenceRequest{ExecutionID: receipt.ExecutionID, OriginalReceiptID: receipt.ReceiptID,
			EvidenceID: "process-evidence", Diagnostic: "independent target observed the original invocation",
			Data: json.RawMessage(`{}`), Evidence: []string{"local-target/invocation/" + receipt.ExecutionID}}
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config["EvidenceFile"], raw, 0600); err != nil {
			t.Fatal(err)
		}
		config["Mode"] = "evidence-reply"
		worker = startWorker("worker-evidence.log")
		var evidenceBoundary struct{ ExecutionID, ReceiptID string }
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, config["BoundaryFile"]), &evidenceBoundary); err != nil || evidenceBoundary.ExecutionID != receipt.ExecutionID || evidenceBoundary.ReceiptID == "" {
			t.Fatal("late evidence did not commit for the original execution", err)
		}
		worker.kill(t)
		config["Mode"] = "normal"
		worker = startWorker("worker-evidence-restarted.log")
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".evidence"), &evidenceReceipt); err != nil {
			t.Fatal(err)
		}
		if evidenceReceipt.ExecutionID != receipt.ExecutionID || evidenceReceipt.ReceiptID != evidenceBoundary.ReceiptID || evidenceReceipt.ReceiptID == receipt.ReceiptID || evidenceReceipt.Disposition != api.ReceiptDispositionAccepted {
			t.Fatal("evidence retry did not preserve its distinct committed receipt", evidenceReceipt)
		}
	}
	worker.stop(t, false)
	server.stop(t, false)
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executionID := receipt.ExecutionID
	if mode == "revoked" {
		executionID = boundary.ExecutionID
		if _, err := os.Stat(receiptFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("revoked worker received a result receipt", err)
		}
	}
	record, exists, err := store.WorkerExecution(t.Context(), executionID)
	terminal, finalized := record.TerminalReceipt()
	if err != nil || !exists || record.Lifecycle == nil || !finalized || mode != "revoked" && terminal.ReceiptID != receipt.ReceiptID {
		t.Fatal("independent processes lost their committed grant or original receipt", err)
	}
	wantStatus := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category]
	wantEffects, wantUnknown := 1, 0
	if mode == "start-reply" || mode == "handler" || mode == "server-restart" || mode == "uncooperative" || mode == "late-evidence" || mode == "revoked" {
		wantStatus = "unknown"
		if category == "check" {
			wantStatus = "noData"
		} else {
			wantUnknown = 1
		}
	}
	if mode == "start-reply" {
		wantEffects = 0
	}
	if mode == "server-restart" || mode == "revoked" {
		if record.Result != nil || record.Finalization == nil || record.Finalization.Reason != "owner_lost" || record.Finalization.NoData != (category == "check") {
			t.Fatal("restarted worker replaced the original owner-loss finalization")
		}
	} else if record.Result == nil || record.Result.Input.Status != wantStatus {
		t.Fatal("committed outcome differs from the observed handler effect", record.Result)
	}
	if mode == "late-evidence" {
		if len(record.Evidence) != 1 || record.Evidence[0].Input.EvidenceID != "process-evidence" ||
			record.Evidence[0].Input.OriginalReceiptID != receipt.ReceiptID || record.Evidence[0].Input.GrantID != record.Lifecycle.GrantID ||
			record.Evidence[0].Receipt.ReceiptID != evidenceReceipt.ReceiptID || record.Evidence[0].CommittedIndex <= record.Result.CommittedIndex {
			t.Fatal("late evidence was duplicated, detached from its grant, or replaced the original outcome", record.Evidence)
		}
	}
	if category != "check" {
		action, exists, err := store.ActionContext(t.Context(), record.Intent.ActionID)
		wantAction := persistence.Succeeded
		if wantUnknown != 0 {
			wantAction = persistence.Unknown
		}
		if err != nil || !exists || action.MonitorID != record.Intent.MonitorID || action.State != wantAction || action.Held != (wantUnknown != 0) {
			t.Fatal("committed result did not preserve the expected incident action state", err)
		}
	}
	effectsMu.Lock()
	defer effectsMu.Unlock()
	if effects[executionID] != wantEffects {
		t.Fatal("original execution invoked its handler an unexpected number of times", effects, wantEffects)
	}
	if mode == "outbox" && len(effects) != 1 {
		t.Fatal("another handler started while result capacity was exhausted", effects)
	}
	for id, count := range effects {
		if count != 1 {
			t.Fatal("duplicate handler invocation", id, count)
		}
	}
	var status struct {
		Records, UnknownActions, PendingOutcomes, PendingEvidence int
		Outcomes                                                  map[string]string
		Retained                                                  map[string]string
	}
	if raw, err := os.ReadFile(statusFile); err != nil || json.Unmarshal(raw, &status) != nil {
		t.Fatal("read worker journal observations", err)
	}
	wantJournalUnknown, wantRecords, wantPending := wantUnknown, wantUnknown, 0
	wantRetained := ""
	if wantUnknown == 1 {
		wantRetained = "unknown"
	}
	if mode == "revoked" {
		wantJournalUnknown, wantRecords, wantPending, wantRetained = 0, 1, 1, "outcome"
		if status.Outcomes[executionID] != map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category] {
			t.Fatal("credential rejection changed the worker's retained known outcome", status.Outcomes)
		}
	}
	if status.Records != len(status.Retained) || status.UnknownActions != wantJournalUnknown || status.PendingEvidence != 0 || status.Retained[executionID] != wantRetained {
		t.Fatal("journal did not retire the acknowledged execution or preserve its unknown action", status)
	}
	if (category != "check" || mode == "revoked" || mode == "outbox") && (status.Records != wantRecords || status.PendingOutcomes != wantPending) {
		t.Fatal("action journal retained unexpected work", status)
	}
	for id, state := range status.Retained {
		retained, found, err := store.WorkerExecution(t.Context(), id)
		if err != nil || !found || retained.Intent.MonitorID != record.Intent.MonitorID || retained.Intent.MonitorUID != record.Intent.MonitorUID || retained.Intent.Category != category {
			t.Fatal("journal retained an execution without its original server record", id, err)
		}
		if effects[id] > 0 && (state == "reserved" || retained.Lifecycle == nil || retained.Lifecycle.GrantID == "") {
			t.Fatal("observed handler effect has no retained execution permission", id, state)
		}
	}
	if len(status.Retained) > wantRecords {
		t.Logf("later scheduled work retained at shutdown; original=%s journal=%v", executionID, status.Retained)
	}
}

func verifyWorkerProcessQueue(t *testing.T, fixture *mainManagementFixture, completedID string) {
	t.Helper()
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error("close stopped queue inspection", err)
		}
	}()
	page, err := store.WorkerExecutions(t.Context(), "", 256)
	if err != nil || page.More {
		t.Fatal("read retained queue after stopping its owner", err)
	}
	completed, pending := false, 0
	for _, execution := range page.Items {
		if execution.Intent.MonitorID != "process-monitor" || execution.Intent.Category != "check" {
			t.Fatal("unexpected execution in the process fixture", execution.Intent.ID)
		}
		if execution.Intent.ID == completedID {
			completed = execution.Result != nil && execution.Result.Input.Status == "success"
			continue
		}
		if execution.Result != nil || execution.Lifecycle != nil && execution.Lifecycle.GrantID != "" {
			t.Fatal("full journal allowed another committed Start", execution.Intent.ID)
		}
		pending++
	}
	if !completed || pending == 0 {
		t.Fatal("capacity test did not leave committed work waiting behind the saved outcome", completed, pending)
	}
	t.Logf("full worker journal retained one outcome while %d committed checks remained unstarted", pending)
}
