//go:build externaljobs

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const workerLoadMonitors = 64

type workerLoadInvocation struct {
	ExecutionID, Kind, MonitorID string
	Count                        int
	Status                       string
}

type workerLoadReceipt struct {
	Receipt                     api.Receipt `json:"receipt"`
	Kind, Status, OutcomeDigest string
	Outcome                     api.Outcome `json:"outcome"`
	CheckExpired                bool        `json:"check_expired,omitempty"`
}

type workerLoadTarget struct {
	mu          sync.Mutex
	invocations map[string]workerLoadInvocation
	checks      map[string]int
	totals      map[string]int
	active      int
	peak        int
	release     chan struct{}
	releaseOnce sync.Once
}

func (s *workerLoadTarget) releaseInitialBatch() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s *workerLoadTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event workerLoadInvocation
	if r.Method != http.MethodPost || r.URL.Path != "/invoke" || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&event) != nil ||
		event.ExecutionID == "" || event.MonitorID == "" || (event.Kind != "check" && event.Kind != "recovery" && event.Kind != "notification") {
		http.Error(w, "invalid invocation", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if len(s.invocations) >= 65536 {
		s.mu.Unlock()
		http.Error(w, "target audit capacity exhausted", http.StatusInsufficientStorage)
		return
	}
	old := s.invocations[event.ExecutionID]
	event.Count = old.Count + 1
	event.Status = map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[event.Kind]
	if event.Kind == "check" {
		s.checks[event.MonitorID]++
		// Real failed and healthy observations repeatedly open and close
		// incidents. Recovery and notification work therefore keep arriving
		// while newer checks compete for the same worker slots.
		if phase := s.checks[event.MonitorID] % 4; phase == 1 || phase == 2 {
			event.Status = "failure"
		}
	}
	s.invocations[event.ExecutionID] = event
	s.totals[event.Kind]++
	s.active++
	if s.active > s.peak {
		s.peak = s.active
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	select {
	case <-s.release:
	case <-r.Context().Done():
		return
	}
	delay := time.NewTimer(2 * time.Second)
	defer delay.Stop()
	select {
	case <-delay.C:
	case <-r.Context().Done():
		return
	}
	_ = json.NewEncoder(w).Encode(api.Outcome{Status: event.Status, Data: json.RawMessage(`{}`)})
}

func (s *workerLoadTarget) counts() (map[string]int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int, len(s.totals))
	for kind, total := range s.totals {
		counts[kind] = total
	}
	return counts, s.peak
}

// The smoke entry point diagnoses fixture setup only. It cannot satisfy the
// five-minute sustained campaign, and ordinary unit runs enable neither case.
func TestMainWorkerLoadSmoke(t *testing.T) {
	if os.Getenv("CPRA_WORKER_LOAD_SMOKE") != "1" {
		t.Skip("explicit real-process load fixture invocation required")
	}
	runWorkerLoad(t, time.Minute)
}

func TestMainWorkerLoad(t *testing.T) {
	if os.Getenv("CPRA_WORKER_LOAD") != "1" {
		t.Skip("explicit five-minute real-process load campaign required")
	}
	runWorkerLoad(t, 5*time.Minute)
}

func seedWorkerLoadMonitors(t *testing.T, fixture *mainManagementFixture, jobs []api.JobType) []string {
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
	drivers := make(map[string]api.DriverConfig)
	refs := make([]persistence.JobTypeReference, 0, len(jobs))
	for _, job := range jobs {
		raw, err := json.Marshal(map[string]any{"jobTypeID": job.Metadata.ID, "version": "v1", "parameters": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		drivers[job.Spec.Kind] = api.DriverConfig{Type: "external", Config: raw}
		refs = append(refs, persistence.JobTypeReference{JobTypeID: job.Metadata.ID, JobTypeUID: job.Metadata.UID,
			Version: "v1", Revision: job.Metadata.ResourceVersion, Category: job.Spec.Kind})
	}
	refs, err = persistence.CanonicalJobTypeReferences(refs)
	if err != nil {
		t.Fatal(err)
	}
	notification := drivers["notification"]
	spec, err := json.Marshal(api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{
		Driver: drivers["check"], Interval: "250ms", Timeout: "30s", HealthyThreshold: api.Pointer(int64(1)), UnhealthyThreshold: api.Pointer(int64(1))},
		Recovery:      &api.RecoverySpec{Driver: drivers["recovery"], MaxAttempts: api.Pointer(int64(1)), MaxFailures: api.Pointer(int64(1)), Cooldown: api.Pointer(api.Duration("0s"))},
		Notifications: api.Pointer(map[string]api.AlertRule{"yellow": {Driver: &notification}, "green": {Driver: &notification}})})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, workerLoadMonitors)
	commands := make([]persistence.Command, 0, workerLoadMonitors)
	for index := range workerLoadMonitors {
		id := fmt.Sprintf("load-%03d", index)
		ids = append(ids, id)
		now := time.Now().UTC()
		record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "Monitor", ID: id}, UID: uuid.NewString(), Revision: uuid.NewString(),
			Generation: 1, Purpose: "desired-resource", CreatedAt: now, UpdatedAt: now}
		record.JobTypeReferences = refs
		resource := api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id, UID: record.UID, ResourceVersion: record.Revision, Generation: 1}, Spec: spec}
		if err := api.ValidateResource(resource); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		record.Payload, err = keys.Sealer().Seal(t.Context(), record.Binding(store.Status().NodeID), payload)
		clear(payload)
		if err != nil {
			t.Fatal(err)
		}
		commands = append(commands, persistence.Command{Kind: "catalog", At: now, Catalog: &persistence.CatalogMutation{Create: true, Record: record}})
	}
	// Public configuration admission remains closed. This stopped, encrypted
	// fixture supplies pinned records; all later dispatch uses normal startup.
	results, err := store.Submit(t.Context(), commands)
	if err != nil || len(results) != len(commands) {
		t.Fatal("seed complete load collection", err, len(results))
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatal("seed pinned load resource", result.Err)
		}
	}
	return ids
}

