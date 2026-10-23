package collection

import (
	"errors"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// InputErrors retains at most ten source-ordered static validation failures.
// Truncated means further details were not retained; it is not an error count.
// Syntax, I/O, storage and quota failures still stop parsing immediately.
type InputErrors struct {
	Details   []*SourceError
	Truncated bool
}

func (e *InputErrors) Error() string {
	var lines []string
	for _, detail := range e.Details[:min(10, len(e.Details))] {
		lines = append(lines, detail.Error())
	}
	if e.Truncated {
		lines = append(lines, "validation details truncated after ten errors")
	}
	return strings.Join(lines, "\n")
}

func (e *InputErrors) Unwrap() []error {
	result := make([]error, 0, min(10, len(e.Details)))
	for _, detail := range e.Details[:min(10, len(e.Details))] {
		result = append(result, detail)
	}
	return result
}

func (b *builder) retainInputError(err error, item Item) error {
	if err == nil {
		return nil
	}
	field, ok := api.ValidationFieldError(err)
	if !ok {
		return err
	}
	// Copy only the reviewed rule and location. Never retain arbitrary decoder
	// or provider errors through the aggregate's Unwrap chain.
	var source *SourceError
	if !errors.As(err, &source) {
		_ = errors.As(item.ValidationError(err), &source)
	}
	detail := &SourceError{Location: source.Location, Field: api.SafeValidationField(source.Field), Cause: api.InvalidField(field.Field, field.Reason)}
	if len(b.inputErrors.Details) == 10 {
		b.inputErrors.Truncated = true
		return &b.inputErrors
	}
	b.inputErrors.Details = append(b.inputErrors.Details, detail)
	return nil
}
