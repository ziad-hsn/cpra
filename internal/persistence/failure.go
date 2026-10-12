package persistence

import "errors"

// FailureDiagnostic contains only reviewed descriptions. Provider response
// bodies, addresses, paths and error strings are never diagnostic inputs.
type FailureDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var ErrFailureDiagnostic = errors.New("invalid failure diagnostic")

// NewFailureDiagnostic returns the canonical safe message for a cause code.
// An unknown code has no diagnostic; callers cannot supply an arbitrary message.
func NewFailureDiagnostic(code string) FailureDiagnostic {
	var message string
	switch code {
	case "deadline_exceeded":
		message = "The operation did not finish within its deadline."
	case "cancelled":
		message = "The operation was cancelled before a confirmed outcome."
	case "dns_failed":
		message = "The target name could not be resolved."
	case "tls_failed":
		message = "TLS certificate verification or negotiation failed."
	case "connection_failed":
		message = "The connection could not be established or completed."
	case "unexpected_http_status":
		message = "The health endpoint returned an unexpected HTTP status."
	case "permission_denied":
		message = "The operation was denied by local access permissions."
	case "rejected":
		message = "The provider explicitly rejected the operation."
	case "job_panicked":
		message = "The job stopped because its implementation panicked."
	case "operation_failed":
		message = "The operation failed; no safe provider detail is available."
	default:
		return FailureDiagnostic{}
	}
	return FailureDiagnostic{Code: code, Message: message}
}

func (d FailureDiagnostic) validate(version int) error {
	if d == (FailureDiagnostic{}) {
		return nil
	}
	if !deliveryStorageFormat(version) || d != NewFailureDiagnostic(d.Code) || d.Code == "" {
		return ErrFailureDiagnostic
	}
	return nil
}
