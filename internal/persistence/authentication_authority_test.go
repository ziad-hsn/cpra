package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func authorityFixture(t *testing.T) (*Store, AuthenticationState, OperatorAuthority) {
	t.Helper()
	s := openCatalogMemory(t)
	c := authenticationBootstrap()
	state, err := s.CommitAuthentication(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := s.ObserveOperatorAuthority(context.Background(), "oncall", c.At)
	if err != nil {
		t.Fatal(err)
	}
	return s, state, authority
}

func TestOperatorAuthorityObservationAndExactRevisionFence(t *testing.T) {
	s, state, authority := authorityFixture(t)
	before := s.Status().CommittedIndex
	encoded, err := json.Marshal(authority)
	if err != nil || bytes.Contains(encoded, []byte(state.Principals[0].TokenSHA256)) || bytes.Contains(encoded, []byte(authenticationToken)) {
		t.Fatal("authority retained credential material", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 3 || fields["actor"] != "oncall" || fields["epoch"] != state.Epoch || fields["revision"] != state.Revision {
		t.Fatal("unexpected authority fields", err)
	}
	if s.Status().CommittedIndex != before {
		t.Fatal("observation changed durable state")
	}
	rotation := lifecycleReplacement(state)
	rotation.Principals[0].TokenSHA256 = authenticationVerifier("rotation")
	updated := lifecycleCommand(t, s, CollectionValidationFormatVersion, rotation)
	if updated.Err != nil {
		t.Fatal(updated.Err)
	}
	if err := s.fsm.checkOperatorAuthority(authority, rotation.At); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("stale authority survived credential rotation", err)
	}
	fresh, err := s.ObserveOperatorAuthority(context.Background(), "oncall", rotation.At)
	if err != nil || fresh.Actor != authority.Actor || fresh.Epoch != authority.Epoch || fresh.Revision == authority.Revision {
		t.Fatal(err)
	}
	if err := s.fsm.checkOperatorAuthority(fresh, rotation.At); err != nil {
		t.Fatal(err)
	}
	// Changing another principal also invalidates the captured policy revision.
	other := lifecycleReplacement(*updated.Authentication)
	other.Principals = append(other.Principals, AuthenticationPrincipal{ID: "reader", Role: "reader", TokenSHA256: authenticationVerifier("reader")})
	if got := lifecycleCommand(t, s, CollectionValidationFormatVersion, other); got.Err != nil {
		t.Fatal(got.Err)
	}
	if err := s.fsm.checkOperatorAuthority(fresh, other.At); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("policy revision fence ignored", err)
	}
}

func TestOperatorAuthorityRejectsIneligiblePolicyAndSuppliedTimes(t *testing.T) {
	s, original, authority := authorityFixture(t)
	for name, mutate := range map[string]func(*AuthenticationState, *OperatorAuthority, *time.Time){
		"historical policy": func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) {
			s.Version = AuthenticationFormatVersion
		},
		"unknown policy":         func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) { s.Version = 99 },
		"bootstrap not consumed": func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) { s.BootstrapConsumed = false },
		"anonymous":              func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) { s.AnonymousLoopback = true },
		"revoked":                func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) { s.Principals[0].Revoked = true },
		"reader":                 func(s *AuthenticationState, _ *OperatorAuthority, _ *time.Time) { s.Principals[0].Role = "reader" },
		"absent":                 func(_ *AuthenticationState, a *OperatorAuthority, _ *time.Time) { a.Actor = "missing" },
		"legacy":                 func(_ *AuthenticationState, a *OperatorAuthority, _ *time.Time) { a.Actor = "legacy-read" },
		"malformed actor":        func(_ *AuthenticationState, a *OperatorAuthority, _ *time.Time) { a.Actor = "bad actor" },
		"expiration boundary":    func(s *AuthenticationState, _ *OperatorAuthority, at *time.Time) { *at = s.Principals[0].ExpiresAt },
		"expired": func(s *AuthenticationState, _ *OperatorAuthority, at *time.Time) {
			*at = s.Principals[0].ExpiresAt.Add(time.Second)
		},
		"zero time": func(_ *AuthenticationState, _ *OperatorAuthority, at *time.Time) { *at = time.Time{} },
		"before policy observation": func(s *AuthenticationState, _ *OperatorAuthority, at *time.Time) {
			*at = s.UpdatedAt.Add(-time.Nanosecond)
		},
		"invalid year": func(_ *AuthenticationState, _ *OperatorAuthority, at *time.Time) {
			*at = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, a, at := original.Clone(), authority, original.UpdatedAt
			mutate(&state, &a, &at)
			s.fsm.mu.Lock()
			s.fsm.image.Authentication = &state
			s.fsm.mu.Unlock()
			if err := s.fsm.checkOperatorAuthority(a, at); !errors.Is(err, ErrOperatorAuthorityDenied) {
				t.Fatal("ineligible authority accepted", err)
			}
			got, err := s.ObserveOperatorAuthority(context.Background(), a.Actor, at)
			if !errors.Is(err, ErrOperatorAuthorityDenied) || got != (OperatorAuthority{}) {
				t.Fatal("failed observation returned authority", err)
			}
		})
	}
	s.fsm.mu.Lock()
	s.fsm.image.Authentication = &original
	s.fsm.mu.Unlock()
	for _, at := range []time.Time{original.UpdatedAt, original.Principals[0].ExpiresAt.Add(-time.Nanosecond)} {
		if err := s.fsm.checkOperatorAuthority(authority, at); err != nil {
			t.Fatal("valid caller time rejected", err)
		}
	}
	s.fsm.mu.Lock()
	original.Principals[0].ExpiresAt = time.Time{}
	s.fsm.mu.Unlock()
	if err := s.fsm.checkOperatorAuthority(authority, original.UpdatedAt.Add(100*24*time.Hour)); err != nil {
		t.Fatal("explicit nonexpiring policy rejected", err)
	}
}

type authenticationWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *authenticationWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestOperatorAuthorityCancellationWhileOwnershipLockHeld(t *testing.T) {
	for _, ownership := range []string{"store", "fsm"} {
		t.Run(ownership, func(t *testing.T) {
			s, state, _ := authorityFixture(t)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &authenticationWaitingContext{Context: base, waiting: make(chan struct{})}
			lock := &s.mu
			if ownership == "fsm" {
				lock = &s.fsm.mu
			}
			lock.Lock()
			defer lock.Unlock()
			completed := make(chan error, 1)
			go func() { _, err := s.ObserveOperatorAuthority(ctx, "oncall", state.UpdatedAt); completed <- err }()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("did not reach contended lock")
			}
			cancel()
			select {
			case err := <-completed:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled authority blocked on ownership lock")
			}
		})
	}
}

func TestOperatorAuthorityResetStorageAndImmutableObservation(t *testing.T) {
	s, state, authority := authorityFixture(t)
	before := state.Clone()
	for range 20 {
		got, err := s.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt)
		if err != nil || got != authority {
			t.Fatal(err)
		}
	}
	after, _ := s.Authentication()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("authority read mutated policy")
	}
	s.fsm.mu.Lock()
	s.fsm.image.Authentication.ResetRequired = true
	s.fsm.mu.Unlock()
	if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt); !errors.Is(err, ErrAuthenticationResetRequired) {
		t.Fatal(err)
	}
	s.fsm.mu.Lock()
	s.fsm.image.Authentication = nil
	s.fsm.mu.Unlock()
	if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt); !errors.Is(err, ErrOperatorAuthorityDenied) {
		t.Fatal(err)
	}
	s.fsm.mu.Lock()
	s.fsm.err = errors.New("private-storage-diagnostic")
	s.fsm.mu.Unlock()
	if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt); !errors.Is(err, ErrAuthenticationUnavailable) || err.Error() == "private-storage-diagnostic" {
		t.Fatal(err)
	}
	s.fsm.mu.Lock()
	s.fsm.err = nil
	s.fsm.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt); !errors.Is(err, ErrAuthenticationUnavailable) {
		t.Fatal(err)
	}
}
