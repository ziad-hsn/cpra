package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func managementActionType(name string) bool {
	return strings.EqualFold(name, "action") || strings.EqualFold(name, "actions")
}

func addManagementActionReads(get *cobra.Command, o *options) {
	list := cpra.ListOptions{Limit: 100}
	command := &cobra.Command{Use: "actions [action-id]", Aliases: []string{"action"}, Short: "Read provider outcomes, holds and exact action review versions", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runManagementActionGet(cmd, o, append([]string{"actions"}, args...), false, list)
	}}
	command.Flags().IntVar(&list.Limit, "limit", 100, "page size (1..500); never fetches another page automatically")
	command.Flags().StringVar(&list.Cursor, "cursor", "", "continue the original action page")
	command.Flags().StringVar(&list.MonitorID, "monitor-id", "", "select one exact stable monitor ID")
	get.AddCommand(command)
}

func runManagementActionGet(cmd *cobra.Command, o *options, args []string, describe bool, list cpra.ListOptions) error {
	name, id, err := managementAddressParts(args, describe)
	if err != nil {
		return err
	}
	if !managementActionType(name) {
		return errors.New("an action/ID address is required")
	}
	if list.Limit < 1 || list.Limit > 500 || list.Selector != "" || (id != "" && (list.Cursor != "" || list.MonitorID != "")) {
		return errors.New("action reads require one exact action ID or bounded paging (1..500) with an optional monitor ID")
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
		reply, err = managementResponse(client.Actions.List(cmd.Context(), list))
	} else {
		reply, err = managementResponse(client.Actions.Get(cmd.Context(), id))
	}
	if err != nil {
		return err
	}
	return writeManagementReply(cmd, o, reply, describe, false)
}

func addManagementActionCommands(root *cobra.Command, o *options) {
	for _, action := range []string{"recover", "review"} {
		var revision, reasonFile, noteFile, evidenceFile, resolution string
		target, flag, description := "monitor/ID", "control-revision", "Request one guarded recovery; the receipt is not proof of provider success"
		if action == "review" {
			target, flag, description = "action/ID", "review-revision", "Audit an unknown action without changing its recorded provider outcome or replaying it"
		}
		command := &cobra.Command{Use: action + " " + target, Short: description, Args: cobra.RangeArgs(1, 2)}
		command.Flags().StringVar(&revision, flag, "", "exact observed version; never fetched or replaced automatically")
		command.Flags().StringVar(&reasonFile, "reason-file", "", "required UTF-8 reason file; - reads stdin; retained in audit history")
		if action == "review" {
			command.Flags().StringVar(&resolution, "resolution", "", "accepted, rejected or inconclusive; an operator assertion separate from provider facts")
			command.Flags().StringVar(&noteFile, "note-file", "", "optional UTF-8 note file; - reads stdin")
			command.Flags().StringVar(&evidenceFile, "evidence-file", "", "optional JSON array file of at most eight opaque evidence references; references are never fetched")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			name, id, err := managementAddressParts(args, true)
			if err != nil {
				return err
			}
			if action == "review" {
				if !managementActionType(name) {
					return errors.New("review requires one action/ID target")
				}
				if !api.ActionReviewResolution(resolution).Valid() {
					return errors.New("--resolution must be accepted, rejected or inconclusive")
				}
			} else if kind, err := managementKind(name); err != nil || kind != "Monitor" {
				return errors.New("recover requires one monitor/ID target")
			}
			if !validManagementVersion(revision) {
				return fmt.Errorf("--%s must contain the explicit, unquoted version from the matching observation", flag)
			}
			stdinInputs := 0
			for _, file := range []string{reasonFile, noteFile, evidenceFile} {
				if file == "-" {
					stdinInputs++
				}
			}
			if stdinInputs > 1 {
				return errors.New("only one audit input may read stdin; use files for the remaining inputs")
			}
			request := api.ControlRequest{Revision: revision, Resolution: resolution}
			request.Reason, err = readManagementControlText(cmd, reasonFile, true)
			if err == nil && action == "review" {
				request.Note, err = readManagementControlText(cmd, noteFile, false)
				if err == nil {
					request.EvidenceRefs, err = readManagementEvidence(cmd, evidenceFile)
				}
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
			if action == "recover" {
				reply, err = managementResponse(client.Monitors.Recover(cmd.Context(), id, request))
			} else {
				reply, err = managementResponse(client.Actions.Review(cmd.Context(), id, request))
			}
			if err != nil {
				return err
			}
			return writeManagementReply(cmd, o, reply, false, true)
		}
		root.AddCommand(command)
	}
}

func readManagementEvidence(cmd *cobra.Command, path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if err := cmd.Context().Err(); err != nil {
		return nil, managementFailure(err)
	}
	reader, err := managementInputReader(cmd, path)
	if err != nil {
		return nil, err
	}
	// Permit JSON escape expansion while bounding the entire encoded input.
	raw, err := readManagementInput(reader, 128*1024)
	closeErr := reader.Close()
	defer clear(raw)
	if contextErr := cmd.Context().Err(); contextErr != nil {
		return nil, managementFailure(contextErr)
	}
	var refs []string
	if err != nil || closeErr != nil || !utf8.Valid(raw) || json.Unmarshal(raw, &refs) != nil || refs == nil || len(refs) > 8 {
		return nil, errors.New("evidence file must contain one JSON array of at most eight strings, within 128 KiB")
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		_, duplicate := seen[ref]
		if ref == "" || len(ref) > 2048 || !utf8.ValidString(ref) || strings.IndexFunc(ref, unicode.IsControl) >= 0 || duplicate {
			return nil, errors.New("evidence references must be unique, nonempty UTF-8 strings of at most 2048 bytes without control characters")
		}
		seen[ref] = struct{}{}
	}
	return refs, nil
}
