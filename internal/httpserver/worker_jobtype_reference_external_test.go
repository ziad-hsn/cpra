//go:build externaljobs

package httpserver

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestJobTypeHTTPWorkerReferenceSurvivesRestartAndExpiry(t *testing.T) {
	for _, release := range []string{"remove-grants", "revoke-expired-worker"} {
		t.Run(release, func(t *testing.T) {
			f, store, directory := newJobTypeHTTPFixture(t)
			created, err := f.sdk.JobTypes().Create(t.Context(), httpJobType("worker-check"))
			if err != nil {
				t.Fatal("create through real SDK", err)
			}
			original, exists, err := store.JobType(t.Context(), created.Data.Metadata.ID)
			if err != nil || !exists {
				t.Fatal("committed JobType missing", err)
			}
			f.http.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			config := runtimeconfig.Default()
			config.Storage.Directory = directory
			admin, err := persistence.OpenAdministrative(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			initialAdmin := admin
			t.Cleanup(func() { _ = initialAdmin.Close() })
			at := time.Now().UTC()
			authority, err := admin.ObserveOperatorAuthority(t.Context(), "operator", at)
			if err != nil {
				t.Fatal(err)
			}
			worker := persistence.WorkerPrincipal{ID: "worker", UID: uuid.NewString(), CredentialRevision: uuid.NewString(), GrantRevision: uuid.NewString(), TokenSHA256: strings.Repeat("a", 64), Grants: []persistence.WorkerGrant{{JobTypeID: created.Data.Metadata.ID, JobTypeUID: created.Data.Metadata.UID, Version: created.Data.Spec.Version, Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"service-a"}}}}
			if release == "revoke-expired-worker" {
				worker.ExpiresAt = at.Add(time.Millisecond)
			}
			policy, err := admin.CommitWorkerPolicy(t.Context(), persistence.WorkerPolicyCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: authority.Actor, Authority: authority, At: at, Worker: worker})
			if err != nil || len(policy.Workers) != 1 {
				t.Fatal("stopped worker grant provisioning failed", err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			store = reopenWorkerJobTypeHTTPFixture(t, f, config)
			retainedPolicy, err := store.WorkerPolicy(t.Context())
			if err != nil || !reflect.DeepEqual(retainedPolicy, policy) {
				t.Fatal("restart changed the committed worker policy", err)
			}
			if release == "revoke-expired-worker" {
				if time.Now().Before(worker.ExpiresAt) || worker.Revoked {
					t.Fatal("fixture requires an expired, nonrevoked credential")
				}
				if _, err := store.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC()); !errors.Is(err, persistence.ErrWorkerUnauthorized) {
					t.Fatal("expired credential remained eligible", err)
				}
			}
			_, err = f.sdk.JobTypes().Delete(t.Context(), created.Data.Metadata.ID, created.ResourceVersion)
			var problem *cpra.Error
			if !errors.As(err, &problem) || problem.StatusCode != 409 || problem.Problem.Code != "resourceReferenced" || problem.OperationID == "" {
				t.Fatal("worker grant did not produce a referenced-resource conflict and retained operation ID", err)
			}
			failedID := problem.OperationID
			failed, err := f.sdk.Operations.Get(t.Context(), failedID)
			if err != nil || failed.Data.ID != failedID || failed.Data.State != "failed" || failed.Data.Committed == nil || *failed.Data.Committed != 0 || failed.Data.Applied == nil || *failed.Data.Applied != 0 || len(failed.Data.Items) != 1 || failed.Data.Items[0].Outcome != "activation_rejected" {
				t.Fatal("failed deletion has no authoritative terminal receipt", err)
			}
			present, err := f.sdk.JobTypes().Get(t.Context(), created.Data.Metadata.ID)
			unchanged, exists, readErr := store.JobType(t.Context(), created.Data.Metadata.ID)
			if err != nil || readErr != nil || !exists || !reflect.DeepEqual(present.Data, created.Data) || !reflect.DeepEqual(unchanged, original) {
				t.Fatal("rejected deletion changed JobType configuration or encrypted history", err, readErr)
			}
			f.http.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			admin, err = persistence.OpenAdministrative(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			reopenedAdmin := admin
			t.Cleanup(func() { _ = reopenedAdmin.Close() })
			policy, err = admin.WorkerPolicy(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			worker = policy.Workers[worker.ID].Clone()
			if release == "remove-grants" {
				worker.Grants, worker.GrantRevision = nil, uuid.NewString()
			} else {
				worker.Revoked, worker.CredentialRevision = true, uuid.NewString()
			}
			at = time.Now().UTC()
			authority, err = admin.ObserveOperatorAuthority(t.Context(), "operator", at)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := admin.CommitWorkerPolicy(t.Context(), persistence.WorkerPolicyCommand{Mode: "upsert", ExpectedEpoch: policy.Epoch, ExpectedRevision: policy.Revision, Epoch: policy.Epoch, Revision: uuid.NewString(), Actor: authority.Actor, Authority: authority, At: at, Worker: worker}); err != nil {
				t.Fatal("stopped grant removal/revocation failed", err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			store = reopenWorkerJobTypeHTTPFixture(t, f, config)
			retainedFailure, err := f.sdk.Operations.Get(t.Context(), failedID)
			if err != nil || !reflect.DeepEqual(retainedFailure.Data, failed.Data) {
				t.Fatal("failed receipt was lost or changed across administrative restart", err)
			}
			deleted, err := f.sdk.JobTypes().Delete(t.Context(), created.Data.Metadata.ID, created.ResourceVersion)
			if err != nil || deleted.OperationID == "" || deleted.OperationID == failedID || deleted.Data.State != "completed" || deleted.Data.Committed == nil || *deleted.Data.Committed != 1 || deleted.Data.Applied == nil || *deleted.Data.Applied != 1 {
				t.Fatal("deletion remained blocked after explicit grant removal/revocation", err)
			}
			if _, err := f.sdk.JobTypes().Get(t.Context(), created.Data.Metadata.ID); !errors.Is(err, cpra.ErrNotFound) {
				t.Fatal("completed deletion left the JobType active", err)
			}
			retainedFailure, err = f.sdk.Operations.Get(t.Context(), failedID)
			if err != nil || !reflect.DeepEqual(retainedFailure.Data, failed.Data) {
				t.Fatal("later successful deletion overwrote the original failure receipt", err)
			}
		})
	}
}

func reopenWorkerJobTypeHTTPFixture(t *testing.T, f *managementFixture, config runtimeconfig.Config) *persistence.Store {
	t.Helper()
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.catalog, f.server.cfg.Store, f.server.cfg.Management = catalog, store, catalog
	enableJobTypeFixture(t, f, true)
	return store
}
