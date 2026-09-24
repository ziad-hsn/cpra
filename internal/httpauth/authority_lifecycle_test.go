package httpauth

import (
	"errors"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestAuthenticationPolicyVersionsPreserveOrdinaryRequests(t *testing.T) {
	for _, version := range []int{persistence.AuthenticationFormatVersion, persistence.AuthenticationLifecycleFormatVersion} {
		state := persistence.AuthenticationState{Version: version, BootstrapConsumed: true, Principals: []persistence.AuthenticationPrincipal{
			{ID: "operator", Role: Operator, TokenSHA256: verifier(t, operatorToken)},
			{ID: "revoked", Role: Operator, TokenSHA256: verifier(t, readerToken), Revoked: true}}}
		a, err := FromAuthentication(state, nil)
		if err != nil {
			t.Fatal("readable policy rejected", version, err)
		}
		if _, err := a.Authorize(requestFixture("POST", "/api/v2/monitors", operatorToken), "CreateMonitor"); err != nil {
			t.Fatal(version, err)
		}
		if _, err := a.Authorize(requestFixture("GET", "/api/v2/self", readerToken), "GetAccess"); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("revoked policy admitted", version, err)
		}
	}
	if _, err := FromAuthentication(persistence.AuthenticationState{Version: 99, BootstrapConsumed: true}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("unknown policy accepted", err)
	}
}
