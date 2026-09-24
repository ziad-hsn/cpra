//go:build externaljobs

package localadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type workerAuthFixture struct {
	state         persistence.WorkerPolicyState
	authority     persistence.OperatorAuthority
	commands      []persistence.WorkerPolicyCommand
	commitErr     error
	commitDespite bool
	authorityErr  error
	closeErr      error
	beforeRead    func()
	beforeCommit  func(persistence.WorkerPolicyCommand)
	opened        int
	closed        int
	observed      int
	observedAt    time.Time
	identityErr   error
}

func newWorkerAuthFixture() *workerAuthFixture {
	return &workerAuthFixture{authority: persistence.OperatorAuthority{Actor: "operator", Epoch: uuid.NewString(), Revision: uuid.NewString()}}
}

func (f *workerAuthFixture) ObserveOperatorAuthority(_ context.Context, actor string, at time.Time) (persistence.OperatorAuthority, error) {
	f.observed++
	f.observedAt = at
	if f.beforeRead != nil {
		f.beforeRead()
	}
	if actor != f.authority.Actor {
		return persistence.OperatorAuthority{}, persistence.ErrOperatorAuthorityDenied
	}
	return f.authority, f.authorityErr
}

func (f *workerAuthFixture) WorkerPolicy(context.Context) (persistence.WorkerPolicyState, error) {
	return f.state.Clone(), nil
}

func (f *workerAuthFixture) WorkerProtocolIdentity(context.Context) (persistence.WorkerServerIdentity, error) {
	return persistence.WorkerServerIdentity{ServerID: "cpra-worker-fixture:" + f.state.Epoch}, f.identityErr
}

func TestWorkerAuthenticationIdentityReadPreservesCommittedReport(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	fixture.identityErr = errors.New("identity-read-canary")
	report, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if err == nil || report.Outcome != "committed" || report.Revision != report.IntendedRevision || report.TokenOutput != request.TokenOutput || len(fixture.commands) != 1 {
		t.Fatal("identity read failure hid a successful credential commit", err)
	}
	fixture.identityErr = nil
	listed, err := administerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: request.DataDirectory, Actor: request.Actor}, fixture.open)
	if err != nil || listed.Revision != report.Revision || listed.ProtocolServerID == "" || len(fixture.commands) != 1 {
		t.Fatal("inspection did not recover the original committed identity", err)
	}
}

func (f *workerAuthFixture) CommitWorkerPolicy(_ context.Context, command persistence.WorkerPolicyCommand) (persistence.WorkerPolicyState, error) {
	f.commands = append(f.commands, command)
	if f.beforeCommit != nil {
		f.beforeCommit(command)
	}
	if err := command.Validate(); err != nil {
		return persistence.WorkerPolicyState{}, err
	}
	if f.commitErr == nil || f.commitDespite {
		if f.state.Workers == nil {
			f.state.Workers = make(map[string]persistence.WorkerPrincipal)
		}
		f.state.Version = persistence.WorkerPolicyVersion
		f.state.Epoch, f.state.Revision, f.state.UpdatedAt = command.Epoch, command.Revision, command.At
		f.state.Workers[command.Worker.ID] = command.Worker.Clone()
		if command.Mode == "provision" {
			f.state.ResetRequired = false
		}
	}
	return f.state.Clone(), f.commitErr
}

func (f *workerAuthFixture) Close() error { f.closed++; return f.closeErr }
func (f *workerAuthFixture) open(_ context.Context, config runtimeconfig.Config) (workerAuthenticationStore, error) {
	f.opened++
	if config.Storage.Mode != "raft" || !filepath.IsAbs(config.Storage.Directory) {
		return nil, errors.New("invalid stopped store configuration")
	}
	return f, nil
}

func workerGrantFixture() []persistence.WorkerGrant {
	return []persistence.WorkerGrant{{JobTypeID: "http-probe", JobTypeUID: "type-incarnation", Version: "v1", Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"service-a"}}}
}

