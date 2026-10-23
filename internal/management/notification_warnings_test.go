package management

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestStartupValidationBoundsMissingBlueWarnings(t *testing.T) {
	resources := make([]api.Resource, 0, 12)
	for i := range 12 {
		resources = append(resources, resource("Monitor", fmt.Sprintf("warning-%d", i), api.MonitorSpec{
			Check:    api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "tcp", Config: json.RawMessage(`{"host":"localhost","port":80}`)}},
			Recovery: &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"url":"https://fixture.invalid/recover"}`)}},
		}))
	}
	result, err := ValidateStartupSource(t.Context(), startupTestOptions(filepath.Join(t.TempDir(), "must-stay-closed"), resources...))
	if err != nil || result.Monitors != 12 || len(result.Warnings) != 10 || !result.WarningsTruncated {
		t.Fatal("bounded advisories", result, err)
	}
	for _, warning := range result.Warnings {
		if warning.Reason != "unknownRecoveryDestination" || warning.Message == "" || warning.Field == "" {
			t.Fatal("warning lost safe classification", warning)
		}
	}
}
