package localadmin

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestAuthenticationCurrentCommandPreservesLegacyPrincipalLifecycle(t *testing.T) {
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	legacy := persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: "epoch", Revision: "revision", BootstrapConsumed: true,
		Principals: []persistence.AuthenticationPrincipal{{ID: "oncall", Role: "operator", TokenSHA256: "fixture-live-verifier"},
			{ID: "retired", Role: "operator", TokenSHA256: "fixture-revoked-verifier", Revoked: true}}}
	original := legacy.Clone()
	c, index, err := prepareAuthenticationCommand(legacy, AuthenticationRequest{Action: "rotate", PrincipalID: "oncall"}, at)
	if err != nil || index != 0 || c.Version != persistence.AuthenticationLifecycleFormatVersion || !reflect.DeepEqual(c.Principals, legacy.Principals) {
		t.Fatal("rotation did not explicitly preserve lifecycle", err)
	}
	c.Principals[0].TokenSHA256 = "new-private-verifier"
	if !reflect.DeepEqual(legacy, original) {
		t.Fatal("rotation preparation changed caller policy")
	}
	for _, action := range []string{"issue", "rotate", "revoke"} {
		_, _, err := prepareAuthenticationCommand(legacy, AuthenticationRequest{Action: action, PrincipalID: "retired", Role: "operator"}, at)
		if !errors.Is(err, ErrAuthenticationState) {
			t.Fatal("retired identity reused", action, err)
		}
	}
}
