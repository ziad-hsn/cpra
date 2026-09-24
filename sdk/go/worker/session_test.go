//go:build externaljobs

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func startReply(request api.StartRequest, disposition api.StartDisposition) *api.StartResponse {
	r := &api.StartResponse{ServerID: request.ServerID, WorkerUID: "worker-uid-1", SessionID: request.SessionID, ExecutionID: request.ExecutionID, ExecutionRevision: request.ExecutionRevision, LeaseID: request.LeaseID, Disposition: disposition}
	switch disposition {
	case api.StartDispositionGranted, api.StartDispositionStarted, api.StartDispositionUnknown:
		r.GrantID, r.Deadline = "grant-"+request.ExecutionID, time.Now().Add(time.Minute)
	case api.StartDispositionTerminal:
		r.ReceiptID = "receipt-" + request.ExecutionID
	}
	return r
}

// protocolFuncs permits precise schedules without making protocol fixtures
// stand in for production-server execution behavior.
type protocolFuncs struct {
	*fakeProtocol
	poll      func(context.Context, api.PollRequest) (*api.Assignments, error)
	start     func(context.Context, api.StartRequest) (*api.StartResponse, error)
	heartbeat func(context.Context, api.HeartbeatRequest) (*api.HeartbeatResponse, error)
	result    func(context.Context, api.Outcome) (*api.Receipt, error)
	evidence  func(context.Context, api.LateEvidenceRequest) (*api.Receipt, error)
}

func (p *protocolFuncs) Poll(ctx context.Context, q api.PollRequest) (*api.Assignments, error) {
	if p.poll != nil {
		return p.poll(ctx, q)
	}
	return p.fakeProtocol.Poll(ctx, q)
}
func (p *protocolFuncs) Start(ctx context.Context, q api.StartRequest) (*api.StartResponse, error) {
	if p.start != nil {
		return p.start(ctx, q)
	}
	return p.fakeProtocol.Start(ctx, q)
}
func (p *protocolFuncs) Heartbeat(ctx context.Context, q api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	if p.heartbeat != nil {
		return p.heartbeat(ctx, q)
	}
	return p.fakeProtocol.Heartbeat(ctx, q)
}
func (p *protocolFuncs) Result(ctx context.Context, q api.Outcome) (*api.Receipt, error) {
	if p.result != nil {
		return p.result(ctx, q)
	}
	return p.fakeProtocol.Result(ctx, q)
}
func (p *protocolFuncs) LateEvidence(ctx context.Context, q api.LateEvidenceRequest) (*api.Receipt, error) {
	if p.evidence != nil {
		return p.evidence(ctx, q)
	}
	return p.fakeProtocol.LateEvidence(ctx, q)
}

func TestPollPreservesFrozenRequestsAndExplicitExpiry(t *testing.T) {
	c := testConfig(t)
	c.Limits.Concurrency = 3
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests []api.PollRequest
	expiry := time.Now().Add(time.Minute)
	p.poll = func(ctx context.Context, q api.PollRequest) (*api.Assignments, error) {
		requests = append(requests, q)
		n := len(requests)
		reply := &api.Assignments{ServerID: q.ServerID, WorkerUID: c.WorkerUID, ClientSessionID: q.ClientSessionID, SessionID: "session-1", PollSequence: q.PollSequence, SessionExpiresAt: expiry}
		switch n {
		case 1:
			return nil, errors.New("lost opening reply")
		case 2:
			if !reflect.DeepEqual(q, requests[0]) {
				t.Error("opening replay changed")
			}
			a := assignment("first", "check")
			reply.Items = []api.Assignment{a}
		case 3:
			if q.SessionID != "session-1" || q.PollSequence != 2 || q.Capacity != 2 {
				t.Errorf("next request: %+v", q)
			}
			r.mu.Lock()
			delete(r.active, "held")
			r.mu.Unlock()
			return nil, errors.New("lost later reply")
		case 4:
			reply.SessionExpiresAt = expiry.Add(time.Minute) // A newly committed sequence renews TTL.
			if !reflect.DeepEqual(q, requests[2]) {
				t.Error("lost reply changed capacity or sequence")
			}
		case 5:
			return nil, cpra.ErrConflict
		case 6:
			if !reflect.DeepEqual(q, requests[4]) {
				t.Error("generic conflict reopened session")
			}
			return nil, cpra.ErrWorkerSessionExpired
		case 7:
			if q.ClientSessionID == requests[0].ClientSessionID || q.SessionID != "" || q.PollSequence != 1 {
				t.Error("explicit expiry did not open fresh session")
			}
			cancel()
			return nil, ctx.Err()
		default:
			t.Errorf("extra poll %d", n)
			cancel()
			return nil, ctx.Err()
		}
		return reply, nil
	}
	batches := make(chan polledBatch)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		batch := <-batches
		r.mu.Lock()
		r.active["held"] = true
		r.mu.Unlock()
		close(batch.admitted)
	}()
	r.poll(ctx, []api.WorkerCapability{{JobTypeID: "example", Version: "v1", Kind: "check"}}, batches, func(err error) { t.Errorf("poll failed: %v", err); cancel() })
	<-joined
	if len(requests) != 7 {
		t.Fatalf("poll count %d", len(requests))
	}
}

