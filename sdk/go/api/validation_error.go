package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
)

// ValidationError identifies a static rule without retaining the rejected value.
// Field uses the canonical resource path. Offset, when available, is a one-based
// byte offset in the JSON value being decoded; it is not a source-file position.
type ValidationError struct {
	Field  string
	Reason string
	Offset int64
	cause  error
}

func (e *ValidationError) Error() string {
	message := ValidationMessage(e.Reason)
	if message == "" {
		message = "configuration does not satisfy the required rule"
	}
	if field := SafeValidationField(e.Field); field != "" {
		return field + ": " + message
	}
	return message
}

func (e *ValidationError) Unwrap() error { return e.cause }

// InvalidField constructs a value-free validation error using a documented rule.
// Unknown reasons and paths are omitted from human-facing diagnostics.
func InvalidField(field, reason string) *ValidationError {
	return &ValidationError{Field: SafeValidationField(field), Reason: reason}
}

// PrefixValidationError adds the enclosing resource path, preserving errors.Is
// and errors.As for the original failure. Non-validation errors pass unchanged.
func PrefixValidationError(prefix string, err error) error {
	var detail *ValidationError
	if !errors.As(err, &detail) {
		return err
	}
	field := prefix
	if detail.Field != "" {
		field += "." + detail.Field
	}
	return &ValidationError{Field: SafeValidationField(field), Reason: detail.Reason, Offset: detail.Offset, cause: err}
}

// ValidationFieldError returns a bounded wire diagnostic with a locally defined
// message. It never includes an underlying decoder, filesystem or provider error.
func ValidationFieldError(err error) (FieldError, bool) {
	var detail *ValidationError
	if !errors.As(err, &detail) {
		return FieldError{}, false
	}
	message := ValidationMessage(detail.Reason)
	if message == "" {
		return FieldError{}, false
	}
	return FieldError{Field: SafeValidationField(detail.Field), Reason: detail.Reason, Message: message}, true
}

func jsonValidationError(err error) error {
	if err == nil {
		return nil
	}
	var existing *ValidationError
	if errors.As(err, &existing) {
		return err
	}
	result := InvalidField("", "invalidJSON")
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		result.Offset = syntax.Offset
	case errors.As(err, &wrongType):
		result.Field = SafeValidationField(wrongType.Field)
		result.Reason, result.Offset = "invalidType", wrongType.Offset
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		result.Reason = "unknownField"
	}
	// Decoder errors may retain rejected scalar text. Do not retain them through
	// Unwrap: the rule and byte offset are sufficient for caller diagnostics.
	return result
}

//go:embed validation_messages.json
var validationMessageBytes []byte

var validationMessages = sync.OnceValue(func() map[string]string {
	var messages map[string]string
	if err := json.Unmarshal(validationMessageBytes, &messages); err != nil {
		panic("invalid compiled validation messages")
	}
	return messages
})

// ValidationMessage resolves a rule code locally. Consumers should use it rather
// than rendering untrusted remote error messages, which may include credentials.
func ValidationMessage(reason string) string { return validationMessages()[reason] }

var validationPath = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*|\[[0-9]{1,10}\])*$`)
var validationIndex = regexp.MustCompile(`\[[0-9]+\]`)
var validationFields = sync.OnceValue(func() map[string]bool {
	// Only property names from this build's schema and the file-manifest aliases
	// may appear. Arbitrary JSON keys, header names and resource IDs are excluded.
	fields := map[string]bool{}
	var schema any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		panic("invalid compiled API schema")
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if properties, ok := node["properties"].(map[string]any); ok {
				for name := range properties {
					fields[fieldKey(name)] = true
				}
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(schema)
	for _, name := range []string{"pulse_check", "intervention", "codes", "notify", "notify_group", "notification_groups", "endpoints", "monitors", "red", "yellow", "green", "cyan", "gray", "blue"} {
		fields[fieldKey(name)] = true
	}
	return fields
})

func fieldKey(field string) string { return strings.ToLower(strings.ReplaceAll(field, "_", "")) }

// SafeValidationField accepts bounded schema property paths, excluding free-form
// mapping keys and input values even when they resemble an identifier.
func SafeValidationField(field string) string {
	if len(field) > 160 || !validationPath.MatchString(field) {
		return ""
	}
	for _, part := range strings.Split(validationIndex.ReplaceAllString(field, ""), ".") {
		if !validationFields()[fieldKey(part)] {
			return ""
		}
	}
	return field
}
