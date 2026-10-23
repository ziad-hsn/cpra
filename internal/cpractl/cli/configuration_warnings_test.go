package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestCollectionWarningOutputIsBoundedAndSourceAttributed(t *testing.T) {
	var input strings.Builder
	input.WriteString("monitors:\n")
	for i := range 12 {
		fmt.Fprintf(&input, "- name: service-%d\n  pulse_check: {type: tcp, interval: 60s, timeout: 5s, config: {host: localhost, port: 80}}\n  intervention: {action: webhook, target: {url: https://fixture.invalid/recover}}\n", i)
	}
	frozen, err := collection.Freeze(t.Context(), []collection.Source{collection.Reader("services.yaml", strings.NewReader(input.String()))}, collection.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	command := &cobra.Command{}
	command.SetContext(t.Context())
	var output bytes.Buffer
	command.SetErr(&output)
	if err := writeCollectionWarnings(command, frozen); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "warning: services.yaml") != 10 || !strings.Contains(output.String(), "Additional configuration warnings omitted") || !strings.Contains(output.String(), "blue notification destination") {
		t.Fatal(output.String())
	}
}

func TestNotificationConditionDescriptionKeepsPendingSeparateFromOutcome(t *testing.T) {
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if err := writeNotificationConditions(command, []api.NotificationCondition{{ConditionID: "closure", Kind: "resolution", Color: "green", State: "pending", Reason: "maintenance", PendingAt: &at, RecoveredAt: &at}, {ConditionID: "held", Kind: "migration", Color: "red", State: "held", OriginalActionID: "original-action", MigrationReason: "ambiguous_binding"}}, true); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"execution outcomes remain", "EARLIER HEALTH RECOVERY", "pending", "held", "ambiguous_binding", "2026-10-22T12:00:00Z", "Additional retained conditions"} {
		if !strings.Contains(output.String(), text) {
			t.Fatal("missing", text, output.String())
		}
	}
	if strings.Contains(output.String(), "0001-") {
		t.Fatal("fabricated timestamp", output.String())
	}
}

func TestNotificationConditionSequencePreservesExactProgress(t *testing.T) {
	for _, value := range []string{"0", "9007199254740993", "18446744073709551615"} {
		if got := notificationConditionSequence(value); got != value {
			t.Fatal("valid exact decimal progress changed", value, got)
		}
	}
	for _, value := range []string{"", "01", "+1", "1\x1b[31m", "18446744073709551616", strings.Repeat("9", 128)} {
		if got := notificationConditionSequence(value); got != "Unavailable" {
			t.Fatal("invalid progress displayed", got)
		}
	}
}
