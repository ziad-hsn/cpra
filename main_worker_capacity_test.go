//go:build externaljobs

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
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

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type workerCapacityStatus struct {
	Records, PendingOutcomes, UnknownActions, ActiveHandlers, AdmissionCapacity int
	LiveBytes, ReservedBytes, AllocatedBytes                                    int64
	AdmissionAvailable                                                          bool
}

func waitWorkerCapacityStatus(t *testing.T, process *workerTestProcess, path string, ready func(workerCapacityStatus) bool) workerCapacityStatus {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var status workerCapacityStatus
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &status); err != nil {
				t.Fatal("decode atomic worker status", err)
			}
			if ready(status) {
				return status
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-process.done:
			t.Fatalf("worker exited before capacity observation: %v\n%s", process.err, process.output())
		case <-deadline.C:
			t.Fatalf("worker capacity observation timed out: %+v\n%s", status, process.output())
		case <-tick.C:
		}
	}
}

// This uses production startup, TLS requests, Raft, the SDK runner and bbolt.
// Only the independent target and the result-reply loss are controlled fixtures.
func runWorkerCapacityCase(t *testing.T, binary, category, limit string) {
	t.Helper()
	unknown := strings.HasPrefix(limit, "unknown-")
	fixture := newMainManagementFixture(t)
	fixture.settings.ExternalJobs.Enabled = true
	writeMainRuntime(t, fixture.options.runtimeFile, fixture.settings)
	var mu sync.Mutex
	effects := make(map[string]int)
	firstEffect := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTarget := func() { releaseOnce.Do(func() { close(release) }) }
	payload := strings.Repeat("capacity-payload-", 6<<10)
	data, err := json.Marshal(map[string]string{"payload": payload})
	if err != nil {
		t.Fatal(err)
	}
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
		mu.Lock()
		first := len(effects) == 0
		effects[event.ExecutionID]++
		mu.Unlock()
		if first {
			firstEffect <- event.ExecutionID
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		status := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category]
		if unknown {
			status = "unknown"
		}
		_ = json.NewEncoder(w).Encode(api.Outcome{Status: status, Data: data})
	}))
	t.Cleanup(target.Close)
	t.Cleanup(releaseTarget)
	client, bootstrap := startWorkerServerProcess(t, fixture)
	job, err := client.JobTypes().Create(t.Context(), api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "capacity-" + category},
		Spec: api.JobTypeSpec{Kind: category, Version: "v1", Handler: "capacity-" + category, ProtocolVersion: "1", Timeout: "15s",
			ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			ResultSchema:    json.RawMessage(`{"type":"object","properties":{"payload":{"type":"string"}},"additionalProperties":false}`)}})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.stop(t, false)
	ids := []string{"capacity-first", "capacity-next"}
	seedWorkerProcessMonitor(t, fixture, job.Data, target.URL, ids...)
	private := filepath.Dir(fixture.options.runtimeFile)
	grants, err := json.Marshal(map[string]any{"grants": []persistence.WorkerGrant{{JobTypeID: job.Data.Metadata.ID, JobTypeUID: job.Data.Metadata.UID,
		Version: "v1", Category: category, ResourceKind: "Monitor", ResourceIDs: ids}}})
	if err != nil {
		t.Fatal(err)
	}
	grantsFile, tokenFile := filepath.Join(private, "grants.json"), filepath.Join(private, "worker.token")
	if err := os.WriteFile(grantsFile, grants, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
		Action: "issue", Actor: "team/oncall", DataDirectory: fixture.settings.Storage.Directory, WorkerID: "capacity-worker", GrantsFile: grantsFile, TokenOutput: tokenFile})
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
	limits := map[string]int64{"Concurrency": 3, "Records": 8, "OutcomeBytes": 128 << 10, "LiveBytes": 160 << 10, "AllocatedBytes": 4 << 20}
	if limit == "allocated-bytes" {
		limits["Concurrency"], limits["LiveBytes"], limits["AllocatedBytes"] = 1, 2<<20, 128<<10
	}
	if limit == "unknown-records" {
		limits["Records"], limits["LiveBytes"] = 1, 2<<20
	}
	configFile := filepath.Join(private, "worker.json")
	config := map[string]any{"ServerID": identity.ProtocolServerID, "WorkerID": "capacity-worker", "WorkerUID": identity.Workers[0].UID,
		"JobTypeID": job.Data.Metadata.ID, "Category": category, "StateDir": filepath.Join(private, "worker-state"),
		"WrappingKeyPath": keyFile, "TokenFile": tokenFile, "CAFile": fixture.settings.Management.TLS.CertFile,
		"TargetURL": target.URL, "Mode": "capacity", "Limits": limits}
	gateFile := filepath.Join(private, "receipt.release")
	if !unknown {
		config["ReceiptGate"] = gateFile
	}
	startWorker := func(phase string) (*workerTestProcess, string, string, string) {
		statusFile, receiptFile, boundaryFile := filepath.Join(private, phase+"-status.json"), filepath.Join(private, phase+"-receipt.json"), filepath.Join(private, phase+"-boundary.json")
		config["StatusFile"], config["ReceiptFile"], config["BoundaryFile"], config["ServerURL"] = statusFile, receiptFile, boundaryFile, "https://"+fixture.options.webAddr
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "-test.run=^TestServerProcessHelper$", "-test.timeout=1m")
		command.Env = append(os.Environ(), "CPRA_WORKER_SERVER_PROCESS_CONFIG="+configFile)
		return startWorkerTestProcess(t, command, filepath.Join(private, phase+".log")), statusFile, receiptFile, boundaryFile
	}
	worker, statusFile, receiptFile, boundaryFile := startWorker("initial")
	initial := waitWorkerCapacityStatus(t, worker, statusFile+".initial", func(s workerCapacityStatus) bool { return true })
	if initial.Records != 0 || initial.AdmissionCapacity < 1 || initial.AllocatedBytes >= limits["AllocatedBytes"] {
		t.Fatal("fixture must start below its configured limit", initial, limits)
	}
	reserved := waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool { return s.ActiveHandlers == 1 && s.ReservedBytes > 0 })
	if reserved.Records != 1 || reserved.LiveBytes != 0 || reserved.ReservedBytes != 160<<10 || limit != "allocated-bytes" && reserved.AdmissionCapacity != 0 {
		t.Fatal("admission did not reserve completion capacity before invoking the target", reserved)
	}
	var originalID string
	select {
	case originalID = <-firstEffect:
	case <-time.After(5 * time.Second):
		t.Fatal("reserved execution did not reach the independent target")
	}
	releaseTarget()
	full := waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool {
		return s.ActiveHandlers == 0 && (unknown && s.UnknownActions == 1 || !unknown && s.PendingOutcomes == 1)
	})
	if full.Records != 1 || full.ReservedBytes != 0 || full.LiveBytes < int64(len(data)) || full.LiveBytes > limits["LiveBytes"] || full.AdmissionCapacity != 0 || full.AdmissionAvailable {
		t.Fatal("full journal lost its payload or allowed new admission", full, limits)
	}
	if limit == "allocated-bytes" && full.AllocatedBytes < limits["AllocatedBytes"] {
		t.Fatal("allocation scenario never crossed the physical file threshold", full)
	}
	var receipt api.Receipt
	if unknown {
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, receiptFile), &receipt); err != nil || receipt.ExecutionID != originalID || receipt.Disposition != api.ReceiptDispositionUnknown {
			t.Fatal("unknown outcome has no original committed receipt", receipt, err)
		}
	} else {
		var boundary struct{ ExecutionID, ReceiptID, OutcomeDigest string }
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, boundaryFile), &boundary); err != nil || boundary.ExecutionID != originalID || boundary.ReceiptID == "" || len(boundary.OutcomeDigest) != 64 {
			t.Fatal("known outcome did not reach the lost-reply boundary", err)
		}
		receipt.ExecutionID, receipt.ReceiptID = boundary.ExecutionID, boundary.ReceiptID
		config["ExpectedExecutionID"], config["ExpectedOutcomeDigest"] = originalID, boundary.OutcomeDigest
	}
	// Observe later scheduling progress before stopping the owner to inspect its
	// committed queue. No fixed sleep stands in for admitted work or a result.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		observed, err := client.SLO.Get(t.Context())
		progress := false
		if err == nil {
			for _, row := range observed.Data.Reports {
				if category == "check" && row.Driver == "external" && row.Expected+row.Pending >= 4 || category != "check" && row.Driver == "http" && row.Samples >= 4 {
					progress = true
				}
			}
		}
		if progress {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("controller made no scheduling progress behind the full journal", err)
		case <-tick.C:
		}
	}
	worker.kill(t)
	server.stop(t, false)
	verifyWorkerCapacityQueue(t, fixture, originalID, receipt.ReceiptID, category, unknown)
	mu.Lock()
	count := len(effects)
	mu.Unlock()
	if count != 1 {
		t.Fatal("full journal allowed a second target invocation", count)
	}
	_, server = startWorkerServerProcess(t, fixture)
	if !unknown {
		config["OutboxOnly"] = true
		if err := os.WriteFile(gateFile, []byte("release"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	worker, statusFile, receiptFile, _ = startWorker("restarted")
	if unknown {
		// A second completed reconciliation proves the first terminal response
		// did not discard the held action merely to regain capacity.
		var reconciled struct{ ExecutionID, ReceiptID string }
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, statusFile+".reconciled"), &reconciled); err != nil || reconciled.ExecutionID != originalID || reconciled.ReceiptID != receipt.ReceiptID {
			t.Fatal("unknown action did not retain its original terminal receipt", reconciled, err)
		}
	}
	restored := waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool {
		return s.ActiveHandlers == 0 && s.PendingOutcomes == 0 && (unknown && s.UnknownActions == 1 || !unknown && s.Records == 0)
	})
	if restored.ReservedBytes != 0 || unknown && (restored.Records != 1 || restored.AdmissionCapacity != 0 || restored.LiveBytes != full.LiveBytes) {
		t.Fatal("restart discarded or changed retained unknown capacity", restored, full)
	}
	if !unknown {
		var retried api.Receipt
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, receiptFile), &retried); err != nil || retried.ExecutionID != originalID || retried.ReceiptID != receipt.ReceiptID {
			t.Fatal("outbox restart lost the original committed receipt", retried, err)
		}
		if restored.LiveBytes != 0 || limit == "allocated-bytes" && (restored.AllocatedBytes < limits["AllocatedBytes"] || restored.AdmissionCapacity != 0) || limit == "live-bytes" && restored.AdmissionCapacity < 1 {
			t.Fatal("receipt retirement confused live and allocated capacity", restored, limits)
		}
	}
	worker.stop(t, false)
	// A positive control enlarges only the exhausted budget. The next handler
	// must then run, without repeating the original execution or deleting it.
	config["OutboxOnly"], config["ReceiptGate"] = false, ""
	switch limit {
	case "allocated-bytes":
		limits["AllocatedBytes"] = 4 << 20
	case "unknown-bytes":
		limits["LiveBytes"] = 320 << 10
	case "unknown-records":
		limits["Records"] = 2
	}
	worker, statusFile, receiptFile, _ = startWorker("resumed")
	if unknown {
		waitWorkerCapacityStatus(t, worker, statusFile+".capacity", func(s workerCapacityStatus) bool { return s.UnknownActions == 2 && s.ActiveHandlers == 0 })
	} else {
		var next api.Receipt
		if err := json.Unmarshal(waitWorkerProcessFile(t, worker, receiptFile), &next); err != nil || next.ExecutionID == originalID || next.ReceiptID == "" {
			t.Fatal("restored capacity did not permit new work", next, err)
		}
	}
	worker.stop(t, false)
	server.stop(t, false)
	mu.Lock()
	defer mu.Unlock()
	if len(effects) < 2 || effects[originalID] != 1 {
		t.Fatal("capacity recovery lost work or repeated the original handler", effects)
	}
	for id, count := range effects {
		if count != 1 {
			t.Fatal("capacity recovery duplicated an external invocation", id, count)
		}
	}
	if unknown {
		var final struct {
			UnknownActions int
			Retained       map[string]string
		}
		raw, err := os.ReadFile(statusFile)
		if err != nil || json.Unmarshal(raw, &final) != nil || final.UnknownActions != 2 || final.Retained[originalID] != "unknown" {
			t.Fatal("new work displaced the original unknown action", final, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(private, "worker-state", "worker.db"))
	if err != nil || bytes.Contains(raw, []byte(payload[:128])) {
		t.Fatal("worker journal exposed the outcome plaintext", err)
	}
	t.Logf("%s: reserved=%d live=%d allocated=%d; restart records=%d live=%d allocated=%d; distinct effects=%d", limit,
		reserved.ReservedBytes, full.LiveBytes, full.AllocatedBytes, restored.Records, restored.LiveBytes, restored.AllocatedBytes, len(effects))
}

func verifyWorkerCapacityQueue(t *testing.T, fixture *mainManagementFixture, id, receiptID, category string, unknown bool) {
	t.Helper()
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.WorkerExecutions(t.Context(), "", 256)
	if err != nil || page.More {
		t.Fatal("inspect stopped capacity queue", err)
	}
	found, pending := false, 0
	for _, execution := range page.Items {
		if execution.Intent.Category != category {
			t.Fatal("unexpected kind in capacity queue", execution.Intent.Category)
		}
		if execution.Intent.ID == id {
			terminal, ok := execution.TerminalReceipt()
			found = ok && terminal.ReceiptID == receiptID && execution.Result != nil && (execution.Result.Input.Status == "unknown") == unknown
			if unknown {
				action, exists, err := store.ActionContext(t.Context(), execution.Intent.ActionID)
				if err != nil || !exists || action.MonitorID != execution.Intent.MonitorID || action.State != persistence.Unknown || !action.Held {
					t.Fatal("server did not hold the unknown incident action", err)
				}
			}
			continue
		}
		if execution.Result != nil || execution.Lifecycle != nil && execution.Lifecycle.GrantID != "" {
			t.Fatal("full worker journal allowed another committed Start", execution.Intent.ID)
		}
		pending++
	}
	if !found || pending == 0 {
		t.Fatal("capacity scenario needs a retained original result and committed waiting work", found, pending)
	}
}
