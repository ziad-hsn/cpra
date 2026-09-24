//go:build externaljobs

package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	bolt "go.etcd.io/bbolt"
)

// fakeProtocol is an explicit protocol fixture, not production-server evidence.
// It derives terminal starts from receipts and never authorizes a completed job.
type fakeProtocol struct {
	mu               sync.Mutex
	assignments      chan api.Assignment
	starts           int
	startFail        bool
	resultFail       bool
	loseFirstReceipt bool
	results          []api.Outcome
	receipts         map[string]*api.Receipt
	evidence         []api.LateEvidenceRequest
	server           string
	heartbeatErr     error
	sessionExpires   time.Time
}

func newFake() *fakeProtocol {
	return &fakeProtocol{assignments: make(chan api.Assignment, 100), receipts: map[string]*api.Receipt{}, server: "server-epoch-1", sessionExpires: time.Now().Add(time.Minute)}
}
func (f *fakeProtocol) Poll(ctx context.Context, request api.PollRequest) (*api.Assignments, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case a := <-f.assignments:
		a.SessionID = "session-1"
		return &api.Assignments{ServerID: f.server, WorkerUID: "worker-uid-1", ClientSessionID: request.ClientSessionID, SessionID: a.SessionID, PollSequence: request.PollSequence, SessionExpiresAt: f.sessionExpires, Items: []api.Assignment{a}}, nil
	}
}
func (f *fakeProtocol) Start(_ context.Context, request api.StartRequest) (*api.StartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startFail {
		f.startFail = false
		return nil, errors.New("start response was lost")
	}
	if receipt := f.receipts[request.ExecutionID]; receipt != nil && receipt.Disposition == "finalized" {
		reply := startReply(request, api.StartDispositionTerminal)
		reply.ServerID, reply.ReceiptID = f.server, receipt.ReceiptID
		return reply, nil
	}
	disposition := api.StartDispositionGranted
	if request.Mode == api.StartModeReconcile {
		disposition = api.StartDispositionStarted
	}
	reply := startReply(request, disposition)
	reply.ServerID = f.server
	return reply, nil
}
func (f *fakeProtocol) Heartbeat(_ context.Context, request api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	if f.heartbeatErr != nil {
		return nil, f.heartbeatErr
	}
	return &api.HeartbeatResponse{ServerID: f.server, WorkerUID: "worker-uid-1", SessionID: request.SessionID, ExecutionID: request.ExecutionID, GrantID: request.GrantID, Accepted: true}, nil
}
func (f *fakeProtocol) Result(_ context.Context, outcome api.Outcome) (*api.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, outcome)
	if f.resultFail {
		return nil, errors.New("result unavailable")
	}
	disposition := api.ReceiptDispositionFinalized
	if outcome.Status == "unknown" {
		disposition = api.ReceiptDispositionUnknown
	}
	receipt := f.receipts[outcome.ExecutionID]
	if receipt == nil {
		receipt = &api.Receipt{ServerID: f.server, WorkerUID: "worker-uid-1", ExecutionID: outcome.ExecutionID, ReceiptID: "receipt-" + outcome.ExecutionID, Disposition: disposition}
		f.receipts[outcome.ExecutionID] = receipt
	}
	if f.loseFirstReceipt {
		f.loseFirstReceipt = false
		return nil, errors.New("committed result receipt was lost")
	}
	copy := *receipt
	return &copy, nil
}
func (f *fakeProtocol) LateEvidence(_ context.Context, evidence api.LateEvidenceRequest) (*api.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evidence = append(f.evidence, evidence)
	return &api.Receipt{ServerID: f.server, WorkerUID: "worker-uid-1", ExecutionID: evidence.ExecutionID, ReceiptID: "evidence-receipt-" + evidence.EvidenceID, Disposition: "accepted"}, nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyDir := filepath.Join(dir, "keys")
	if err := makePrivateDir(keyDir); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(keyDir, "wrapping.key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Client: newFake(), Registry: NewRegistry(), WorkerID: "worker-1", WorkerUID: "worker-uid-1", ServerID: "server-epoch-1", StateDir: filepath.Join(dir, "state"), WrappingKeyPath: key, RetryInterval: time.Millisecond, HeartbeatInterval: time.Hour, DrainTimeout: time.Second}
}

func assignment(id, kind string) api.Assignment {
	return api.Assignment{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", SessionID: "session-1", JobTypeUID: "job-type-uid-1", ExecutionID: id, ExecutionRevision: "revision-1", LeaseID: "lease-1", MonitorID: "monitor-1", IncarnationUID: "incarnation-1", JobTypeID: "example", JobTypeVersion: "v1", Kind: kind, Deadline: time.Now().Add(time.Minute), Parameters: json.RawMessage(`{"target":"never-persist-provider-parameters"}`)}
}

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition did not complete")
		case <-ticker.C:
		}
	}
}