func TestPollRejectsWholeBatchBeforeStart(t *testing.T) {
	for _, field := range []string{"server", "worker", "nonce", "session", "sequence", "expiry", "duplicate", "duplicate-lease", "job-uid", "item-worker", "item-session", "unknown-handler", "parameters", "capacity"} {
		t.Run(field, func(t *testing.T) {
			c := testConfig(t)
			p := &protocolFuncs{fakeProtocol: newFake()}
			c.Client = p
			var calls atomic.Int64
			_ = c.Registry.Register("example", "v1", "check", func(context.Context, Job) (api.Outcome, error) { calls.Add(1); return api.Outcome{}, nil })
			p.poll = func(ctx context.Context, q api.PollRequest) (*api.Assignments, error) {
				a := assignment("offered", "check")
				b := &api.Assignments{ServerID: q.ServerID, WorkerUID: c.WorkerUID, ClientSessionID: q.ClientSessionID, SessionID: a.SessionID, PollSequence: q.PollSequence, SessionExpiresAt: time.Now().Add(time.Minute), Items: []api.Assignment{a}}
				switch field {
				case "server":
					b.ServerID = "different"
				case "worker":
					b.WorkerUID = "different"
				case "nonce":
					b.ClientSessionID = "different"
				case "session":
					b.SessionID = ""
				case "sequence":
					b.PollSequence++
				case "expiry":
					b.SessionExpiresAt = time.Now().Add(-time.Second)
				case "duplicate":
					b.Items = append(b.Items, a)
				case "duplicate-lease":
					a.ExecutionID = "second"
					b.Items = append(b.Items, a)
				case "job-uid":
					b.Items[0].JobTypeUID = ""
				case "item-worker":
					b.Items[0].WorkerUID = "different"
				case "item-session":
					b.Items[0].SessionID = "different"
				case "unknown-handler":
					b.Items[0].JobTypeVersion = "missing"
				case "parameters":
					b.Items[0].Parameters = json.RawMessage(`null` + strings.Repeat(" ", 128<<10))
				case "capacity":
					for len(b.Items) <= int(q.Capacity) {
						x := a
						x.ExecutionID = fmt.Sprint(len(b.Items))
						b.Items = append(b.Items, x)
					}
				}
				return b, nil
			}
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err = r.Run(ctx); !errors.Is(err, ErrIdentity) {
				t.Fatalf("wanted identity failure: %v", err)
			}
			if calls.Load() != 0 || p.starts != 0 {
				t.Fatal("malformed batch partially executed")
			}
		})
	}
}

func TestRecoveryPendingNeverRepeatsBegin(t *testing.T) {
	c := testConfig(t)
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var modes []api.StartMode
	pending := true
	p.start = func(ctx context.Context, q api.StartRequest) (*api.StartResponse, error) {
		modes = append(modes, q.Mode)
		if q.Mode == api.StartModeBegin {
			return nil, errors.New("start may commit later")
		}
		if pending {
			return startReply(q, api.StartDispositionPending), nil
		}
		return startReply(q, api.StartDispositionStarted), nil
	}
	var calls int
	r.execute(context.Background(), assignment("delayed", "recovery"), func(context.Context, Job) (api.Outcome, error) { calls++; return api.Outcome{}, nil }, func(err error) { t.Fatal(err) })
	records, err := r.journal.records()
	if err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	before, _ := json.Marshal(records[0])
	for range 2 {
		if err = r.deliver(context.Background(), records[0]); err != nil {
			t.Fatal(err)
		}
	}
	records, err = r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(records[0])
	if !bytes.Equal(before, after) {
		t.Fatal("pending released or altered reservation")
	}
	pending = false
	if err = r.deliver(context.Background(), records[0]); err != nil {
		t.Fatal(err)
	}
	records, err = r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != "outcome" || records[0].Outcome.Status != "unknown" || calls != 0 {
		t.Fatal("confirmed interrupted start did not remain unexecuted")
	}
	for i, mode := range modes {
		if i == 0 && mode != api.StartModeBegin || i > 0 && mode != api.StartModeReconcile {
			t.Fatal("begin repeated")
		}
	}
}

