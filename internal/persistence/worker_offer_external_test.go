//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func workerOfferFixture(t *testing.T, category string) (*Store, WorkerAuthority, WorkerPollRequest, WorkerExecutionIntent, WorkerPrincipal) {
	t.Helper()
	s := openCatalogMemory(t)
	in, job := workerExecutionFixture(t, s, category)
	s.administrative = true
	p := workerPrincipal("assigned-worker")
	p.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: category, ResourceKind: in.Source.Kind, ResourceIDs: []string{in.Source.ID}}}
	policy := commitWorker(t, s, workerCommand(t, s, "bootstrap", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	a, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	request := WorkerPollRequest{ServerID: server.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 10, Limit: 10, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: category}}}
	return s, a, request, in, p
}
func workerStartFromOffer(response WorkerPollResponse) WorkerStartRequest {
	offer := response.Offers[0]
	return WorkerStartRequest{ServerID: response.ServerID, SessionID: response.SessionID, ExecutionID: offer.ExecutionID, ExecutionRevision: offer.ExecutionRevision, LeaseID: offer.LeaseID, Mode: "begin"}
}
func workerStartAt(t *testing.T, s *Store, a WorkerAuthority, r WorkerStartRequest, at time.Time) Result {
	t.Helper()
	cmd := WorkerStartCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: r, ProposedGrantID: uuid.NewString()}
	return submit(t, s, Command{Kind: "worker_start", At: at, commandExtensions: commandExtensions{WorkerStart: &cmd}})[0]
}
func TestWorkerOfferExactReplayAndOneStartAllCategories(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, request, in, _ := workerOfferFixture(t, category)
			before, _ := s.Get(in.MonitorID)
			response, err := s.CommitWorkerPoll(t.Context(), a, request)
			if err != nil || len(response.Offers) != 1 {
				t.Fatal("offer", err, len(response.Offers))
			}
			if !response.Offers[0].Deadline.Equal(in.Deadline) {
				t.Fatal("wire shortened original execution deadline")
			}
			original, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if original.Lifecycle.Phase != "offered" || original.Lifecycle.OfferDeadline.After(original.Lifecycle.OfferedAt.Add(WorkerOfferLifetime)) {
				t.Fatal("offer state")
			}
			replay, err := s.CommitWorkerPoll(t.Context(), a, request)
			if err != nil || !reflect.DeepEqual(replay, response) {
				t.Fatal("poll replay", err)
			}
			unchanged, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if !reflect.DeepEqual(unchanged, original) {
				t.Fatal("poll replay renewed lease")
			}
			after, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("offer mutated action/health")
			}
			if err = s.VerifyWorkerPollResponse(t.Context(), a, response); err != nil {
				t.Fatal(err)
			}
			response.Offers[0].ExecutionID = "changed"
			if err = s.VerifyWorkerPollResponse(t.Context(), a, response); err == nil {
				t.Fatal("changed response accepted")
			}
			response = replay
			requestStart := workerStartFromOffer(response)
			requestStart.Mode = "reconcile"
			pending, err := s.CommitWorkerStart(t.Context(), a, requestStart)
			if err != nil || pending.Disposition != "pending" || pending.GrantID != "" || !pending.Deadline.IsZero() {
				t.Fatal("reconcile created grant", pending, err)
			}
			requestStart.Mode = "begin"
			granted, err := s.CommitWorkerStart(t.Context(), a, requestStart)
			if err != nil || granted.Disposition != "granted" || granted.GrantID == "" || !granted.Deadline.Equal(in.Deadline) {
				t.Fatal("start", granted, err)
			}
			duplicate, err := s.CommitWorkerStart(t.Context(), a, requestStart)
			if err != nil || duplicate.Disposition != "started" || duplicate.GrantID != granted.GrantID {
				t.Fatal("duplicate begin regranted", duplicate, err)
			}
			requestStart.Mode = "reconcile"
			reconciled, err := s.CommitWorkerStart(t.Context(), a, requestStart)
			if err != nil || reconciled.Disposition != "started" || reconciled.GrantID != granted.GrantID {
				t.Fatal("reconcile", reconciled, err)
			}
			m, _ := s.Get(in.MonitorID)
			if category == "check" {
				if !reflect.DeepEqual(m, before) {
					t.Fatal("check start changed health")
				}
			} else {
				action := m.Actions[in.ActionID]
				if action.State != Started || action.ExecutorKind != "worker" || action.ExecutorSession != granted.GrantID {
					t.Fatal("remote action start missing")
				}
				if actionExecutorFenced(action, "different-server-owner") {
					t.Fatal("server restart treated as remote process fencing")
				}
			}
			if err = validateWorkerSessionImage(s.fsm.image); err != nil {
				t.Fatal("session image", err)
			}
			if err = validateWorkerExecutionImage(s.fsm.image); err != nil {
				t.Fatal("execution image", err)
			}
			if err = validateRecoveryReviewImage(s.fsm.image); err != nil {
				t.Fatal("action image", err)
			}
		})
	}
}
func TestWorkerOfferScopeProjectionAndNextSequence(t *testing.T) {
	s, a, request, in, p := workerOfferFixture(t, "check")
	typ, _, err := s.JobType(t.Context(), in.JobType.JobTypeID)
	if err != nil {
		t.Fatal(err)
	}
	outOfScope := workerExecutionFixtureType(t, s, typ)
	if _, err = s.CommitWorkerExecution(t.Context(), outOfScope); err != nil {
		t.Fatal(err)
	}
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil || len(response.Offers) != 1 || response.Offers[0].ExecutionID != in.ID {
		t.Fatal("scope enlarged", response, err)
	}
	request.SessionID = response.SessionID
	request.PollSequence++
	next, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil || len(next.Offers) != 0 {
		t.Fatal("existing/unscoped work reoffered", err)
	}
	if err = s.VerifyWorkerPollResponse(t.Context(), a, response); err == nil {
		t.Fatal("superseded response accepted")
	}
	p.Grants = nil
	p.GrantRevision = uuid.NewString()
	s.administrative = true
	policy := commitWorker(t, s, workerCommand(t, s, "upsert", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if err = s.VerifyWorkerPollResponse(t.Context(), a, next); err == nil {
		t.Fatal("revoked scope retained plaintext authority")
	}
}
func TestWorkerOfferRejectedLeaseFencesDelayedBegin(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "recovery")
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r := workerStartFromOffer(response)
	record, _, _ := s.WorkerExecution(t.Context(), in.ID)
	r.Mode = "reconcile"
	rejected := workerStartAt(t, s, a, r, record.Lifecycle.OfferDeadline)
	if rejected.Err != nil || rejected.WorkerStart == nil || rejected.WorkerStart.Disposition != "rejected" || rejected.WorkerStart.GrantID != "" {
		t.Fatal("expiry not durably fenced", rejected.Err)
	}
	r.Mode = "begin"
	late := workerStartAt(t, s, a, r, record.Lifecycle.OfferDeadline.Add(-time.Nanosecond))
	if late.Allowed && late.WorkerStart != nil && late.WorkerStart.Disposition == "granted" {
		t.Fatal("delayed begin bypassed committed rejection")
	}
	current := workerStartAt(t, s, a, r, record.Lifecycle.OfferDeadline.Add(time.Second))
	if current.Err != nil || current.WorkerStart.Disposition != "rejected" {
		t.Fatal("rejected lease reopened", current.Err)
	}
	m, _ := s.Get(in.MonitorID)
	if m.Actions[in.ActionID].State != Queued {
		t.Fatal("unstarted rejection mutated action")
	}
	r.LeaseID = uuid.NewString()
	if _, err = s.CommitWorkerStart(t.Context(), a, r); !errors.Is(err, ErrWorkerExecutionConflict) {
		t.Fatal("unknown lease became authoritative rejection", err)
	}
}
func TestWorkerStartConcurrentDuplicateBeginAndUnknown(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "notification")
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r := workerStartFromOffer(response)
	var wg sync.WaitGroup
	responses := make(chan WorkerStartResponse, 8)
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.CommitWorkerStart(t.Context(), a, r)
			if e != nil {
				errs <- e
			} else {
				responses <- v
			}
		}()
	}
	wg.Wait()
	close(responses)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	grants := 0
	grant := ""
	for v := range responses {
		if v.Disposition == "granted" {
			grants++
		} else if v.Disposition != "started" {
			t.Fatal(v.Disposition)
		}
		if grant != "" && grant != v.GrantID {
			t.Fatal("multiple grants")
		}
		grant = v.GrantID
	}
	if grants != 1 {
		t.Fatal("not exactly one executable disposition", grants)
	}
	r.Mode = "reconcile"
	unknown := workerStartAt(t, s, a, r, in.Deadline)
	if unknown.Err != nil || unknown.WorkerStart.Disposition != "unknown" || unknown.WorkerStart.GrantID != grant {
		t.Fatal("unknown", unknown.Err)
	}
	m, _ := s.Get(in.MonitorID)
	if m.Actions[in.ActionID].State != Unknown || !m.Actions[in.ActionID].Held() {
		t.Fatal("remote outcome not conservatively held")
	}
	if validateRecoveryReviewImage(s.fsm.image) != nil {
		t.Fatal("unknown snapshot invalid")
	}
}
func TestWorkerStartReconcileCurrentCredentialWithoutCurrentSessionOrGrant(t *testing.T) {
	s, a, request, _, p := workerOfferFixture(t, "check")
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r := workerStartFromOffer(response)
	started, err := s.CommitWorkerStart(t.Context(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	p.Grants = nil
	p.GrantRevision = uuid.NewString()
	s.administrative = true
	policy := commitWorker(t, s, workerCommand(t, s, "upsert", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	r.Mode = "reconcile"
	at := response.SessionExpiresAt.Add(time.Second)
	result := workerStartAt(t, s, a, r, at)
	if result.Err != nil || result.WorkerStart.Disposition != "started" || result.WorkerStart.GrantID != started.GrantID {
		t.Fatal("prior grant/session required for reconcile", result.Err)
	}
	p.TokenSHA256 = authenticationVerifier("rotated-current-worker")
	p.CredentialRevision = uuid.NewString()
	s.administrative = true
	policy = commitWorker(t, s, workerCommand(t, s, "upsert", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err = s.CommitWorkerStart(t.Context(), a, r); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("old credential accepted", err)
	}
	fresh, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	result = workerStartAt(t, s, fresh, r, at.Add(time.Second))
	if result.Err != nil || result.WorkerStart.Disposition != "started" {
		t.Fatal("current credential denied original work", result.Err)
	}
}
func TestWorkerOfferFormatsSnapshotsAndCancelledRequests(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "check")
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r := workerStartFromOffer(response)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.CommitWorkerStart(ctx, a, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cmd := WorkerStartCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: r, ProposedGrantID: uuid.NewString()}
	command := Command{Kind: "worker_start", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerStart: &cmd}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerOfferFormatVersion) {
			t.Fatal("start format", version, err)
		}
	}
	snap := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(snap, []byte(workerOfferSnapshotMagic)) {
		t.Fatal("snapshot frame")
	}
	restored, ledger, err := decodeSnapshot(bytes.NewReader(snap), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for name, change := range map[string]func(*image){"format": func(i *image) { i.Version = WorkerExecutionFormatVersion }, "worker": func(i *image) {
		v := i.WorkerExecutions.Records[in.ID]
		v.Lifecycle.WorkerUID = "other"
		i.WorkerExecutions.Records[in.ID] = v
	}, "lease": func(i *image) {
		v := i.WorkerExecutions.Records[in.ID]
		v.Lifecycle.LeaseID = "other"
		i.WorkerExecutions.Records[in.ID] = v
	}, "grant-on-offer": func(i *image) {
		v := i.WorkerExecutions.Records[in.ID]
		v.Lifecycle.GrantID = "grant"
		i.WorkerExecutions.Records[in.ID] = v
	}} {
		t.Run(name, func(t *testing.T) {
			bad := restored
			bad.imageExtensions = cloneImageExtensions(restored)
			change(&bad)
			if validateImageExtensions(bad) == nil {
				t.Fatal("corrupt lifecycle accepted")
			}
		})
	}
}

func TestWorkerOfferNativeRestartFencesOffersAndHoldsStarts(t *testing.T) {
	for _, started := range []bool{false, true} {
		for _, snapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("started=%t/snapshot=%t", started, snapshot), func(t *testing.T) {
				config := testConfig(t)
				store, err := Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if store != nil {
						_ = store.Close()
					}
				}()
				category := "check"
				if started {
					category = "recovery"
				}
				in, job := workerExecutionFixture(t, store, category)
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				store = nil
				admin := openAuthenticationAdmin(t, config)
				p := workerPrincipal("native-offer-worker")
				p.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: category, ResourceKind: in.Source.Kind, ResourceIDs: []string{in.Source.ID}}}
				commitWorker(t, admin, workerCommand(t, admin, "bootstrap", p))
				if err = admin.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.CommitWorkerExecution(t.Context(), in); err != nil {
					t.Fatal(err)
				}
				identity, err := store.WorkerProtocolIdentity(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				auth, err := store.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				req := WorkerPollRequest{ServerID: identity.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: category}}}
				offered, err := store.CommitWorkerPoll(t.Context(), auth, req)
				if err != nil || len(offered.Offers) != 1 {
					t.Fatal("offer", err)
				}
				start := workerStartFromOffer(offered)
				var grant WorkerStartResponse
				if started {
					grant, err = store.CommitWorkerStart(t.Context(), auth, start)
					if err != nil || grant.Disposition != "granted" {
						t.Fatal("grant", err)
					}
				}
				saved, _, _ := store.WorkerExecution(t.Context(), in.ID)
				if snapshot {
					if err = store.Snapshot(); err != nil {
						t.Fatal(err)
					}
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				store = nil
				inspect := openAuthenticationAdmin(t, config)
				retained, ok, err := inspect.WorkerExecution(t.Context(), in.ID)
				if err != nil || !ok || !reflect.DeepEqual(saved, retained) {
					t.Fatal("stopped replay changed original execution", err)
				}
				if err = inspect.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				auth, err = store.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err = store.VerifyWorkerPollResponse(t.Context(), auth, offered); err == nil {
					t.Fatal("old owner disclosed offer")
				}
				start.Mode = "reconcile"
				response, err := store.CommitWorkerStart(t.Context(), auth, start)
				if err != nil {
					t.Fatal(err)
				}
				if started {
					if response.Disposition != "unknown" || response.GrantID != grant.GrantID {
						t.Fatal("restart regranted or lost uncertainty", response)
					}
					monitor, _ := store.Get(in.MonitorID)
					action := monitor.Actions[in.ActionID]
					if action.State != Unknown || !action.Held() || actionExecutorFenced(action, store.executorSession) {
						t.Fatal("remote action not held")
					}
				} else if response.Disposition != "rejected" || response.GrantID != "" {
					t.Fatal("old offer not fenced", response)
				}
				start.Mode = "begin"
				again, err := store.CommitWorkerStart(t.Context(), auth, start)
				if err != nil || again.Disposition == "granted" {
					t.Fatal("old begin became executable", again, err)
				}
				if err = store.Snapshot(); err != nil {
					t.Fatal("post-reconciliation snapshot", err)
				}
			})
		}
	}
}
func TestWorkerOfferAtomicQuotasAndProjectionBounds(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "check")
	// Session admission failure must leave the selected execution unoffered.
	s.fsm.image.WorkerSessions = &workerSessionImage{Version: 1, NodeID: s.nodeID, PolicyEpoch: a.Epoch, Sessions: map[string]workerSessionRecord{}, EncodedBytes: MaxWorkerSessionBytes}
	if _, err := s.CommitWorkerPoll(t.Context(), a, request); !errors.Is(err, ErrWorkerSessionQuota) {
		t.Fatal("session quota", err)
	}
	original, _, _ := s.WorkerExecution(t.Context(), in.ID)
	if original.Lifecycle != nil {
		t.Fatal("failed session left an offer")
	}
	s.fsm.image.WorkerSessions = nil
	used := s.fsm.image.WorkerExecutions.EncodedBytes
	s.fsm.image.WorkerExecutions.EncodedBytes = MaxWorkerExecutionBytes
	if _, err := s.CommitWorkerPoll(t.Context(), a, request); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("offer reservation quota", err)
	}
	if s.fsm.image.WorkerSessions != nil {
		t.Fatal("failed offer committed a session")
	}
	s.fsm.image.WorkerExecutions.EncodedBytes = used
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.WorkerExecution(t.Context(), in.ID)
	beforeCost, _ := workerExecutionRecordCost(in.ID, before)
	// Lifecycle reservation ensures Start and unknown persistence need no extra bytes.
	s.fsm.image.WorkerExecutions.EncodedBytes = MaxWorkerExecutionBytes
	r := workerStartFromOffer(response)
	if _, err = s.CommitWorkerStart(t.Context(), a, r); err != nil {
		t.Fatal("reserved grant capacity missing", err)
	}
	r.Mode = "reconcile"
	result := workerStartAt(t, s, a, r, in.Deadline)
	if result.Err != nil || result.WorkerStart.Disposition != "unknown" {
		t.Fatal("reserved unknown capacity missing", result.Err)
	}
	after, _, _ := s.WorkerExecution(t.Context(), in.ID)
	afterCost, _ := workerExecutionRecordCost(in.ID, after)
	if beforeCost != afterCost {
		t.Fatal("lifecycle reservation changed", beforeCost, afterCost)
	}
}
