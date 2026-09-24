//go:build externaljobs

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ziad-hsn/cpra/examples/sdk/internal/cprafixture"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/worker"
)

// This Protocol fixture stands in for a future CPRa dispatcher. It is local to
// the tutorial: it has no network listener, durability, or authorization claim.
type demoProtocol struct {
	mu             sync.Mutex
	assignments    chan api.Assignment
	completed      chan api.Outcome
	receipts       map[string]api.Receipt
	results        map[string][]byte
	starts         map[string]api.StartResponse
	lastPoll       *api.PollRequest
	lastBatch      *api.Assignments
	lostSMSReceipt bool
	resultAttempts int
}

const demoServerID = "dao-demo-store-epoch-1"
const demoWorkerUID = "dao-demo-worker-uid"
const demoSessionID = "dao-demo-session"

func (p *demoProtocol) Poll(ctx context.Context, request api.PollRequest) (*api.Assignments, error) {
	if request.Capacity < 1 || request.Limit < 1 || request.ServerID != demoServerID || request.WorkerID != "dao-demo-worker" || request.ClientSessionID == "" {
		return nil, errors.New("poll has no admission capacity")
	}
	p.mu.Lock()
	if p.lastPoll != nil && reflect.DeepEqual(request, *p.lastPoll) {
		batch := *p.lastBatch
		batch.Items = append([]api.Assignment(nil), batch.Items...)
		p.mu.Unlock()
		return &batch, nil
	}
	valid := p.lastPoll == nil && request.SessionID == "" && request.PollSequence == 1 ||
		p.lastPoll != nil && request.SessionID == demoSessionID && request.ClientSessionID == p.lastPoll.ClientSessionID && request.PollSequence == p.lastPoll.PollSequence+1
	p.mu.Unlock()
	if !valid {
		return nil, errors.New("fixture poll sequence does not match")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case assignment := <-p.assignments:
		assignment.WorkerUID, assignment.SessionID = demoWorkerUID, demoSessionID
		batch := &api.Assignments{ServerID: demoServerID, WorkerUID: demoWorkerUID, ClientSessionID: request.ClientSessionID,
			SessionID: demoSessionID, PollSequence: request.PollSequence, SessionExpiresAt: time.Now().Add(2 * time.Minute), Items: []api.Assignment{assignment}}
		p.mu.Lock()
		p.lastPoll, p.lastBatch = &request, batch
		p.mu.Unlock()
		return batch, nil
	}
}
func (p *demoProtocol) Start(ctx context.Context, request api.StartRequest) (*api.StartResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.ServerID != demoServerID || request.SessionID != demoSessionID {
		return nil, errors.New("fixture start identity does not match")
	}
	response := api.StartResponse{ServerID: demoServerID, WorkerUID: demoWorkerUID, SessionID: request.SessionID,
		ExecutionID: request.ExecutionID, ExecutionRevision: request.ExecutionRevision, LeaseID: request.LeaseID}
	previous, started := p.starts[request.ExecutionID]
	if started && (previous.ExecutionRevision != request.ExecutionRevision || previous.LeaseID != request.LeaseID) {
		return nil, errors.New("fixture start attempt changed")
	}
	if receipt, ok := p.receipts[request.ExecutionID]; ok {
		response.Disposition, response.ReceiptID = api.StartDispositionTerminal, receipt.ReceiptID
		return &response, nil
	}
	if started {
		previous.Disposition = api.StartDispositionStarted
		return &previous, nil
	}
	if request.Mode == api.StartModeReconcile {
		response.Disposition = api.StartDispositionPending
		return &response, nil
	}
	if request.Mode != api.StartModeBegin {
		return nil, errors.New("fixture start mode is invalid")
	}
	response.Disposition, response.GrantID, response.Deadline = api.StartDispositionGranted, "grant-"+request.ExecutionID, time.Now().Add(10*time.Second)
	if p.starts == nil {
		p.starts = make(map[string]api.StartResponse)
	}
	p.starts[request.ExecutionID] = response
	return &response, nil
}
func (p *demoProtocol) Heartbeat(ctx context.Context, request api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &api.HeartbeatResponse{ServerID: demoServerID, WorkerUID: demoWorkerUID, SessionID: request.SessionID, ExecutionID: request.ExecutionID, GrantID: request.GrantID, Accepted: true}, nil
}
func (p *demoProtocol) Result(ctx context.Context, outcome api.Outcome) (*api.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	started, ok := p.starts[outcome.ExecutionID]
	if !ok || outcome.ServerID != demoServerID || outcome.WorkerUID != demoWorkerUID || outcome.GrantID != started.GrantID {
		return nil, errors.New("fixture outcome identity does not match")
	}
	p.resultAttempts++
	raw, err := json.Marshal(outcome)
	if err != nil {
		return nil, err
	}
	if previous, ok := p.results[outcome.ExecutionID]; ok && string(previous) != string(raw) {
		return nil, errors.New("same execution delivered contradictory outcomes")
	}
	if _, ok := p.receipts[outcome.ExecutionID]; !ok {
		p.results[outcome.ExecutionID] = raw
		p.receipts[outcome.ExecutionID] = api.Receipt{ServerID: demoServerID, WorkerUID: demoWorkerUID, ExecutionID: outcome.ExecutionID, ReceiptID: "receipt-" + outcome.ExecutionID, Disposition: api.ReceiptDispositionFinalized}
		p.completed <- outcome
	}
	if outcome.Kind == "notification" && !p.lostSMSReceipt {
		p.lostSMSReceipt = true
		return nil, errors.New("fixture lost the first SMS outcome receipt")
	}
	receipt := p.receipts[outcome.ExecutionID]
	return &receipt, nil
}
func (*demoProtocol) LateEvidence(context.Context, api.LateEvidenceRequest) (*api.Receipt, error) {
	return nil, errors.New("this demo does not implement late evidence; see the worker library guide")
}

