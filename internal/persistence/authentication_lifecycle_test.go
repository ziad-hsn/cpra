package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func lifecycleCommand(t *testing.T, s *Store, format int, c AuthenticationCommand) Result {
	t.Helper()
	return applyCatalogFormat(t, s, format, Command{Kind: "authentication", At: c.At, Authentication: &c})[0]
}

func lifecycleReplacement(s AuthenticationState) AuthenticationCommand {
	return AuthenticationCommand{Version: AuthenticationLifecycleFormatVersion, Mode: "replace", Epoch: s.Epoch,
		ExpectedEpoch: s.Epoch, ExpectedRevision: s.Revision, Revision: uuid.NewString(), Actor: "local-administrator",
		At: s.UpdatedAt.Add(time.Second), Principals: s.Clone().Principals, LegacyTokenSHA256: s.LegacyTokenSHA256}
}

func TestAuthenticationLifecycleHistoricalReplayAndExplicitUpgrade(t *testing.T) {
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			s := openCatalogMemory(t)
			c := authenticationBootstrap()
			first := lifecycleCommand(t, s, format, c)
			if first.Err != nil || first.Authentication.Version != AuthenticationFormatVersion {
				t.Fatal("legacy policy changed", first.Err)
			}
			// Historical replacements could remove a principal and later reuse
			// its name. Retained log replay must not retroactively reject either.
			remove := lifecycleReplacement(*first.Authentication)
			remove.Version, remove.Principals = 0, nil
			removed := lifecycleCommand(t, s, format, remove)
			if removed.Err != nil || len(removed.Authentication.Principals) != 0 {
				t.Fatal("historical deletion changed", removed.Err)
			}
			reuse := lifecycleReplacement(*removed.Authentication)
			reuse.Version, reuse.Principals = 0, c.Principals
			legacy := lifecycleCommand(t, s, format, reuse)
			if legacy.Err != nil || legacy.Authentication.Version != AuthenticationFormatVersion {
				t.Fatal("historical reuse changed", legacy.Err)
			}
			if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", reuse.At); !errors.Is(err, ErrOperatorAuthorityDenied) {
				t.Fatal("legacy policy became background authority", err)
			}
			upgrade := lifecycleReplacement(*legacy.Authentication)
			before := legacy.Authentication.Clone()
			current := lifecycleCommand(t, s, CollectionValidationFormatVersion, upgrade)
			if current.Err != nil || current.Authentication.Version != AuthenticationLifecycleFormatVersion ||
				!reflect.DeepEqual(current.Authentication.Principals, before.Principals) || s.fsm.image.Version != CollectionValidationFormatVersion {
				t.Fatal("explicit policy upgrade failed", current.Err)
			}
			if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", upgrade.At); err != nil {
				t.Fatal(err)
			}
			downgrade := lifecycleReplacement(*current.Authentication)
			downgrade.Version, downgrade.Principals = 0, nil
			if got := lifecycleCommand(t, s, CollectionValidationFormatVersion, downgrade); !errors.Is(got.Err, ErrAuthenticationConflict) {
				t.Fatal("current policy downgraded", got.Err)
			}
			state, _ := s.Authentication()
			if !reflect.DeepEqual(state, *current.Authentication) {
				t.Fatal("failed downgrade changed policy")
			}
		})
	}
}