func TestCancelledGrantAndResolverCannotInvokeHandler(t *testing.T) {
	for _, stage := range []string{"grant", "resolver", "invoke"} {
		t.Run(stage, func(t *testing.T) {
			c := testConfig(t)
			p := &protocolFuncs{fakeProtocol: newFake()}
			c.Client = p
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.start = func(_ context.Context, q api.StartRequest) (*api.StartResponse, error) {
				if stage == "grant" {
					cancel()
				}
				return startReply(q, api.StartDispositionGranted), nil
			}
			a := assignment("cancelled", "recovery")
			if stage == "resolver" {
				a.CredentialProfile = "private-profile"
				c.Credentials = func(context.Context, string) (any, error) { cancel(); return "ignored", nil }
			}
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			calls := 0
			h := func(context.Context, Job) (api.Outcome, error) { calls++; return api.Outcome{Status: "completed"}, nil }
			if stage == "invoke" {
				cancel()
				_, err = invoke(ctx, h, Job{})
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				r.execute(ctx, a, h, func(err error) { t.Fatal(err) })
				records, e := r.journal.records()
				if e != nil || len(records) != 1 || records[0].State != "outcome" || records[0].Outcome.Status != "unknown" {
					t.Fatalf("missing interrupted outcome: %v", e)
				}
			}
			if calls != 0 {
				t.Fatal("handler ignored pre-invocation cancellation")
			}
		})
	}
}

func TestStartResponseEchoesAndDisposition(t *testing.T) {
	q := api.StartRequest{ServerID: "server-epoch-1", SessionID: "session-1", Mode: api.StartModeReconcile, ExecutionID: "one", ExecutionRevision: "rev", LeaseID: "lease"}
	for _, field := range []string{"server", "worker", "session", "execution", "revision", "lease", "grant", "deadline", "receipt", "reconcile-granted", "unknown-disposition"} {
		t.Run(field, func(t *testing.T) {
			reply := startReply(q, api.StartDispositionStarted)
			switch field {
			case "server":
				reply.ServerID = "other"
			case "worker":
				reply.WorkerUID = "other"
			case "session":
				reply.SessionID = "other"
			case "execution":
				reply.ExecutionID = "other"
			case "revision":
				reply.ExecutionRevision = "other"
			case "lease":
				reply.LeaseID = "other"
			case "grant":
				reply.GrantID = ""
			case "deadline":
				reply.Deadline = time.Time{}
			case "receipt":
				reply.ReceiptID = "unexpected"
			case "reconcile-granted":
				reply.Disposition = api.StartDispositionGranted
			case "unknown-disposition":
				reply.Disposition = "future"
			}
			if !errors.Is(validateStart(reply, q, "worker-uid-1"), ErrIdentity) {
				t.Fatal("malformed start accepted")
			}
		})
	}
	for _, disposition := range []api.StartDisposition{api.StartDispositionPending, api.StartDispositionRejected, api.StartDispositionTerminal, api.StartDispositionUnknown, api.StartDispositionStarted} {
		if err := validateStart(startReply(q, disposition), q, "worker-uid-1"); err != nil {
			t.Fatalf("valid %s: %v", disposition, err)
		}
	}
}

