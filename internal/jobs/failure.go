package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/grpc/codes"
)

// GRPCHealthError keeps only the protocol enum, never the remote status message.
type GRPCHealthError struct {
	Status  string
	RPCCode codes.Code
}

func (e *GRPCHealthError) Error() string {
	if e.Status != "" {
		return "gRPC health status: " + e.Status
	}
	return "gRPC health RPC: " + e.RPCCode.String()
}
func (e *GRPCHealthError) Unwrap() error {
	if e.RPCCode == codes.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	if e.RPCCode == codes.Canceled {
		return context.Canceled
	}
	return nil
}

type CertificateExpiryError struct {
	NotAfter      time.Time
	DaysRemaining int
}

func (e *CertificateExpiryError) Error() string {
	return fmt.Sprintf("TLS certificate expires in %d day(s)", e.DaysRemaining)
}

type FailureDetail struct {
	HTTPStatus               int
	GRPCStatus, GRPCCode     string
	CertificateNotAfter      time.Time
	CertificateDaysRemaining int
}

func FailureDetails(err error) FailureDetail {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return FailureDetail{}
	}
	var httpStatus *HTTPStatusError
	var grpcStatus *GRPCHealthError
	var expiry *CertificateExpiryError
	switch {
	case errors.As(err, &httpStatus):
		if httpStatus.Status >= 100 && httpStatus.Status <= 599 {
			return FailureDetail{HTTPStatus: httpStatus.Status}
		}
		return FailureDetail{}
	case errors.As(err, &grpcStatus):
		if grpcStatus.Status != "" {
			return FailureDetail{GRPCStatus: grpcStatus.Status}
		}
		code := grpcStatus.RPCCode
		if code < codes.Canceled || code > codes.Unauthenticated {
			code = codes.Unknown
		}
		return FailureDetail{GRPCCode: code.String()}
	case errors.As(err, &expiry):
		if expiry.NotAfter.IsZero() || expiry.NotAfter.Year() < 1 || expiry.NotAfter.Year() > 9999 || expiry.DaysRemaining < -4000000 || expiry.DaysRemaining > 4000000 {
			return FailureDetail{}
		}
		return FailureDetail{CertificateNotAfter: expiry.NotAfter.UTC(), CertificateDaysRemaining: expiry.DaysRemaining}
	}
	return FailureDetail{}
}

var ErrJobPanic = errors.New("job panicked")

// ConfigurationError identifies local check preparation that failed before
// target invocation. It contains a reviewed code, never a path or file content.
type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return "local check configuration could not be prepared" }

func ConfigurationFailure(err error) string {
	var local *ConfigurationError
	if errors.As(err, &local) {
		switch local.Code {
		case "ca_file_missing", "ca_file_unreadable", "ca_file_invalid":
			return local.Code
		}
	}
	return ""
}

// HTTPStatusError retains an observed status without a response body. It does
// not establish rejection of an external effect or authorize another attempt.
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("received unexpected status code: %d", e.Status)
}

// checkAccessError carries a reviewed check diagnostic. The wrapped provider
// error remains available in the executing process but never supplies public
// text. This classification grants no retry or external-effect permission.
type checkAccessError struct {
	code  string
	cause error
}

func (e *checkAccessError) Error() string {
	switch e.code {
	case "authentication_failed":
		return "The target rejected the configured credentials."
	case "authorization_failed":
		return "The target denied the operation for the configured identity."
	default:
		return "The target check failed."
	}
}

func (e *checkAccessError) Unwrap() error { return e.cause }

// FailureCode classifies typed causes without inspecting arbitrary error text.
// This is an explanation only; effect rejection and retry permission are
// determined independently by the controller's delivery contract.
func FailureCode(err error) string {
	if err == nil {
		return ""
	}
	if code := ConfigurationFailure(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrJobPanic):
		return "job_panicked"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, ErrInterventionRejected):
		return "rejected"
	}
	var access *checkAccessError
	if errors.As(err, &access) {
		switch access.code {
		case "authentication_failed", "authorization_failed":
			return access.code
		}
	}
	var rejection *DeliveryError
	var httpStatus *HTTPStatusError
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var network net.Error
	var grpcStatus *GRPCHealthError
	var expiry *CertificateExpiryError
	switch {
	case errors.As(err, &grpcStatus):
		return "grpc_health_failed"
	case errors.As(err, &expiry):
		return "certificate_expiring"
	case errors.As(err, &rejection):
		return "rejected"
	case errors.As(err, &httpStatus):
		return "unexpected_http_status"
	case errors.As(err, &dns):
		return "dns_failed"
	case errors.As(err, &certificate) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate):
		return "tls_failed"
	case errors.As(err, &network):
		if network.Timeout() {
			return "deadline_exceeded"
		}
		return "connection_failed"
	default:
		return "operation_failed"
	}
}
