package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func readMonitorObservations(cmd *cobra.Command, o *options, client *cpra.Client, id string, describe bool, list cpra.ListOptions) error {
	var reply managementReply
	var err error
	if id == "" {
		reply, err = managementResponse(client.MonitorObservations.List(cmd.Context(), list))
	} else {
		reply, err = managementResponse(client.MonitorObservations.Get(cmd.Context(), id))
	}
	if err != nil {
		return err
	}
	reply.monitorObservations = true
	return writeManagementReply(cmd, o, reply, describe, false)
}

func observationTimestamp(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "Unavailable"
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func writeMonitorObservations(cmd *cobra.Command, o *options, raw []byte, describe bool) error {
	var page api.MonitorObservationList
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("invalid monitor observation response")
	}
	if _, ok := fields["items"]; ok {
		if json.Unmarshal(raw, &page) != nil {
			return errors.New("invalid monitor observation page")
		}
	} else {
		var item api.MonitorObservation
		if json.Unmarshal(raw, &item) != nil {
			return errors.New("invalid monitor observation")
		}
		page.Items = []api.MonitorObservation{item}
	}
	if describe && len(page.Items) == 1 {
		item := page.Items[0]
		latency := "Unavailable"
		if item.LastCheckLatencyMS.Available {
			latency = fmt.Sprintf("%.3f ms", item.LastCheckLatencyMS.Value)
		}
		failure := "Unavailable"
		if item.LastFailure != nil {
			failure = item.LastFailure.Code + ": " + item.LastFailure.Message
		}
		return kv(cmd.OutOrStdout(), [][2]string{
			{"View", "Monitor observation (read-only)"}, {"Stable ID", managementDisplay(item.MonitorID)},
			{"Name", managementDisplay(item.Name)}, {"Incarnation", managementDisplay(item.IncarnationUID)},
			{"Configuration source", managementDisplay(string(item.Source))}, {"Observed health", managementDisplay(string(item.Health))},
			{"Incident phase", managementDisplay(string(item.Phase))}, {"Enabled", strconv.FormatBool(item.Enabled)},
			{"Ignored", strconv.FormatBool(item.Ignored)}, {"Snoozed until", observationTimestamp(item.SnoozedUntil)},
			{"Check type", managementDisplay(item.CheckType)}, {"Target", managementDisplay(item.Target)},
			{"Certificate verification disabled", strconv.FormatBool(item.CertificateVerificationDisabled)},
			{"Interval", fmt.Sprintf("%d ms", item.IntervalMS)}, {"Last check", observationTimestamp(item.LastCheckedAt)},
			{"Last success", observationTimestamp(item.LastSuccessfulAt)}, {"Next check", observationTimestamp(item.NextCheckAt)},
			{"Last check latency", latency}, {"Last failure", managementDisplay(failure)},
			{"Configuration problem", diagnosticText(item.ConfigurationProblem)},
			{"Preparation failed at", observationTimestamp(item.ConfigurationProblemAt)},
			{"Configured notifications", managementDisplay(strings.Join(item.ConfiguredNotifications, ", "))},
			{"Pending notifications", strconv.FormatInt(item.PendingNotifications, 10)},
			{"Delivered notifications (retained)", strconv.FormatInt(item.CompletedNotifications, 10)},
			{"Failed notifications (retained)", strconv.FormatInt(item.FailedNotifications, 10)},
			{"Unknown notifications", strconv.FormatInt(item.UnknownNotifications, 10)},
			{"Unknown provider outcomes", strconv.FormatInt(item.UnknownActions, 10)},
			{"Actions needing review", strconv.FormatInt(item.ActionsNeedingReview, 10)},
		})
	}
	headers := []string{"ID", "NAME", "OBSERVED HEALTH", "ENABLED", "IGNORED", "PENDING NOTIFICATIONS"}
	if o.output == formatWide {
		headers = append(headers, "CHECK", "TARGET", "LAST CHECK")
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		row := []string{managementDisplay(item.MonitorID), managementDisplay(item.Name), managementDisplay(string(item.Health)), strconv.FormatBool(item.Enabled), strconv.FormatBool(item.Ignored), strconv.FormatInt(item.PendingNotifications, 10)}
		if o.output == formatWide {
			row = append(row, managementDisplay(item.CheckType), managementDisplay(item.Target), observationTimestamp(item.LastCheckedAt))
		}
		rows = append(rows, row)
	}
	if err := table(cmd.OutOrStdout(), headers, rows); err != nil {
		return err
	}
	if page.NextCursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(page.NextCursor))
		return err
	}
	return nil
}
