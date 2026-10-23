package collection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestDecodeInputErrorsRemainOrderedBoundedAndSourceAttributed(t *testing.T) {
	var source strings.Builder
	source.WriteString("\ufeffmonitors:\n")
	for n := 0; n < 12; n++ {
		fmt.Fprintf(&source, "- name: service-%d\n  pulse_check: {type: http, interval: invalid-secret-%d, config: {url: https://example.test}}\n", n, n)
	}
	emitted := 0
	err := Decode(context.Background(), strings.NewReader(source.String()), DecodeOptions{SourceName: "services.yaml"}, func(Item) error { emitted++; return nil })
	var details *InputErrors
	if !errors.As(err, &details) || len(details.Details) != 10 || !details.Truncated || emitted != 0 {
		t.Fatalf("bounded errors missing: %v (%d emissions)", err, emitted)
	}
	previous := 0
	for _, detail := range details.Details {
		if detail.Location.Source != "services.yaml" || detail.Location.Line <= previous || detail.Location.Column < 1 {
			t.Fatal("source order lost", detail)
		}
		previous = detail.Location.Line
		if !strings.Contains(detail.Error(), "pulse_check.interval") || strings.Contains(detail.Error(), "invalid-secret") {
			t.Fatal("original field or secret handling failed", detail)
		}
	}
	var field *api.ValidationError
	if !errors.As(err, &field) || field.Field != "spec.check.interval" {
		t.Fatal("typed canonical cause lost", err)
	}
}

func TestRejectedApplyRetainsFirstValidationPageAndRejectedPhase(t *testing.T) {
	frozen := freezeTest(t, monitor("a"))
	operations := &fakeOperations{valid: false}
	result, err := Apply(context.Background(), operations, frozen)
	if !errors.Is(err, ErrPreflightRejected) || result.OperationID != "epoch.1" || result.Operation.State != "rejected" || result.ValidationPage == nil {
		t.Fatal("validation receipt or phase lost", err, result)
	}
	if strings.Contains(strings.Join(operations.calls, ","), "activate") {
		t.Fatal("rejected input activated")
	}
}

func TestYAMLAliasBudgetAppliesBeforeExpansion(t *testing.T) {
	for _, input := range []string{"first: &value {nested: text}\nsecond: *value\n", "first: &value [*value]\n"} {
		var anchors YAMLAnchors
		if _, err := anchors.ParseBounded([]byte(input), nil, 1); err == nil {
			t.Fatal("alias expansion ignored remaining byte budget")
		}
	}
	var anchors YAMLAnchors
	if _, err := anchors.ParseBounded([]byte("first: scalar\n"), nil, 0); err != nil {
		t.Fatal("no expansion consumed alias budget", err)
	}
	if _, err := anchors.Parse([]byte("first: &value {nested: text}\n"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := anchors.ParseBounded([]byte("second: *value\n"), nil, 1); err == nil {
		t.Fatal("cross-fragment alias ignored remaining bytes")
	}
}

func TestDecodeNormalizedByteQuotaPrecedesStagingCallback(t *testing.T) {
	input := "monitors:\n- name: first\n  pulse_check: {type: tcp, interval: 60s, timeout: 5s, config: {host: localhost, port: 80}}\n"
	visited := 0
	err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{MaxBytes: int64(len(input))}, func(Item) error { visited++; return nil })
	if err == nil || visited != 0 || !strings.Contains(err.Error(), "byte quota") {
		t.Fatal("normalized output exceeded quota before callback", err, visited)
	}
}

func TestDecodeJSONBOMMatchesYAMLSourceContract(t *testing.T) {
	for _, prefix := range []string{"", "\ufeff"} {
		seen := 0
		err := Decode(context.Background(), strings.NewReader(prefix+monitor("a")), DecodeOptions{SourceName: "services.json"}, func(item Item) error {
			if item.Resource.Metadata.ID != "a" || item.Location.Source != "services.json" {
				t.Fatal("source contract changed", item.Location)
			}
			seen++
			return nil
		})
		if err != nil || seen != 1 {
			t.Fatal("JSON BOM acceptance differs", err, seen)
		}
	}
}
