package httpserver

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestNonAdmissionMarkerRequiresProofBeforeSubmission(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		proof bool
	}{
		{"guard", mutationUnavailable(), true},
		{"same problem without proof", managementFailure(503, "admissionUnavailable", "This request was not admitted."), false},
		{"uncertain durable write", errors.Join(persistence.ErrCommitUnconfirmed, mutationUnavailable()), false},
		{"uncertain catalog write", errors.Join(management.ErrOutcomeUnconfirmed, mutationUnavailable()), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeManagementError(w, test.err)
			var problem api.Problem
			if json.Unmarshal(w.Body.Bytes(), &problem) != nil || w.Code != 503 {
				t.Fatal("invalid problem response")
			}
			if (w.Header().Get("X-CPRa-Admission") == "not-submitted") != test.proof {
				t.Fatal("non-admission marker was inferred from prose or survived uncertain submission")
			}
			if test.proof && (problem.Code != "admissionUnavailable" || w.Header().Get("Retry-After") != "5") {
				t.Fatal("proven rejection omitted its exact SDK contract")
			}
		})
	}
}
