package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

// Apply has already checked page and source identities. Resolve positions only
// for the bounded displayed failures; never fetch the rest of the collection.
func writeCollectionValidationFailure(cmd *cobra.Command, frozen *collection.Frozen, page api.ValidationResultPage) error {
	shown, omitted := 0, page.NextCursor != ""
	for _, row := range page.Items {
		if row.Issue == "" {
			continue
		}
		if shown == 10 {
			omitted = true
			continue
		}
		item, err := frozen.Item(cmd.Context(), int(row.Ordinal-1))
		if err != nil {
			return managementFailure(err)
		}
		clear(item.Resource.Spec)
		clear(item.Resource.Status)
		reason := row.Reason
		message := api.ValidationMessage(reason)
		if message == "" {
			message = "The resource or its references do not satisfy the validation contract."
		}
		field, location := api.SafeValidationField(row.Field), item.Location
		var source *collection.SourceError
		if errors.As(item.ValidationError(api.InvalidField(field, reason)), &source) {
			location = source.Location
			if source.Field != "" {
				field = source.Field
			}
		}
		if field != "" {
			message = field + ": " + message
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", managementDisplay(location.String()), message); err != nil {
			return err
		}
		shown++
	}
	if omitted {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Validation details are truncated; read the original operation's paginated validation results."); err != nil {
			return err
		}
	}
	if safeManagementHandle(page.OperationID) {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "cpractl get operation %s --validation\n", page.OperationID)
		return err
	}
	return nil
}
