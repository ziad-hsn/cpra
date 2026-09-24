//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerPrincipal(id string) WorkerPrincipal {
	return WorkerPrincipal{ID: id, UID: uuid.NewString(), CredentialRevision: uuid.NewString(), GrantRevision: uuid.NewString(), TokenSHA256: authenticationVerifier("worker-token-" + id)}
}
func workerCommand(t *testing.T, s *Store, mode string, p WorkerPrincipal) WorkerPolicyCommand {
	t.Helper()
	at := time.Now().UTC()
	current, err := s.WorkerPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !at.After(current.UpdatedAt) {
		at = current.UpdatedAt.Add(time.Millisecond)
	}
	management, err := s.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	if !at.After(management.UpdatedAt) {
		at = management.UpdatedAt.Add(time.Millisecond)
	}
	auth, err := s.ObserveOperatorAuthority(t.Context(), "oncall", at)
	if err != nil {
		t.Fatal(err)
	}
	c := WorkerPolicyCommand{Mode: mode, Worker: p, Actor: auth.Actor, Authority: auth, At: at, Epoch: uuid.NewString(), Revision: uuid.NewString()}
	if mode != "bootstrap" {
		c.Epoch = current.Epoch
		c.ExpectedEpoch = current.Epoch
		c.ExpectedRevision = current.Revision
	}
	return c
}
func workerFixture(t *testing.T) (*Store, JobTypeState, WorkerPolicyCommand) {
	t.Helper()
	s := openCatalogMemory(t)
	state := commitJobType(t, s, jobTypeFixture(t, s, "worker-check"))
	// Isolated memory fixtures exercise Apply while the production Submit guard
	// is independently tested using real stopped/native administrative stores.
	s.administrative = true
	p := workerPrincipal("worker-one")
	p.Grants = []WorkerGrant{{JobTypeID: state.Current.Record.Key.ID, JobTypeUID: state.Current.Record.UID, Version: state.Current.Version, Category: state.Current.Category, ResourceKind: "Monitor", ResourceIDs: []string{"monitor-one"}}}
	return s, state, workerCommand(t, s, "bootstrap", p)
}
func commitWorker(t *testing.T, s *Store, c WorkerPolicyCommand) WorkerPolicyState {
	t.Helper()
	state, err := s.CommitWorkerPolicy(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func workerScope(c WorkerPolicyCommand) WorkerScope {
	g := c.Worker.Grants[0]
	return WorkerScope{JobTypeID: g.JobTypeID, JobTypeUID: g.JobTypeUID, Version: g.Version, Category: g.Category, ResourceKind: g.ResourceKind, ResourceID: g.ResourceIDs[0]}
}

func TestWorkerPolicyCommitScopesAndIndependentRevisions(t *testing.T) {
	s, _, c := workerFixture(t)
	state := commitWorker(t, s, c)
	raw, _ := json.Marshal(state)
	if int64(len(raw)) != state.EncodedBytes {
		t.Fatal("policy cost not exact", len(raw), state.EncodedBytes)
	}
	if got := commitWorker(t, s, c); !reflect.DeepEqual(got, state) {
		t.Fatal("lost response idempotence changed policy")
	}
	a, err := s.AuthenticateWorker(t.Context(), c.Worker.TokenSHA256, c.At)
	if err != nil || a.WorkerID != c.Worker.ID || a.WorkerUID != c.Worker.UID {
		t.Fatal("credential observation", err)
	}
	if err := s.CheckWorkerAuthority(t.Context(), a, workerScope(c), c.At); err != nil {
		t.Fatal(err)
	}
	scope := workerScope(c)
	scope.ResourceID = "other"
	if err := s.CheckWorkerAuthority(t.Context(), a, scope, c.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("scope expanded", err)
	}
	scope = workerScope(c)
	scope.JobTypeUID = "another-incarnation"
	if err := s.CheckWorkerAuthority(t.Context(), a, scope, c.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("foreign type incarnation", err)
	}
	encoded, _ := json.Marshal(a)
	if bytes.Contains(encoded, []byte("sha256")) || bytes.Contains(encoded, []byte(c.Worker.TokenSHA256)) {
		t.Fatal("assertion retained verifier")
	}
	rotate := workerCommand(t, s, "upsert", state.Workers[c.Worker.ID].Clone())
	rotate.Worker.TokenSHA256 = authenticationVerifier("rotated-worker")
	if _, err := s.CommitWorkerPolicy(t.Context(), rotate); !errors.Is(err, ErrWorkerPolicyConflict) {
		t.Fatal("credential changed without revision", err)
	}
	rotate.Worker.CredentialRevision = uuid.NewString()
	state = commitWorker(t, s, rotate)
	if state.Workers[c.Worker.ID].GrantRevision != c.Worker.GrantRevision {
		t.Fatal("credential rotation changed scope revision")
	}
	if _, err := s.AuthenticateWorker(t.Context(), c.Worker.TokenSHA256, rotate.At); !errors.Is(err, ErrWorkerUnauthorized) {
		t.Fatal("old token authorized", err)
	}
	if err := s.CheckWorkerAuthority(t.Context(), a, workerScope(c), rotate.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("old fence authorized", err)
	}
	grants := workerCommand(t, s, "upsert", state.Workers[c.Worker.ID].Clone())
	grants.Worker.Grants = nil
	grants.Worker.GrantRevision = uuid.NewString()
	state = commitWorker(t, s, grants)
	fresh, err := s.AuthenticateWorker(t.Context(), rotate.Worker.TokenSHA256, grants.At)
	if err != nil {
		t.Fatal("empty grants prevented credential-only result identity", err)
	}
	if err := s.CheckWorkerAuthority(t.Context(), fresh, workerScope(c), grants.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("empty grants permitted new work", err)
	}
	if fresh.CredentialRevision != rotate.Worker.CredentialRevision {
		t.Fatal("grant change rotated credential")
	}
	detached, err := s.WorkerPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	delete(detached.Workers, c.Worker.ID)
	if got, _ := s.WorkerPolicy(t.Context()); len(got.Workers) != 1 {
		t.Fatal("policy read leaked mutable map")
	}
	page, err := s.History().Page("resource/Worker/"+c.Worker.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatal("missing per-worker audit", len(page.Events))
	}
	reasons := map[string]bool{}
	for _, e := range page.Events {
		if e.CatalogUID != c.Worker.UID || e.Actor != c.Actor || e.Kind != "Worker" {
			t.Fatal("worker audit identity lost")
		}
		reasons[e.Reason] = true
	}
	if !reasons["created"] || !reasons["credential_changed"] || !reasons["grants_changed"] {
		t.Fatal("worker audit disposition lost", reasons)
	}
	audit, _ := json.Marshal(page)
	if bytes.Contains(audit, []byte("token_sha256")) || bytes.Contains(audit, []byte(c.Worker.TokenSHA256)) || bytes.Contains(audit, []byte("worker-token-")) {
		t.Fatal("audit exposed verifier or token")
	}
}

func TestWorkerPolicyAdminAndAuthorityFences(t *testing.T) {
	s, _, c := workerFixture(t)
	s.administrative = false
	if _, err := s.CommitWorkerPolicy(t.Context(), c); !errors.Is(err, ErrWorkerPolicyAdminRequired) {
		t.Fatal("running store accepted provisioning", err)
	}
	if _, err := s.Submit(t.Context(), []Command{{Kind: "worker_policy", At: c.At, commandExtensions: commandExtensions{WorkerPolicy: &c}}}); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("raw Submit bypassed stopped guard", err)
	}
	s.administrative = true
	for _, mutate := range []func(*WorkerPolicyCommand){func(c *WorkerPolicyCommand) { c.Actor = "another" }, func(c *WorkerPolicyCommand) { c.Authority.Revision = "stale" }, func(c *WorkerPolicyCommand) { c.Authority.Actor = "missing"; c.Actor = "missing" }} {
		bad := c
		mutate(&bad)
		if _, err := s.CommitWorkerPolicy(t.Context(), bad); err == nil {
			t.Fatal("foreign actor or authority accepted")
		}
	}
	if s.fsm.image.WorkerPolicy != nil {
		t.Fatal("failed authority changed policy")
	}
	oldAuth := s.fsm.image.Authentication
	s.fsm.image.Authentication = nil
	if _, err := s.CommitWorkerPolicy(t.Context(), c); err == nil {
		t.Fatal("empty store implicitly bootstrapped workers")
	}
	s.fsm.image.Authentication = oldAuth
	state := commitWorker(t, s, c)
	// Management rotation/revocation does not strand worker result credentials.
	auth := lifecycleReplacement(*oldAuth)
	auth.Principals[0].Revoked = true
	if got := lifecycleCommand(t, s, CollectionValidationFormatVersion, auth); got.Err != nil {
		t.Fatal(got.Err)
	}
	a, err := s.AuthenticateWorker(t.Context(), c.Worker.TokenSHA256, auth.At)
	if err != nil {
		t.Fatal("management revocation stranded worker identity", err)
	}
	if err := s.CheckWorkerAuthority(t.Context(), a, workerScope(c), auth.At); err != nil {
		t.Fatal("worker grants depended on dynamic operator eligibility", err)
	}
	update := c
	update.Mode = "upsert"
	update.ExpectedEpoch = state.Epoch
	update.ExpectedRevision = state.Revision
	update.Revision = uuid.NewString()
	update.At = auth.At
	if _, err := s.CommitWorkerPolicy(t.Context(), update); err == nil {
		t.Fatal("revoked provisioner changed policy")
	}
}

func TestWorkerPolicyGrantShapeAndCrossVerifierCollisions(t *testing.T) {
	s, _, c := workerFixture(t)
	for name, change := range map[string]func(*WorkerPolicyCommand){
		"wildcard mix":    func(c *WorkerPolicyCommand) { c.Worker.Grants[0].ResourceIDs = []string{"*", "one"} },
		"pattern":         func(c *WorkerPolicyCommand) { c.Worker.Grants[0].ResourceIDs = []string{"prefix*"} },
		"duplicate scope": func(c *WorkerPolicyCommand) { c.Worker.Grants[0].ResourceIDs = []string{"one", "one"} },
		"kind":            func(c *WorkerPolicyCommand) { c.Worker.Grants[0].ResourceKind = "Credential" },
		"type UID":        func(c *WorkerPolicyCommand) { c.Worker.Grants[0].JobTypeUID = "other" },
		"type version":    func(c *WorkerPolicyCommand) { c.Worker.Grants[0].Version = "missing" },
		"category":        func(c *WorkerPolicyCommand) { c.Worker.Grants[0].Category = "recovery" },
		"management token": func(c *WorkerPolicyCommand) {
			c.Worker.TokenSHA256 = strings.ToUpper(s.fsm.image.Authentication.Principals[0].TokenSHA256)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			bad.Worker = c.Worker.Clone()
			change(&bad)
			if _, err := s.CommitWorkerPolicy(t.Context(), bad); err == nil {
				t.Fatal("invalid policy accepted")
			}
			if s.fsm.image.WorkerPolicy != nil {
				t.Fatal("rejection changed policy")
			}
		})
	}
	state := commitWorker(t, s, c)
	other := workerCommand(t, s, "upsert", workerPrincipal("other"))
	other.Worker.TokenSHA256 = strings.ToUpper(c.Worker.TokenSHA256)
	if _, err := s.CommitWorkerPolicy(t.Context(), other); !errors.Is(err, ErrWorkerPolicyInvalid) {
		t.Fatal("duplicate worker verifier", err)
	}
	replacement := lifecycleReplacement(*s.fsm.image.Authentication)
	replacement.Principals[0].TokenSHA256 = strings.ToUpper(c.Worker.TokenSHA256)
	if _, err := s.CommitAuthentication(t.Context(), replacement); !errors.Is(err, ErrAuthenticationInvalid) {
		t.Fatal("management accepted worker credential", err)
	}
	replacement = lifecycleReplacement(*s.fsm.image.Authentication)
	replacement.LegacyTokenSHA256 = c.Worker.TokenSHA256
	if _, err := s.CommitAuthentication(t.Context(), replacement); !errors.Is(err, ErrAuthenticationInvalid) {
		t.Fatal("legacy reader accepted worker credential", err)
	}
	revoke := workerCommand(t, s, "upsert", state.Workers[c.Worker.ID].Clone())
	revoke.Worker.Revoked = true
	revoke.Worker.CredentialRevision = uuid.NewString()
	revoked := commitWorker(t, s, revoke)
	replacement = lifecycleReplacement(*s.fsm.image.Authentication)
	replacement.Principals[0].TokenSHA256 = c.Worker.TokenSHA256
	if _, err := s.CommitAuthentication(t.Context(), replacement); !errors.Is(err, ErrAuthenticationInvalid) {
		t.Fatal("revoked worker hash reused by management", err)
	}
	resurrect := workerCommand(t, s, "upsert", revoked.Workers[c.Worker.ID].Clone())
	resurrect.Worker.Revoked = false
	resurrect.Worker.CredentialRevision = uuid.NewString()
	resurrect.Worker.TokenSHA256 = authenticationVerifier("resurrection")
	if _, err := s.CommitWorkerPolicy(t.Context(), resurrect); !errors.Is(err, ErrWorkerPolicyConflict) {
		t.Fatal("revoked identity reactivated", err)
	}
}