func runWorker(t *testing.T, c Config) (*Runner, context.CancelFunc, <-chan error) {
	t.Helper()
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runner did not shut down")
		}
	})
	return r, cancel, done
}

func TestRegistryFrozenAndAllCategories(t *testing.T) {
	for _, kind := range []string{"check", "recovery", "notification"} {
		t.Run(kind, func(t *testing.T) {
			c := testConfig(t)
			f := c.Client.(*fakeProtocol)
			var calls atomic.Int64
			status := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[kind]
			if err := c.Registry.Register("example", "v1", kind, func(context.Context, Job) (api.Outcome, error) {
				calls.Add(1)
				return api.Outcome{Status: status, ExecutionID: "forged", Kind: "forged", GrantID: "forged"}, nil
			}); err != nil {
				t.Fatal(err)
			}
			r, _, _ := runWorker(t, c)
			f.assignments <- assignment("execution-1", kind)
			eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.receipts["execution-1"] != nil })
			if calls.Load() != 1 {
				t.Fatal("handler not called exactly once")
			}
			if err := c.Registry.Register("other", "v1", kind, func(context.Context, Job) (api.Outcome, error) { return api.Outcome{}, nil }); err == nil {
				t.Fatal("mutable registry")
			}
			f.mu.Lock()
			outcome := f.results[0]
			f.mu.Unlock()
			if outcome.ExecutionID != "execution-1" || outcome.Kind != kind || outcome.GrantID != "grant-execution-1" {
				t.Fatal("handler-controlled protocol identity")
			}
			f.assignments <- assignment("execution-1", kind)
			eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.starts >= 2 })
			if calls.Load() != 1 {
				t.Fatal("terminal duplicate invoked handler")
			}
			if _, err := r.Status(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLostStartNeverInvokesHandlerAndPreservesUnknown(t *testing.T) {
	c := testConfig(t)
	f := c.Client.(*fakeProtocol)
	f.startFail = true
	var calls atomic.Int64
	_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
		calls.Add(1)
		return api.Outcome{Status: "completed"}, nil
	})
	r, _, _ := runWorker(t, c)
	f.assignments <- assignment("ambiguous", "recovery")
	eventually(t, func() bool { s, _ := r.Status(); return s.UnknownActions == 1 })
	if calls.Load() != 0 {
		t.Fatal("ambiguous start executed handler")
	}
	f.mu.Lock()
	out := f.results[0]
	f.mu.Unlock()
	if out.Status != "unknown" {
		t.Fatal("ambiguous action was not held")
	}
	if err := r.QueueLateEvidence(context.Background(), api.LateEvidenceRequest{ExecutionID: "ambiguous", OriginalReceiptID: "receipt-ambiguous", EvidenceID: "evidence-1", Diagnostic: "operator independently checked target"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.evidence) == 1 })
	s, _ := r.Status()
	if s.UnknownActions != 1 {
		t.Fatal("late evidence automatically cleared unknown")
	}
	f.mu.Lock()
	f.receipts["ambiguous"].Disposition = "finalized"
	f.mu.Unlock()
	eventually(t, func() bool { s, _ := r.Status(); return s.Records == 0 })
}

