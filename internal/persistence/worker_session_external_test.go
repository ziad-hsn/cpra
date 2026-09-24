//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func workerSessionFixture(t *testing.T) (*Store, WorkerAuthority, WorkerPollRequest, WorkerPolicyCommand) {
	t.Helper()
	s, _, policy := workerFixture(t)
	commitWorker(t, s, policy)
	s.administrative = false // Isolated memory FSM fixture; native ownership tested separately.
	if wait := time.Until(policy.At); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	a, err := s.AuthenticateWorker(t.Context(), policy.Worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := policy.Worker.Grants[0]
	r := WorkerPollRequest{ServerID: id.ServerID, WorkerID: a.WorkerID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capabilities: []WorkerSessionCapability{{JobTypeID: g.JobTypeID, Version: g.Version, Category: g.Category}}, Capacity: 16, Limit: 10, WaitSeconds: 25}
	return s, a, r, policy
}
func sessionApply(t *testing.T, s *Store, a WorkerAuthority, r WorkerPollRequest, at time.Time) Result {
	t.Helper()
	r, err := normalizeWorkerPoll(r)
	if err != nil {
		t.Fatal(err)
	}
	c := WorkerSessionCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: r, ProposedSessionID: uuid.NewString()}
	results, err := s.Submit(t.Context(), []Command{{Kind: "worker_poll", At: at, commandExtensions: commandExtensions{WorkerSession: &c}}})
	if err != nil || len(results) != 1 {
		t.Fatal(err, results)
	}
	return results[0]
}
func TestWorkerSessionExactReplayAndFrozenCapabilities(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	first, err := s.CommitWorkerPoll(t.Context(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	before := s.fsm.image.WorkerSessions.clone()
	replay, err := s.CommitWorkerPoll(t.Context(), a, r)
	if err != nil || !reflect.DeepEqual(replay, first) || !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("initial lost reply changed receipt or expiry", err)
	}
	changed := r.clone()
	changed.Limit--
	if _, err = s.CommitWorkerPoll(t.Context(), a, changed); !errors.Is(err, ErrWorkerSessionConflict) {
		t.Fatal("changed repeated request accepted", err)
	}
	changed = r.clone()
	changed.ClientSessionID = uuid.NewString()
	if _, err = s.CommitWorkerPoll(t.Context(), a, changed); !errors.Is(err, ErrWorkerSessionConflict) {
		t.Fatal("live owner taken over", err)
	}
	next := r.clone()
	next.SessionID = first.SessionID
	next.PollSequence = 2
	next.Limit = 1
	second, err := s.CommitWorkerPoll(t.Context(), a, next)
	if err != nil || second.SessionID != first.SessionID || second.PollSequence != 2 || second.SessionExpiresAt.Before(first.SessionExpiresAt) {
		t.Fatal("next poll", err)
	}
	before = s.fsm.image.WorkerSessions.clone()
	replay, err = s.CommitWorkerPoll(t.Context(), a, next)
	if err != nil || !reflect.DeepEqual(replay, second) || !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("repeat renewed session", err)
	}
	if _, err = s.CommitWorkerPoll(t.Context(), a, r); !errors.Is(err, ErrWorkerSessionSequence) {
		t.Fatal("old initial poll reopened session", err)
	}
	next.PollSequence = 4
	if _, err = s.CommitWorkerPoll(t.Context(), a, next); !errors.Is(err, ErrWorkerSessionSequence) {
		t.Fatal("sequence gap accepted", err)
	}
	next.PollSequence = 3
	next.SessionID = uuid.NewString()
	if _, err = s.CommitWorkerPoll(t.Context(), a, next); !errors.Is(err, ErrWorkerSessionExpired) {
		t.Fatal("unknown session reopened", err)
	}
	next.SessionID = first.SessionID
	next.Capabilities[0].Version = "not-granted"
	if _, err = s.CommitWorkerPoll(t.Context(), a, next); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("capability enlarged authority", err)
	}
	if !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("failed request mutated session")
	}
	if err = validateWorkerSessionImage(s.fsm.image); err != nil {
		t.Fatal("session image invalid", err)
	}
}
func TestWorkerSessionExpiryOwnerAndIdentityFences(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	first, err := s.CommitWorkerPoll(t.Context(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	if result := sessionApply(t, s, a, r, first.SessionExpiresAt); !errors.Is(result.Err, ErrWorkerSessionExpired) {
		t.Fatal("expired initial retry extended lifetime", result.Err)
	}
	fresh := r.clone()
	fresh.ClientSessionID = uuid.NewString()
	opened := sessionApply(t, s, a, fresh, first.SessionExpiresAt)
	if opened.Err != nil || opened.WorkerPoll == nil || opened.WorkerPoll.SessionID == first.SessionID {
		t.Fatal("explicit fresh nonce after expiry", opened.Err)
	}
	oldOwner := s.executorSession
	s.executorSession = uuid.NewString()
	results := submit(t, s, Command{Kind: "local_session", At: first.SessionExpiresAt.Add(time.Millisecond), ExecutorSession: s.executorSession})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatal(results)
	}
	if result := sessionApply(t, s, a, fresh, first.SessionExpiresAt.Add(time.Second)); !errors.Is(result.Err, ErrWorkerSessionExpired) {
		t.Fatal("old owner initial replay survived restart", result.Err)
	}
	stale := WorkerSessionCommand{NodeID: s.nodeID, OwnerEpoch: oldOwner, Authority: a, Request: fresh, ProposedSessionID: uuid.NewString()}
	result := submit(t, s, Command{Kind: "worker_poll", At: first.SessionExpiresAt.Add(time.Second), commandExtensions: commandExtensions{WorkerSession: &stale}})[0]
	if !errors.Is(result.Err, ErrWorkerSessionExpired) {
		t.Fatal("old queued command survived owner fence", result.Err)
	}
	fresh.ClientSessionID = uuid.NewString()
	if result := sessionApply(t, s, a, fresh, first.SessionExpiresAt.Add(time.Second)); result.Err != nil {
		t.Fatal(result.Err)
	}
	fresh.ServerID = workerProtocolServerID(s.nodeID, "different-epoch")
	if _, err := s.CommitWorkerPoll(t.Context(), a, fresh); !errors.Is(err, ErrWorkerSessionIdentity) {
		t.Fatal("restored server identity accepted", err)
	}
}
func TestWorkerSessionCurrentPolicyAndCancelledAdmission(t *testing.T) {
	s, a, r, policy := workerSessionFixture(t)
	first, err := s.CommitWorkerPoll(t.Context(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := s.fsm.image.WorkerSessions.clone()
	if _, err = s.CommitWorkerPoll(ctx, a, r); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("cancelled poll mutated state", err)
	}
	s.administrative = true
	if _, err = s.CommitWorkerPoll(t.Context(), a, r); !errors.Is(err, ErrWorkerSessionUnavailable) {
		t.Fatal("administrative poll admitted", err)
	}
	p := policy.Worker.Clone()
	p.TokenSHA256 = authenticationVerifier("rotated-worker-secret")
	p.CredentialRevision = uuid.NewString()
	updated := commitWorker(t, s, workerCommand(t, s, "upsert", p))
	s.administrative = false
	if wait := time.Until(updated.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err = s.CommitWorkerPoll(t.Context(), a, r); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("stale credential observation accepted", err)
	}
	newAuthority, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.CommitWorkerPoll(t.Context(), newAuthority, r); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("same UID rotation changed replay", err)
	}
	s.administrative = true
	p.Grants = nil
	p.GrantRevision = uuid.NewString()
	updated = commitWorker(t, s, workerCommand(t, s, "upsert", p))
	s.administrative = false
	if wait := time.Until(updated.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	newAuthority, err = s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitWorkerPoll(t.Context(), newAuthority, r); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("grant removal admitted replay", err)
	}
	if !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("denial erased retained response")
	}
}
func TestWorkerSessionBoundsFormatsAndSnapshots(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	for _, edit := range []func(*WorkerPollRequest){
		func(r *WorkerPollRequest) { r.Capacity = 0 }, func(r *WorkerPollRequest) { r.Capacity = 101 }, func(r *WorkerPollRequest) { r.Limit = r.Capacity + 1 }, func(r *WorkerPollRequest) { r.WaitSeconds = 26 }, func(r *WorkerPollRequest) { r.PollSequence = math.MaxInt64 + 1 }, func(r *WorkerPollRequest) { r.Capabilities = nil }, func(r *WorkerPollRequest) { r.Capabilities = append(r.Capabilities, r.Capabilities[0]) },
	} {
		bad := r.clone()
		edit(&bad)
		if _, err := s.CommitWorkerPoll(t.Context(), a, bad); !errors.Is(err, ErrWorkerSessionInvalid) {
			t.Fatal("invalid shape", err)
		}
	}
	c := WorkerSessionCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: r, ProposedSessionID: uuid.NewString()}
	command := Command{Kind: "worker_poll", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerSession: &c}}
	for version := 1; version <= WorkerSessionFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerSessionFormatVersion || version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
			t.Fatal("format gate", version, err)
		}
	}
	if _, err := operationDigestEncoding(command); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operationDigest(command); !errors.Is(err, ErrOperationReservation) {
		t.Fatal("poll entered legacy digest", err)
	}
	first, err := s.CommitWorkerPoll(t.Context(), a, r)
	if err != nil {
		t.Fatal(err)
	}
	if s.fsm.image.Version != WorkerOfferFormatVersion {
		t.Fatal("format not advanced")
	}
	clone := cloneImageExtensions(s.fsm.image)
	clone.WorkerSessions.Sessions[a.WorkerUID] = workerSessionRecord{}
	if !reflect.DeepEqual(s.fsm.image.WorkerSessions.Sessions[a.WorkerUID].Response, first) {
		t.Fatal("snapshot alias")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if !reflect.DeepEqual(i.WorkerSessions, s.fsm.image.WorkerSessions) {
		t.Fatal("snapshot lost response")
	}
	i.Version = CatalogJobTypeFormatVersion
	if validateWorkerSessionImage(i) == nil {
		t.Fatal("old snapshot format accepted sessions")
	}
	full := s.fsm.image.WorkerSessions.EncodedBytes
	s.fsm.image.WorkerSessions.EncodedBytes = MaxWorkerSessionBytes
	r.SessionID = first.SessionID
	r.PollSequence = 2
	r.Capacity = 100
	r.Limit = 100
	result := sessionApply(t, s, a, r, time.Now().UTC())
	if !errors.Is(result.Err, ErrWorkerSessionQuota) {
		t.Fatal("encoded quota", result.Err)
	}
	s.fsm.image.WorkerSessions.EncodedBytes = full
}
func TestWorkerSessionConcurrentSameInitialPoll(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	var wg sync.WaitGroup
	responses := make(chan WorkerPollResponse, 8)
	failures := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.CommitWorkerPoll(t.Context(), a, r)
			if err != nil {
				failures <- err
			} else {
				responses <- v
			}
		}()
	}
	wg.Wait()
	close(responses)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var original WorkerPollResponse
	for v := range responses {
		if original.SessionID == "" {
			original = v
		}
		if !reflect.DeepEqual(original, v) {
			t.Fatal("concurrent replay allocated another response")
		}
	}
	if len(s.fsm.image.WorkerSessions.Sessions) != 1 {
		t.Fatal("more than one session")
	}
}

