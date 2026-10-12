//go:build teams

package drivertest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestTeamsReportDistinguishesHTTPStatusFromThrottleCode(t *testing.T) {
	cfg, _ := localHTTPScenario(t)
	var invoked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		invoked.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Microsoft Teams endpoint returned HTTP error 429")
	}))
	t.Cleanup(target.Close)
	cfg.Manifest.Monitors[0].Codes = manifest.Codes{"red": {Notify: "teams", Dispatch: true, Config: &manifest.CodeNotificationTeams{WebhookURL: target.URL}}}
	cfg.Cases[0].Kind, cfg.Cases[0].Driver, cfg.Cases[0].Color = "code", "teams", "red"
	r, err := Run(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Records {
		if row.Kind == "code" && row.Name == "teams" {
			if row.Status != "fail" || row.HTTPStatus != http.StatusOK || row.RejectionCode != http.StatusTooManyRequests || row.Outcome != "rejected" || !row.Invoked || row.Accepted || row.Observed || invoked.Load() != 1 {
				t.Fatal("logical throttle was reported as received HTTP status or repeated", row, invoked.Load())
			}
			return
		}
	}
	t.Fatal("configured Teams case missing from the report")
}