func runWorkerLoad(t *testing.T, duration time.Duration) {
	t.Helper()
	binary := buildWorkerProcessHelper(t)
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	targetState := &workerLoadTarget{invocations: make(map[string]workerLoadInvocation), checks: make(map[string]int), totals: make(map[string]int), release: make(chan struct{})}
	target := httptest.NewServer(targetState)
	t.Cleanup(target.Close)
	t.Cleanup(targetState.releaseInitialBatch)
	t.Cleanup(func() {
		if t.Failed() {
			totals, peak := targetState.counts()
			t.Logf("failed load target totals=%v peak=%d", totals, peak)
		}
	})
	client, bootstrap := startWorkerServerProcess(t, fixture)
	jobs := make([]api.JobType, 0, 3)
	for _, kind := range []string{"check", "recovery", "notification"} {
		job, err := client.JobTypes().Create(t.Context(), api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "load-" + kind},
			Spec: api.JobTypeSpec{Kind: kind, Version: "v1", Handler: "load-" + kind, ProtocolVersion: "1", Timeout: "30s",
				ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), ResultSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}})
		if err != nil {
			t.Fatal("create load JobType through actual server", err)
		}
		jobs = append(jobs, job.Data)
	}
	bootstrap.stop(t, false)
	ids := seedWorkerLoadMonitors(t, fixture, jobs)
	// Offset failure cycles so all categories keep arriving throughout the
	// campaign. Green recovery notices use the normal cooldown bypass; yellow
	// notices retain the production five-minute suppression policy.
	for index, id := range ids {
		targetState.checks[id] = index % 4
	}
	private := filepath.Dir(fixture.options.runtimeFile)
	grants := make([]persistence.WorkerGrant, 0, len(jobs))
	handlers := make([]map[string]string, 0, len(jobs))
	for _, job := range jobs {
		grants = append(grants, persistence.WorkerGrant{JobTypeID: job.Metadata.ID, JobTypeUID: job.Metadata.UID, Version: "v1", Category: job.Spec.Kind, ResourceKind: "Monitor", ResourceIDs: ids})
		handlers = append(handlers, map[string]string{"ID": job.Metadata.ID, "Category": job.Spec.Kind})
	}
	raw, err := json.Marshal(map[string]any{"grants": grants})
	if err != nil {
		t.Fatal(err)
	}
	grantsFile, tokenFile := filepath.Join(private, "grants.json"), filepath.Join(private, "worker.token")
	if err := os.WriteFile(grantsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
		Action: "issue", Actor: "team/oncall", DataDirectory: fixture.settings.Storage.Directory, WorkerID: "load-worker", GrantsFile: grantsFile, TokenOutput: tokenFile})
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
	statusFile, auditFile := filepath.Join(private, "status.json"), filepath.Join(private, "receipts.jsonl")
	pollAdmissionFile := filepath.Join(private, "stop-poll-admission")
	config := map[string]any{"ServerURL": "https://" + fixture.options.webAddr, "ServerID": identity.ProtocolServerID, "WorkerID": "load-worker", "WorkerUID": identity.Workers[0].UID,
		"JobTypes": handlers, "StateDir": filepath.Join(private, "worker-state"), "WrappingKeyPath": keyFile, "TokenFile": tokenFile,
		"CAFile": fixture.settings.Management.TLS.CertFile, "TargetURL": target.URL, "Mode": "load", "StatusFile": statusFile,
		"ReceiptFile": filepath.Join(private, "receipt.json"), "ReceiptAuditFile": auditFile, "PollAdmissionFile": pollAdmissionFile}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(private, "worker.json")
	if err := os.WriteFile(configFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestServerProcessHelper$", "-test.timeout=10m")
	profileSelected := false
	for _, setting := range []struct{ env, flag string }{
		{"CPRA_WORKER_LOAD_CPU_PROFILE", "-test.cpuprofile="},
		{"CPRA_WORKER_LOAD_MUTEX_PROFILE", "-test.mutexprofile="},
	} {
		profile := os.Getenv(setting.env)
		if profile == "" {
			continue
		}
		if profileSelected {
			t.Fatal("worker diagnostics collect one profile type per run")
		}
		profileSelected = true
		if !filepath.IsAbs(profile) {
			t.Fatal("worker profile requires an absolute private output path")
		}
		if _, err := os.Lstat(profile); !os.IsNotExist(err) {
			t.Fatal("worker profile output must not already exist", err)
		}
		source, err := os.Open(binary)
		if err != nil {
			t.Fatal(err)
		}
		retained, err := os.OpenFile(profile+".test", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(retained, source)
		if err := errors.Join(copyErr, retained.Close(), source.Close()); err != nil {
			t.Fatal("retain worker profile executable", err)
		}
		command.Args = append(command.Args, setting.flag+profile)
		if setting.env == "CPRA_WORKER_LOAD_MUTEX_PROFILE" {
			command.Args = append(command.Args, "-test.mutexprofilefraction=10")
		}
	}
	command.Env = append(os.Environ(), "CPRA_WORKER_SERVER_PROCESS_CONFIG="+configFile)
	worker := startWorkerTestProcess(t, command, filepath.Join(private, "worker.log"))
	var limits map[string]int64
	if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".limits"), &limits); err != nil {
		t.Fatal(err)
	}
	for field, expected := range map[string]int64{"Concurrency": 16, "Records": 4096, "LiveBytes": 256 << 20, "OutcomeBytes": 128 << 10, "DiagnosticBytes": 64 << 10, "AllocatedBytes": 512 << 20} {
		if limits[field] != expected {
			t.Fatalf("load campaign changed default %s: %d != %d", field, limits[field], expected)
		}
	}
	initial := waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool {
		_, peak := targetState.counts()
		return s.ActiveHandlers == 16 && s.Records >= 16 && s.ReservedBytes > 0 && peak == 16
	})
	if initial.UnknownActions != 0 || initial.Records < 16 || initial.ReservedBytes == 0 || initial.AdmissionAvailable {
		t.Fatal("default concurrency did not stop further admission while slots were owned", initial)
	}
	targetState.releaseInitialBatch()
	started := time.Now()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	previous := map[string]int{}
	lastProgress := map[string]time.Time{"check": started, "recovery": started, "notification": started}
	windows := make([]map[string]any, 0, int(duration/(30*time.Second)))
	windowStart := started
	samples, saturated := 0, 0
