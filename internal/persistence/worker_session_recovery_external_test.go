//go:build externaljobs

package persistence

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerSessionNativeRestartAndRestore(t *testing.T) {
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.WorkerProtocolIdentity(t.Context()); !errors.Is(err, ErrWorkerPolicyUnavailable) {
		t.Fatal("unprovisioned identity", err)
	}
	if _, err := admin.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	live, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	job := commitJobType(t, live, jobTypeFixture(t, live, "native-worker-check"))
	if err = live.Close(); err != nil {
		t.Fatal(err)
	}
	provisioner := openAuthenticationAdmin(t, config)
	p := workerPrincipal("native-worker")
	p.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: job.Current.Category, ResourceKind: "Monitor", ResourceIDs: []string{"one"}}}
	commitWorker(t, provisioner, workerCommand(t, provisioner, "bootstrap", p))
	identity, err := provisioner.WorkerProtocolIdentity(t.Context())
	if err != nil || identity.ServerID == "" || identity.OwnerEpoch != "" {
		t.Fatal("administrative identity", err)
	}
	if err = provisioner.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	a, err := first.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerPollRequest{ServerID: identity.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: job.Current.Category}}, Capacity: 1, Limit: 1}
	reply, err := first.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	saved := first.fsm.image.WorkerSessions.clone()
	if err = first.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	observer := openAuthenticationAdmin(t, config)
	if !reflect.DeepEqual(saved, observer.fsm.image.WorkerSessions) {
		t.Fatal("native replay changed retained response")
	}
	read, err := observer.WorkerProtocolIdentity(t.Context())
	if err != nil || read != identity {
		t.Fatal("identity changed on administrative reopen", err)
	}
	if _, err = observer.CommitWorkerPoll(t.Context(), a, request); !errors.Is(err, ErrWorkerSessionUnavailable) {
		t.Fatal("stopped administration opened a session", err)
	}
	if err = observer.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	newIdentity, err := restarted.WorkerProtocolIdentity(t.Context())
	if err != nil || newIdentity.ServerID != identity.ServerID || newIdentity.OwnerEpoch == saved.Sessions[a.WorkerUID].OwnerEpoch {
		t.Fatal("restart ownership", err)
	}
	a, err = restarted.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.CommitWorkerPoll(t.Context(), a, request); !errors.Is(err, ErrWorkerSessionExpired) {
		t.Fatal("lost original reply reopened after server restart", err)
	}
	continued := request.clone()
	continued.SessionID = reply.SessionID
	continued.PollSequence = 2
	if _, err = restarted.CommitWorkerPoll(t.Context(), a, continued); !errors.Is(err, ErrWorkerSessionExpired) {
		t.Fatal("old offered session survived restart", err)
	}
	request.ClientSessionID = uuid.NewString()
	if _, err = restarted.CommitWorkerPoll(t.Context(), a, request); err != nil {
		t.Fatal("fresh run blocked by old owner", err)
	}
	if err = restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if err = MarkRestored(config.Storage.Directory, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	restored := openAuthenticationAdmin(t, config)
	if restored.fsm.image.WorkerSessions != nil {
		t.Fatal("restore retained worker sessions")
	}
	after, err := restored.WorkerProtocolIdentity(t.Context())
	if err != nil || after.ServerID == identity.ServerID || after.OwnerEpoch != "" {
		t.Fatal("restore-qualified protocol identity", err)
	}
	if _, err = restored.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC()); !errors.Is(err, ErrWorkerPolicyUnavailable) {
		t.Fatal("restored credential admitted", err)
	}
}
