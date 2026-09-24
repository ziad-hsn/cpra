package httpauth

import (
	"errors"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestExpirationRecheckedAtDelayedAdmission(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	config := configFixture(t)
	config.Principals[1].ExpiresAt = now.Add(time.Second)
	a := newFixture(t, config)
	a.now = func() time.Time { return now }
	r := requestFixture("POST", "/api/v2/monitors", operatorToken)
	if _, err := a.Authorize(r, "CreateMonitor"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	called := false
	err := a.WithAdmission(r, "CreateMonitor", func(api.AccessInfo) error { called = true; return nil })
	if !errors.Is(err, ErrUnauthorized) || called {
		t.Fatal("expired observation remained an admission grant", err)
	}
	if _, err := a.Authorize(requestFixture("GET", "/api/v2/self", readerToken), "GetAccess"); err != nil {
		t.Fatal("non-expiring reader rejected", err)
	}
}

func TestCommittedEmptyAuthorityDeniesAndPolicyExpiryParses(t *testing.T) {
	state := persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, BootstrapConsumed: true}
	a, err := FromAuthentication(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authorize(requestFixture("GET", "/api/v2/self", legacyToken), "GetAccess"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	state.ResetRequired = true
	if _, err := FromAuthentication(state, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("restore reset accepted", err)
	}
	config, err := ParsePolicy([]byte("principals:\n  - id: oncall\n    role: operator\n    token_sha256: " + verifier(t, operatorToken) + "\n    expires_at: 2027-01-01T00:00:00Z\n"))
	if err != nil || config.Principals[0].ExpiresAt.Year() != 2027 {
		t.Fatal("expiry missing", err)
	}
}