func TestJournalFormatOneAndUIDMismatchPreserveRecords(t *testing.T) {
	for _, change := range []string{"format1", "worker-id", "worker-uid", "server-id"} {
		t.Run(change, func(t *testing.T) {
			c := testConfig(t)
			c, _ = c.validate()
			j, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = j.reserve(assignment("retained", "recovery")); err != nil {
				t.Fatal(err)
			}
			if err = j.close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(c.StateDir, "worker.db")
			if change == "format1" {
				db, e := bolt.Open(path, 0600, nil)
				if e != nil {
					t.Fatal(e)
				}
				err = db.Update(func(tx *bolt.Tx) error {
					b := tx.Bucket(metadataBucket)
					var meta journalMetadata
					if e := json.Unmarshal(b.Get([]byte("identity")), &meta); e != nil {
						return e
					}
					meta.Format = 1
					raw, _ := json.Marshal(meta)
					return b.Put([]byte("identity"), raw)
				})
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			key, _ := os.ReadFile(c.WrappingKeyPath)
			switch change {
			case "worker-id":
				c.WorkerID = "other"
			case "worker-uid":
				c.WorkerUID = "other"
			case "server-id":
				c.ServerID = "other"
			}
			other, err := openJournal(c)
			if other != nil {
				_ = other.close()
			}
			want := ErrIdentity
			if change == "format1" {
				want = ErrJournalVersion
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v", err)
			}
			after, _ := os.ReadFile(path)
			afterKey, _ := os.ReadFile(c.WrappingKeyPath)
			if !bytes.Equal(original, after) || !bytes.Equal(key, afterKey) {
				t.Fatal("rejected journal altered records or wrapping key")
			}
		})
	}
	c := testConfig(t)
	c.WorkerUID = ""
	if r, err := New(c); err == nil {
		_ = r.Close()
		t.Fatal("missing UID accepted")
	}
}

func TestOriginalOutboxDeliversWithoutSessionOrCapabilities(t *testing.T) {
	c := testConfig(t)
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	a := assignment("old-session", "notification")
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	if err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error { rec.GrantID = "old-grant"; rec.State = "started"; return nil }); err != nil {
		t.Fatal(err)
	}
	r.saveOutcome(a.ExecutionID, api.Outcome{ServerID: "forged", WorkerUID: "forged", Status: "delivered"}, func(err error) { t.Fatal(err) })
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	p.poll = func(context.Context, api.PollRequest) (*api.Assignments, error) {
		t.Fatal("outbox requires no polling session")
		return nil, nil
	}
	p.start = func(context.Context, api.StartRequest) (*api.StartResponse, error) {
		t.Fatal("outbox requires no start/grants")
		return nil, nil
	}
	p.result = func(_ context.Context, out api.Outcome) (*api.Receipt, error) {
		if out.ServerID != c.ServerID || out.WorkerUID != c.WorkerUID || out.GrantID != "old-grant" {
			t.Fatal("outbox rebound")
		}
		return &api.Receipt{ServerID: c.ServerID, WorkerUID: c.WorkerUID, ExecutionID: out.ExecutionID, ReceiptID: "retained-receipt", Disposition: api.ReceiptDispositionFinalized}, nil
	}
	r, err = New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	records, err := r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	if err = r.deliver(context.Background(), records[0]); err != nil {
		t.Fatal(err)
	}
	records, err = r.journal.records()
	if err != nil || len(records) != 0 {
		t.Fatal("outbox did not drain with replacement client")
	}
}

func TestFlushReloadsOutcomeAfterHandlerFinishes(t *testing.T) {
	c := testConfig(t)
	p := &protocolFuncs{fakeProtocol: newFake()}
	c.Client = p
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := assignment("finished", "notification")
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	inventory, err := r.journal.records()
	if err != nil {
		t.Fatal(err)
	}
	r.active[a.ExecutionID] = true
	if err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error { rec.State = "started"; rec.GrantID = "original-grant"; return nil }); err != nil {
		t.Fatal(err)
	}
	r.saveOutcome(a.ExecutionID, api.Outcome{Status: "delivered", Diagnostic: "confirmed-original-outcome"}, func(err error) { t.Fatal(err) })
	delete(r.active, a.ExecutionID)
	p.start = func(context.Context, api.StartRequest) (*api.StartResponse, error) {
		t.Fatal("stale reservation was reconciled")
		return nil, nil
	}
	if err = r.deliverInactive(context.Background(), inventory[0].ExecutionID); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) != 1 || p.results[0].Status != "delivered" || p.results[0].Diagnostic != "confirmed-original-outcome" {
		t.Fatal("final outcome was replaced")
	}
}

func TestHeartbeatRejectsChangedExecutionEcho(t *testing.T) {
	for _, field := range []string{"server", "worker", "session", "execution", "grant"} {
		t.Run(field, func(t *testing.T) {
			c := testConfig(t)
			c.HeartbeatInterval = time.Millisecond
			p := &protocolFuncs{fakeProtocol: newFake()}
			c.Client = p
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			a := assignment("heartbeat", "check")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.heartbeat = func(_ context.Context, q api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
				if q.ServerID != a.ServerID || q.SessionID != a.SessionID || q.ExecutionID != a.ExecutionID || q.GrantID != "grant" || q.WorkerID != c.WorkerID {
					t.Fatal("heartbeat rebound")
				}
				reply := &api.HeartbeatResponse{ServerID: q.ServerID, WorkerUID: c.WorkerUID, SessionID: q.SessionID, ExecutionID: q.ExecutionID, GrantID: q.GrantID, Accepted: true}
				switch field {
				case "server":
					reply.ServerID = "other"
				case "worker":
					reply.WorkerUID = "other"
				case "session":
					reply.SessionID = "other"
				case "execution":
					reply.ExecutionID = "other"
				case "grant":
					reply.GrantID = "other"
				}
				return reply, nil
			}
			var failure error
			r.heartbeat(ctx, a, "grant", cancel, func(err error) { failure = err })
			if !errors.Is(failure, ErrIdentity) || ctx.Err() == nil {
				t.Fatal("changed heartbeat did not stop execution")
			}
		})
	}
}

