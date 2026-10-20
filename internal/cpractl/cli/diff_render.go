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
	ID            string       `json:"id" yaml:"id"`
	Outcome       string       `json:"outcome" yaml:"outcome"`
	OldVersion    string       `json:"oldVersion,omitempty" yaml:"oldVersion,omitempty"`
	ChangedFields []string     `json:"changedFields,omitempty" yaml:"changedFields,omitempty"`
	Source        diffLocation `json:"source" yaml:"source"`
}
type diffLocation struct {
	File     string `json:"file" yaml:"file"`
	Document int    `json:"document" yaml:"document"`
	Item     int    `json:"item" yaml:"item"`
	Line     int    `json:"line,omitempty" yaml:"line,omitempty"`
	Column   int    `json:"column,omitempty" yaml:"column,omitempty"`
}
type diffIssue struct {
	ID      string        `json:"id,omitempty" yaml:"id,omitempty"`
	Source  *diffLocation `json:"source,omitempty" yaml:"source,omitempty"`
	Field   string        `json:"field,omitempty" yaml:"field,omitempty"`
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
		if len(proposed.ChangedFields) > 64 {
			return result, invalid
		}
		for _, field := range proposed.ChangedFields {
			if api.SafeValidationField(field) == "" {
				return result, invalid
			}
		}
		result.Items = append(result.Items, diffItem{ID: item.ID, Outcome: proposed.Outcome, OldVersion: proposed.OldVersion, ChangedFields: proposed.ChangedFields, Source: diffSource(item.Location)})
	}
	for _, problem := range preflight.Errors {
		issue := diffIssue{Reason: "invalidGraph", Message: "The collection cannot be validated against the observed catalog."}
		if strings.HasPrefix(problem.Field, "items[") {
			number, suffix, found := strings.Cut(strings.TrimPrefix(problem.Field, "items["), "].resource")
			index, err := strconv.Atoi(number)
			if !found || len(problem.Field) > 192 || err != nil || index < 0 || index >= len(result.Items) || strconv.Itoa(index) != number || suffix != "" && !strings.HasPrefix(suffix, ".") {
				return result, invalid
			}
			item := result.Items[index]
			issue.ID, issue.Source = item.ID, &item.Source
			issue.Field = api.SafeValidationField(strings.TrimPrefix(suffix, "."))
			original, err := frozen.Item(ctx, index)
			if err != nil {
				return result, managementFailure(err)
			}
			clear(original.Resource.Spec)
			clear(original.Resource.Status)
			var located *collection.SourceError
			if errors.As(original.ValidationError(api.InvalidField(issue.Field, problem.Reason)), &located) {
				source := diffSource(located.Location)
				issue.Source = &source
				if api.SafeValidationField(located.Field) != "" {
					issue.Field = located.Field
				}
			}
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
		default:
			if message := api.ValidationMessage(problem.Reason); message != "" {
				issue.Reason, issue.Message = problem.Reason, message
			}
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
			location := item.Source.String()
			rows = append(rows, []string{managementDisplay(item.ID), item.Outcome, managementDisplay(strOrDash(item.OldVersion)), strOrDash(strings.Join(item.ChangedFields, ", ")), managementDisplay(location)})
		}
		if err := table(w, []string{"RESOURCE", "CHANGE", "OLD VERSION", "FIELDS", "SOURCE"}, rows); err != nil {
			return err
		}
		for _, issue := range result.Errors {
			location := "collection"
			if issue.Source != nil {
				location = issue.Source.String()
			}
			if issue.Field != "" {
				location += " " + issue.Field
			}
			if _, err := fmt.Fprintf(w, "%s: %s: %s\n", managementDisplay(location), issue.Reason, issue.Message); err != nil {
				return err
			}
		}
		return nil
	})
}

func diffSource(location collection.Location) diffLocation {
	return diffLocation{File: location.Source, Document: location.Document, Item: location.Item, Line: location.Line, Column: location.Column}
}
func (location diffLocation) String() string {
	return (collection.Location{Source: location.File, Document: location.Document, Item: location.Item, Line: location.Line, Column: location.Column}).String()
}
