package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

const AuthenticationFormatVersion = 1
const MaxAuthenticationPrincipals = 1024

var (
	ErrAuthenticationInvalid       = errors.New("invalid durable authentication policy")
	ErrAuthenticationConflict      = errors.New("authentication epoch or revision conflict")
	ErrAuthenticationResetRequired = errors.New("restored authentication requires explicit stopped local provisioning")
	ErrAuthenticationAdminRequired = errors.New("authentication changes require stopped local administration")
	ErrAuthenticationUnavailable   = errors.New("durable authentication unavailable")
)

// AuthenticationPrincipal contains only a verifier, never a bearer token.
// A zero ExpiresAt preserves an explicitly non-expiring credential. Expiration
// is checked by the authorizer at admission, not by a replay-time clock.
type AuthenticationPrincipal struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	TokenSHA256 string    `json:"token_sha256"`
	Revoked     bool      `json:"revoked,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

type AuthenticationState struct {
	Version           int                       `json:"version"`
	Epoch             string                    `json:"epoch"`
	Revision          string                    `json:"revision"`
	BootstrapConsumed bool                      `json:"bootstrap_consumed"`
	ResetRequired     bool                      `json:"reset_required,omitempty"`
	AnonymousLoopback bool                      `json:"anonymous_loopback,omitempty"`
	Principals        []AuthenticationPrincipal `json:"principals,omitempty"`
	LegacyTokenSHA256 string                    `json:"legacy_token_sha256,omitempty"`
	UpdatedAt         time.Time                 `json:"updated_at"`
	CommandDigest     string                    `json:"command_digest"`
	RestoreID         string                    `json:"restore_id,omitempty"`
}

func (s AuthenticationState) Clone() AuthenticationState {
	s.Principals = slices.Clone(s.Principals)
	return s
}

// AuthenticationCommand replaces one bounded policy conditionally. bootstrap
// is accepted only once; replace and post-restore provision require an
// administrative store. All identity and time inputs originate outside Apply.
// The exact command digest reconciles a lost reply without replaying a change.
type AuthenticationCommand struct {
	// Zero is the exact historical command format. New API submissions select
	// the current lifecycle format before serialization and digest calculation.
	Version           int                       `json:"version,omitempty"`
	Mode              string                    `json:"mode"`
	ExpectedEpoch     string                    `json:"expected_epoch,omitempty"`
	ExpectedRevision  string                    `json:"expected_revision,omitempty"`
	Epoch             string                    `json:"epoch"`
	Revision          string                    `json:"revision"`
	Actor             string                    `json:"actor"`
	At                time.Time                 `json:"at"`
	AnonymousLoopback bool                      `json:"anonymous_loopback,omitempty"`
	Principals        []AuthenticationPrincipal `json:"principals,omitempty"`
	LegacyTokenSHA256 string                    `json:"legacy_token_sha256,omitempty"`
}

func validAuthenticationID(id string) bool {
	if len(id) < 1 || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validVerifier(value string) bool {
	var decoded [32]byte
	if len(value) != 64 {
		return false
	}
	n, err := hex.Decode(decoded[:], []byte(value))
	return n == len(decoded) && err == nil
}

func validateAuthenticationPrincipals(principals []AuthenticationPrincipal, legacy string) error {
	if len(principals) > MaxAuthenticationPrincipals || legacy != "" && !validVerifier(legacy) {
		return ErrAuthenticationInvalid
	}
	ids := make(map[string]bool, len(principals))
	verifiers := make(map[[32]byte]bool, len(principals)+1)
	if legacy != "" {
		var digest [32]byte
		_, _ = hex.Decode(digest[:], []byte(legacy))
		verifiers[digest] = true
	}
	for _, p := range principals {
		if !validAuthenticationID(p.ID) || p.ID == "legacy-read" || (p.Role != "reader" && p.Role != "operator") || !validVerifier(p.TokenSHA256) || ids[p.ID] {
			return ErrAuthenticationInvalid
		}
		if !p.ExpiresAt.IsZero() && (p.ExpiresAt.Year() < 1 || p.ExpiresAt.Year() > 9999) {
			return ErrAuthenticationInvalid
		}
		var digest [32]byte
		_, _ = hex.Decode(digest[:], []byte(p.TokenSHA256))
		if verifiers[digest] {
			return ErrAuthenticationInvalid
		}
		ids[p.ID], verifiers[digest] = true, true
	}
	return nil
}

func (c AuthenticationCommand) validate() error {
	if c.Version != 0 && c.Version != AuthenticationLifecycleFormatVersion || !validAuthenticationID(c.Epoch) || !validAuthenticationID(c.Revision) || !validAuthenticationID(c.Actor) || c.At.IsZero() || c.At.Year() < 1 || c.At.Year() > 9999 {
		return ErrAuthenticationInvalid
	}
	switch c.Mode {
	case "bootstrap":
		if c.ExpectedEpoch != "" || c.ExpectedRevision != "" || len(c.Principals) == 0 && c.LegacyTokenSHA256 == "" && !c.AnonymousLoopback {
			return ErrAuthenticationInvalid
		}
	case "replace", "provision":
		if c.ExpectedEpoch != c.Epoch || !validAuthenticationID(c.ExpectedRevision) || c.ExpectedRevision == c.Revision {
			return ErrAuthenticationInvalid
		}
		if c.Mode == "provision" && len(c.Principals) == 0 && c.LegacyTokenSHA256 == "" {
			return ErrAuthenticationInvalid
		}
	default:
		return ErrAuthenticationInvalid
	}
	if c.AnonymousLoopback && (len(c.Principals) != 0 || c.LegacyTokenSHA256 != "" || c.Mode == "provision") {
		return ErrAuthenticationInvalid
	}
	return validateAuthenticationPrincipals(c.Principals, c.LegacyTokenSHA256)
}

func (c AuthenticationCommand) digest() string {
	data, _ := json.Marshal(authenticationCommandDigestV1(c))
	return identity("authentication-command/v1/" + string(data))
}

func (f *machine) applyAuthentication(c AuthenticationCommand) Result {
	if err := f.validateAuthenticationExtensions(c); err != nil {
		return Result{Err: err}
	}
	digest := c.digest()
	old := f.image.Authentication
	if old != nil && old.Revision == c.Revision && old.CommandDigest == digest {
		copy := old.Clone()
		return Result{Allowed: true, Authentication: &copy}
	}
	if c.Mode == "bootstrap" {
		if old != nil || f.image.Restore != nil {
			return Result{Err: ErrAuthenticationConflict}
		}
	} else {
		if old == nil || old.Epoch != c.ExpectedEpoch || old.Revision != c.ExpectedRevision || c.At.Before(old.UpdatedAt) {
			return Result{Err: ErrAuthenticationConflict}
		}
		if old.ResetRequired != (c.Mode == "provision") || f.restorePending() {
			return Result{Err: ErrAuthenticationResetRequired}
		}
		if c.AnonymousLoopback && !old.AnonymousLoopback {
			return Result{Err: ErrAuthenticationConflict}
		}
	}
	if err := validateAuthenticationLifecycle(old, c); err != nil {
		return Result{Err: err}
	}
	policyVersion := AuthenticationFormatVersion
	if c.Version == AuthenticationLifecycleFormatVersion {
		policyVersion = AuthenticationLifecycleFormatVersion
	}
	next := AuthenticationState{Version: policyVersion, Epoch: c.Epoch, Revision: c.Revision,
		BootstrapConsumed: true, Principals: slices.Clone(c.Principals), LegacyTokenSHA256: c.LegacyTokenSHA256,
		UpdatedAt: c.At, CommandDigest: digest, AnonymousLoopback: c.AnonymousLoopback}
	if old != nil {
		next.RestoreID = old.RestoreID
	}
	slices.SortFunc(next.Principals, func(a, b AuthenticationPrincipal) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	f.image.Version = max(f.image.Version, CatalogFormatVersion)
	if c.Version == AuthenticationLifecycleFormatVersion {
		f.image.Version = max(f.image.Version, CollectionValidationFormatVersion)
	}
	f.image.Authentication = &next
	copy := next.Clone()
	return Result{Allowed: true, Authentication: &copy, Events: []Event{{MonitorID: "authentication", Revision: c.Revision,
		At: c.At, Actor: c.Actor, Type: "authentication_" + c.Mode, Kind: "authentication", Outcome: "committed"}}}
}

func validateAuthenticationImage(i image) error {
	s := i.Authentication
	if s == nil {
		if i.Restore != nil {
			return ErrAuthenticationInvalid
		}
		return nil
	}
	if !catalogFormat(i.Version) || !AuthenticationPolicyFormat(s.Version) ||
		s.Version == AuthenticationLifecycleFormatVersion && i.Version < CollectionValidationFormatVersion || !s.BootstrapConsumed ||
		!validAuthenticationID(s.Epoch) || !validAuthenticationID(s.Revision) || !validVerifier(s.CommandDigest) || s.UpdatedAt.IsZero() ||
		validateAuthenticationPrincipals(s.Principals, s.LegacyTokenSHA256) != nil {
		return ErrAuthenticationInvalid
	}
	for n := 1; n < len(s.Principals); n++ {
		if s.Principals[n-1].ID >= s.Principals[n].ID {
			return ErrAuthenticationInvalid
		}
	}
	if s.ResetRequired && (len(s.Principals) != 0 || s.LegacyTokenSHA256 != "" || s.RestoreID == "" || s.AnonymousLoopback) {
		return ErrAuthenticationInvalid
	}
	if s.AnonymousLoopback && (len(s.Principals) != 0 || s.LegacyTokenSHA256 != "") {
		return ErrAuthenticationInvalid
	}
	if i.Restore == nil && s.RestoreID != "" {
		return ErrAuthenticationInvalid
	}
	return validateRestoreImage(i)
}

func (s *Store) authenticationReadReady() error {
	s.mu.RLock()
	err := s.err
	s.mu.RUnlock()
	select {
	case <-s.stop:
		return ErrAuthenticationUnavailable
	default:
	}
	if err != nil {
		return ErrAuthenticationUnavailable
	}
	return nil
}

// Authentication returns a detached policy including verifier material for the
// startup adapter/local administrator. It is not a public HTTP response model.
// Version zero denotes an uninitialized store, never permission to rebootstrap
// an initialized or explicitly restored authentication authority.
func (s *Store) Authentication() (AuthenticationState, error) {
	if err := s.authenticationReadReady(); err != nil {
		return AuthenticationState{}, err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.err != nil {
		return AuthenticationState{}, ErrAuthenticationUnavailable
	}
	if s.fsm.image.Authentication == nil {
		return AuthenticationState{}, nil
	}
	return s.fsm.image.Authentication.Clone(), nil
}

func (s *Store) CommitAuthentication(ctx context.Context, c AuthenticationCommand) (AuthenticationState, error) {
	if c.Mode != "bootstrap" && !s.administrative {
		return AuthenticationState{}, ErrAuthenticationAdminRequired
	}
	if ctx == nil {
		return AuthenticationState{}, ErrAuthenticationInvalid
	}
	if err := ctx.Err(); err != nil {
		return AuthenticationState{}, err
	}
	if c.Version == 0 {
		// An exact lost response from before the format upgrade still reconciles
		// its original receipt. Do not rewrite that command with a new digest.
		old, err := s.authenticationSnapshot(ctx)
		if err != nil {
			return AuthenticationState{}, err
		}
		if old.Version == AuthenticationFormatVersion && old.Revision == c.Revision && old.CommandDigest == c.digest() {
			return old, nil
		}
		c.Version = AuthenticationLifecycleFormatVersion
	}
	results, err := s.Submit(ctx, []Command{{Kind: "authentication", At: c.At, Authentication: &c}})
	if err != nil {
		return AuthenticationState{}, err
	}
	if len(results) != 1 || results[0].Authentication == nil && results[0].Err == nil {
		return AuthenticationState{}, errors.Join(ErrCommitUnconfirmed, ErrAuthenticationUnavailable)
	}
	if results[0].Err != nil {
		return AuthenticationState{}, results[0].Err
	}
	return results[0].Authentication.Clone(), nil
}
