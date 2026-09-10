package verification

import (
	"context"
	"testing"
)

func TestUnconfiguredInventoryNeverPasses(t *testing.T) {
	r, err := Run(context.Background(), Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || len(r.Records) != 33 {
		t.Fatal(r)
	}
	counts := map[string]int{}
	for _, row := range r.Records {
		counts[row.Kind]++
		if row.Status != "not_configured" || row.Accepted || row.Observed {
			t.Fatal(row)
		}
	}
	if counts["pulse"] != 14 || counts["intervention"] != 5 || counts["code"] != 14 {
		t.Fatal(counts)
	}
}
func TestConfiguredOperationsRequireExplicitLiveInvocation(t *testing.T) {
	_, err := Run(context.Background(), Config{Cases: []Case{{Kind: "intervention", Driver: "aws", Configured: true}}}, false)
	if err == nil {
		t.Fatal("live operation implicitly enabled")
	}
}

func TestRetainedEvidenceDoesNotContainArbitraryProviderText(t *testing.T) {
	data := redactedObservation([]byte(`{"observed":true,"count":2,"token":"secret","url":"https://user:password@example.com","before":"credentials","resource_digest":"invalid"}`))
	if len(data) != 3 || data["observed"] != true || data["count"] != float64(2) {
		t.Fatal(data)
	}
}
