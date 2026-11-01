package persistence

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// FailureDiagnostic contains only reviewed descriptions. Provider response
// bodies, addresses, paths and error strings are never diagnostic inputs.
type FailureDiagnostic struct {
	TaxonomyVersion          string    `json:"taxonomy_version,omitempty"`
	Phase                    string    `json:"phase,omitempty"`
	Code                     string    `json:"code"`
	Message                  string    `json:"message"`
	HTTPStatus               int       `json:"http_status,omitempty"`
	GRPCStatus               string    `json:"grpc_status,omitempty"`
	GRPCCode                 string    `json:"grpc_code,omitempty"`
	CertificateNotAfter      time.Time `json:"certificate_not_after,omitzero"`
	CertificateDaysRemaining int       `json:"certificate_days_remaining,omitempty"`
}

var ErrFailureDiagnostic = errors.New("invalid failure diagnostic")

func (d FailureDiagnostic) MonitoringUnavailable() bool { return d.configurationProblem() }

// NewFailureDiagnostic returns the canonical safe message for a cause code.
// An unknown code has no diagnostic; callers cannot supply an arbitrary message.
func NewFailureDiagnostic(code string) FailureDiagnostic {
	var message string
	switch code {
	case "ca_file_missing":
		message = "The configured CA file does not exist. No target check was performed."
	case "ca_file_unreadable":
		message = "The configured CA file cannot be read by CPRa. No target check was performed."
	case "ca_file_invalid":
		message = "The configured CA file must be a regular PEM certificate file of at most 1 MiB. No target check was performed."
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
	case "grpc_health_failed":
		message = "The gRPC health endpoint did not report SERVING."
	case "certificate_expiring":
		message = "The TLS certificate has reached its configured expiry threshold."
	case "permission_denied":
		message = "The operation was denied by local access permissions."
	case "authentication_failed":
		message = "The target rejected the configured credentials."
	case "authorization_failed":
		message = "The target denied the operation for the configured identity."
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

func (d FailureDiagnostic) configurationProblem() bool {
	if d.TaxonomyVersion != "" {
		if (d.Code == "grpc_health_failed" || d.Code == "grpc.rpc_failed") && d.GRPCCode == "Canceled" {
			return true
		}
		definition, ok := api.FailureCodeDefinition(d.TaxonomyVersion, d.Code)
		return ok && !definition.Available
	}
	return d.Code == "ca_file_missing" || d.Code == "ca_file_unreadable" || d.Code == "ca_file_invalid"
}

// CheckFailure copies an allowlisted observation into durable value fields.
// Validation still runs at the authoritative command boundary.
func CheckFailure(f api.FailureDiagnostic) FailureDiagnostic {
	d := FailureDiagnostic{TaxonomyVersion: f.TaxonomyVersion, Phase: f.Phase, Code: f.Code, Message: f.Message}
	if f.HTTPStatus != nil {
		d.HTTPStatus = *f.HTTPStatus
	}
	if f.GRPCCode != nil {
		d.GRPCCode = *f.GRPCCode
	}
	if f.GRPCStatus != nil {
		d.GRPCStatus = string(*f.GRPCStatus)
	}
	if f.CertificateNotAfter != nil {
		d.CertificateNotAfter = f.CertificateNotAfter.UTC()
	}
	if f.CertificateDaysRemaining != nil {
		d.CertificateDaysRemaining = *f.CertificateDaysRemaining
	}
	return d
}

func (d FailureDiagnostic) Public() api.FailureDiagnostic {
	f := api.FailureDiagnostic{TaxonomyVersion: d.TaxonomyVersion, Phase: d.Phase, Code: d.Code, Message: d.Message}
	if d.HTTPStatus != 0 {
		f.HTTPStatus = &d.HTTPStatus
	}
	if d.GRPCCode != "" {
		f.GRPCCode = &d.GRPCCode
	}
	if d.GRPCStatus != "" {
		v := api.FailureDiagnosticGrpcStatus(d.GRPCStatus)
		f.GRPCStatus = &v
	}
	if !d.CertificateNotAfter.IsZero() {
		f.CertificateNotAfter, f.CertificateDaysRemaining = &d.CertificateNotAfter, &d.CertificateDaysRemaining
	}
	return f
}

func (d FailureDiagnostic) requiresConditionFormat() bool {
	return d.Code == "authentication_failed" || d.Code == "authorization_failed"
}

// WithDetails renders protocol fields without accepting message text from a
// caller. Persistent validation checks the fields and recomputes this message.
func (d FailureDiagnostic) WithDetails() FailureDiagnostic {
	d.Message = NewFailureDiagnostic(d.Code).Message
	if d.HTTPStatus >= 100 && d.HTTPStatus <= 599 {
		d.Message += fmt.Sprintf(" HTTP status: %d.", d.HTTPStatus)
	}
	if d.GRPCStatus != "" {
		d.Message += " Health status: " + d.GRPCStatus + "."
	}
	if d.GRPCCode != "" {
		d.Message += " RPC code: " + d.GRPCCode + "."
	}
	if !d.CertificateNotAfter.IsZero() {
		d.Message += fmt.Sprintf(" Expires at %s (%d days remaining).", d.CertificateNotAfter.UTC().Format(time.RFC3339), d.CertificateDaysRemaining)
	}
	return d
}

func (d FailureDiagnostic) requiresLifecycleFormat() bool {
	return d.HTTPStatus != 0 || d.GRPCStatus != "" || d.GRPCCode != "" || !d.CertificateNotAfter.IsZero() || d.CertificateDaysRemaining != 0 || d.Code == "grpc_health_failed" || d.Code == "certificate_expiring"
}

func (d FailureDiagnostic) validate(version int) error {
	if d == (FailureDiagnostic{}) {
		return nil
	}
	if d.TaxonomyVersion != "" || d.Phase != "" {
		if validCustomFailure(d, version) {
			return nil
		}
		definition, ok := api.FailureCodeDefinition(d.TaxonomyVersion, d.Code)
		if !recoveryRulesStorageFormat(version) || !ok || len(definition.Drivers) == 0 || d.Message != definition.Message || api.ValidateFailureObservation(definition.Drivers[0], d.Public()) != nil {
			return ErrFailureDiagnostic
		}
		return nil
	}
	if !deliveryStorageFormat(version) || d.Code == "" || NewFailureDiagnostic(d.Code).Code == "" {
		return ErrFailureDiagnostic
	}
	if d.configurationProblem() && !actionStorageFormat(version) {
		return ErrFailureDiagnostic
	}
	if d.requiresConditionFormat() && !notificationConditionStorageFormat(version) {
		return ErrFailureDiagnostic
	}
	if !d.requiresLifecycleFormat() {
		if d != NewFailureDiagnostic(d.Code) {
			return ErrFailureDiagnostic
		}
		return nil
	}
	if !lifecycleStorageFormat(version) || d != d.WithDetails() || len(d.Message) > 256 {
		return ErrFailureDiagnostic
	}
	if d.HTTPStatus != 0 && (d.Code != "unexpected_http_status" || d.HTTPStatus < 100 || d.HTTPStatus > 599) {
		return ErrFailureDiagnostic
	}
	if d.GRPCStatus != "" && (d.Code != "grpc_health_failed" || d.GRPCCode != "" || !slices.Contains([]string{"UNKNOWN", "NOT_SERVING", "SERVICE_UNKNOWN"}, d.GRPCStatus)) {
		return ErrFailureDiagnostic
	}
	if d.GRPCCode != "" && (d.Code != "grpc_health_failed" || !slices.Contains([]string{"Canceled", "Unknown", "InvalidArgument", "DeadlineExceeded", "NotFound", "AlreadyExists", "PermissionDenied", "ResourceExhausted", "FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented", "Internal", "Unavailable", "DataLoss", "Unauthenticated"}, d.GRPCCode)) {
		return ErrFailureDiagnostic
	}
	if !d.CertificateNotAfter.IsZero() || d.CertificateDaysRemaining != 0 {
		if d.Code != "certificate_expiring" || d.CertificateNotAfter.IsZero() || d.CertificateNotAfter.Year() < 1 || d.CertificateNotAfter.Year() > 9999 || d.CertificateDaysRemaining < -4000000 || d.CertificateDaysRemaining > 4000000 {
			return ErrFailureDiagnostic
		}
	}
	return nil
}
