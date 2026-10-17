package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func TestManagementValidationReadsOneBoundedPage(t *testing.T) {
	at := time.Now().UTC()
	page := api.ValidationResultPage{OperationID: sequencedOperationID, IdentityFormat: commitment.Format, ContentDigest: strings.Repeat("a", 64), ItemCount: 2,
		Summary: api.ValidationResultSummary{ResultID: "validation", Valid: false, Issue: "invalidResource", Count: 2, Digest: strings.Repeat("b", 64), CapabilitiesDigest: strings.Repeat("c", 64), FinalizedAt: at, ExpiresAt: at.Add(time.Hour)},
		Items:   []api.ValidationResultItem{{Ordinal: 1, Kind: "Monitor", ID: "service", Source: "source.00000000000000000001", SourceDocument: 2, SourceItem: 3, Issue: "invalidResource", Field: "spec.check.driver.config.url", Reason: "required"}}, NextCursor: "next-page"}
	var count atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+sequencedOperationID+"/validation" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("cursor") != "" {
			t.Error("validation read changed the operation or page")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})
	stdout, stderr, err := fixture.run(t, nil, "get", "operation", sequencedOperationID, "--validation", "--limit", "1")
	if err != nil || count.Load() != 1 || !strings.Contains(stdout, page.Items[0].Field) || !strings.Contains(stdout, api.ValidationMessage("required")) || !strings.Contains(stdout, "2/3") || stderr != "Next cursor: next-page\n" {
		t.Fatal("bounded validation diagnostics failed", stdout, stderr, err, count.Load())
	}
	for _, args := range [][]string{
		{"get", "operation", "--validation"},
		{"get", "operation", sequencedOperationID, "--validation", "--results"},
		{"get", "operation", sequencedOperationID, "--validation", "--limit", "501"},
	} {
		if _, _, err := fixture.run(t, nil, args...); err == nil {
			t.Fatal("invalid validation read accepted", args)
		}
	}
	if count.Load() != 1 {
		t.Fatal("invalid read made a request")
	}
}

func TestManagementValidationLocalErrorIsActionableAndSafe(t *testing.T) {
	err := managementFailure(api.InvalidField("spec.check.driver.config.url", "required"))
	if err == nil || !strings.Contains(err.Error(), "spec.check.driver.config.url") || !strings.Contains(err.Error(), api.ValidationMessage("required")) {
		t.Fatal("local SDK validation lost its field", err)
	}
}

func TestManagementCreateKeepsOriginalInputLocation(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input contacted server") })
	input := "apiVersion: cpra.io/v2\nkind: Monitor\nmetadata: {id: service}\nspec:\n  check:\n    interval: 1s\n    timeout: 1s\n    driver:\n      type: http\n      config:\n        url: PRIVATE_INVALID_VALUE\n"
	_, _, err := fixture.run(t, []byte(input), "create", "monitor", "-f", "-")
	if err == nil || !strings.Contains(err.Error(), "stdin") || !strings.Contains(err.Error(), "line 11") || !strings.Contains(err.Error(), "spec.check.driver.config.url") || strings.Contains(err.Error(), "PRIVATE_INVALID_VALUE") {
		t.Fatal("create lost safe source coordinates", err)
	}
}
