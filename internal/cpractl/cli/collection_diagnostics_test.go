package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestCollectionValidationDetailsUseOriginalFieldsAndStayBounded(t *testing.T) {
	var input strings.Builder
	input.WriteString("monitors:\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&input, "- name: service-%d\n  pulse_check: {type: http, interval: 60s, timeout: 5s, config: {url: https://example.test}}\n", i)
	}
	frozen, err := collection.Freeze(context.Background(), []collection.Source{collection.Reader("services.yaml", strings.NewReader(input.String()))}, collection.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	page := api.ValidationResultPage{OperationID: sequencedOperationID, NextCursor: "remaining"}
	for i := 0; i < 12; i++ {
		page.Items = append(page.Items, api.ValidationResultItem{Ordinal: int64(i + 1), Issue: "invalidResource", Field: "spec.check.driver.config.url", Reason: "required"})
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	var output bytes.Buffer
	command.SetErr(&output)
	if err := writeCollectionValidationFailure(command, frozen, page); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Count(text, "services.yaml") != 10 || !strings.Contains(text, "pulse_check.config.url") || !strings.Contains(text, "truncated") || !strings.Contains(text, "--validation") {
		t.Fatal("bounded source detail lost", text)
	}
}

func TestCollectionManagementFailureRetainsOrderedInputErrors(t *testing.T) {
	details := &collection.InputErrors{Truncated: true}
	for i := 0; i < 10; i++ {
		details.Details = append(details.Details, &collection.SourceError{Location: collection.Location{Source: "config.yaml", Document: 1, Item: i + 1, Line: i + 1, Column: 1}, Field: "pulse_check.interval", Cause: api.InvalidField("spec.check.interval", "positiveDuration")})
	}
	err := managementFailure(details)
	if err == nil || !strings.Contains(err.Error(), "pulse_check.interval") || !strings.Contains(err.Error(), "truncated") || !errors.Is(err, details) {
		t.Fatal("typed details lost", err)
	}
}
