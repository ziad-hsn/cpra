//go:build externaljobs

package persistence

import (
	"encoding/json"
	"reflect"
)

func validateWorkerGrantReference(i image, p WorkerPrincipal, g WorkerGrant) error {
	if i.JobTypes == nil {
		return ErrWorkerPolicyInvalid
	}
	state, ok := i.JobTypes.Records[g.JobTypeID]
	if !ok {
		return ErrWorkerPolicyInvalid
	}
	version, ok := state.Versions[g.Version]
	if !ok || version.Record.UID != g.JobTypeUID || version.Category != g.Category {
		return ErrWorkerPolicyInvalid
	}
	// Expiry does not release configuration references. Grant removal or explicit
	// revocation is required; otherwise extending a credential could revive them.
	if !p.Revoked && (state.Current.Record.Removed || state.Current.Record.UID != g.JobTypeUID) {
		return ErrWorkerPolicyInvalid
	}
	return nil
}
func validateWorkerVerifierDomains(policy *WorkerPolicyState, auth *AuthenticationState) error {
	if policy == nil {
		return nil
	}
	owners := make(map[[32]byte]string, len(policy.Workers)*2)
	for id, p := range policy.Workers {
		for _, value := range []string{p.TokenSHA256, p.RestoredTokenSHA256} {
			if value == "" {
				continue
			}
			key := workerVerifier(value)
			if owner, ok := owners[key]; ok && owner != id {
				return ErrWorkerPolicyInvalid
			}
			owners[key] = id
		}
	}
	if auth != nil {
		if auth.LegacyTokenSHA256 != "" {
			if _, ok := owners[workerVerifier(auth.LegacyTokenSHA256)]; ok {
				return ErrWorkerPolicyInvalid
			}
		}
		for _, p := range auth.Principals {
			if _, ok := owners[workerVerifier(p.TokenSHA256)]; ok {
				return ErrWorkerPolicyInvalid
			}
		}
	}
	return nil
}
func validateWorkerPolicyImage(i image) error {
	s := i.WorkerPolicy
	if s == nil {
		return nil
	}
	if (i.Version != WorkerPolicyFormatVersion && i.Version != CatalogJobTypeFormatVersion && i.Version != WorkerSessionFormatVersion && i.Version != WorkerExecutionFormatVersion && i.Version != WorkerOfferFormatVersion) || s.Version != WorkerPolicyVersion || !validAuthenticationID(s.Epoch) || !validAuthenticationID(s.Revision) || !bootstrapHash(s.CommandDigest) || s.UpdatedAt.IsZero() || s.UpdatedAt.Year() < 1 || s.UpdatedAt.Year() > 9999 || s.Workers == nil || len(s.Workers) > MaxWorkers || i.Authentication == nil || i.Authentication.AnonymousLoopback {
		return ErrWorkerPolicyInvalid
	}
	if s.RestoreID != "" && (i.Restore == nil || s.RestoreID != i.Restore.Marker.ID) || s.ResetRequired && s.RestoreID == "" {
		return ErrWorkerPolicyInvalid
	}
	if i.Restore != nil && (s.RestoreID != i.Restore.Marker.ID || s.Epoch != workerRestoreEpoch(i.Restore.Marker) || s.UpdatedAt.Before(i.Restore.Marker.At)) {
		return ErrWorkerPolicyInvalid
	}
	uids := make(map[string]bool, len(s.Workers))
	for id, p := range s.Workers {
		if id != p.ID || p.Validate() != nil || uids[p.UID] || s.ResetRequired && !p.ResetRequired || p.ResetRequired && (s.RestoreID == "" || p.RestoredTokenSHA256 == "") || s.RestoreID == "" && p.RestoredTokenSHA256 != "" {
			return ErrWorkerPolicyInvalid
		}
		uids[p.UID] = true
		for _, g := range p.Grants {
			if err := validateWorkerGrantReference(i, p, g); err != nil {
				return err
			}
		}
	}
	if err := validateWorkerVerifierDomains(s, i.Authentication); err != nil {
		return err
	}
	cost, err := workerPolicyCost(*s)
	if err != nil {
		return err
	}
	if cost != s.EncodedBytes {
		return ErrWorkerPolicyInvalid
	}
	return nil
}
func (f *machine) applyWorkerPolicy(c WorkerPolicyCommand) Result {
	// The local administrator supplies the committed operator observation; its
	// continued eligibility is checked again before idempotent reconciliation.
	if err := f.checkOperatorAuthority(c.Authority, c.At); err != nil {
		return Result{Err: err}
	}
	old := workerPolicyForProvision(f.image)
	digest := c.digest()
	if old != nil && old.Revision == c.Revision && old.CommandDigest == digest {
		copy := old.Clone()
		return Result{Allowed: true, resultExtensions: resultExtensions{WorkerPolicy: &copy}}
	}
	if c.Mode == "bootstrap" {
		if old != nil || f.image.Restore != nil {
			return Result{Err: ErrWorkerPolicyConflict}
		}
	} else if old == nil || old.Epoch != c.ExpectedEpoch || old.Revision != c.ExpectedRevision || c.At.Before(old.UpdatedAt) {
		return Result{Err: ErrWorkerPolicyConflict}
	}
	next := WorkerPolicyState{Version: WorkerPolicyVersion, Epoch: c.Epoch, Revision: c.Revision, UpdatedAt: c.At, CommandDigest: digest, Workers: map[string]WorkerPrincipal{}}
	if old != nil {
		next.ResetRequired, next.RestoreID = old.ResetRequired, old.RestoreID
		for id, p := range old.Workers {
			next.Workers[id] = p
		}
	}
	previous, exists := next.Workers[c.Worker.ID]
	candidate := c.Worker.Clone()
	if !exists {
		if len(next.Workers) >= MaxWorkers {
			return Result{Err: ErrWorkerPolicyQuota}
		}
		if candidate.ResetRequired || candidate.RestoredTokenSHA256 != "" || c.Mode == "provision" && next.RestoreID == "" || next.RestoreID != "" && c.Mode != "provision" {
			return Result{Err: ErrWorkerPolicyConflict}
		}
	} else {
		if candidate.UID != previous.UID || candidate.RestoredTokenSHA256 != previous.RestoredTokenSHA256 {
			return Result{Err: ErrWorkerPolicyConflict}
		}
		if previous.Revoked {
			if !reflect.DeepEqual(previous, candidate) || c.Mode == "provision" {
				return Result{Err: ErrWorkerPolicyConflict}
			}
		} else if c.Mode == "provision" {
			if !previous.ResetRequired || candidate.ResetRequired || candidate.Revoked || workerVerifier(candidate.TokenSHA256) == workerVerifier(previous.TokenSHA256) || candidate.CredentialRevision == previous.CredentialRevision || candidate.GrantRevision == previous.GrantRevision {
				return Result{Err: ErrWorkerPolicyConflict}
			}
		} else if previous.ResetRequired {
			// A reset identity may be permanently revoked without issuing a token.
			expected := previous.Clone()
			expected.Revoked = true
			expected.CredentialRevision = candidate.CredentialRevision
			if !candidate.Revoked || candidate.CredentialRevision == previous.CredentialRevision || !reflect.DeepEqual(expected, candidate) {
				return Result{Err: ErrWorkerPolicyResetRequired}
			}
		} else if candidate.ResetRequired || workerCredentialsEqual(previous, candidate) != (candidate.CredentialRevision == previous.CredentialRevision) || workerGrantsEqual(previous.Grants, candidate.Grants) != (candidate.GrantRevision == previous.GrantRevision) {
			return Result{Err: ErrWorkerPolicyConflict}
		}
	}
	for id, p := range next.Workers {
		if id != candidate.ID && p.UID == candidate.UID {
			return Result{Err: ErrWorkerPolicyConflict}
		}
	}
	if c.Mode == "provision" {
		next.ResetRequired = false
	}
	next.Workers[candidate.ID] = candidate
	for _, g := range candidate.Grants {
		if err := validateWorkerGrantReference(f.image, candidate, g); err != nil {
			return Result{Err: err}
		}
	}
	if err := validateWorkerVerifierDomains(&next, f.image.Authentication); err != nil {
		return Result{Err: err}
	}
	cost, err := workerPolicyCost(next)
	if err != nil {
		return Result{Err: err}
	}
	next.EncodedBytes = cost
	f.image.WorkerPolicy = &next
	f.image.Version = max(f.image.Version, WorkerPolicyFormatVersion)
	copy := next.Clone()
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerPolicy: &copy}, Events: []Event{workerPolicyEvent(c, previous, exists)}}
}
func workerRestoreEpoch(m RestoreMarker) string {
	return identity("worker-policy-restore-epoch/v1/" + m.ID)
}
func (f *machine) resetWorkerPolicy(m RestoreMarker) []Event {
	f.image.WorkerSessions = nil
	old := f.image.WorkerPolicy
	if old == nil {
		return nil
	}
	next := old.Clone()
	next.Epoch = workerRestoreEpoch(m)
	next.Revision = identity("worker-policy-restore-revision/v1/" + m.ID)
	next.RestoreID = m.ID
	next.ResetRequired = true
	next.UpdatedAt = m.At
	raw, _ := json.Marshal(m)
	next.CommandDigest = identity("worker-policy-restore/v1/" + string(raw))
	for id, p := range next.Workers {
		p.RestoredTokenSHA256 = p.TokenSHA256
		p.Grants = nil
		p.ResetRequired = true
		next.Workers[id] = p
	}
	// Reset only shrinks grants; one bounded restored-verifier field per worker
	// is accounted for by the normal policy cap and validated before installation.
	cost, err := workerPolicyCost(next)
	if err != nil {
		f.err = err
		return nil
	}
	next.EncodedBytes = cost
	f.image.WorkerPolicy = &next
	f.image.Version = max(f.image.Version, WorkerPolicyFormatVersion)
	return []Event{{MonitorID: "worker-policy", Revision: next.Revision, At: m.At, Actor: "local-administrator", Type: "worker_policy_reset", Kind: "worker_policy", Reason: "explicit_restore", Outcome: "provision_required"}}
}
func (f *machine) jobTypeWorkerReferenced(id, uid string) bool {
	if f.image.WorkerPolicy == nil {
		return false
	}
	for _, p := range f.image.WorkerPolicy.Workers {
		if p.Revoked {
			continue
		}
		for _, g := range p.Grants {
			if g.JobTypeID == id && g.JobTypeUID == uid {
				return true
			}
		}
	}
	return false
}
func (f *machine) validateAuthenticationExtensions(c AuthenticationCommand) error {
	candidate := AuthenticationState{Principals: c.Principals, LegacyTokenSHA256: c.LegacyTokenSHA256}
	if validateWorkerVerifierDomains(f.image.WorkerPolicy, &candidate) != nil {
		return ErrAuthenticationInvalid
	}
	return nil
}
func administrativeCommandExtension(c Command) bool { return c.Kind == "worker_policy" }

