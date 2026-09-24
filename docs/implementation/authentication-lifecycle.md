# Durable principal lifecycle and operator observations

Current authentication policies use policy format 2 inside storage format 6.
Ordinary authentication remains compatible with historical policy format 1.
This change does not enable public collection validation, activation or a
background executor.

A named principal ID is permanent within its authentication epoch. Policy
replacement retains every existing principal, including revoked entries.
Credential rotation changes the verifier while retaining the principal ID.
Revocation is final for that ID: its revoked record is retained unchanged as a
tombstone. A new operator requires a new ID. Tombstones count toward the existing
1,024-principal policy limit; this change does not introduce automatic removal.

Historical commands without an explicit command version retain their original
replay and digest semantics. Current commands explicitly select lifecycle
format 2 and cannot be replayed by storage readers older than format 6. Once a
policy enters format 2, a historical replacement cannot downgrade it. An exact
lost response still reconciles the original committed command without issuing a
new principal or audit event.

An existing format-1 policy continues to authenticate ordinary HTTP requests.
It cannot provide background operator authority until an explicit stopped local
administration action replaces it with a current policy. Issuing, rotating or
revoking through local administration preserves the retained IDs and performs
that versioned replacement. Migration cannot reconstruct IDs deleted by
historical policies. New collection creation records a durable owner observation.
Historical ownerless uploads cannot acquire background authority from a reused
textual actor. A new operation under the current policy establishes ownership;
see the [validation staging contract](collection-validation-staging.md).

Explicit restore replaces the authentication epoch and requires stopped local
reprovisioning. The new epoch may provision names used in the old epoch; the old
authority fence remains invalid. Restoring a snapshot during ordinary replay
does not itself change identity or reactivate revoked principals.

`Store.ObserveOperatorAuthority(ctx, actor, at)` returns only the committed epoch,
revision and named actor. It contains no token or verifier, performs no writes,
and does not renew expiry. It rejects absent, revoked, expired, reader, legacy,
anonymous, reset-required and historical-policy authority. The observation time
is supplied by the caller; replay never reads the wall clock. Expiry is exclusive:
the principal is denied at its exact expiration time.

This observation is a conditional fence, not a credential. The HTTP boundary
must establish the actor and bind it to the durable operation owner. A future
collection command must compare the same epoch and revision against current
durable policy inside its atomic mutation, at its supplied admission time.
Every policy revision, including a change to another principal, invalidates an
in-flight fence. A fresh observation may authorize subsequent work but must
never refresh resource guards or replace the original operation identity.