func writeWorkerGrantFile(t *testing.T, state, name string, grants []persistence.WorkerGrant) string {
	t.Helper()
	directory := filepath.Join(filepath.Dir(state), "grants")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	raw, err := json.Marshal(struct {
		Grants []persistence.WorkerGrant `json:"grants"`
	}{Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func workerIssueFixture(t *testing.T) (*workerAuthFixture, WorkerAuthenticationRequest) {
	t.Helper()
	state, output := authTestPaths(t)
	return newWorkerAuthFixture(), WorkerAuthenticationRequest{Action: "issue", DataDirectory: state, Actor: "operator", WorkerID: "worker-a", GrantsFile: writeWorkerGrantFile(t, state, "initial.json", workerGrantFixture()), TokenOutput: output}
}

func TestWorkerAuthenticationLifecycleAndSafeReports(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	expiry := time.Now().UTC().Add(time.Hour)
	request.ExpiresAt = &expiry
	fixture.beforeCommit = func(c persistence.WorkerPolicyCommand) {
		assertAuthToken(t, request.TokenOutput, c.Worker.TokenSHA256)
	}
	issued, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if err != nil || issued.Outcome != "committed" || issued.Revision != issued.IntendedRevision || len(issued.Workers) != 1 || issued.ProtocolServerID == "" {
		t.Fatal("worker issuance failed", err)
	}
	first := fixture.state.Workers[request.WorkerID].Clone()
	firstToken := assertAuthToken(t, request.TokenOutput, first.TokenSHA256)
	defer clear(firstToken)
	if fixture.commands[0].Mode != "bootstrap" || fixture.commands[0].Authority != fixture.authority || fixture.commands[0].Actor != request.Actor {
		t.Fatal("issuance lost committed operator authority or explicit audit identity")
	}
	request.Action, request.GrantsFile, request.ExpiresAt = "rotate", "", nil
	request.TokenOutput = filepath.Join(filepath.Dir(request.TokenOutput), "rotated.token")
	rotated, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	second := fixture.state.Workers[request.WorkerID].Clone()
	if err != nil || rotated.ProtocolServerID != issued.ProtocolServerID || second.UID != first.UID || second.CredentialRevision == first.CredentialRevision || second.TokenSHA256 == first.TokenSHA256 || second.GrantRevision != first.GrantRevision || !reflect.DeepEqual(second.Grants, first.Grants) || !second.ExpiresAt.Equal(expiry) {
		t.Fatal("rotation failed to preserve identity, grants and expiry", err)
	}
	fixture.beforeCommit = nil
	request.Action, request.TokenOutput = "set-grants", ""
	request.GrantsFile = writeWorkerGrantFile(t, request.DataDirectory, "none.json", []persistence.WorkerGrant{})
	changed, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	third := fixture.state.Workers[request.WorkerID].Clone()
	if err != nil || len(third.Grants) != 0 || third.GrantRevision == second.GrantRevision || third.CredentialRevision != second.CredentialRevision || third.TokenSHA256 != second.TokenSHA256 {
		t.Fatal("grant replacement changed credentials or retained old scope", err)
	}
	unchanged, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if err != nil || unchanged.Workers[0].GrantRevision != third.GrantRevision {
		t.Fatal("identical empty grants changed the grant revision", err)
	}
	request.Action, request.GrantsFile = "revoke", ""
	revoked, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if err != nil || !revoked.Workers[0].Revoked || revoked.Workers[0].CredentialRevision == third.CredentialRevision || revoked.Workers[0].GrantRevision != third.GrantRevision {
		t.Fatal("revocation did not permanently remove eligibility", err)
	}
	listed, err := administerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: request.DataDirectory, Actor: request.Actor}, fixture.open)
	if err != nil || listed.Outcome != "observed" || listed.Revision != revoked.Revision || len(fixture.commands) != 5 || fixture.closed != 6 {
		t.Fatal("inspection mutated policy or leaked store ownership", err)
	}
	raw, _ := json.Marshal([]WorkerAuthenticationReport{issued, rotated, changed, unchanged, revoked, listed})
	if bytes.Contains(raw, firstToken) || bytes.Contains(raw, []byte(first.TokenSHA256)) || bytes.Contains(raw, []byte(second.TokenSHA256)) || bytes.Contains(raw, []byte("token_sha256")) {
		t.Fatal("safe reports disclosed worker token material")
	}
	for _, c := range fixture.commands {
		raw, _ := json.Marshal(c)
		if bytes.Contains(raw, firstToken) {
			t.Fatal("raw bearer entered a durable command")
		}
	}
}

func TestWorkerAuthenticationUnconfirmedCommitKeepsOutputWithoutRetry(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	const canary = "private-storage-path-or-payload"
	fixture.commitErr, fixture.commitDespite = fmt.Errorf("%w: %s", persistence.ErrCommitUnconfirmed, canary), true
	report, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if !errors.Is(err, persistence.ErrCommitUnconfirmed) || strings.Contains(err.Error(), canary) || report.Outcome != "unconfirmed" || report.IntendedRevision != fixture.state.Revision || report.TokenOutput != request.TokenOutput || len(fixture.commands) != 1 {
		t.Fatal("unconfirmed commit lost original identity, retried or leaked its cause", err)
	}
	token := assertAuthToken(t, request.TokenOutput, fixture.state.Workers[request.WorkerID].TokenSHA256)
	defer clear(token)
	if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); err == nil || fixture.opened != 1 || len(fixture.commands) != 1 {
		t.Fatal("repeated issuance replaced or retried the original token")
	}
	listed, err := administerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: request.DataDirectory, Actor: request.Actor}, fixture.open)
	if err != nil || listed.Revision != report.IntendedRevision {
		t.Fatal("read-only reconciliation lost the original intended revision", err)
	}
}