func TestAuthenticationLifecyclePermanentIDsAndRevokedTombstones(t *testing.T) {
	s := openCatalogMemory(t)
	c := authenticationBootstrap()
	c.Version = AuthenticationLifecycleFormatVersion
	c.Principals = append(c.Principals, AuthenticationPrincipal{ID: "retired", Role: "reader", Revoked: true,
		TokenSHA256: authenticationVerifier("retired-verifier"), ExpiresAt: c.At.Add(-time.Hour)})
	initial := lifecycleCommand(t, s, CollectionValidationFormatVersion, c)
	if initial.Err != nil {
		t.Fatal(initial.Err)
	}
	for name, mutate := range map[string]func(*AuthenticationCommand){
		"drop live":                func(c *AuthenticationCommand) { c.Principals = c.Principals[1:] },
		"drop revoked":             func(c *AuthenticationCommand) { c.Principals = c.Principals[:1] },
		"reactivate":               func(c *AuthenticationCommand) { c.Principals[1].Revoked = false },
		"replace revoked verifier": func(c *AuthenticationCommand) { c.Principals[1].TokenSHA256 = authenticationVerifier("new") },
		"change revoked role":      func(c *AuthenticationCommand) { c.Principals[1].Role = "operator" },
		"change revoked expiry":    func(c *AuthenticationCommand) { c.Principals[1].ExpiresAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := lifecycleReplacement(*initial.Authentication)
			mutate(&candidate)
			got := lifecycleCommand(t, s, CollectionValidationFormatVersion, candidate)
			if !errors.Is(got.Err, ErrAuthenticationConflict) || got.Allowed || len(got.Events) != 0 {
				t.Fatal("principal identity changed", got.Err)
			}
			state, _ := s.Authentication()
			if !reflect.DeepEqual(state, *initial.Authentication) {
				t.Fatal("rejected replacement mutated authority")
			}
		})
	}
	rotation := lifecycleReplacement(*initial.Authentication)
	rotation.Principals[0].TokenSHA256 = authenticationVerifier("rotated")
	rotation.Principals[1].ExpiresAt = rotation.Principals[1].ExpiresAt.In(time.FixedZone("fixture", 19800))
	rotated := lifecycleCommand(t, s, CollectionValidationFormatVersion, rotation)
	if rotated.Err != nil || rotated.Authentication.Principals[0].ID != "oncall" {
		t.Fatal("rotation or equivalent tombstone instant rejected", rotated.Err)
	}
	revoke := lifecycleReplacement(*rotated.Authentication)
	revoke.Principals[0].Revoked = true
	revoked := lifecycleCommand(t, s, CollectionValidationFormatVersion, revoke)
	if revoked.Err != nil {
		t.Fatal(revoked.Err)
	}
	retry := lifecycleCommand(t, s, CollectionValidationFormatVersion, revoke)
	if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(*retry.Authentication, *revoked.Authentication) {
		t.Fatal("exact revoke retry changed outcome", retry.Err)
	}
}

func TestAuthenticationLifecycleSubmissionStampAndLegacyReceipt(t *testing.T) {
	s := openAuthenticationAdmin(t, testConfig(t))
	c := authenticationBootstrap()
	caller, _ := json.Marshal(c)
	state, err := s.CommitAuthentication(context.Background(), c)
	if err != nil || state.Version != AuthenticationLifecycleFormatVersion {
		t.Fatal(err)
	}
	after, _ := json.Marshal(c)
	if c.Version != 0 || !bytes.Equal(caller, after) {
		t.Fatal("submission mutated caller command")
	}
	index := s.Status().CommittedIndex
	repeated, err := s.CommitAuthentication(context.Background(), c)
	if err != nil || !reflect.DeepEqual(state, repeated) {
		t.Fatal("current lost reply changed policy", err)
	}
	if s.Status().CommittedIndex <= index {
		t.Fatal("fixture did not replay current exact command")
	}
	legacyStore := openAuthenticationAdmin(t, testConfig(t))
	legacy := authenticationBootstrap()
	committed := lifecycleCommand(t, legacyStore, CatalogFormatVersion, legacy)
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
	index = legacyStore.Status().CommittedIndex
	reconciled, err := legacyStore.CommitAuthentication(context.Background(), legacy)
	if err != nil || !reflect.DeepEqual(reconciled, *committed.Authentication) || legacyStore.Status().CommittedIndex != index {
		t.Fatal("pre-upgrade lost reply was rewritten", err)
	}
	// The historical omitted version must preserve the old canonical digest.
	raw, _ := json.Marshal(legacy)
	if bytes.Contains(raw, []byte(`"version"`)) || legacy.digest() != identity("authentication-command/v1/"+string(raw)) {
		t.Fatal("historical command digest changed")
	}
}