func TestLostReceiptRetriesIdenticalOutcome(t *testing.T) {
	c := testConfig(t)
	f := c.Client.(*fakeProtocol)
	f.loseFirstReceipt = true
	var calls atomic.Int64
	_ = c.Registry.Register("example", "v1", "notification", func(context.Context, Job) (api.Outcome, error) {
		calls.Add(1)
		return api.Outcome{Status: "delivered", Data: json.RawMessage(`{"receipt":"provider-result"}`)}, nil
	})
	r, _, _ := runWorker(t, c)
	f.assignments <- assignment("delivery", "notification")
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.results) >= 2 })
	f.mu.Lock()
	a, _ := json.Marshal(f.results[0])
	b, _ := json.Marshal(f.results[1])
	f.mu.Unlock()
	if !bytes.Equal(a, b) || calls.Load() != 1 {
		t.Fatal("receipt loss changed or repeated provider result")
	}
	eventually(t, func() bool { s, _ := r.Status(); return s.Records == 0 })
}

func TestOutboxFullStopsStartAndPreservesPending(t *testing.T) {
	c := testConfig(t)
	c.Limits.Records = 1
	f := c.Client.(*fakeProtocol)
	f.resultFail = true
	_ = c.Registry.Register("example", "v1", "check", func(context.Context, Job) (api.Outcome, error) { return api.Outcome{Status: "success"}, nil })
	r, _, _ := runWorker(t, c)
	f.assignments <- assignment("first", "check")
	eventually(t, func() bool { s, _ := r.Status(); return s.PendingOutcomes == 1 })
	f.assignments <- assignment("second", "check")
	// Directly prove admission rejects a second execution before any start call.
	if err := r.journal.reserve(assignment("third", "check")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reservation: %v", err)
	}
	f.mu.Lock()
	starts := f.starts
	f.mu.Unlock()
	if starts != 1 {
		t.Fatal("outbox saturation issued another start")
	}
	s, _ := r.Status()
	if s.Records != 1 || s.AdmissionAvailable {
		t.Fatal("pending outcome was discarded")
	}
}

func TestUncooperativeHandlerRetainsLockPastDeadline(t *testing.T) {
	c := testConfig(t)
	c.DrainTimeout = 20 * time.Millisecond
	release := make(chan struct{})
	entered := make(chan struct{})
	_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
		close(entered)
		<-release
		return api.Outcome{Status: "completed"}, nil
	})
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	c.Client.(*fakeProtocol).assignments <- assignment("blocking", "recovery")
	<-entered
	cancel()
	eventually(t, func() bool { s, _ := r.Status(); return s.DrainDeadlineExpired })
	select {
	case <-done:
		t.Fatal("Run returned while handler was alive")
	default:
	}
	if err = r.Close(); !errors.Is(err, ErrRunning) {
		t.Fatalf("closed active runner: %v", err)
	}
	if other, err := New(c); err == nil {
		_ = other.Close()
		t.Fatal("journal lock released while handler was alive")
	}
	close(release)
	select {
	case err = <-done:
		if !errors.Is(err, ErrDrainDeadline) {
			t.Fatalf("missing drain error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released handler did not stop")
	}
}