func TestWorkerPolicyTypeDeletionRequiresExplicitGrantRemoval(t *testing.T) {
	s, state, c := workerFixture(t)
	c.Worker.ExpiresAt = c.At.Add(-time.Hour)
	committed := commitWorker(t, s, c)
	s.administrative = false
	deletion := jobTypeChange(t, s, state, state.Current.Version)
	deletion.Action = "delete"
	deletion.Value.Record.Removed = true
	deletion.Value.Record.Payload = secureconfig.Envelope{}
	if _, err := s.CommitJobType(t.Context(), deletion, deletion.Value.Record.UpdatedAt); !errors.Is(err, ErrCatalogReferenced) {
		t.Fatal("expired but nonrevoked grant released type", err)
	}
	s.administrative = true
	revoke := workerCommand(t, s, "upsert", committed.Workers[c.Worker.ID].Clone())
	revoke.Worker.Revoked = true
	revoke.Worker.CredentialRevision = uuid.NewString()
	commitWorker(t, s, revoke)
	s.administrative = false
	if _, err := s.CommitJobType(t.Context(), deletion, deletion.Value.Record.UpdatedAt); err != nil {
		t.Fatal("explicit revocation did not release deletion", err)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal("revoked reference made deleted state unrecoverable", err)
	}
}

type workerEnteredContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *workerEnteredContext) Err() error {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Err()
}

func TestWorkerPolicyLockWaitExpiryAndCancellation(t *testing.T) {
	for _, method := range []string{"authenticate", "scope"} {
		t.Run(method, func(t *testing.T) {
			s, _, c := workerFixture(t)
			c.Worker.ExpiresAt = c.At.Add(90 * time.Millisecond)
			commitWorker(t, s, c)
			a, err := s.AuthenticateWorker(t.Context(), c.Worker.TokenSHA256, c.At)
			if err != nil {
				t.Fatal(err)
			}
			at := c.Worker.ExpiresAt.Add(-20 * time.Millisecond)
			s.fsm.mu.Lock()
			done := make(chan error, 1)
			entered := &workerEnteredContext{Context: t.Context(), entered: make(chan struct{})}
			go func() {
				if method == "authenticate" {
					_, err := s.AuthenticateWorker(entered, c.Worker.TokenSHA256, at)
					done <- err
				} else {
					done <- s.CheckWorkerAuthority(entered, a, workerScope(c), at)
				}
			}()
			<-entered.entered
			time.Sleep(60 * time.Millisecond)
			s.fsm.mu.Unlock()
			if err := <-done; !errors.Is(err, ErrWorkerUnauthorized) && !errors.Is(err, ErrWorkerAuthorityDenied) {
				t.Fatal("expired while waiting but was authorized", err)
			}
			s.fsm.mu.Lock()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
			_, err = s.AuthenticateWorker(ctx, c.Worker.TokenSHA256, c.At)
			cancel()
			s.fsm.mu.Unlock()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lock wait ignored context", err)
			}
		})
	}
}

