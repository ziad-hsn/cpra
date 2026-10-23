package manifestcheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManifestMissingBlueIsAdvisory(t *testing.T) {
	for _, test := range []struct {
		name, codes string
		want        int
	}{
		{"absent", "", 1},
		{"disabled", "    codes: {blue: {notify: log, dispatch: false, config: {}}}\n", 1},
		{"configured", "    codes: {blue: {notify: log, config: {}}}\n", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := "monitors:\n  - id: service\n    name: Service\n    pulse_check: {type: tcp, interval: 1m, timeout: 1s, config: {host: localhost, port: 80}}\n    intervention: {action: webhook, target: {url: https://fixture.invalid/recover}}\n" + test.codes
			path := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			var warnings []api.FieldError
			if err := Manifest(t.Context(), path, false, false, false, func(w api.FieldError) { warnings = append(warnings, w) }); err != nil {
				t.Fatal("advisory invalidated configuration", err)
			}
			if len(warnings) != test.want {
				t.Fatal("warning count", warnings)
			}
			if len(warnings) > 0 && (warnings[0].Field != "monitors[0].codes.blue" || warnings[0].Reason != "unknownRecoveryDestination" || warnings[0].Message == "") {
				t.Fatal("warning lost field/cause", warnings)
			}
		})
	}
}
