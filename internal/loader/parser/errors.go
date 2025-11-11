package parser

import "fmt"

var (
	ErrInvalidYamlFormat = fmt.Errorf("invalid yaml format")

	ErrUnknownField     = fmt.Errorf("unknown field")
	ErrInvalidPulseType = fmt.Errorf("invalid pulse type")
	ErrRequiredField    = fmt.Errorf("required field")
	ErrInvalidType      = fmt.Errorf("invalid type")
)

type requiredMonitorFieldError struct {
	reason    error
	monitor   string
	parentKey string
	field     string
	line      int
}

func (e *requiredMonitorFieldError) Error() string {
	if e.monitor == "" {
		return fmt.Sprintf("misssing required %s field %q (line %d)", e.parentKey, e.field, e.line)
	} else {
		return fmt.Sprintf("misssing required %s field %q in monitor %q (line %d)", e.parentKey, e.field, e.monitor, e.line)
	}

}

type monitorFieldTypeError struct {
	FieldName string
	FiledType string
	validType string
}

func (r *monitorFieldTypeError) Error() string {
	return fmt.Sprintf("invalid field type %s for %q: valid types are %s", r.FiledType, r.FieldName, r.validType)
}

type invalidMonitorFieldError struct {
	reason    error
	monitor   string
	parentKey string
	field     string
	line      int
}

func (e *invalidMonitorFieldError) Error() string {
	return fmt.Sprintf("invalid %s field %q in monitor %q (line %d): %s", e.parentKey, e.field, e.monitor, e.line, e.reason)
}

// Unwrap NOT USED REMOVE LATER
func (e *invalidMonitorFieldError) Unwrap() error {
	return e.reason
}

type duplicateMonitorNameError struct {
	name string
	line int
}

func (e *duplicateMonitorNameError) Error() string {
	return fmt.Sprintf("duplicate monitor name %q (line %d), monitor names must be unique and cannot be reused", e.name, e.line)
}