func TestWorkerPolicyFormatsSnapshotsAndQuota(t *testing.T) {
	s, _, c := workerFixture(t)
	state := commitWorker(t, s, c)
	command := Command{Kind: "worker_policy", At: c.At, commandExtensions: commandExtensions{WorkerPolicy: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerPolicyFormatVersion || version == CatalogJobTypeFormatVersion || version == WorkerSessionFormatVersion || version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
			t.Fatal("policy format gate", version, err)
		}
	}
	raw, _ := json.Marshal(command)
	bound, err := encodedBound(command)
	if err != nil || bound < len(raw) {
		t.Fatal("private extension budget", err)
	}
	raw = bytes.Replace(raw, []byte(`"mode":"bootstrap"`), []byte(`"mode":"bootstrap","unknown":null`), 1)
	var roundtrip Command
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&roundtrip) == nil {
		t.Fatal("unknown policy field accepted")
	}
	blob := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(blob, []byte(workerPolicySnapshotMagic)) {
		t.Fatal("format17 framing")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(blob), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if !reflect.DeepEqual(*i.WorkerPolicy, state) {
		t.Fatal("snapshot lost policy")
	}
	i.WorkerPolicy.Workers[c.Worker.ID].Grants[0].ResourceIDs[0] = "tamper"
	if current, _ := s.WorkerPolicy(t.Context()); current.Workers[c.Worker.ID].Grants[0].ResourceIDs[0] != "monitor-one" {
		t.Fatal("snapshot alias")
	}
	for _, mutate := range []func(*image){func(i *image) { i.Version = JobTypeOperationFormatVersion }, func(i *image) { i.WorkerPolicy.EncodedBytes++ }, func(i *image) {
		p := i.WorkerPolicy.Workers[c.Worker.ID]
		p.TokenSHA256 = i.Authentication.Principals[0].TokenSHA256
		i.WorkerPolicy.Workers[p.ID] = p
	}} {
		bad := s.fsm.image
		bad.imageExtensions = cloneImageExtensions(bad)
		mutate(&bad)
		if validateImageExtensions(bad) == nil {
			t.Fatal("corrupt policy snapshot accepted")
		}
	}
	oversized := c
	oversized.Worker = c.Worker.Clone()
	oversized.Worker.Grants = make([]WorkerGrant, MaxWorkerGrants+1)
	if oversized.Validate() == nil {
		t.Fatal("grant count not bounded")
	}
	oversized = c
	oversized.Worker = c.Worker.Clone()
	oversized.Worker.Grants[0].ResourceIDs = make([]string, MaxWorkerGrantResources+1)
	if oversized.Validate() == nil {
		t.Fatal("scope count not bounded")
	}
	full := state.Clone()
	for n := 1; n < MaxWorkers+1; n++ {
		p := workerPrincipal(fmt.Sprint(n))
		full.Workers[p.ID] = p
	}
	if _, err := workerPolicyCost(full); !errors.Is(err, ErrWorkerPolicyQuota) {
		t.Fatal("worker count quota", err)
	}
	dense := state.Clone()
	dense.Workers = map[string]WorkerPrincipal{}
	for n := 0; n < 40; n++ {
		p := workerPrincipal(fmt.Sprint(n))
		for g := 0; g < 64; g++ {
			grant := c.Worker.Grants[0].Clone()
			grant.Version = fmt.Sprint(g)
			grant.ResourceIDs = nil
			for j := 0; j < 64; j++ {
				grant.ResourceIDs = append(grant.ResourceIDs, fmt.Sprintf("%03d-", j)+strings.Repeat("x", 252))
			}
			p.Grants = append(p.Grants, grant)
		}
		dense.Workers[p.ID] = p
	}
	if _, err := workerPolicyCost(dense); !errors.Is(err, ErrWorkerPolicyQuota) {
		t.Fatal("encoded policy quota", err)
	}
}