func TestLateEvidencePinsOriginalServerAndUID(t *testing.T) {
	c := testConfig(t)
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := assignment("unknown", "notification")
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	if err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.State = "unknown"
		rec.GrantID = "original-grant"
		rec.ReceiptID = "original-receipt"
		rec.Outcome = &api.Outcome{ServerID: c.ServerID, WorkerUID: c.WorkerUID, ExecutionID: a.ExecutionID, GrantID: rec.GrantID, Kind: a.Kind, Status: "unknown"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e := api.LateEvidenceRequest{ExecutionID: a.ExecutionID, OriginalReceiptID: "original-receipt", EvidenceID: "evidence"}
	forged := e
	forged.ServerID = "restored"
	if err = r.QueueLateEvidence(context.Background(), forged); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign server accepted")
	}
	forged = e
	forged.WorkerUID = "other"
	if err = r.QueueLateEvidence(context.Background(), forged); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign worker accepted")
	}
	if err = r.QueueLateEvidence(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	rec, found, err := r.journal.record(a.ExecutionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if rec.Evidence.ServerID != c.ServerID || rec.Evidence.WorkerUID != c.WorkerUID {
		t.Fatal("missing original evidence identity")
	}
	if err = r.deliver(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err = r.checkReceipt(a.ExecutionID, &api.Receipt{ServerID: c.ServerID, WorkerUID: "recreated-worker", ExecutionID: a.ExecutionID, ReceiptID: "receipt", Disposition: api.ReceiptDispositionFinalized}); !errors.Is(err, ErrIdentity) {
		t.Fatal("different worker receipt accepted")
	}
}

func TestJournalReservationCoversEscapedIdentityMetadata(t *testing.T) {
	c := testConfig(t)
	escaped := strings.Repeat("\x01", 256)
	c.ServerID = escaped
	c.WorkerUID = escaped
	c.WorkerID = escaped
	c.Limits.Records = 1
	c.Limits.LiveBytes = 128<<10 + journalMetadataReserve
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := assignment(escaped, "recovery")
	a.ServerID = escaped
	a.WorkerUID = escaped
	a.SessionID = escaped
	a.JobTypeUID = escaped
	a.IncarnationUID = escaped
	a.ExecutionRevision = escaped
	a.LeaseID = escaped
	if err = r.journal.reserve(a); err != nil {
		t.Fatal(err)
	}
	if err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error { rec.State = "started"; rec.GrantID = escaped; return nil }); err != nil {
		t.Fatal(err)
	}
	out := api.Outcome{ServerID: escaped, WorkerUID: escaped, ExecutionID: escaped, GrantID: escaped, Kind: a.Kind, Status: "completed", Evidence: []string{}, Data: json.RawMessage(`""`)}
	baseline, _ := json.Marshal(out)
	out.Data = json.RawMessage(`"` + strings.Repeat("x", r.config.Limits.OutcomeBytes-len(baseline)) + `"`)
	r.saveOutcome(a.ExecutionID, out, func(err error) { t.Fatal(err) })
	rec, found, err := r.journal.record(a.ExecutionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rec.Outcome)
	if len(encoded) != r.config.Limits.OutcomeBytes || rec.Outcome.Status != "completed" {
		t.Fatal("reserved maximum outcome could not be persisted")
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.LiveBytes > c.Limits.LiveBytes || status.ReservedBytes != 0 {
		t.Fatal("completion exceeded reservation")
	}
}

func TestRegistryProtocolBounds(t *testing.T) {
	handler := func(context.Context, Job) (api.Outcome, error) { return api.Outcome{}, nil }
	for _, bad := range []string{"", strings.Repeat("x", 257), "bad\x00id", "bad\r\nid", string([]byte{0xff})} {
		if err := NewRegistry().Register(bad, "1", "check", handler); err == nil {
			t.Fatal("invalid job type accepted")
		}
		if err := NewRegistry().Register("valid", bad, "check", handler); err == nil {
			t.Fatal("invalid version accepted")
		}
	}
	registry := NewRegistry()
	for i := range 64 {
		if err := registry.Register(fmt.Sprint(i), "1", "check", handler); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register("overflow", "1", "check", handler); err == nil {
		t.Fatal("capability bound exceeded")
	}
}