func TestJournalEncryptionRestartAndIdentity(t *testing.T) {
	c := testConfig(t)
	c, _ = c.validate()
	j, err := openJournal(c)
	if err != nil {
		t.Fatal(err)
	}
	a := assignment("secret-outcome", "recovery")
	if err = j.reserve(a); err != nil {
		t.Fatal(err)
	}
	needle := "sensitive-provider-result-contents"
	if err = j.update(a.ExecutionID, func(r *journalRecord) error {
		r.State = "outcome"
		r.Outcome = &api.Outcome{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", ExecutionID: a.ExecutionID, Kind: "recovery", Status: "unknown", Diagnostic: needle}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldEpoch := j.epoch
	_ = j.close()
	data, err := os.ReadFile(filepath.Join(c.StateDir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(needle)) || bytes.Contains(data, []byte("never-persist-provider-parameters")) || bytes.Contains(data, bytes.Repeat([]byte{7}, 32)) {
		t.Fatal("plaintext escaped encrypted journal")
	}
	j, err = openJournal(c)
	if err != nil {
		t.Fatal(err)
	}
	if j.epoch == oldEpoch {
		t.Fatal("startup did not rotate data key")
	}
	records, err := j.records()
	if err != nil || len(records) != 1 || records[0].Outcome.Diagnostic != needle {
		t.Fatalf("recovery: %v %#v", err, records)
	}
	_ = j.close()
	changed := c
	changed.ServerID = "restored-server"
	if wrong, err := openJournal(changed); !errors.Is(err, ErrIdentity) {
		if wrong != nil {
			_ = wrong.close()
		}
		t.Fatalf("restored identity accepted: %v", err)
	}
	if err = os.WriteFile(c.WrappingKeyPath, bytes.Repeat([]byte{8}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if wrong, err := openJournal(c); !errors.Is(err, ErrCorrupt) {
		if wrong != nil {
			_ = wrong.close()
		}
		t.Fatalf("wrong key accepted: %v", err)
	}
}

func TestJournalCorruptionAndLimitRotation(t *testing.T) {
	c := testConfig(t)
	c, _ = c.validate()
	j, err := openJournal(c)
	if err != nil {
		t.Fatal(err)
	}
	j.encryptions = 65536
	old := j.epoch
	if err = j.reserve(assignment("one", "check")); err != nil {
		t.Fatal(err)
	}
	if j.epoch == old {
		t.Fatal("encryption limit did not rotate key")
	}
	_ = j.close()
	db, err := bolt.Open(filepath.Join(c.StateDir, "worker.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(recordsBucket)
		var e encryptedRecord
		if err := json.Unmarshal(b.Get([]byte("one")), &e); err != nil {
			return err
		}
		e.Ciphertext[0] ^= 0xff
		encoded, _ := json.Marshal(e)
		return b.Put([]byte("one"), encoded)
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if corrupt, err := openJournal(c); !errors.Is(err, ErrCorrupt) {
		if corrupt != nil {
			_ = corrupt.close()
		}
		t.Fatalf("corruption accepted: %v", err)
	}
}

func TestExistingJournalNeverBootstrapsMissingState(t *testing.T) {
	for _, corruption := range []string{"zero", "truncated", "metadata-bucket", "records-bucket", "epochs-bucket", "identity", "key-check"} {
		t.Run(corruption, func(t *testing.T) {
			c := testConfig(t)
			c, _ = c.validate()
			j, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = j.reserve(assignment("held", "recovery")); err != nil {
				t.Fatal(err)
			}
			_ = j.close()
			path := filepath.Join(c.StateDir, "worker.db")
			switch corruption {
			case "zero":
				err = os.Truncate(path, 0)
			case "truncated":
				err = os.Truncate(path, 100)
			default:
				db, e := bolt.Open(path, 0600, nil)
				if e != nil {
					t.Fatal(e)
				}
				err = db.Update(func(tx *bolt.Tx) error {
					switch corruption {
					case "metadata-bucket":
						return tx.DeleteBucket(metadataBucket)
					case "records-bucket":
						return tx.DeleteBucket(recordsBucket)
					case "epochs-bucket":
						return tx.DeleteBucket(epochsBucket)
					default:
						return tx.Bucket(metadataBucket).Delete([]byte(corruption))
					}
				})
				_ = db.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if fresh, err := openJournal(c); err == nil {
				_ = fresh.close()
				t.Fatal("corrupt existing state was silently bootstrapped")
			}
		})
	}
}

func TestLimitValidationAndPollReservationCapacity(t *testing.T) {
	if _, err := (Limits{OutcomeBytes: int(^uint(0) >> 1)}).defaults(); err == nil {
		t.Fatal("overflowing outcome size accepted")
	}
	c := testConfig(t)
	c.Limits.LiveBytes = 2 * (128<<10 + journalMetadataReserve)
	c, _ = c.validate()
	j, err := openJournal(c)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	s, err := j.stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.AdmissionCapacity != 2 {
		t.Fatalf("byte capacity: %d", s.AdmissionCapacity)
	}
	if err = j.reserve(assignment("reserved", "check")); err != nil {
		t.Fatal(err)
	}
	s, err = j.stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.AdmissionCapacity != 1 {
		t.Fatalf("reservation capacity: %d", s.AdmissionCapacity)
	}
}

func TestJournalRejectsPathsAndMissingKey(t *testing.T) {
	for _, scenario := range []string{"relative-state", "relative-key", "key-in-state", "missing-key", "public-key", "symlink-key", "public-state"} {
		t.Run(scenario, func(t *testing.T) {
			if runtime.GOOS == "windows" && (scenario == "public-key" || scenario == "public-state") {
				t.Skip("Unix permission-bit case; Windows DACLs are checked separately")
			}
			c := testConfig(t)
			c, _ = c.validate()
			switch scenario {
			case "relative-state":
				c.StateDir = "state"
			case "relative-key":
				c.WrappingKeyPath = "key"
			case "key-in-state":
				c.StateDir = filepath.Dir(c.WrappingKeyPath)
			case "missing-key":
				c.WrappingKeyPath += "missing"
			case "public-key":
				_ = os.Chmod(c.WrappingKeyPath, 0644)
			case "symlink-key":
				link := c.WrappingKeyPath + "-link"
				_ = os.Symlink(c.WrappingKeyPath, link)
				c.WrappingKeyPath = link
			case "public-state":
				_ = os.Mkdir(c.StateDir, 0755)
			}
			if j, err := openJournal(c); err == nil {
				_ = j.close()
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestInterruptedMarkerNeverReexecutes(t *testing.T) {
	for _, state := range []string{"reserved", "started"} {
		t.Run(state, func(t *testing.T) {
			c := testConfig(t)
			c, _ = c.validate()
			j, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			a := assignment("interrupted", "recovery")
			if err = j.reserve(a); err != nil {
				t.Fatal(err)
			}
			if state == "started" {
				if err = j.update(a.ExecutionID, func(r *journalRecord) error { r.State = "started"; r.GrantID = "grant-interrupted"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			_ = j.close()
			var calls atomic.Int64
			_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
				calls.Add(1)
				return api.Outcome{Status: "completed"}, nil
			})
			r, _, _ := runWorker(t, c)
			c.Client.(*fakeProtocol).assignments <- a
			eventually(t, func() bool { s, _ := r.Status(); return s.UnknownActions == 1 })
			if calls.Load() != 0 {
				t.Fatal("interrupted handler replayed")
			}
		})
	}
}

func TestDiagnosticsBoundsAndErrorsDoNotLeak(t *testing.T) {
	c := testConfig(t)
	c.Limits.DiagnosticBytes = 32
	f := c.Client.(*fakeProtocol)
	_ = c.Registry.Register("example", "v1", "check", func(context.Context, Job) (api.Outcome, error) {
		return api.Outcome{Status: "success", Diagnostic: strings.Repeat("é", 100)}, nil
	})
	_, _, _ = runWorker(t, c)
	f.assignments <- assignment("truncated", "check")
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.results) > 0 })
	f.mu.Lock()
	out := f.results[0]
	f.mu.Unlock()
	if len(out.Diagnostic) > 32 || !out.DiagnosticTruncated {
		t.Fatal("diagnostic truncation not explicit")
	}
}

func TestTerminalCleanupPreservesQueuedLateEvidence(t *testing.T) {
	c := testConfig(t)
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := assignment("held", "recovery")
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.State = "unknown"
		rec.GrantID = "grant"
		rec.ReceiptID = "original"
		rec.Outcome = &api.Outcome{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", ExecutionID: "held", Kind: "recovery", GrantID: "grant", Status: "unknown"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e := api.LateEvidenceRequest{ExecutionID: "held", OriginalReceiptID: "original", EvidenceID: "append-1", Diagnostic: "new observation"}
	if err = r.QueueLateEvidence(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err = r.journal.remove("held"); err != nil {
		t.Fatal(err)
	}
	records, err := r.journal.records()
	if err != nil || len(records) != 1 || records[0].Evidence == nil {
		t.Fatalf("terminal cleanup dropped queued evidence: %v", err)
	}
}

func TestAllocationLimitStopsAdmissionButNotCompletion(t *testing.T) {
	c := testConfig(t)
	c, _ = c.validate()
	j, err := openJournal(c)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if err = j.reserve(assignment("accepted", "check")); err != nil {
		t.Fatal(err)
	}
	j.limits.AllocatedBytes = 1
	if err = j.reserve(assignment("excess", "check")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("allocation did not stop admission: %v", err)
	}
	err = j.update("accepted", func(rec *journalRecord) error {
		rec.State = "outcome"
		rec.Outcome = &api.Outcome{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", ExecutionID: "accepted", Kind: "check", Status: "noData"}
		return nil
	})
	if err != nil {
		t.Fatalf("reserved completion was blocked: %v", err)
	}
}

func TestMalformedReceiptCannotConsumeReservedCapacity(t *testing.T) {
	c := testConfig(t)
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := assignment("pending", "recovery")
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.State = "outcome"
		rec.Outcome = &api.Outcome{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", ExecutionID: "pending", Kind: "recovery", Status: "unknown"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f := c.Client.(*fakeProtocol)
	f.receipts["pending"] = &api.Receipt{ServerID: c.ServerID, WorkerUID: c.WorkerUID, ExecutionID: "pending", Disposition: "unknown", ReceiptID: strings.Repeat("x", 513)}
	records, err := r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	if err = r.deliver(context.Background(), records[0]); err == nil {
		t.Fatal("oversized receipt accepted")
	}
	records, err = r.journal.records()
	if err != nil || len(records) != 1 || records[0].State != "outcome" || records[0].ReceiptID != "" {
		t.Fatalf("malformed receipt altered pending outcome: %v", err)
	}
}

func TestChangedServerIdentityStopsBeforeExecution(t *testing.T) {
	c := testConfig(t)
	f := c.Client.(*fakeProtocol)
	f.server = "restored-server"
	var calls atomic.Int64
	_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
		calls.Add(1)
		return api.Outcome{Status: "completed"}, nil
	})
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.assignments <- assignment("wrong-store", "recovery")
	if err = r.Run(ctx); !errors.Is(err, ErrIdentity) {
		t.Fatalf("missing identity failure: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("restored server caused execution")
	}
}

func TestRevokedHeartbeatCancelsHandlerAndStopsAdmission(t *testing.T) {
	c := testConfig(t)
	c.HeartbeatInterval = time.Millisecond
	f := c.Client.(*fakeProtocol)
	f.heartbeatErr = &cpra.Error{StatusCode: 403}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	_ = c.Registry.Register("example", "v1", "recovery", func(ctx context.Context, _ Job) (api.Outcome, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return api.Outcome{}, ctx.Err()
	})
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	f.assignments <- assignment("revoked", "recovery")
	<-entered
	select {
	case err = <-done:
		if !errors.Is(err, cpra.ErrUnauthorized) {
			t.Fatalf("unexpected revocation result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not drain")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("handler was not cancelled")
	}
	s, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if s.AdmissionAvailable || s.Records != 1 {
		t.Fatal("revocation dropped outcome or allowed admission")
	}
}

func TestHandlerErrorsAndPanicsAreNotPersisted(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			c := testConfig(t)
			f := c.Client.(*fakeProtocol)
			_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
				if panics {
					panic("secret-provider-token")
				}
				return api.Outcome{}, errors.New("secret-provider-token")
			})
			r, _, _ := runWorker(t, c)
			f.assignments <- assignment("failed-handler", "recovery")
			eventually(t, func() bool { s, _ := r.Status(); return s.UnknownActions == 1 })
			f.mu.Lock()
			encoded, _ := json.Marshal(f.results)
			f.mu.Unlock()
			if bytes.Contains(encoded, []byte("secret-provider-token")) {
				t.Fatal("handler error leaked into protocol result")
			}
		})
	}
}

func TestProcessJournalHelper(t *testing.T) {
	if os.Getenv("CPRA_WORKER_HELPER") != "1" {
		return
	}
	c := Config{Client: newFake(), Registry: NewRegistry(), WorkerID: "worker-1", WorkerUID: "worker-uid-1", ServerID: "server-epoch-1", StateDir: os.Getenv("CPRA_WORKER_STATE"), WrappingKeyPath: os.Getenv("CPRA_WORKER_KEY")}
	c, _ = c.validate()
	j, err := openJournal(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	a := assignment("process-execution", "recovery")
	if err = j.reserve(a); err != nil {
		os.Exit(3)
	}
	phase := os.Getenv("CPRA_WORKER_PHASE")
	if phase != "reserved" {
		if err = j.update(a.ExecutionID, func(r *journalRecord) error { r.State = "started"; r.GrantID = "grant-process-execution"; return nil }); err != nil {
			os.Exit(4)
		}
	}
	if phase == "external-success" || phase == "outcome" {
		if err = os.WriteFile(filepath.Join(filepath.Dir(c.StateDir), "effect"), []byte("one effect"), 0600); err != nil {
			os.Exit(5)
		}
	}
	if phase == "outcome" {
		if err = j.update(a.ExecutionID, func(r *journalRecord) error {
			r.State = "outcome"
			r.Outcome = &api.Outcome{ServerID: "server-epoch-1", WorkerUID: "worker-uid-1", ExecutionID: r.ExecutionID, GrantID: r.GrantID, Kind: "recovery", Status: "completed"}
			return nil
		}); err != nil {
			os.Exit(6)
		}
	}
	fmt.Println("durable-marker-ready")
	<-time.After(time.Hour)
	os.Exit(7)
}

func TestForcedProcessTerminationRecoversCommittedBoundaries(t *testing.T) {
	for _, phase := range []string{"reserved", "started", "external-success", "outcome"} {
		t.Run(phase, func(t *testing.T) {
			c := testConfig(t)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestProcessJournalHelper$")
			cmd.Env = append(os.Environ(), "CPRA_WORKER_HELPER=1", "CPRA_WORKER_STATE="+c.StateDir, "CPRA_WORKER_KEY="+c.WrappingKeyPath, "CPRA_WORKER_PHASE="+phase)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				ready <- scanner.Scan() && scanner.Text() == "durable-marker-ready"
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("child not ready: %s", stderr.String())
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("child timeout")
			}
			c, _ = c.validate()
			if held, err := openJournal(c); err == nil {
				_ = held.close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("cross-process journal lock not held")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			j, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			records, err := j.records()
			if err != nil || len(records) != 1 {
				t.Fatalf("committed record missing: %v", err)
			}
			expected := phase
			if phase == "external-success" {
				expected = "started"
			}
			if records[0].State != expected {
				t.Fatalf("got %s want %s", records[0].State, expected)
			}
			_ = j.close()
			var calls atomic.Int64
			_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
				calls.Add(1)
				return api.Outcome{Status: "completed"}, nil
			})
			r, _, _ := runWorker(t, c)
			eventually(t, func() bool {
				s, _ := r.Status()
				if phase == "outcome" {
					return s.Records == 0
				}
				return s.UnknownActions == 1
			})
			if calls.Load() != 0 {
				t.Fatal("restored process invoked handler")
			}
		})
	}
}
