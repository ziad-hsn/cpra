package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func writeCollectionWarnings(cmd *cobra.Command, frozen *collection.Frozen) error {
	shown := 0
	truncated := errors.New("warning display limit")
	err := frozen.Range(cmd.Context(), func(item collection.Item) error {
		defer clear(item.Resource.Spec)
		defer clear(item.Resource.Status)
		for _, warning := range api.ResourceWarnings(item.Resource) {
			if shown == 10 {
				return truncated
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s %s: %s\n", managementDisplay(item.Location.String()), api.SafeValidationField(warning.Field), api.ValidationMessage(warning.Reason)); err != nil {
				return err
			}
			shown++
		}
		return nil
	})
	if errors.Is(err, truncated) {
		_, err = fmt.Fprintln(cmd.ErrOrStderr(), "Additional configuration warnings omitted; inspect individual monitor configurations.")
	}
	return err
}

func configurationWarnings(warnings []api.FieldError) string {
	var messages []string
	for _, warning := range warnings[:min(len(warnings), 10)] {
		if message := api.ValidationMessage(warning.Reason); message != "" {
			messages = append(messages, message)
		}
	}
	if len(warnings) > 10 {
		messages = append(messages, "Additional configuration warnings omitted.")
	}
	if len(messages) == 0 {
		return "none reported"
	}
	return strings.Join(messages, "; ")
}

func observationBool(value *bool) string {
	if value == nil {
		return "Unavailable"
	}
	if *value {
		return "true"
	}
	return "false"
}

func writeNotificationConditions(cmd *cobra.Command, conditions []api.NotificationCondition, truncated bool) error {
	if len(conditions) == 0 && !truncated {
		return nil
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "\nNotification conditions (execution outcomes remain on their original actions):"); err != nil {
		return err
	}
	rows := make([][]string, 0, min(len(conditions), 100))
	for _, condition := range conditions[:min(len(conditions), 100)] {
		reason := condition.Reason
		if condition.MigrationReason != "" {
			reason = condition.MigrationReason
		}
		rows = append(rows, []string{managementDisplay(condition.ConditionID), managementDisplay(condition.Kind), managementDisplay(condition.Color), managementDisplay(condition.State), observationTimestamp(condition.PendingAt), observationTimestamp(condition.RecoveredAt), managementDisplay(reason), managementDisplay(condition.OriginalActionID)})
	}
	if err := table(cmd.OutOrStdout(), []string{"CONDITION", "KIND", "COLOR", "STATE", "PENDING SINCE", "EARLIER HEALTH RECOVERY", "REASON", "ORIGINAL ACTION"}, rows); err != nil {
		return err
	}
	if truncated || len(conditions) > 100 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Additional retained conditions may be omitted or unavailable; inspect monitor history and action records.")
		return err
	}
	return nil
}

func notificationConditionSequence(value string) string {
	if value == "" || len(value) > 20 {
		return "Unavailable"
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value {
		return "Unavailable"
	}
	return value
}
