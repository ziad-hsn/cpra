//go:build externaljobs

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	bolt "go.etcd.io/bbolt"
)

// The application module builds this helper independently. It uses the real
// SDK transport and runner; the worker module imports no application package.
type serverProcessConfig struct {
	ServerURL, ServerID, WorkerID, WorkerUID, JobTypeID, Category string
	StateDir, WrappingKeyPath, TokenFile, CAFile, TargetURL       string
	Mode, BoundaryFile, ReceiptFile, StatusFile                   string
	EvidenceFile                                                  string
	ReceiptGate, ExpectedExecutionID, ExpectedOutcomeDigest       string
	Limits                                                        Limits
	OutboxOnly                                                    bool
	JobTypes                                                      []serverProcessJobType
	ReceiptAuditFile                                              string
	PollAdmissionFile                                             string
}

type serverProcessJobType struct {
	ID, Category string
}

type serverProcessProtocol struct {
	*cpra.WorkerClient
	config        serverProcessConfig
	reconciled    atomic.Int64
	receiptAudit  *os.File
	auditMu       sync.Mutex
	resultMu      sync.Mutex
	resultStats   serverProcessResultStats
	protocolMu    sync.Mutex
	protocolStats serverProcessProtocolStats
}

type serverProcessCallStats struct {
	Calls, Failed                      int
	TotalMilliseconds, MaxMilliseconds float64
}

func (s *serverProcessCallStats) observe(elapsed time.Duration, err error) {
	s.Calls++
	if err != nil {
		s.Failed++
	}
	milliseconds := float64(elapsed) / float64(time.Millisecond)
	s.TotalMilliseconds += milliseconds
	s.MaxMilliseconds = max(s.MaxMilliseconds, milliseconds)
}

type serverProcessProtocolStats struct {
	Poll, Start                                        serverProcessCallStats
	RequestedCapacity, ReturnedAssignments, EmptyPolls int64
	StartDispositions                                  map[api.StartDisposition]int
	StartModes                                         map[string]int
	StartFailures, PollFailures                        map[string]int
	MinimumRemainingSeconds                            *float64
}

func countProtocolFailure(counts *map[string]int, err error) {
	if err == nil {
		return
	}
	code := "transport"
	var problem *cpra.Error
	if errors.As(err, &problem) {
		switch problem.Problem.Code {
		case "unavailable", "admissionUnavailable", "requestInterrupted", "workerStartUnconfirmed", "workerExecutionQuota", "workerExecutionExpired", "workerCheckExpired", "workerPollUnconfirmed", "workerSessionExpired":
			code = problem.Problem.Code
		default:
			code = "otherProblem"
		}
	}
	if *counts == nil {
		*counts = make(map[string]int)
	}
	(*counts)[code]++
}

func (p *serverProcessProtocol) recordPoll(request api.PollRequest, reply *api.Assignments, started time.Time, err error) {
	if p.config.Mode != "load" {
		return
	}
	observed := time.Now()
	p.protocolMu.Lock()
	defer p.protocolMu.Unlock()
	s := &p.protocolStats
	s.Poll.observe(observed.Sub(started), err)
	countProtocolFailure(&s.PollFailures, err)
	s.RequestedCapacity += request.Capacity
	if err == nil && reply != nil {
		s.ReturnedAssignments += int64(len(reply.Items))
		if len(reply.Items) == 0 {
			s.EmptyPolls++
		}
		for _, assignment := range reply.Items {
			remaining := assignment.Deadline.Sub(observed).Seconds()
			if s.MinimumRemainingSeconds == nil || remaining < *s.MinimumRemainingSeconds {
				s.MinimumRemainingSeconds = &remaining
			}
		}
	}
}