measurement:
	for {
		select {
		case <-worker.done:
			t.Fatalf("worker exited during load: %v\n%s", worker.err, worker.output())
		case <-server.done:
			t.Fatalf("server exited during load: %v\n%s", server.err, server.output())
		case <-deadline.C:
			break measurement
		case now := <-tick.C:
			statusRaw, err := os.ReadFile(statusFile + ".capacity")
			if err != nil {
				t.Fatal(err)
			}
			var status workerCapacityStatus
			if err := json.Unmarshal(statusRaw, &status); err != nil {
				t.Fatal(err)
			}
			samples++
			if status.ActiveHandlers >= 15 {
				saturated++
			}
			if status.ActiveHandlers > 16 || status.UnknownActions != 0 || status.Records > 4096 || status.AllocatedBytes > 512<<20 {
				if raw, err := os.ReadFile(statusFile + ".results"); err == nil && len(raw) <= 8192 {
					t.Logf("worker result request statistics %s", raw)
				}
				if status.UnknownActions > 0 {
					worker.stop(t, false)
					server.stop(t, false)
					logWorkerLoadUnknowns(t, fixture, statusFile+".capacity.unknown")
				}
				t.Fatal("worker violated default bounds during healthy load", status)
			}
			totals, _ := targetState.counts()
			for _, kind := range []string{"check", "recovery", "notification"} {
				if totals[kind] > previous[kind] {
					lastProgress[kind] = now
				}
				if now.Sub(lastProgress[kind]) > 30*time.Second {
					t.Fatalf("%s made no progress for 30 seconds while work kept arriving: %v", kind, totals)
				}
			}
			previous = totals
			if int(now.Sub(started).Seconds())%5 == 0 {
				t.Logf("load sample elapsed=%s totals=%v active=%d records=%d", now.Sub(started).Round(time.Second), totals, status.ActiveHandlers, status.Records)
			}
			if now.Sub(windowStart) >= 30*time.Second {
				windows = append(windows, map[string]any{"elapsed_seconds": now.Sub(started).Seconds(), "invocations": totals, "active_handlers": status.ActiveHandlers})
				windowStart = now
				t.Logf("mixed load elapsed=%s totals=%v active=%d records=%d", now.Sub(started).Round(time.Second), totals, status.ActiveHandlers, status.Records)
				if raw, err := os.ReadFile(statusFile + ".results"); err == nil && len(raw) <= 8192 {
					t.Logf("worker result request statistics %s", raw)
				}
			}
		}
	}
	measured := time.Since(started)
	if samples == 0 || saturated*3 < samples {
		t.Fatalf("workload did not sustain concurrency pressure: %d saturated samples out of %d", saturated, samples)
	}
	// Stop requesting new work and drain accepted handlers before snoozing.
	// Snooze intentionally cancels remote continuation and may leave an unknown
	// outcome; that behavior belongs to the separate control/crash campaigns.
	if err := os.WriteFile(pollAdmissionFile, []byte("stop new polling\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = waitWorkerProcessFile(t, worker, pollAdmissionFile+".blocked")
	drained := waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool {
		return s.ActiveHandlers == 0 && s.PendingOutcomes == 0 && s.Records == 0
	})
	// Audited controls then cancel unsent server work while completion routes
	// stay available. Configuration writes remain unavailable.
	for _, id := range ids {
		monitor, err := client.Monitors.Get(t.Context(), id)
		if err != nil {
			t.Fatal("read original control revision before drain", err)
		}
		if _, err := client.Monitors.Snooze(t.Context(), id, api.ControlRequest{Revision: monitor.Data.Status.ControlRevision, Duration: "30m", Reason: "Complete private worker load campaign"}); err != nil {
			t.Fatal("stop admission through real monitor control", err)
		}
	}
	worker.stop(t, false)
	server.stop(t, false)
	var final struct {
		Records, PendingOutcomes, UnknownActions int
	}
	if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile), &final); err != nil || final.Records != 0 || final.PendingOutcomes != 0 || final.UnknownActions != 0 {
		t.Fatal("graceful load shutdown did not drain the journal", final, err)
	}
	expiredChecks := verifyWorkerLoadReceipts(t, fixture, targetState, auditFile)
	if len(expiredChecks) > 0 {
		// The count bound intentionally retires older completed checks. Confirm
		// explicit expiry through the normal restarted server; a generic missing
		// record, conflict or unavailable response cannot satisfy reconciliation.
		_, restarted := startWorkerServerProcess(t, fixture)
		token, err := os.ReadFile(tokenFile)
		if err != nil {
			t.Fatal(err)
		}
		protocol, err := cpra.NewWorkerClient(cpra.Config{BaseURL: "https://" + fixture.options.webAddr,
			AuthToken: strings.TrimSpace(string(token)), HTTPClient: fixture.client})
		clear(token)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(protocol.CloseIdleConnections)
		for _, row := range expiredChecks {
			if _, err := protocol.Result(t.Context(), row.Outcome); !errors.Is(err, cpra.ErrWorkerCheckExpired) {
				t.Fatal("missing check lacked authoritative expiry after restart", row.Outcome.ExecutionID, err)
			}
		}
		restarted.stop(t, false)
	}
	totals, peak := targetState.counts()
	if peak != 16 || totals["check"] < workerLoadMonitors*int(duration/(30*time.Second)) {
		t.Fatal("target did not observe the intended sustained workload", peak, totals)
	}
	expiredResults := 0
	for _, row := range expiredChecks {
		if row.CheckExpired {
			expiredResults++
		}
	}
	record := map[string]any{"duration_seconds": measured.Seconds(), "monitors": len(ids), "limits": limits,
		"target_invocations": totals, "target_peak_handlers": peak, "status_samples": samples, "saturated_samples": saturated,
		"windows": windows, "drained": drained, "duplicate_invocations": 0, "unreconciled_outcomes": 0,
		"expired_check_receipts":         len(expiredChecks) - expiredResults,
		"expired_check_finalizations":    expiredResults,
		"public_configuration_qualified": false, "release_qualified": false}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("WORKER_LOAD_EVIDENCE %s", encoded)
}