func TestWorkerPolicyStaleCallerTimeCannotReviveCredential(t *testing.T) {
	s, _, c := workerFixture(t)
	c.Worker.ExpiresAt = c.At.Add(30 * time.Millisecond)
	commitWorker(t, s, c)
	a := workerAuthority(s.fsm.image.WorkerPolicy, c.Worker)
	if wait := time.Until(c.Worker.ExpiresAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err := s.AuthenticateWorker(t.Context(), c.Worker.TokenSHA256, c.At); !errors.Is(err, ErrWorkerUnauthorized) {
		t.Fatal("stale caller time revived credential", err)
	}
	if err := s.CheckWorkerAuthority(t.Context(), a, workerScope(c), c.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("stale caller time revived new-work scope", err)
	}
}

func TestWorkerPolicyCategoriesAndExplicitWildcard(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s := openCatalogMemory(t)
			job := jobTypeFixture(t, s, "category-type")
			job.Value.Category = category
			state := commitJobType(t, s, job)
			s.administrative = true
			kind := "Monitor"
			if category == "notification" {
				kind = "NotificationEndpoint"
			}
			p := workerPrincipal("scoped-worker")
			p.Grants = []WorkerGrant{{JobTypeID: state.Current.Record.Key.ID, JobTypeUID: state.Current.Record.UID, Version: state.Current.Version, Category: category, ResourceKind: kind, ResourceIDs: []string{"*"}}}
			c := workerCommand(t, s, "bootstrap", p)
			commitWorker(t, s, c)
			a, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, c.At)
			if err != nil {
				t.Fatal(err)
			}
			scope := workerScope(c)
			scope.ResourceID = "not-created-yet"
			if err := s.CheckWorkerAuthority(t.Context(), a, scope, c.At); err != nil {
				t.Fatal("explicit stable-ID wildcard rejected", err)
			}
			scope.ResourceKind = "Credential"
			if err := s.CheckWorkerAuthority(t.Context(), a, scope, c.At); !errors.Is(err, ErrWorkerAuthorityDenied) {
				t.Fatal("wildcard crossed resource kind", err)
			}
		})
	}
}

func TestWorkerPolicyFormatPreservesCollectionSections(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 2)
	before, err := s.fsm.collections.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	commitJobType(t, s, jobTypeFixture(t, s, "probe"))
	s.administrative = true
	committed := commitWorker(t, s, workerCommand(t, s, "bootstrap", workerPrincipal("no-new-work")))
	blob := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(blob), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	after, err := ledger.Bytes()
	if err != nil || before != after || !reflect.DeepEqual(i.Collections[head.ID], head) || !reflect.DeepEqual(*i.WorkerPolicy, committed) {
		t.Fatal("format17 lost collection namespaces", err)
	}
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := validateCollectionRows(i, view); err != nil {
		t.Fatal(err)
	}
}