func demo(ctx context.Context, output io.Writer) error {
	ctx, timeout := context.WithTimeout(ctx, 15*time.Second)
	defer timeout()
	fmt.Fprintln(output, "Local protocol fixtures only: no blockchain provider, SMS account, or CPRa v2 server is contacted.")
	management := cprafixture.New()
	defer management.Close()
	client, err := cpra.New(cpra.Config{BaseURL: management.URL, AuthToken: cprafixture.Token, AllowInsecureHTTP: true})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	parameters := rpcParameters{ExpectedChainID: "0x1", Governor: "0x1111111111111111111111111111111111111111", MaxBlockAge: 120}
	registration, err := register(ctx, client, parameters)
	if err != nil {
		return err
	}
	items := management.Snapshot()
	if len(items) != 5 {
		return fmt.Errorf("expected five registered resources, got %d", len(items))
	}
	fmt.Fprintf(output, "Registered 2 JobTypes, 1 SMS endpoint, 1 notification group, and 1 monitor (operation %s).\n", registration.OperationID)
	var checkConfig, smsConfig api.ExternalConfig
	for _, item := range items {
		switch item.Kind {
		case "Monitor":
			var spec api.MonitorSpec
			if err = json.Unmarshal(item.Spec, &spec); err != nil {
				return err
			}
			if err = json.Unmarshal(spec.Check.Driver.Config, &checkConfig); err != nil {
				return err
			}
		case "NotificationEndpoint":
			var driver api.DriverConfig
			if err = json.Unmarshal(item.Spec, &driver); err != nil {
				return err
			}
			if err = json.Unmarshal(driver.Config, &smsConfig); err != nil {
				return err
			}
		}
	}
	var stale atomic.Bool
	var rpcRequests atomic.Int64
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcRequests.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture-rpc-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Method  string `json:"method"`
			Params  []any  `json:"params"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var result any
		switch request.Method {
		case "eth_chainId":
			result = "0x1"
		case "eth_syncing":
			result = false
		case "eth_getBlockByNumber":
			age := 5 * time.Second
			if stale.Load() {
				age = 10 * time.Minute
			}
			result = map[string]string{"number": "0x123", "timestamp": "0x" + strconv.FormatInt(time.Now().Add(-age).Unix(), 16)}
		case "eth_getCode":
			result = "0x6001600055"
		default:
			http.Error(w, "unsupported fixture method", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer rpc.Close()
	var smsRequests atomic.Int64
	sms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		smsRequests.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture-sms-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var request struct {
			To          string `json:"to"`
			Text        string `json:"text"`
			ExecutionID string `json:"executionID"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request) != nil || request.To != "fixture-recipient" || request.ExecutionID != "notification-1" || request.Text == "" {
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "fixture-sms-1", "status": "accepted"})
	}))
	defer sms.Close()
	dir, err := os.MkdirTemp("", "cpra-dao-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, "wrapping.key")
	if err = os.WriteFile(keyPath, key, 0600); err != nil {
		return err
	}
	protocol := &demoProtocol{assignments: make(chan api.Assignment, 3), completed: make(chan api.Outcome, 3), receipts: make(map[string]api.Receipt), results: make(map[string][]byte)}
	provider := providerClient()
	defer provider.CloseIdleConnections()
	registered, err := registry(provider)
	if err != nil {
		return err
	}
	resolver := func(ctx context.Context, profile string) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch profile {
		case rpcProfile:
			return rpcCredentials{URL: rpc.URL, Token: "fixture-rpc-token"}, nil
		case smsProfile:
			return smsCredentials{URL: sms.URL, Token: "fixture-sms-token", Recipients: map[string]string{"dao-oncall": "fixture-recipient"}}, nil
		default:
			return nil, errors.New("unknown fixture profile")
		}
	}
	runner, err := worker.New(worker.Config{Client: protocol, Registry: registered, WorkerID: "dao-demo-worker", WorkerUID: demoWorkerUID, ServerID: demoServerID, StateDir: filepath.Join(dir, "state"), WrappingKeyPath: keyPath, Credentials: resolver, Limits: worker.Limits{Concurrency: 2}, RetryInterval: 20 * time.Millisecond, PollWait: time.Second, DrainTimeout: time.Second})
	if err != nil {
		return err
	}
	defer runner.Close()
	runCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(runCtx) }()
	defer func() { cancel(); <-finished }()
	assignment := func(id, kind string, config api.ExternalConfig) api.Assignment {
		return api.Assignment{ExecutionID: id, ExecutionRevision: "1", IncarnationUID: "demo-monitor-incarnation", LeaseID: "lease-" + id, ServerID: demoServerID, MonitorID: monitorID, JobTypeID: config.JobTypeID, JobTypeUID: "demo-" + config.JobTypeID + "-uid", JobTypeVersion: config.Version, Kind: kind, CredentialProfile: config.CredentialProfile, Parameters: config.Parameters, Deadline: time.Now().Add(10 * time.Second)}
	}
	await := func(expected string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case outcome := <-protocol.completed:
			fmt.Fprintf(output, "%s: %s — %s\n", outcome.Kind, outcome.Status, outcome.Diagnostic)
			if outcome.Status != expected {
				return fmt.Errorf("fixture expected %s, observed %s", expected, outcome.Status)
			}
			return nil
		}
	}
	protocol.assignments <- assignment("check-1", "check", checkConfig)
	if err = await("success"); err != nil {
		return err
	}
	stale.Store(true)
	protocol.assignments <- assignment("check-2", "check", checkConfig)
	if err = await("failure"); err != nil {
		return err
	}
	// This explicit fixture branch imitates incident dispatch; it is not the
	// production controller's incident lifecycle or notification scheduling.
	protocol.assignments <- assignment("notification-1", "notification", smsConfig)
	if err = await("accepted"); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := runner.Status()
		if err != nil {
			return err
		}
		if status.Records == 0 && status.ActiveHandlers == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	protocol.mu.Lock()
	attempts := protocol.resultAttempts
	protocol.mu.Unlock()
	if smsRequests.Load() != 1 || attempts != 4 || rpcRequests.Load() != 7 {
		return fmt.Errorf("unexpected fixture accounting: RPC=%d SMS=%d result attempts=%d", rpcRequests.Load(), smsRequests.Load(), attempts)
	}
	fmt.Fprintf(output, "RPC requests: %d; SMS requests: %d; outcome submissions: %d (one receipt was lost).\n", rpcRequests.Load(), smsRequests.Load(), attempts)
	fmt.Fprintln(output, "Encrypted outbox drained. Temporary fixture state and wrapping key are removed when the demo exits.")
	return nil
}