func TestWorkerAuthenticationInputAndAuthorityFailBeforePublication(t *testing.T) {
	_, original := workerIssueFixture(t)
	for _, edit := range []func(*WorkerAuthenticationRequest){
		func(r *WorkerAuthenticationRequest) { r.Actor = "" },
		func(r *WorkerAuthenticationRequest) { r.Actor = "legacy-read" },
		func(r *WorkerAuthenticationRequest) { r.WorkerID = "bad identity" },
		func(r *WorkerAuthenticationRequest) { r.GrantsFile = "" },
		func(r *WorkerAuthenticationRequest) { r.GrantsFile = "relative.json" },
		func(r *WorkerAuthenticationRequest) { r.TokenOutput = "-" },
		func(r *WorkerAuthenticationRequest) { r.Action = "bootstrap" },
		func(r *WorkerAuthenticationRequest) { r.Action = "revoke" },
		func(r *WorkerAuthenticationRequest) { r.DataDirectory = "relative" },
	} {
		request, fixture := original, newWorkerAuthFixture()
		edit(&request)
		if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); !errors.Is(err, ErrWorkerAuthenticationInput) || fixture.opened != 0 {
			t.Fatal("invalid input opened administrative state", err)
		}
	}
	fixture := newWorkerAuthFixture()
	fixture.authorityErr = fmt.Errorf("%w: secret-authority-cause", persistence.ErrOperatorAuthorityDenied)
	if _, err := administerWorkerAuthentication(t.Context(), original, fixture.open); !errors.Is(err, persistence.ErrOperatorAuthorityDenied) || strings.Contains(err.Error(), "secret-authority-cause") || len(fixture.commands) != 0 || fixture.closed != 1 {
		t.Fatal("missing current operator authority reached a mutation or leaked error", err)
	}
	if _, err := os.Stat(original.TokenOutput); !os.IsNotExist(err) {
		t.Fatal("rejected input or authority published a worker token")
	}
}