func TestWorkerSessionSnapshotRejectsCorruption(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	if _, err := s.CommitWorkerPoll(t.Context(), a, r); err != nil {
		t.Fatal(err)
	}
	original := s.fsm.image
	for name, edit := range map[string]func(*image, *workerSessionRecord){
		"request_body":   func(_ *image, r *workerSessionRecord) { r.Request.Limit-- },
		"request_digest": func(_ *image, r *workerSessionRecord) { r.RequestDigest = strings.Repeat("0", 64) },
		"worker_uid":     func(_ *image, r *workerSessionRecord) { r.Response.WorkerUID = "other" },
		"client_nonce":   func(_ *image, r *workerSessionRecord) { r.Response.ClientSessionID = "other" },
		"session_id":     func(_ *image, r *workerSessionRecord) { r.Response.SessionID = "" },
		"sequence":       func(_ *image, r *workerSessionRecord) { r.Response.PollSequence++ },
		"server_id":      func(_ *image, r *workerSessionRecord) { r.Response.ServerID = "other" },
		"expiry": func(_ *image, r *workerSessionRecord) {
			r.Response.SessionExpiresAt = r.Response.SessionExpiresAt.Add(time.Nanosecond)
		},
		"opened_year": func(_ *image, r *workerSessionRecord) { r.OpenedAt = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) },
		"format":      func(i *image, _ *workerSessionRecord) { i.Version = CatalogJobTypeFormatVersion },
		"count": func(i *image, _ *workerSessionRecord) {
			for n := 0; n <= MaxWorkerSessions; n++ {
				i.WorkerSessions.Sessions[uuid.NewString()] = workerSessionRecord{}
			}
		},
		"namespace_version": func(i *image, _ *workerSessionRecord) { i.WorkerSessions.Version++ },
		"policy_epoch":      func(i *image, _ *workerSessionRecord) { i.WorkerSessions.PolicyEpoch = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			i := original
			i.imageExtensions = cloneImageExtensions(original)
			record := i.WorkerSessions.Sessions[a.WorkerUID]
			edit(&i, &record)
			i.WorkerSessions.Sessions[a.WorkerUID] = record
			// Recompute accounting so corruption is rejected for its identity/body,
			// rather than relying on an incidental change in encoded length.
			i.WorkerSessions.EncodedBytes = workerSessionImageOverhead
			for key, value := range i.WorkerSessions.Sessions {
				cost, err := workerSessionRecordCost(key, value)
				if err != nil {
					t.Fatal(err)
				}
				i.WorkerSessions.EncodedBytes += cost
			}
			raw, err := json.Marshal(i)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = decodeImage(bytes.NewReader(raw)); err == nil {
				t.Fatal("corrupt snapshot accepted")
			}
		})
	}
	i := original
	i.imageExtensions = cloneImageExtensions(original)
	i.WorkerSessions.EncodedBytes++
	if validateWorkerSessionImage(i) == nil {
		t.Fatal("false encoded count accepted")
	}
	// Captured snapshots own all nested capability storage.
	i = original
	i.imageExtensions = cloneImageExtensions(original)
	row := i.WorkerSessions.Sessions[a.WorkerUID]
	row.Request.Capabilities[0].Version = "altered"
	i.WorkerSessions.Sessions[a.WorkerUID] = row
	if original.WorkerSessions.Sessions[a.WorkerUID].Request.Capabilities[0].Version == "altered" {
		t.Fatal("snapshot capability alias")
	}
}

