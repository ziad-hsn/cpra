package httpserver

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionValidationReadRefreshesObservation(t *testing.T) {
	for _, advances := range []bool{true, false} {
		name := "unchanged-clock"
		if advances {
			name = "completion-during-read"
		}
		t.Run(name, func(t *testing.T) {
			f := newManagementFixture(t, true)
			validationHTTPAuthority(t, f)
			head := validationHTTPOperation(t, f, preflightMonitor("service", "https://unused.example.test"))
			path := "/api/v2/operations/" + head.ID + "/validation"
			response, _ := f.request(t, http.MethodPost, "/api/v2/operations/"+head.ID+"/validate", managementOperatorToken, nil, nil)
			if response.StatusCode != http.StatusAccepted {
				t.Fatal("validation admission", response.StatusCode)
			}
			validationHTTPWorker(t, f)
			first := validationHTTPResult(t, f, path)
			before := first.Summary.FinalizedAt.Add(-time.Nanosecond)
			after := first.Summary.FinalizedAt.Add(time.Millisecond)
			var calls atomic.Int64
			f.server.managementHTTP.now = func() time.Time {
				if calls.Add(1) == 1 || !advances {
					return before
				}
				return after
			}
			response, body := f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
			if !advances {
				var problem api.Problem
				if response.StatusCode != http.StatusServiceUnavailable || json.Unmarshal(body, &problem) != nil || problem.Code != "unavailable" || calls.Load() != 2 {
					t.Fatal("stale clock published a result or retried without a bound", response.StatusCode, calls.Load())
				}
				return
			}
			var page api.ValidationResultPage
			if response.StatusCode != http.StatusOK || api.DecodeResponse(body, &page) != nil || page.Summary != first.Summary || calls.Load() != 3 {
				t.Fatal("new completion changed the original result", response.StatusCode, calls.Load())
			}
		})
	}
}
