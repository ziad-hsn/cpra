package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

type diffItem struct {
	ID         string       `json:"id" yaml:"id"`
	Outcome    string       `json:"outcome" yaml:"outcome"`
	OldVersion string       `json:"oldVersion,omitempty" yaml:"oldVersion,omitempty"`
	Source     diffLocation `json:"source" yaml:"source"`
}
type diffLocation struct {
	File     string `json:"file" yaml:"file"`
	Document int    `json:"document" yaml:"document"`
	Item     int    `json:"item" yaml:"item"`
}
type diffIssue struct {
	ID      string        `json:"id,omitempty" yaml:"id,omitempty"`
	Source  *diffLocation `json:"source,omitempty" yaml:"source,omitempty"`
	Reason  string        `json:"reason" yaml:"reason"`
	Message string        `json:"message" yaml:"message"`
}
type diffReport struct {
	Valid   bool        `json:"valid" yaml:"valid"`
	Changed bool        `json:"changed" yaml:"changed"`
	Items   []diffItem  `json:"items" yaml:"items"`
	Errors  []diffIssue `json:"errors,omitempty" yaml:"errors,omitempty"`
}

func collectionDiffReport(ctx context.Context, frozen *collection.Frozen, preflight *api.Preflight) (diffReport, error) {
	result := diffReport{Items: []diffItem{}}
	invalid := errors.New("server returned an inconsistent collection diff")
	if preflight == nil || len(preflight.Items) != frozen.Len() || preflight.Valid && len(preflight.Errors) != 0 {
		return result, invalid
	}
	result.Valid = preflight.Valid
	for index, proposed := range preflight.Items {
		item, err := frozen.Item(ctx, index)
		if err != nil {
			return result, managementFailure(err)
		}
		clear(item.Resource.Spec)
		clear(item.Resource.Status)
		if item.ID != proposed.ID || proposed.Committed == nil || *proposed.Committed || proposed.Applied == nil || *proposed.Applied || len(proposed.OldVersion) > 256 {
			return result, invalid
		}
		switch proposed.Outcome {
		case "create", "update":
			result.Changed = true
		case "unchanged":
		case "invalid", "notValidated":
			if preflight.Valid {
				return result, invalid
			}
		default:
			return result, invalid
		}
		result.Items = append(result.Items, diffItem{ID: item.ID, Outcome: proposed.Outcome, OldVersion: proposed.OldVersion, Source: diffLocation{item.Location.Source, item.Location.Document, item.Location.Item}})
	}
	for _, problem := range preflight.Errors {
		issue := diffIssue{Reason: "invalidGraph", Message: "The collection cannot be validated against the observed catalog."}
		if strings.HasPrefix(problem.Field, "items[") && strings.HasSuffix(problem.Field, "].resource") {
			number := strings.TrimSuffix(strings.TrimPrefix(problem.Field, "items["), "].resource")
			index, err := strconv.Atoi(number)
			if err != nil || index < 0 || index >= len(result.Items) || strconv.Itoa(index) != number {
				return result, invalid
			}
			item := result.Items[index]
			issue.ID, issue.Source = item.ID, &item.Source
		}
		// Error messages from a server are not input for terminal output. Render
		// reviewed classifications while keeping payloads and private labels out.
		switch problem.Reason {
		case "conflict":
			issue.Reason, issue.Message = "conflict", "A resource version changed; reload and review the intended changes."
		case "missingReference":
			issue.Reason, issue.Message = "missingReference", "A required reference is unavailable in submitted or authorized retained resources."
		case "unsafePrefix":
			issue.Reason, issue.Message = "unsafePrefix", "Dependency-first application would invalidate an existing consumer."
		case "duplicateIdentity":
			issue.Reason, issue.Message = "duplicateIdentity", "A resource identity occurs more than once."
		case "invalidResource":
			issue.Reason, issue.Message = "invalidResource", "The resource schema, compiled driver or configuration is invalid."
		}
		result.Errors = append(result.Errors, issue)
	}
	if !result.Valid && len(result.Errors) == 0 {
		return result, invalid
	}
	return result, nil
}

func writeCollectionDiff(cmd *cobra.Command, o *options, result diffReport) error {
	return newPrinter(cmd, o).render(result, func(w io.Writer) error {
		rows := make([][]string, 0, len(result.Items))
		for _, item := range result.Items {
			location := fmt.Sprintf("%s document %d item %d", item.Source.File, item.Source.Document, item.Source.Item)
			rows = append(rows, []string{managementDisplay(item.ID), item.Outcome, managementDisplay(strOrDash(item.OldVersion)), managementDisplay(location)})
		}
		if err := table(w, []string{"RESOURCE", "CHANGE", "OLD VERSION", "SOURCE"}, rows); err != nil {
			return err
		}
		for _, issue := range result.Errors {
			location := "collection"
			if issue.Source != nil {
				location = fmt.Sprintf("%s document %d item %d", issue.Source.File, issue.Source.Document, issue.Source.Item)
			}
			if _, err := fmt.Fprintf(w, "%s: %s: %s\n", managementDisplay(location), issue.Reason, issue.Message); err != nil {
				return err
			}
		}
		return nil
	})
}