func (p *serverProcessProtocol) recordStart(request api.StartRequest, reply *api.StartResponse, started time.Time, err error) {
	if p.config.Mode != "load" {
		return
	}
	elapsed := time.Since(started)
	p.protocolMu.Lock()
	defer p.protocolMu.Unlock()
	s := &p.protocolStats
	s.Start.observe(elapsed, err)
	countProtocolFailure(&s.StartFailures, err)
	if s.StartModes == nil {
		s.StartModes = make(map[string]int)
	}
	mode := string(request.Mode)
	if request.Mode != api.StartModeBegin && request.Mode != api.StartModeReconcile {
		mode = "invalid"
	}
	s.StartModes[mode]++
	if err == nil && reply != nil {
		if s.StartDispositions == nil {
			s.StartDispositions = make(map[api.StartDisposition]int)
		}
		disposition := reply.Disposition
		switch disposition {
		case api.StartDispositionGranted, api.StartDispositionStarted, api.StartDispositionPending, api.StartDispositionRejected, api.StartDispositionTerminal, api.StartDispositionUnknown:
		default:
			disposition = "invalid"
		}
		s.StartDispositions[disposition]++
	}
}

func (p *serverProcessProtocol) writeProtocolStats() error {
	p.protocolMu.Lock()
	s := p.protocolStats
	s.StartDispositions = maps.Clone(s.StartDispositions)
	s.StartModes = maps.Clone(s.StartModes)
	s.StartFailures, s.PollFailures = maps.Clone(s.StartFailures), maps.Clone(s.PollFailures)
	// MinimumRemainingSeconds is replaced, never mutated through its pointer.
	p.protocolMu.Unlock()
	return writeServerProcessJSON(p.config.StatusFile+".protocol", s)
}

type serverProcessResultStats struct {
	Attempts, Replies, Failed int
	TotalMilliseconds         float64
	MaxMilliseconds           float64
	Statuses                  map[int]int
	Dispositions              map[api.ReceiptDisposition]int
	Recent                    []serverProcessResultCall
}

type serverProcessResultCall struct {
	Kind         string
	Milliseconds float64
	HTTPStatus   int
}

func (p *serverProcessProtocol) recordResultCall(kind string, started time.Time, receipt *api.Receipt, err error) {
	if p.config.Mode != "load" {
		return
	}
	duration := float64(time.Since(started)) / float64(time.Millisecond)
	status := http.StatusOK
	if err != nil {
		status = 0
		var problem *cpra.Error
		if errors.As(err, &problem) {
			status = problem.StatusCode
		}
	}
	p.resultMu.Lock()
	defer p.resultMu.Unlock()
	stats := &p.resultStats
	stats.Attempts++
	stats.TotalMilliseconds += duration
	stats.MaxMilliseconds = max(stats.MaxMilliseconds, duration)
	if err == nil {
		stats.Replies++
		if receipt != nil {
			if stats.Dispositions == nil {
				stats.Dispositions = make(map[api.ReceiptDisposition]int)
			}
			disposition := receipt.Disposition
			switch disposition {
			case api.ReceiptDispositionAccepted, api.ReceiptDispositionFinalized, api.ReceiptDispositionUnknown:
			default:
				disposition = "invalid"
			}
			stats.Dispositions[disposition]++
		}
	} else {
		stats.Failed++
	}
	if stats.Statuses == nil {
		stats.Statuses = make(map[int]int)
	}
	stats.Statuses[status]++
	stats.Recent = append(stats.Recent, serverProcessResultCall{Kind: kind, Milliseconds: duration, HTTPStatus: status})
	if len(stats.Recent) > 8 {
		stats.Recent = stats.Recent[1:]
	}
}

func (p *serverProcessProtocol) writeResultStats() error {
	p.resultMu.Lock()
	stats := p.resultStats
	stats.Statuses = maps.Clone(stats.Statuses)
	stats.Dispositions = maps.Clone(stats.Dispositions)
	stats.Recent = append([]serverProcessResultCall(nil), stats.Recent...)
	p.resultMu.Unlock()
	return writeServerProcessJSON(p.config.StatusFile+".results", stats)
}

func writeServerProcessJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(raw)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (p *serverProcessProtocol) boundary(ctx context.Context, executionID, receiptID string) error {
	if err := writeServerProcessJSON(p.config.BoundaryFile, map[string]string{"executionID": executionID, "receiptID": receiptID}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (p *serverProcessProtocol) Start(ctx context.Context, request api.StartRequest) (*api.StartResponse, error) {
	started := time.Now()
	reply, err := p.WorkerClient.Start(ctx, request)
	p.recordStart(request, reply, started, err)
	if err == nil && p.config.Mode == "capacity" && request.Mode == api.StartModeReconcile && reply.Disposition == api.StartDispositionTerminal && p.reconciled.Add(1) >= 2 {
		if err := writeServerProcessJSON(p.config.StatusFile+".reconciled", map[string]string{"executionID": request.ExecutionID, "receiptID": reply.ReceiptID}); err != nil {
			return nil, err
		}
	}
	if err == nil && p.config.Mode == "start-reply" && request.Mode == api.StartModeBegin && reply.Disposition == api.StartDispositionGranted {
		// The server committed a grant, but the runner has not received it.
		return nil, p.boundary(ctx, request.ExecutionID, "")
	}
	return reply, err
}

func (p *serverProcessProtocol) observeRevocation(operation, executionID string, err error) error {
	var response *cpra.Error
	if p.config.Mode == "revoked" && errors.As(err, &response) && response.StatusCode == http.StatusUnauthorized && response.Problem.Code == "unauthorized" {
		// Each protocol loop owns a distinct file. Record only the explicit
		// authentication decision, never the bearer token or response details.
		return errors.Join(err, writeServerProcessJSON(p.config.StatusFile+".denied-"+operation, map[string]any{"unauthorized": true, "executionID": executionID}))
	}
	return err
}

func (p *serverProcessProtocol) Poll(ctx context.Context, request api.PollRequest) (*api.Assignments, error) {
	if p.config.PollAdmissionFile != "" {
		if _, err := os.Stat(p.config.PollAdmissionFile); err == nil {
			if err := writeServerProcessJSON(p.config.PollAdmissionFile+".blocked", map[string]bool{"admissionStopped": true}); err != nil {
				return nil, err
			}
			// Stop new requests while accepted handlers and real result
			// delivery drain. No server reply is replaced by the fixture.
			<-ctx.Done()
			return nil, ctx.Err()
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	started := time.Now()
	reply, err := p.WorkerClient.Poll(ctx, request)
	p.recordPoll(request, reply, started, err)
	return reply, p.observeRevocation("poll", "", err)
}

func (p *serverProcessProtocol) Result(ctx context.Context, outcome api.Outcome) (*api.Receipt, error) {
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if outcome.ExecutionID == p.config.ExpectedExecutionID && digest != p.config.ExpectedOutcomeDigest {
		return nil, errors.New("restarted worker changed its original outcome")
	}
	if p.config.Mode == "result-request" {
		// The real runner has synchronously persisted this outcome already.
		return nil, p.boundary(ctx, outcome.ExecutionID, "")
	}
	started := time.Now()
	receipt, err := p.WorkerClient.Result(ctx, outcome)
	p.recordResultCall(outcome.Kind, started, receipt, err)
	if err != nil {
		if outcome.Kind == "check" && errors.Is(err, cpra.ErrWorkerCheckExpired) {
			if auditErr := p.auditResult(outcome, digest, nil, true); auditErr != nil {
				return nil, auditErr
			}
		}
		return receipt, p.observeRevocation("result", outcome.ExecutionID, err)
	}
	if p.config.Mode == "result-reply" || p.config.Mode == "outbox" {
		// The real server receipt exists; the runner still owns its outbox row.
		return nil, p.boundary(ctx, outcome.ExecutionID, receipt.ReceiptID)
	}
	if p.config.ReceiptGate != "" {
		if err := writeServerProcessJSON(p.config.BoundaryFile, map[string]string{"executionID": outcome.ExecutionID, "receiptID": receipt.ReceiptID, "outcomeDigest": digest}); err != nil {
			return nil, err
		}
		for {
			if _, err := os.Stat(p.config.ReceiptGate); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if !pause(ctx, 20*time.Millisecond) {
				return nil, ctx.Err()
			}
		}
	}
	if err := writeServerProcessJSON(p.config.ReceiptFile, receipt); err != nil {
		return nil, err
	}
	if err := p.auditResult(outcome, digest, receipt, false); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (p *serverProcessProtocol) auditResult(outcome api.Outcome, digest string, receipt *api.Receipt, checkExpired bool) error {
	if p.receiptAudit == nil {
		return nil
	}
	p.auditMu.Lock()
	defer p.auditMu.Unlock()
	return json.NewEncoder(p.receiptAudit).Encode(struct {
		Receipt                     *api.Receipt `json:"receipt,omitempty"`
		Kind, Status, OutcomeDigest string
		Outcome                     api.Outcome `json:"outcome"`
		CheckExpired                bool        `json:"check_expired,omitempty"`
	}{receipt, outcome.Kind, outcome.Status, digest, outcome, checkExpired})
}

func TestServerProcessCheckExpiryAudit(t *testing.T) {
	for _, test := range []struct {
		name, kind, status, code string
		wantAudit                bool
	}{
		{"check-expired", "check", "success", "workerCheckExpired", true},
		{"generic-gone", "check", "success", "expired", false},
		{"action-expired", "recovery", "completed", "workerCheckExpired", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Expired", "status": 410, "code": test.code})
			}))
			defer server.Close()
			client, err := cpra.NewWorkerClient(cpra.Config{BaseURL: server.URL, AuthToken: "fixture-token", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			directory := t.TempDir()
			file, err := os.Create(filepath.Join(directory, "audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			protocol := &serverProcessProtocol{WorkerClient: client, receiptAudit: file,
				config: serverProcessConfig{StatusFile: filepath.Join(directory, "status")}}
			outcome := api.Outcome{ServerID: "server", WorkerUID: "worker", ExecutionID: "execution", GrantID: "grant",
				Kind: test.kind, Status: test.status, Evidence: []string{}, Data: json.RawMessage(`{}`)}
			receipt, err := protocol.Result(t.Context(), outcome)
			if err == nil || receipt != nil {
				t.Fatal("expired result unexpectedly received a receipt", receipt, err)
			}
			var problem *cpra.Error
			if requests.Load() != 1 || !errors.As(err, &problem) || problem.StatusCode != http.StatusGone || problem.Problem.Code != test.code {
				t.Fatal("result did not receive the configured server expiry", requests.Load(), err)
			}
			raw, err := os.ReadFile(file.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !test.wantAudit {
				if len(raw) != 0 {
					t.Fatal("generic expiry or action created a check-expiry audit", string(raw))
				}
				return
			}
			var row struct {
				Receipt       *api.Receipt `json:"receipt"`
				CheckExpired  bool         `json:"check_expired"`
				Outcome       api.Outcome  `json:"outcome"`
				OutcomeDigest string
			}
			if err := json.Unmarshal(raw, &row); err != nil || row.Receipt != nil || !row.CheckExpired || row.Outcome.ExecutionID != outcome.ExecutionID {
				t.Fatal("expiry audit fabricated a receipt or lost execution identity", string(raw), err)
			}
			encoded, err := json.Marshal(outcome)
			if err != nil || row.OutcomeDigest != fmt.Sprintf("%x", sha256.Sum256(encoded)) {
				t.Fatal("expiry audit changed the original outcome", err)
			}
		})
	}
}

func (p *serverProcessProtocol) LateEvidence(ctx context.Context, evidence api.LateEvidenceRequest) (*api.Receipt, error) {
	receipt, err := p.WorkerClient.LateEvidence(ctx, evidence)
	if err != nil {
		return receipt, err
	}
	if p.config.Mode == "evidence-reply" {
		return nil, p.boundary(ctx, evidence.ExecutionID, receipt.ReceiptID)
	}
	if err := writeServerProcessJSON(p.config.StatusFile+".evidence", receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func observeServerProcessStatus(ctx context.Context, runner *Runner, protocol *serverProcessProtocol, mode, path string) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	unknownObserved := false
	var protocolAt time.Time
	for {
		status, err := runner.Status()
		if err != nil {
			return err
		}
		if mode == "load" && time.Since(protocolAt) >= time.Second {
			if err := protocol.writeProtocolStats(); err != nil {
				return err
			}
			if err := protocol.writeResultStats(); err != nil {
				return err
			}
			protocolAt = time.Now()
		}
		ready := status.DrainDeadlineExpired
		if mode == "capacity" || mode == "load" {
			ready = true
		}
		if mode == "outbox" {
			ready = status.PendingOutcomes > 0 && status.ActiveHandlers == 0
		}
		if ready {
			if mode == "load" && status.UnknownActions > 0 && !unknownObserved {
				records, err := runner.journal.records()
				if err != nil {
					return err
				}
				unknown := make(map[string]map[string]string)
				for _, record := range records {
					if record.State == "unknown" && len(unknown) < 16 {
						item := map[string]string{"kind": record.Kind, "receiptID": record.ReceiptID}
						if record.Outcome != nil {
							item["outcome"] = record.Outcome.Status
						}
						unknown[record.ExecutionID] = item
					}
				}
				if err := writeServerProcessJSON(path+".unknown", unknown); err != nil {
					return err
				}
				unknownObserved = true
			}
			if mode == "uncooperative" && !errors.Is(runner.Close(), ErrRunning) {
				return errors.New("worker released its journal while a handler remained active")
			}
			if err := writeServerProcessJSON(path, map[string]any{
				"draining": status.Draining, "deadlineExpired": status.DrainDeadlineExpired,
				"activeHandlers": status.ActiveHandlers, "admissionAvailable": status.AdmissionAvailable,
				"admissionCapacity": status.AdmissionCapacity, "records": status.Records,
				"pendingOutcomes": status.PendingOutcomes,
				"unknownActions":  status.UnknownActions, "liveBytes": status.LiveBytes,
				"reservedBytes": status.ReservedBytes, "allocatedBytes": status.AllocatedBytes,
				"deadlineError": errors.Is(status.LastError, ErrDrainDeadline),
			}); err != nil {
				return err
			}
			if mode != "capacity" && mode != "load" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func TestServerProcessHelper(t *testing.T) {
	path := os.Getenv("CPRA_WORKER_SERVER_PROCESS_CONFIG")
	if path == "" {
		return
	}
	var c serverProcessConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	certificate, err := os.ReadFile(c.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		t.Fatal("invalid process-fixture certificate")
	}
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)
	client, err := cpra.NewWorkerClient(cpra.Config{BaseURL: c.ServerURL, AuthToken: strings.TrimSpace(string(token)), RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	protocol := &serverProcessProtocol{WorkerClient: client, config: c}
	if c.ReceiptAuditFile != "" {
		protocol.receiptAudit, err = os.OpenFile(c.ReceiptAuditFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer protocol.receiptAudit.Close()
	}
	registry := NewRegistry()
	target := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{}}
	if c.Mode == "load" {
		target.Timeout = 30 * time.Second
	}
	defer target.CloseIdleConnections()
	handler := func(ctx context.Context, job Job) (api.Outcome, error) {
		body, err := json.Marshal(struct {
			ExecutionID  string                  `json:"executionID"`
			Kind         string                  `json:"kind"`
			MonitorID    string                  `json:"monitorID"`
			Notification *api.WorkerNotification `json:"notification,omitempty"`
		}{job.Assignment.ExecutionID, job.Assignment.Kind, job.Assignment.MonitorID, job.Assignment.Notification})
		if err != nil {
			return api.Outcome{}, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TargetURL+"/invoke", bytes.NewReader(body))
		if err != nil {
			return api.Outcome{}, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := target.Do(request)
		if err != nil {
			return api.Outcome{}, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return api.Outcome{}, fmt.Errorf("independent target returned %d", response.StatusCode)
		}
		var outcome api.Outcome
		if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&outcome); err != nil {
			return api.Outcome{}, err
		}
		if c.Mode == "handler" {
			return api.Outcome{}, protocol.boundary(ctx, job.Assignment.ExecutionID, "")
		}
		if c.Mode == "uncooperative" {
			if err := writeServerProcessJSON(c.BoundaryFile, map[string]string{"executionID": job.Assignment.ExecutionID}); err != nil {
				return api.Outcome{}, err
			}
			// This child deliberately ignores cancellation. The parent verifies
			// drain/lock ownership before enforcing its hard process deadline.
			select {}
		}
		return outcome, nil
	}
	jobTypes := c.JobTypes
	if len(jobTypes) == 0 {
		jobTypes = []serverProcessJobType{{ID: c.JobTypeID, Category: c.Category}}
	}
	for _, jobType := range jobTypes {
		if err := registry.Register(jobType.ID, "v1", jobType.Category, handler); err != nil {
			t.Fatal(err)
		}
	}
	limits := Limits{Concurrency: 1}
	if c.Mode == "capacity" {
		limits = c.Limits
	}
	if c.Mode == "load" {
		// This campaign exercises the actual library defaults, not lowered
		// fixture limits. The parent verifies the resolved values below.
		limits = Limits{}
	}
	if c.Mode == "outbox" || c.Mode == "outbox-recover" {
		limits.Records = 1
	}
	if c.Mode == "outbox-recover" || c.OutboxOnly {
		// The documented outbox-only runner delivers stored outcomes without
		// advertising handlers or requesting another assignment.
		registry = NewRegistry()
	}
	runner, err := New(Config{Client: protocol, Registry: registry, WorkerID: c.WorkerID, WorkerUID: c.WorkerUID, ServerID: c.ServerID,
		StateDir: c.StateDir, WrappingKeyPath: c.WrappingKeyPath, Limits: limits,
		PollWait: time.Second, RetryInterval: 20 * time.Millisecond, HeartbeatInterval: time.Second, DrainTimeout: 3 * time.Second})
	if c.Mode == "lock-probe" {
		if runner != nil {
			_ = runner.Close()
		}
		if !errors.Is(err, bolt.ErrTimeout) {
			t.Fatal("second process did not encounter the owned journal lock", err)
		}
		if err := writeServerProcessJSON(c.StatusFile, map[string]bool{"locked": true}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode == "capacity" || c.Mode == "load" {
		status, err := runner.Status()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeServerProcessJSON(c.StatusFile+".initial", status); err != nil {
			t.Fatal(err)
		}
		if c.Mode == "load" {
			if err := writeServerProcessJSON(c.StatusFile+".limits", runner.config.Limits); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c.Mode == "evidence-reply" {
		raw, err := os.ReadFile(c.EvidenceFile)
		if err != nil {
			_ = runner.Close()
			t.Fatal(err)
		}
		var evidence api.LateEvidenceRequest
		if err := json.Unmarshal(raw, &evidence); err != nil {
			_ = runner.Close()
			t.Fatal(err)
		}
		if err := runner.QueueLateEvidence(t.Context(), evidence); err != nil {
			_ = runner.Close()
			t.Fatal(err)
		}
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	var statusObserved <-chan error
	stopObserving := func() {}
	if c.Mode == "uncooperative" || c.Mode == "outbox" || c.Mode == "capacity" || c.Mode == "load" {
		observeCtx, cancel := context.WithCancel(t.Context())
		stopObserving = cancel
		result := make(chan error, 1)
		statusObserved = result
		path := c.StatusFile + ".draining"
		if c.Mode == "outbox" || c.Mode == "capacity" || c.Mode == "load" {
			path = c.StatusFile + ".capacity"
		}
		go func() { result <- observeServerProcessStatus(observeCtx, runner, protocol, c.Mode, path) }()
	}
	err = runner.Run(ctx)
	stopObserving()
	if statusObserved != nil {
		if observeErr := <-statusObserved; observeErr != nil && !errors.Is(observeErr, context.Canceled) {
			t.Fatal(observeErr)
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status, err := runner.Status()
	if err != nil {
		t.Fatal(err)
	}
	// Run has joined all owners and released the lock. Inspect the encrypted
	// journal with the real key so the parent can distinguish the original
	// acknowledged execution from newer scheduled checks retained at shutdown.
	journal, err := openJournal(runner.config)
	if err != nil {
		t.Fatal(err)
	}
	records, readErr := journal.records()
	closeErr := journal.close()
	if readErr != nil || closeErr != nil {
		t.Fatal(readErr, closeErr)
	}
	retained := make(map[string]string, len(records))
	outcomes := make(map[string]string)
	pendingEvidence := 0
	for _, record := range records {
		retained[record.ExecutionID] = record.State
		if record.Outcome != nil {
			outcomes[record.ExecutionID] = record.Outcome.Status
		}
		if record.Evidence != nil {
			pendingEvidence++
		}
	}
	if err := writeServerProcessJSON(c.StatusFile, map[string]any{"records": status.Records, "unknownActions": status.UnknownActions,
		"pendingOutcomes": status.PendingOutcomes, "retained": retained, "outcomes": outcomes, "pendingEvidence": pendingEvidence}); err != nil {
		t.Fatal(err)
	}
}