func TestWorkerSessionAccountingBoundsCanonicalEncoding(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	if _, err := s.CommitWorkerPoll(t.Context(), a, r); err != nil {
		t.Fatal(err)
	}
	state := s.fsm.image.WorkerSessions.clone()
	state.NodeID = strings.Repeat("\x01", 256)
	state.PolicyEpoch = strings.Repeat("<", 128)
	if !catalogIdentifier(state.NodeID, 256) || !validAuthenticationID(state.PolicyEpoch) {
		t.Fatal("fixture does not use accepted identity bounds")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) > state.EncodedBytes {
		t.Fatalf("encoded %d exceeds charge %d", len(raw), state.EncodedBytes)
	}
}

func TestWorkerSessionRejectsForgedMixedCommand(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	c := WorkerSessionCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: r, ProposedSessionID: uuid.NewString()}
	for _, command := range []Command{
		{Kind: "observe", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerSession: &c}},
		{Kind: "worker_poll", At: time.Now().UTC(), MonitorID: "unexpected", commandExtensions: commandExtensions{WorkerSession: &c}},
		{Kind: "worker_poll", At: time.Now().UTC()},
	} {
		for _, version := range []int{14, 15, 16, 17, 18, 19} {
			raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = decodeEnvelope(raw); err == nil {
				t.Fatal("mixed command accepted", version, command.Kind)
			}
		}
	}
}

