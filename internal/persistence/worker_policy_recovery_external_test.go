//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerPolicyNativeRestoreAndFreshProvision(t *testing.T) {
	for _, workerless := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained-workers", true: "workerless-backup"}[workerless], func(t *testing.T) {
			config := testConfig(t)
			s := openAuthenticationAdmin(t, config)
			if _, err := s.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
				t.Fatal(err)
			}
			var before WorkerPolicyState
			var first WorkerPolicyCommand
			if !workerless {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				live, err := Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				job := commitJobType(t, live, jobTypeFixture(t, live, "retained-grant"))
				if err := live.Close(); err != nil {
					t.Fatal(err)
				}
				s = openAuthenticationAdmin(t, config)
				worker := workerPrincipal("one")
				worker.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: job.Current.Category, ResourceKind: "Monitor", ResourceIDs: []string{"original-monitor"}}}
				first = workerCommand(t, s, "bootstrap", worker)
				before = commitWorker(t, s, first)
				second := workerCommand(t, s, "upsert", workerPrincipal("two"))
				before = commitWorker(t, s, second)
				revoked := workerPrincipal("revoked")
				revoked.Revoked = true
				before = commitWorker(t, s, workerCommand(t, s, "upsert", revoked))
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// A true administrative reopen first proves exact persisted policy recovery.
			s = openAuthenticationAdmin(t, config)
			recovered, err := s.WorkerPolicy(t.Context())
			if err != nil || !reflect.DeepEqual(recovered, before) {
				t.Fatal("native policy reopen", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := MarkRestored(config.Storage.Directory, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			s = openAuthenticationAdmin(t, config)
			reset, err := s.WorkerPolicy(t.Context())
			if err != nil || !reset.ResetRequired || reset.RestoreID == "" || reset.Epoch == before.Epoch {
				t.Fatal("restore descriptor", err)
			}
			if workerless && s.fsm.image.WorkerPolicy != nil {
				t.Fatal("read implicitly initialized worker policy")
			}
			auth, err := s.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			provision := lifecycleReplacement(auth)
			provision.Mode = "provision"
			provision.Principals = authenticationBootstrap().Principals
			if _, err := s.CommitAuthentication(t.Context(), provision); err != nil {
				t.Fatal(err)
			}
			// A fresh management policy alone cannot reactivate restored worker tokens.
			if !workerless {
				if _, err := s.AuthenticateWorker(t.Context(), first.Worker.TokenSHA256, provision.At); !errors.Is(err, ErrWorkerPolicyUnavailable) {
					t.Fatal("global reset authenticated old token", err)
				}
				for id, old := range before.Workers {
					p := reset.Workers[id]
					if p.UID != old.UID || p.TokenSHA256 != old.TokenSHA256 || p.RestoredTokenSHA256 != old.TokenSHA256 || p.Revoked != old.Revoked || !p.ResetRequired || len(p.Grants) != 0 {
						t.Fatal("restore lost retained identity/verifier/tombstone")
					}
				}
			}
			var candidate WorkerPrincipal
			if workerless {
				candidate = workerPrincipal("fresh")
			} else {
				candidate = reset.Workers["one"].Clone()
				candidate.ResetRequired = false
				candidate.TokenSHA256 = authenticationVerifier("fresh-after-restore")
				candidate.CredentialRevision = uuid.NewString()
				candidate.GrantRevision = uuid.NewString()
			}
			cmd := workerCommand(t, s, "provision", candidate)
			cmd.At = provision.At.Add(time.Millisecond)
			if !workerless {
				stale := cmd
				stale.Worker = candidate.Clone()
				stale.Worker.TokenSHA256 = before.Workers["one"].TokenSHA256
				if _, err := s.CommitWorkerPolicy(t.Context(), stale); err == nil {
					t.Fatal("immediately restored token reaccepted")
				}
				stale = cmd
				stale.Worker = candidate.Clone()
				stale.Worker.GrantRevision = before.Workers["one"].GrantRevision
				if _, err := s.CommitWorkerPolicy(t.Context(), stale); !errors.Is(err, ErrWorkerPolicyConflict) {
					t.Fatal("reprovision kept old grant revision", err)
				}
			}
			current := commitWorker(t, s, cmd)
			if current.ResetRequired || current.Workers[candidate.ID].UID != candidate.UID || s.fsm.image.Version != WorkerPolicyFormatVersion {
				t.Fatal("explicit provisioning failed reset boundary")
			}
			if _, err := s.AuthenticateWorker(t.Context(), candidate.TokenSHA256, cmd.At); err != nil {
				t.Fatal("fresh credential denied", err)
			}
			if !workerless {
				if _, err := s.AuthenticateWorker(t.Context(), before.Workers["two"].TokenSHA256, cmd.At); !errors.Is(err, ErrWorkerUnauthorized) {
					t.Fatal("other reset entry became active", err)
				}
				revoke := workerCommand(t, s, "upsert", current.Workers["two"].Clone())
				revoke.Worker.Revoked = true
				revoke.Worker.CredentialRevision = uuid.NewString()
				current = commitWorker(t, s, revoke)
				if !current.Workers["two"].ResetRequired || current.Workers["two"].TokenSHA256 != before.Workers["two"].TokenSHA256 {
					t.Fatal("reset revocation changed credential")
				}
				for _, id := range []string{"two", "revoked"} {
					bad := workerCommand(t, s, "provision", current.Workers[id].Clone())
					bad.Worker.Revoked = false
					bad.Worker.ResetRequired = false
					bad.Worker.TokenSHA256 = authenticationVerifier("revoked-reissue-" + id)
					bad.Worker.CredentialRevision = uuid.NewString()
					bad.Worker.GrantRevision = uuid.NewString()
					if _, err := s.CommitWorkerPolicy(t.Context(), bad); !errors.Is(err, ErrWorkerPolicyConflict) {
						t.Fatal("revoked worker reprovisioned", err)
					}
				}
				rotate := workerCommand(t, s, "upsert", current.Workers["one"].Clone())
				rotate.Worker.TokenSHA256 = before.Workers["one"].TokenSHA256
				rotate.Worker.CredentialRevision = uuid.NewString()
				if _, err := s.CommitWorkerPolicy(t.Context(), rotate); err == nil {
					t.Fatal("restored verifier reused in later rotation")
				}
				collide := lifecycleReplacement(*s.fsm.image.Authentication)
				collide.Principals[0].TokenSHA256 = before.Workers["one"].TokenSHA256
				if _, err := s.CommitAuthentication(t.Context(), collide); !errors.Is(err, ErrAuthenticationInvalid) {
					t.Fatal("management reused restored verifier", err)
				}
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
			expected, err := s.WorkerPolicy(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("offline policy recovery", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openAuthenticationAdmin(t, config)
			actual, err := reopened.WorkerPolicy(t.Context())
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("post-provision snapshot recovery", err)
			}
		})
	}
}

func TestWorkerPolicyLostCommitAndSnapshotIsolation(t *testing.T) {
	seed, _, c := workerFixture(t)
	s, start := dormantCatalogStore(t)
	s.administrative = true
	auth := seed.fsm.image.Authentication.Clone()
	s.fsm.image.Authentication = &auth
	s.fsm.image.JobTypes = cloneImageExtensions(seed.fsm.image).JobTypes
	s.fsm.image.Version = JobTypeFormatVersion
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.CommitWorkerPolicy(ctx, c); done <- err }()
	waitBudgetCondition(t, "worker policy admission", func() bool { return len(s.requests) == 1 })
	cancel()
	if err := <-done; !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("lost policy response not uncertain", err)
	}
	start()
	if err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := s.WorkerPolicy(t.Context())
	if err != nil || got.Revision != c.Revision {
		t.Fatal("original policy revision cannot reconcile", err)
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	changed := workerCommand(t, s, "upsert", got.Workers[c.Worker.ID].Clone())
	changed.Worker.Grants = nil
	changed.Worker.GrantRevision = uuid.NewString()
	commitWorker(t, s, changed)
	original := frozen.(*frozenSnapshot).image.WorkerPolicy
	if original.Revision != c.Revision || len(original.Workers[c.Worker.ID].Grants) != 1 {
		t.Fatal("frozen snapshot observed mutable policy")
	}
	data, _ := json.Marshal(original)
	if bytes.Contains(data, []byte("worker-token-")) {
		t.Fatal("policy persisted bearer")
	}
}
