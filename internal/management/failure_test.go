package management

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestActionFailureObservationPreservesSafeCauseAndOutcome(t *testing.T) {
	failure := persistence.NewFailureDiagnostic("deadline_exceeded")
	cause := persistence.NewFailureDiagnostic("dns_failed")
	action := actionView(persistence.ActionRecord{MonitorID: "monitor", Action: persistence.Action{ID: "action", Kind: "code", State: persistence.Unknown, Outcome: "external_outcome_unknown", Failure: failure, Cause: cause}})
	if action.Failure == nil || action.Failure.Code != failure.Code || action.Failure.Message != failure.Message || action.Cause == nil || action.Cause.Code != cause.Code || action.State != "unknown" {
		t.Fatal("observation lost safe diagnostic or changed outcome")
	}
	if failureView(persistence.FailureDiagnostic{}) != nil {
		t.Fatal("unavailable diagnostic invented")
	}
}