func TestAuthenticationLifecycleRestoreStartsNewEpoch(t *testing.T) {
	config := testConfig(t)
	s := openAuthenticationAdmin(t, config)
	c := authenticationBootstrap()
	c.Principals[0].Revoked = true
	old, err := s.CommitAuthentication(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MarkRestored(config.Storage.Directory, c.At.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restored := openAuthenticationAdmin(t, config)
	reset, err := restored.Authentication()
	if err != nil || !reset.ResetRequired || reset.Epoch == old.Epoch {
		t.Fatal("restore failed to fence old identity", err)
	}
	provision := lifecycleReplacement(reset)
	provision.Mode, provision.Principals = "provision", authenticationBootstrap().Principals
	current, err := restored.CommitAuthentication(context.Background(), provision)
	if err != nil || current.Version != AuthenticationLifecycleFormatVersion || current.Principals[0].Revoked {
		t.Fatal("new epoch reprovision denied", err)
	}
	if err := restored.fsm.checkOperatorAuthority(OperatorAuthority{Epoch: old.Epoch, Revision: old.Revision, Actor: "oncall"}, provision.At); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("old epoch remained authority", err)
	}
	if _, err := restored.ObserveOperatorAuthority(context.Background(), "oncall", provision.At); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationLifecycleCommandAndImageVersionBoundaries(t *testing.T) {
	c := authenticationBootstrap()
	c.Version = AuthenticationLifecycleFormatVersion
	command := Command{Kind: "authentication", At: c.At, Authentication: &c}
	if got := commandWriteFormat(command); got != CollectionValidationFormatVersion {
		t.Fatal("current authentication writer did not fence older readers", got)
	}
	for _, version := range []int{CatalogFormatVersion, CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion, CollectionValidationFormatVersion} {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == CollectionValidationFormatVersion) {
			t.Fatal("command/envelope format mismatch accepted", version, err)
		}
	}
	c.Version = 1
	if c.validate() == nil {
		t.Fatal("ambiguous explicitly versioned legacy command accepted")
	}
	c.Version = 99
	if c.validate() == nil {
		t.Fatal("unknown command format accepted")
	}
	s := openCatalogMemory(t)
	current, err := s.CommitAuthentication(context.Background(), authenticationBootstrap())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	image := snapshot.(*frozenSnapshot).image
	if err := validateAuthenticationImage(image); err != nil {
		t.Fatal(err)
	}
	image.Version = CollectionPlanFormatVersion
	if validateAuthenticationImage(image) == nil {
		t.Fatal("current policy in old storage format accepted")
	}
	image.Version = CollectionValidationFormatVersion
	legacy := current.Clone()
	legacy.Version = AuthenticationFormatVersion
	image.Authentication = &legacy
	if err := validateAuthenticationImage(image); err != nil {
		t.Fatal("historical policy rejected in newer image", err)
	}
}

func TestAuthenticationLifecycleHistoricalCommandCanonicalIdentity(t *testing.T) {
	// Literal pre-version command shape, including field order and explicit
	// zero expiration. A retained receipt was hashed using these exact bytes.
	raw := `{"mode":"bootstrap","epoch":"historical-epoch","revision":"historical-revision","actor":"local-administrator","at":"2026-09-20T00:00:00Z","principals":[{"id":"oncall","role":"operator","token_sha256":"` + strings.Repeat("a", 64) + `","expires_at":"0001-01-01T00:00:00Z"}]}`
	var historical AuthenticationCommand
	if err := json.Unmarshal([]byte(raw), &historical); err != nil {
		t.Fatal(err)
	}
	if err := historical.validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(historical)
	if err != nil || string(encoded) != raw || historical.digest() != identity("authentication-command/v1/"+raw) {
		t.Fatal("pre-version command or receipt identity changed", err)
	}
}
