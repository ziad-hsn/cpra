//go:build externaljobs

package persistence

import (
	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"testing"
	"time"
)

func structuredRuleWorker(t *testing.T) (*Store, WorkerAuthority, WorkerPollRequest, WorkerExecutionIntent) {
	t.Helper()
	s := openWorkerMemory(t)
	create := jobTypeFixture(t, s, "structured-probe")
	create.Value.ProtocolVersion = "2"
	create.Value.FailureCodes = []string{"custom.cache.full"}
	job := commitJobType(t, s, create)
	monitor := testMonitor()
	monitor.ID = uuid.NewString()
	monitor.Revision = identity("structured-rule-configuration/" + monitor.ID)
	monitor.Policy.Unhealthy, monitor.Policy.Intervention = 1, true
	var err error
	monitor.Policy.RecoveryRules, err = NewExternalRecoveryRules(job.Current.FailureContract(), "1", []api.RulePredicate{{ID: "cache", Match: api.FailureMatch{Codes: []string{"custom.cache.full"}}}}, []RecoveryRuleTarget{{RuleID: "cache", Driver: "webhook", Binding: identity("cache-target"), Budget: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	source := createCatalog(t, s, catalogJobTypeRecord(t, s, monitor.ID, job.Current))
	guard := CatalogGuard{Conditions: catalogConditions(t, s, []CatalogKey{source.Key})}
	now := time.Now().UTC()
	result := applyCatalogFormat(t, s, WorkerRecoveryRulesFormatVersion, Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, Guard: &guard, At: now})[0]
	if result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	monitor, _ = s.Get(monitor.ID)
	in := WorkerExecutionIntent{ID: uuid.NewString(), Revision: uuid.NewString(), MonitorID: monitor.ID, MonitorUID: monitor.CatalogUID, MonitorRevision: monitor.Revision, ControlRevision: monitor.ControlRevision, Category: "check", Source: source.Key, SourceUID: source.UID, SourceRevision: source.Revision, JobType: catalogJobTypeRef(job.Current), Guard: guard, Scheduled: now, Deadline: now.Add(time.Minute), Generation: 1}
	in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-parameters"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	s.administrative = true
	p := workerPrincipal("structured-worker")
	p.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: "check", ResourceKind: source.Key.Kind, ResourceIDs: []string{source.Key.ID}}}
	policy := commitWorker(t, s, workerCommand(t, s, "bootstrap", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	auth, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	poll := WorkerPollRequest{ServerID: server.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: "check"}}}
	return s, auth, poll, in
}

func TestStructuredWorkerResultQualifiesOnlyOriginalDeclaredContract(t *testing.T) {
	s, authority, poll, in := structuredRuleWorker(t)
	old, err := s.CommitWorkerPoll(t.Context(), authority, poll)
	if err != nil || len(old.Offers) != 0 {
		t.Fatal("protocol1 received protocol2 assignment", err)
	}
	// Capability membership is immutable within a live worker session. A
	// separate configured worker starts its own protocol-2 session.
	s, authority, poll, in = structuredRuleWorker(t)
	poll.Capabilities[0].ProtocolVersion = "2"
	offered, err := s.CommitWorkerPoll(t.Context(), authority, poll)
	if err != nil || len(offered.Offers) != 1 {
		t.Fatal("protocol2 offer", err)
	}
	request := workerStartFromOffer(offered)
	grant, err := s.CommitWorkerStart(t.Context(), authority, request)
	if err != nil || grant.Disposition != "granted" {
		t.Fatal(grant, err)
	}
	again, err := s.CommitWorkerStart(t.Context(), authority, request)
	if err != nil || again.Disposition != "started" || again.Deadline != grant.Deadline {
		t.Fatal("repeat start", again, err)
	}
	payload, err := catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution", ID: in.ID, UID: in.MonitorUID, Revision: in.Revision, Purpose: "worker-outcome-test"}, []byte("structured-result"))
	if err != nil {
		t.Fatal(err)
	}
	input := WorkerResultInput{ServerID: grant.ServerID, ExecutionID: in.ID, ExecutionRevision: in.Revision, GrantID: grant.GrantID, Status: "failure", Digest: identity("structured-result"), Payload: payload}
	typ, _, _ := s.JobType(t.Context(), in.JobType.JobTypeID)
	report, err := api.CanonicalWorkerFailure(typ.Current.FailureContract(), api.WorkerFailure{Code: "custom.cache.full", Phase: "check"})
	if err != nil {
		t.Fatal(err)
	}
	input.Failure = CheckFailure(report)
	// The execution remains bound to v1 even when the operator publishes a
	// different vocabulary. Neither the current contract nor a worker's report
	// can reinterpret an already granted observation.
	next := jobTypeChange(t, s, typ, "v2")
	next.Value.FailureCodes = []string{"custom.cache.evicted"}
	commitJobType(t, s, next)
	bad := input
	bad.Failure.Code = "custom.cache.evicted"
	if _, err = s.CommitWorkerResult(t.Context(), authority, bad); err == nil {
		t.Fatal("new contract reinterpreted the original execution")
	}
	receipt, err := s.CommitWorkerResult(t.Context(), authority, input)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(in.MonitorID)
	if m.RecoveryDecision.RuleID != "cache" || m.RecoveryDecision.Count != 1 {
		t.Fatal("rule not qualified", m.RecoveryDecision)
	}
	action := queuedRuleAction(t, m)
	if action.Rule.RuleID != "cache" || action.State != Queued {
		t.Fatal("incorrect action", action)
	}
	replay, err := s.CommitWorkerResult(t.Context(), authority, input)
	if err != nil || replay != receipt {
		t.Fatal("lost receipt replay", err)
	}
	after, _ := s.Get(in.MonitorID)
	if after.RecoveryDecision != m.RecoveryDecision || after.recoveryAttempts() != 1 {
		t.Fatal("duplicate observation or action")
	}
	if err = validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal("snapshot contract", err)
	}
}
