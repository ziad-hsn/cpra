package api

import (
	"testing"
	"time"
)

func TestCertificateFailureDetailsRemainCanonical(t *testing.T) {
	base, err := CanonicalFailure(FailureTaxonomyVersion, "certificate_expiring", "tls")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		date, days bool
		valid      bool
	}{
		{"none", false, false, true}, {"date-only", true, false, false}, {"days-only", false, true, false}, {"both", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			if tc.date {
				at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
				o.CertificateNotAfter = &at
			}
			if tc.days {
				o.CertificateDaysRemaining = Pointer(0)
			}
			if err := ValidateFailureObservation("tls", o); (err == nil) != tc.valid {
				t.Fatal("certificate detail pairing", err)
			}
		})
	}
}
