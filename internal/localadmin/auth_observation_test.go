package localadmin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestRestoredObservationAuthorityUsesFreshTokenAndEpoch(t *testing.T) {
	directory, output := authTestPaths(t)
	f := &authStoreFixture{state: persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: "restored-epoch", Revision: "reset-revision", ResetRequired: true, BootstrapConsumed: true, LegacyTokenSHA256: strings.Repeat("a", 64), Principals: []persistence.AuthenticationPrincipal{{ID: "old-operator", Role: "operator", TokenSHA256: strings.Repeat("b", 64)}}}}
	r := AuthenticationRequest{Action: "reprovision", Mode: "observation", DataDirectory: directory, TokenOutput: output}
	for _, bad := range []AuthenticationRequest{
		{Action: r.Action, Mode: r.Mode, DataDirectory: directory, TokenOutput: output, PrincipalID: "unexpected"},
		{Action: r.Action, Mode: r.Mode, DataDirectory: directory, TokenOutput: output, Role: "operator"},
		{Action: r.Action, Mode: r.Mode, DataDirectory: directory, TokenOutput: output, ExpiresAt: new(time.Time)},
	} {
		if _, err := administerAuthentication(t.Context(), bad, f.open); !errors.Is(err, ErrAuthenticationInput) || f.opened != 0 {
			t.Fatal("invalid mode request opened storage", err)
		}
	}
	report, err := administerAuthentication(t.Context(), r, f.open)
	if err != nil || report.Epoch != "restored-epoch" || report.ResetRequired || !report.LegacyReadEnabled || len(report.Principals) != 0 {
		t.Fatal("observation reprovision", report, err)
	}
	c := f.commands[0]
	if c.ExpectedEpoch != "restored-epoch" || c.ExpectedRevision != "reset-revision" || c.Mode != "provision" || len(c.Principals) != 0 || c.LegacyTokenSHA256 == strings.Repeat("a", 64) {
		t.Fatal("restore authority was replaced or resurrected")
	}
}