func TestWorkerAuthenticationStrictBoundedGrantFile(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": `{}`, "null": `{"grants":null}`, "case": `{"Grants":[]}`,
		"duplicate": `{"grants":[],"grants":[]}`, "case-shadow": `{"grants":[],"Grants":[]}`,
		"unknown": `{"grants":[],"private-secret":"never-print"}`, "trailing": `{"grants":[]} {}`,
		"invalid-utf8": "{\"grants\":[],\"bad\":\"\xff\"}", "oversized": strings.Repeat(" ", maxWorkerGrantFileBytes+1),
		"null-entry": `{"grants":[null]}`, "unknown-entry": `{"grants":[{"private-secret":"never-print"}]}`,
		"duplicate-entry-field":  `{"grants":[{"job_type_id":"a","job_type_id":"b"}]}`,
		"too-many-small-entries": `{"grants":[` + strings.Repeat(`{},`, persistence.MaxWorkerGrants) + `{ }]}`,
		"too-many-resource-ids":  `{"grants":[{"resource_ids":[` + strings.Repeat(`"a",`, persistence.MaxWorkerGrantResources) + `"b"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture, request := workerIssueFixture(t)
			if err := os.WriteFile(request.GrantsFile, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
			if err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "never-print") || len(fixture.commands) != 0 {
				t.Fatal("invalid grants reached a commit or leaked input", err)
			}
			if _, err := os.Stat(request.TokenOutput); !os.IsNotExist(err) {
				t.Fatal("invalid grants published a token")
			}
		})
	}
	for _, grants := range [][]persistence.WorkerGrant{
		append(workerGrantFixture(), workerGrantFixture()...),
		{{JobTypeID: "a", JobTypeUID: "a", Version: "v1", Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"*", "another"}}},
		{{JobTypeID: "a", JobTypeUID: "a", Version: "v1", Category: "check", ResourceKind: "NotificationEndpoint", ResourceIDs: []string{"a"}}},
	} {
		fixture, request := workerIssueFixture(t)
		request.GrantsFile = writeWorkerGrantFile(t, request.DataDirectory, "invalid.json", grants)
		if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); err == nil || len(fixture.commands) != 0 {
			t.Fatal("invalid scope shape bypassed storage validation", err)
		}
		if _, err := os.Stat(request.TokenOutput); !os.IsNotExist(err) {
			t.Fatal("invalid scope published a token")
		}
	}
}

func TestWorkerAuthenticationRefreshesAuthorityTimeAfterFileIO(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	// Expiry follows the eligible initial observation. File reading/publication
	// occurs before the final commit checks that same fence at its fresh time.
	var expires time.Time
	fixture.beforeRead = func() { expires = fixture.observedAt.Add(time.Nanosecond) }
	fixture.beforeCommit = func(command persistence.WorkerPolicyCommand) {
		if !command.At.Before(expires) {
			fixture.commitErr = persistence.ErrOperatorAuthorityDenied
		}
	}
	report, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	if !errors.Is(err, persistence.ErrOperatorAuthorityDenied) || report.Outcome != "not_committed" || report.TokenOutput != request.TokenOutput || len(fixture.commands) != 1 || report.IntendedRevision != fixture.commands[0].Revision {
		t.Fatal("expired authority committed or lost the published token and intended revision", err)
	}
	if fixture.commands[0].Authority != fixture.authority {
		t.Fatal("final check replaced the original operator authority fence")
	}
	token := assertAuthToken(t, request.TokenOutput, fixture.commands[0].Worker.TokenSHA256)
	clear(token)
}

func TestWorkerAuthenticationPublicationRaceAndCancellation(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	if err := os.MkdirAll(filepath.Dir(request.TokenOutput), 0700); err != nil {
		t.Fatal(err)
	}
	fixture.beforeRead = func() {
		if err := os.WriteFile(request.TokenOutput, []byte("existing-private-file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	data, readErr := os.ReadFile(request.TokenOutput)
	if err == nil || readErr != nil || report.Outcome != "not_submitted" || string(data) != "existing-private-file" || len(fixture.commands) != 0 {
		t.Fatal("concurrent output was overwritten or verifier submitted", err, readErr)
	}
	fixture, request = workerIssueFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := administerWorkerAuthentication(ctx, request, fixture.open); !errors.Is(err, context.Canceled) || fixture.opened != 0 {
		t.Fatal("canceled request opened state", err)
	}
	fixture.commitErr = context.Canceled
	report, err = administerWorkerAuthentication(t.Context(), request, fixture.open)
	if !errors.Is(err, context.Canceled) || report.Outcome != "not_committed" || report.TokenOutput != request.TokenOutput || len(fixture.commands) != 1 {
		t.Fatal("canceled submission lost the published token or retried", err)
	}
	assertAuthToken(t, request.TokenOutput, fixture.commands[0].Worker.TokenSHA256)
}

func TestWorkerAuthenticationRestorePreparationAndRevokedIdentity(t *testing.T) {
	fixture, request := workerIssueFixture(t)
	if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); err != nil {
		t.Fatal(err)
	}
	previous := fixture.state.Workers[request.WorkerID].Clone()
	previous.ResetRequired, previous.RestoredTokenSHA256, previous.Grants = true, previous.TokenSHA256, nil
	fixture.state.Workers[previous.ID] = previous
	fixture.state.RestoreID, fixture.state.Epoch, fixture.state.ResetRequired = uuid.NewString(), uuid.NewString(), true
	request.TokenOutput = filepath.Join(filepath.Dir(request.TokenOutput), "restored.token")
	for _, action := range []string{"issue", "rotate", "set-grants"} {
		copy := request
		copy.Action = action
		if action == "rotate" {
			copy.GrantsFile = ""
		}
		if action == "set-grants" {
			copy.TokenOutput = ""
		}
		if _, err := administerWorkerAuthentication(t.Context(), copy, fixture.open); !errors.Is(err, ErrWorkerAuthenticationState) || len(fixture.commands) != 1 {
			t.Fatal("ordinary mutation bypassed explicit restored provisioning", err)
		}
	}
	request.Action = "reprovision"
	report, err := administerWorkerAuthentication(t.Context(), request, fixture.open)
	current := fixture.state.Workers[request.WorkerID]
	if err != nil || report.ResetRequired || current.ResetRequired || current.UID != previous.UID || current.RestoredTokenSHA256 != previous.RestoredTokenSHA256 || current.TokenSHA256 == previous.TokenSHA256 || current.CredentialRevision == previous.CredentialRevision || current.GrantRevision == previous.GrantRevision {
		t.Fatal("reprovision lost stable identity or retained restored authority", err)
	}
	current.ResetRequired, current.Grants = true, nil
	fixture.state.Workers[current.ID] = current
	request.Action, request.TokenOutput, request.GrantsFile = "revoke", "", ""
	if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); err != nil {
		t.Fatal("reset worker could not be permanently revoked", err)
	}
	last := fixture.state.Workers[current.ID]
	if !last.Revoked || !last.ResetRequired || last.TokenSHA256 != current.TokenSHA256 || last.GrantRevision != current.GrantRevision || last.CredentialRevision == current.CredentialRevision {
		t.Fatal("revocation changed reset identity material or failed to fence credential")
	}
	request.Action, request.TokenOutput = "reprovision", filepath.Join(filepath.Dir(request.DataDirectory), "forbidden.token")
	request.GrantsFile = writeWorkerGrantFile(t, request.DataDirectory, "reprovision.json", []persistence.WorkerGrant{})
	if _, err := administerWorkerAuthentication(t.Context(), request, fixture.open); !errors.Is(err, ErrWorkerAuthenticationState) {
		t.Fatal("revoked identity was revived", err)
	}
	raw, _ := json.Marshal(workerAuthenticationReport(fixture.state))
	if bytes.Contains(raw, []byte(previous.RestoredTokenSHA256)) || bytes.Contains(raw, []byte(current.TokenSHA256)) {
		t.Fatal("restore report disclosed historical verifiers")
	}
}
