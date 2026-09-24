package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func managementIncidentType(name string) bool {
	switch strings.ToLower(name) {
	case "incident", "incidents", "incident-record", "incident-records":
		return true
	}
	return false
}

func addManagementIncidentReads(get *cobra.Command, o *options) {
	list := cpra.ListOptions{Limit: 100}
	command := &cobra.Command{Use: "incidents [incident-id]", Aliases: []string{"incident", "inc", "incident-records", "incident-record"}, Short: "Read v2 incident attention and its exact control revision", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runManagementIncidentGet(cmd, o, append([]string{"incident-records"}, args...), false, list)
	}}
	command.Flags().IntVar(&list.Limit, "limit", 100, "page size (1..500)")
	command.Flags().StringVar(&list.Cursor, "cursor", "", "continue the original incident page")
	command.Flags().StringVar(&list.MonitorID, "monitor-id", "", "select the latest retained incident for one exact stable monitor ID")
	get.AddCommand(command)
}

func runManagementIncidentGet(cmd *cobra.Command, o *options, args []string, describe bool, list cpra.ListOptions) error {
	name, id, err := managementAddressParts(args, describe)
	if err != nil {
		return err
	}
	if !managementIncidentType(name) {
		return errors.New("an incident/ID address is required")
	}
	if list.Limit < 1 || list.Limit > 500 {
		return errors.New("page limit must be between 1 and 500")
	}
	if list.Selector != "" || (id != "" && (list.Cursor != "" || list.MonitorID != "")) {
		return errors.New("incident reads require one exact incident ID or bounded paging with an optional monitor ID")
	}
	if list.MonitorID != "" {
		if _, _, err := managementAddressParts([]string{"monitor", list.MonitorID}, true); err != nil {
			return err
		}
	}
	client, err := o.mustClient()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var reply managementReply
	if id == "" {
		reply, err = managementResponse(client.Incidents.List(cmd.Context(), list))
	} else {
		reply, err = managementResponse(client.Incidents.Get(cmd.Context(), id))
	}
	if err != nil {
		return err
	}
	return writeManagementReply(cmd, o, reply, describe, false)
}

func addManagementControlCommands(root *cobra.Command, o *options) {
	for _, action := range []string{"acknowledge", "dismiss", "reopen", "snooze", "unsnooze", "disable", "enable"} {
		var revision, noteFile, reasonFile, duration string
		incident := action == "acknowledge" || action == "dismiss" || action == "reopen"
		configuration := action == "disable" || action == "enable"
		target, flag := "monitor/ID", "control-revision"
		if incident {
			target, flag = "incident/ID", "revision"
		} else if configuration {
			flag = "resource-version"
		}
		command := &cobra.Command{Use: action + " " + target, Short: managementControlDescription(action), Args: cobra.RangeArgs(1, 2)}
		command.Flags().StringVar(&revision, flag, "", "the exact version observed before this request; never fetched automatically")
		if action == "acknowledge" {
			command.Flags().StringVar(&noteFile, "note-file", "", "optional UTF-8 operator note file; - reads stdin; retained in the audit history")
		}
		if action == "dismiss" || action == "snooze" {
			command.Flags().StringVar(&reasonFile, "reason-file", "", "required UTF-8 reason file; - reads stdin; retained in the audit history")
		}
		if action == "snooze" {
			command.Flags().StringVar(&duration, "duration", "", "positive Go duration up to 720h, for example 30m")
			command.Flags().StringVar(&duration, "for", "", "alias of --duration")
			command.MarkFlagsMutuallyExclusive("duration", "for")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			name, id, err := managementAddressParts(args, true)
			if err != nil {
				return err
			}
			if incident {
				if !managementIncidentType(name) {
					return errors.New("this operation requires one incident/ID target")
				}
			} else if kind, err := managementKind(name); err != nil || kind != "Monitor" {
				return errors.New("this operation requires one monitor/ID target")
			}
			if !validManagementVersion(revision) {
				return fmt.Errorf("--%s must contain the explicit, unquoted version from the matching observation", flag)
			}
			request := api.ControlRequest{Revision: revision}
			if action == "snooze" {
				interval, err := time.ParseDuration(duration)
				if err != nil || interval <= 0 || interval > 720*time.Hour {
					return errors.New("snooze duration must be positive and at most 720h")
				}
				request.Duration = duration
			}
			if action == "acknowledge" {
				request.Note, err = readManagementControlText(cmd, noteFile, false)
			} else if action == "dismiss" || action == "snooze" {
				request.Reason, err = readManagementControlText(cmd, reasonFile, true)
			}
			if err != nil {
				return err
			}
			client, err := o.mustClient()
			if err != nil {
				return err
			}
			defer client.CloseIdleConnections()
			var reply managementReply
			switch action {
			case "acknowledge":
				reply, err = managementResponse(client.Incidents.Acknowledge(cmd.Context(), id, request))
			case "dismiss":
				reply, err = managementResponse(client.Incidents.Dismiss(cmd.Context(), id, request))
			case "reopen":
				reply, err = managementResponse(client.Incidents.Reopen(cmd.Context(), id, request))
			case "snooze":
				reply, err = managementResponse(client.Monitors.Snooze(cmd.Context(), id, request))
			case "unsnooze":
				reply, err = managementResponse(client.Monitors.Unsnooze(cmd.Context(), id, request))
			case "disable":
				reply, err = managementResponse(client.Monitors.Patch(cmd.Context(), id, revision, api.MergePatch(`{"spec":{"enabled":false}}`)))
			case "enable":
				reply, err = managementResponse(client.Monitors.Patch(cmd.Context(), id, revision, api.MergePatch(`{"spec":{"enabled":true}}`)))
			}
			if err != nil {
				return err
			}
			return writeManagementReply(cmd, o, reply, false, true)
		}
		root.AddCommand(command)
	}
}

func managementControlDescription(action string) string {
	switch action {
	case "acknowledge":
		return "Record the authenticated person investigating an incident; notifications continue"
	case "dismiss":
		return "Suppress notifications for this exact incident while checks and recovery continue"
	case "reopen":
		return "Resume future notifications for the same still-active incident"
	case "snooze":
		return "Pause checks, notifications and new recovery for a bounded period"
	case "unsnooze":
		return "End snooze while respecting disabled state and maintenance"
	case "disable":
		return "Save enabled=false, retaining monitor state and history"
	default:
		return "Save enabled=true, respecting snooze and maintenance"
	}
}

func readManagementControlText(cmd *cobra.Command, path string, required bool) (string, error) {
	if err := cmd.Context().Err(); err != nil {
		return "", managementFailure(err)
	}
	if path == "" {
		if required {
			return "", errors.New("--reason-file is required; use - to read stdin")
		}
		return "", nil
	}
	reader, err := managementInputReader(cmd, path)
	if err != nil {
		return "", err
	}
	raw, err := readManagementInput(reader, 4096)
	closeErr := reader.Close()
	defer clear(raw)
	if contextErr := cmd.Context().Err(); contextErr != nil {
		return "", managementFailure(contextErr)
	}
	if err != nil || closeErr != nil || !utf8.Valid(raw) || strings.ContainsAny(string(raw), "\x00\r") {
		return "", errors.New("operator comment must be UTF-8, at most 4096 bytes, without NUL or carriage returns")
	}
	text := string(raw)
	if required && strings.TrimSpace(text) == "" {
		return "", errors.New("an operator reason cannot be empty")
	}
	return text, nil
}
