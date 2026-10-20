//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func inlineNotificationFixture(t *testing.T, s *Store) (WorkerExecutionIntent, JobTypeState) {
	t.Helper()
	command := jobTypeFixture(t, s, "inline-notification")
	command.Value.Category = "notification"
	job := commitJobType(t, s, command)
	return workerExecutionSourceFixture(t, s, job, true), job
}

func TestWorkerNotificationSourceAdmissionAndFormat(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := inlineNotificationFixture(t, s)
	for name, change := range map[string]func(*WorkerExecutionIntent){
		"other-monitor":     func(i *WorkerExecutionIntent) { i.Source.ID = "other-monitor" },
		"other-incarnation": func(i *WorkerExecutionIntent) { i.SourceUID = "other-incarnation" },
		"endpoint-alias":    func(i *WorkerExecutionIntent) { i.Source.Kind = "NotificationEndpoint" },
		"other-action":      func(i *WorkerExecutionIntent) { i.ActionID = "other-action" },
		"other-revision":    func(i *WorkerExecutionIntent) { i.SourceRevision = "other-revision" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in.Clone()
			change(&bad)
			if _, err := s.CommitWorkerExecution(t.Context(), bad); err == nil {
				t.Fatal("changed notification source admitted")
			}
		})
	}
	record, err := s.CommitWorkerExecution(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.CommitWorkerExecution(t.Context(), in)
	if err != nil || !reflect.DeepEqual(record, repeated) {
		t.Fatal("admission replay changed the original notification", err)
	}
	if s.fsm.image.Version != ActionWorkerFormatVersion {
		t.Fatal("inline source did not advance the storage format")
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	sink := &collectionTestSink{}
	if err = snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(actionWorkerSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("private-execution-parameters-canary")) {
		t.Fatal("snapshot format or secrecy changed")
	}
	saved, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if !reflect.DeepEqual(saved.WorkerExecutions.Records[in.ID], record) {
		t.Fatal("snapshot changed the execution source")
	}
	saved.Version = WorkerCheckRetentionFormatVersion
	if validateWorkerExecutionImage(saved) == nil {
		t.Fatal("older snapshot format admitted inline notification policy")
	}
	monitor := saved.Monitors[in.MonitorID]
	monitor.Policy.WorkerNotificationSources = nil
	saved.Monitors[in.MonitorID] = monitor
	if validateWorkerExecutionImage(saved) == nil {
		t.Fatal("older snapshot format admitted a retained inline execution")
	}
	command := Command{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession}}}
	for version := WorkerExecutionFormatVersion; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion) {
			t.Fatal("inline command format", version, err)
		}
	}
}

func TestWorkerNotificationGrantsSeparateMonitorAndEndpoint(t *testing.T) {
	for _, kind := range []string{"Monitor", "NotificationEndpoint"} {
		t.Run(kind, func(t *testing.T) {
			s := openWorkerMemory(t)
			inline, job := inlineNotificationFixture(t, s)
			endpoint := workerExecutionSourceFixture(t, s, job, false)
			for _, in := range []WorkerExecutionIntent{inline, endpoint} {
				if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
					t.Fatal(err)
				}
			}
			s.administrative = true
			p := workerPrincipal("notification-worker")
			p.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: "notification", ResourceKind: kind, ResourceIDs: []string{"*"}}}
			cmd := workerCommand(t, s, "bootstrap", p)
			policy := commitWorker(t, s, cmd)
			s.administrative = false
			if wait := time.Until(policy.UpdatedAt); wait > 0 {
				time.Sleep(wait + time.Millisecond)
			}
			authority, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			identity, err := s.WorkerProtocolIdentity(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			poll := WorkerPollRequest{ServerID: identity.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 10, Limit: 10, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: "notification"}}}
			response, err := s.CommitWorkerPoll(t.Context(), authority, poll)
			want, denied := endpoint, inline
			if kind == "Monitor" {
				want, denied = inline, endpoint
			}
			if err != nil || len(response.Offers) != 1 || response.Offers[0].ExecutionID != want.ID {
				t.Fatal("grant crossed configuration kinds", err)
			}
			scope := WorkerScope{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: "notification", ResourceKind: denied.Source.Kind, ResourceID: denied.Source.ID}
			if err = s.CheckWorkerAuthority(t.Context(), authority, scope, time.Now().UTC()); !errors.Is(err, ErrWorkerAuthorityDenied) {
				t.Fatal("wildcard grant crossed configuration kinds", err)
			}
			started, err := s.CommitWorkerStart(t.Context(), authority, workerStartFromOffer(response))
			if err != nil || started.Disposition != "granted" {
				t.Fatal("matching notification failed to start", err)
			}
			monitor, _ := s.Get(want.MonitorID)
			if monitor.Actions[want.ActionID].State != Started {
				t.Fatal("start was not committed to original incident action")
			}
			if kind == "Monitor" {
				old := s.fsm.image
				old.Version = WorkerCheckRetentionFormatVersion
				if validateWorkerPolicyImage(old) == nil {
					t.Fatal("older snapshot admitted monitor notification grant")
				}
				wire := Command{Kind: "worker_policy", At: cmd.At, commandExtensions: commandExtensions{WorkerPolicy: &cmd}}
				if commandWriteFormat(wire) != WorkerNotificationFormatVersion {
					t.Fatal("grant failed to stamp the required format")
				}
			}
		})
	}
}

func TestWorkerNotificationPolicyKindsAndEndpointEncoding(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := inlineNotificationFixture(t, s)
	m, _ := s.Get(in.MonitorID)
	command := Command{Kind: "configure", Config: &m}
	if commandWriteFormat(command) != ActionWorkerFormatVersion {
		t.Fatal("configuration failed to stamp inline source format")
	}
	for _, kind := range []string{"Credential", "JobType", "monitor", "*"} {
		p := m.Policy.Clone()
		p.WorkerNotificationSources["red"][0].Kind = kind
		if validatePolicyExtensions(p) == nil {
			t.Fatal("unsupported source kind accepted", kind)
		}
	}
	raw, err := json.Marshal(WorkerNotificationSource{ID: "endpoint", UID: "uid", Revision: "revision"})
	if err != nil || string(raw) != `{"id":"endpoint","uid":"uid","revision":"revision"}` {
		t.Fatal("existing endpoint wire identity changed", err)
	}
	for _, kind := range []string{`null`, `""`, `"Credential"`} {
		var source WorkerNotificationSource
		if err := json.Unmarshal([]byte(`{"kind":`+kind+`,"id":"endpoint","uid":"uid","revision":"revision"}`), &source); err == nil {
			t.Fatal("explicit invalid kind collapsed into an endpoint", kind)
		}
	}
}
