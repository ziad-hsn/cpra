package collection

import (
	"context"
	"strings"
	"testing"
)

func TestIncompleteFinalDriverPreventsSubmission(t *testing.T) {
	last := `{"apiVersion":"cpra.io/v2","kind":"NotificationEndpoint","metadata":{"id":"missing-destination"},"spec":{"type":"discord","config":{}}}`
	for _, preflight := range []bool{false, true} {
		frozen, err := Freeze(context.Background(), []Source{Reader("service.json", strings.NewReader(monitor("first"))), Reader("notifications.json", strings.NewReader(last))}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = frozen.Close() })
		operations := &fakeOperations{valid: true}
		if preflight {
			_, err = Preflight(context.Background(), operations, frozen)
		} else {
			_, err = Apply(context.Background(), operations, frozen)
		}
		if err == nil || !strings.Contains(err.Error(), "notifications.json") || !strings.Contains(err.Error(), "webhookUrl") {
			t.Fatalf("missing source-attributed settings error: %v", err)
		}
		if len(operations.calls) != 0 {
			t.Fatal("incomplete final input submitted earlier resources", operations.calls)
		}
	}
}