// A historical workerless restore has no worker-policy namespace. Its committed
// restore marker nevertheless defines an exact empty reset descriptor, allowing
// only explicit first provisioning under format17 without rewriting old logs.
func workerPolicyForProvision(i image) *WorkerPolicyState {
	if i.WorkerPolicy != nil {
		return i.WorkerPolicy
	}
	if i.Restore == nil || i.Restore.Phase != "complete" {
		return nil
	}
	m := i.Restore.Marker
	raw, _ := json.Marshal(m)
	p := WorkerPolicyState{Version: WorkerPolicyVersion, Epoch: workerRestoreEpoch(m), Revision: identity("worker-policy-restore-revision/v1/" + m.ID), ResetRequired: true, RestoreID: m.ID, UpdatedAt: m.At, CommandDigest: identity("worker-policy-restore/v1/" + string(raw)), Workers: map[string]WorkerPrincipal{}}
	p.EncodedBytes, _ = workerPolicyCost(p)
	return &p
}

func workerPolicyEvent(c WorkerPolicyCommand, previous WorkerPrincipal, exists bool) Event {
	reason := "unchanged"
	switch {
	case c.Mode == "provision":
		reason = "restored"
	case !exists:
		reason = "created"
	case !previous.Revoked && c.Worker.Revoked:
		reason = "revoked"
		if !workerGrantsEqual(previous.Grants, c.Worker.Grants) {
			reason = "revoked_and_grants_changed"
		}
	case !workerCredentialsEqual(previous, c.Worker) && !workerGrantsEqual(previous.Grants, c.Worker.Grants):
		reason = "credential_and_grants_changed"
	case !workerCredentialsEqual(previous, c.Worker):
		reason = "credential_changed"
	case !workerGrantsEqual(previous.Grants, c.Worker.Grants):
		reason = "grants_changed"
	}
	return Event{MonitorID: "resource/Worker/" + c.Worker.ID, CatalogUID: c.Worker.UID, Revision: c.Revision, At: c.At, Actor: c.Actor, Type: "worker_policy_" + c.Mode, Kind: "Worker", Reason: reason, Outcome: "committed"}
}