func logWorkerLoadUnknowns(t *testing.T, fixture *mainManagementFixture, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Log("unknown-outcome observation unavailable", err)
		return
	}
	var rows map[string]map[string]string
	if json.Unmarshal(raw, &rows) != nil {
		t.Log("unknown-outcome observation was invalid")
		return
	}
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Log("recovered unknown-outcome state unavailable", err)
		return
	}
	defer store.Close()
	for id, row := range rows {
		record, found, err := store.WorkerExecution(t.Context(), id)
		if err != nil || !found {
			t.Log("original unknown execution unavailable", id, found, err)
			continue
		}
		t.Logf("unknown original execution=%s worker=%v deadline=%s lifecycle=%+v finalization=%+v", id, row, record.Intent.Deadline.Format(time.RFC3339Nano), record.Lifecycle, record.Finalization)
	}
}

func verifyWorkerLoadReceipts(t *testing.T, fixture *mainManagementFixture, target *workerLoadTarget, path string) []workerLoadReceipt {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		t.Fatal("receipt audit is not a bounded regular file", err)
	}
	scanner := bufio.NewScanner(io.LimitReader(file, 32<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	receipts := make(map[string]api.Receipt)
	outcomes := make(map[string]workerLoadInvocation)
	expiredChecks := make(map[string]workerLoadReceipt)
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for scanner.Scan() {
		var row workerLoadReceipt
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		id := row.Outcome.ExecutionID
		if len(outcomes) >= 65536 || id == "" {
			t.Fatal("invalid or unbounded result audit")
		}
		encoded, err := json.Marshal(row.Outcome)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(encoded)) != row.OutcomeDigest ||
			row.Outcome.Kind != row.Kind || row.Outcome.Status != row.Status {
			t.Fatal("result audit changed the original outcome", id, err)
		}
		if previous, exists := expiredChecks[id]; exists && previous.OutcomeDigest != row.OutcomeDigest {
			t.Fatal("expired check audit changed its original outcome", id)
		}
		if row.CheckExpired {
			if row.Kind != "check" || row.Receipt != (api.Receipt{}) || receipts[id].ReceiptID != "" {
				t.Fatal("explicit check expiry contradicted a receipt or action outcome", id)
			}
			expiredChecks[id] = row
		} else {
			if row.Receipt.ExecutionID != id || row.Receipt.ReceiptID == "" || expiredChecks[id].CheckExpired {
				t.Fatal("invalid or contradictory accepted receipt", id)
			}
			if old, exists := receipts[id]; exists && old != row.Receipt {
				t.Fatal("same outcome received conflicting receipts", id)
			}
			receipts[id] = row.Receipt
		}
		outcomes[id] = workerLoadInvocation{Kind: row.Kind, Status: row.Status}
		saved, found, err := store.WorkerExecution(t.Context(), id)
		if err != nil {
			t.Fatal("committed outcome lookup failed", id, err)
		}
		if !found && row.Kind == "check" {
			expiredChecks[id] = row
			continue
		}
		if !found || saved.Result == nil {
			t.Fatal("accepted outcome missing from recovered server state", id, found, err)
		}
		if saved.Result.Receipt.ReceiptID != row.Receipt.ReceiptID || saved.Result.Input.Status != row.Status || saved.Intent.Category != row.Kind {
			t.Fatal("receipt audit differs from original committed result", id)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// Every server-issued start, including a grant whose reply was lost, must
	// have an authoritative terminal outcome after the controlled drain.
	for next := ""; ; {
		page, err := store.WorkerExecutions(t.Context(), next, 256)
		if err != nil {
			t.Fatal(err)
		}
		for _, execution := range page.Items {
			if execution.Lifecycle != nil && execution.Lifecycle.GrantID != "" && execution.Result == nil {
				t.Fatal("started server execution had no committed outcome after drain", execution.Intent.ID)
			}
		}
		if !page.More {
			break
		}
		next = page.Next
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(outcomes) != len(target.invocations) {
		t.Fatalf("independent effects and authoritative results differ: %d effects, %d results", len(target.invocations), len(outcomes))
	}
	perMonitor := make(map[string]map[string]int)
	for id, invocation := range target.invocations {
		if invocation.Count != 1 || receipts[id].ReceiptID == "" && !expiredChecks[id].CheckExpired {
			t.Fatal("repeated or unreconciled target invocation", id, invocation.Count)
		}
		if outcomes[id].Kind != invocation.Kind || outcomes[id].Status != invocation.Status {
			t.Fatal("committed outcome differs from independent target observation", id)
		}
		if perMonitor[invocation.MonitorID] == nil {
			perMonitor[invocation.MonitorID] = make(map[string]int)
		}
		perMonitor[invocation.MonitorID][invocation.Kind]++
	}
	if len(perMonitor) != workerLoadMonitors {
		t.Fatal("some configured monitors never received execution capacity", len(perMonitor))
	}
	for id, categories := range perMonitor {
		for _, kind := range []string{"check", "recovery", "notification"} {
			if categories[kind] == 0 {
				t.Fatal("monitor category starved under mixed load", id, kind)
			}
		}
	}
	retired := make([]workerLoadReceipt, 0, len(expiredChecks))
	for _, row := range expiredChecks {
		retired = append(retired, row)
	}
	return retired
}
