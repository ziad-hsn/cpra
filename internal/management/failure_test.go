package management

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestFailureCertificateObservationPreservesKnownZero(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	d := (persistence.FailureDiagnostic{Code: "certificate_expiring", CertificateNotAfter: at}).WithDetails()
	v := failureView(d)
	if v.CertificateDaysRemaining == nil || *v.CertificateDaysRemaining != 0 || v.CertificateNotAfter == nil || !v.CertificateNotAfter.Equal(at) {
		t.Fatal("certificate observation lost known zero or expiry time")
	}
	raw, err := json.Marshal(v)
	if err != nil || !strings.Contains(string(raw), `"certificateDaysRemaining":0`) {
		t.Fatal("certificate observation omitted known zero", err)
	}
}