func TestWorkerSessionExactReplayIgnoresSampleOrder(t *testing.T) {
	s, a, r, _ := workerSessionFixture(t)
	at := time.Now().UTC().Add(time.Second)
	first := sessionApply(t, s, a, r, at)
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	before := s.fsm.image.WorkerSessions.clone()
	replay := sessionApply(t, s, a, r, at.Add(-time.Millisecond))
	if replay.Err != nil || replay.WorkerPoll == nil || !reflect.DeepEqual(*replay.WorkerPoll, *first.WorkerPoll) || !reflect.DeepEqual(before, s.fsm.image.WorkerSessions) {
		t.Fatal("concurrent exact retry depended on sampled timestamp order", replay.Err)
	}
	r.SessionID = first.WorkerPoll.SessionID
	r.PollSequence++
	if next := sessionApply(t, s, a, r, at.Add(-time.Millisecond)); !errors.Is(next.Err, ErrWorkerSessionConflict) {
		t.Fatal("new poll regressed time", next.Err)
	}
}

func TestWorkerSessionFrozenPollDigest(t *testing.T) {
	current, frozen := reflect.TypeFor[WorkerPollRequest](), reflect.TypeFor[workerPollDigestV1]()
	if current.NumField() != frozen.NumField() {
		t.Fatal("poll fields changed without versioned digest review")
	}
	for n := 0; n < current.NumField(); n++ {
		field := current.Field(n)
		old, ok := frozen.FieldByName(field.Name)
		if !ok || old.Type != field.Type || old.Tag != field.Tag {
			t.Fatal("poll digest projection changed", field.Name)
		}
	}
	capability := reflect.TypeFor[WorkerSessionCapability]()
	if capability.NumField() != 3 {
		t.Fatal("nested capability digest fields need versioned projection")
	}
	for n, expected := range []string{"JobTypeID", "Version", "Category"} {
		f := capability.Field(n)
		if f.Name != expected || f.Type.Kind() != reflect.String {
			t.Fatal("capability projection changed")
		}
	}
	r := WorkerPollRequest{ServerID: "server", WorkerID: "worker", ClientSessionID: "run", SessionID: "session", PollSequence: 7, Capabilities: []WorkerSessionCapability{{JobTypeID: "type", Version: "v1", Category: "check"}}, Capacity: 16, Limit: 10, WaitSeconds: 25}
	const wire = `{"server_id":"server","worker_id":"worker","client_session_id":"run","session_id":"session","poll_sequence":7,"capabilities":[{"job_type_id":"type","version":"v1","category":"check"}],"capacity":16,"limit":10,"wait_seconds":25}`
	if workerPollDigest(r) != identity("cpra/worker/poll/v1\x00"+wire) {
		t.Fatal("frozen poll encoding changed")
	}
}
