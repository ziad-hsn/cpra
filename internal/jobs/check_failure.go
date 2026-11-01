package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"google.golang.org/grpc/codes"
)

// checkConditionError records a protocol observation made by a built-in check.
// Its code is selected by the adapter, never from provider message text.
type checkConditionError struct{ code string }

type checkUnavailableError struct{ cause error }

func (e *checkUnavailableError) Error() string { return "The check could not obtain an observation." }
func (e *checkUnavailableError) Unwrap() error { return e.cause }

func (e *checkConditionError) Error() string {
	d, ok := api.FailureCodeDefinition(api.FailureTaxonomyVersion, e.code)
	if !ok {
		return "The check did not obtain a healthy observation."
	}
	return d.Message
}

// ClassifyCheckFailure interprets typed causes at observation time. It does not
// establish the cause of an outage, the outcome of an intervention, or permission
// to retry. At is captured with the result; replay must use the committed result.
func ClassifyCheckFailure(driver string, err error, at time.Time) api.FailureDiagnostic {
	if err == nil {
		return api.FailureDiagnostic{}
	}
	code, phase := "operation_failed", "check"
	if code = ConfigurationFailure(err); code != "" {
		return checkFailure(driver, code, "local")
	}
	var unavailable *checkUnavailableError
	if errors.As(err, &unavailable) {
		return checkFailure(driver, "check.unavailable", "local")
	}
	switch {
	case errors.Is(err, context.Canceled):
		return checkFailure(driver, "cancelled", "local")
	case errors.Is(err, ErrJobPanic):
		return checkFailure(driver, "job_panicked", "local")
	case errors.Is(err, os.ErrPermission):
		return checkFailure(driver, "permission_denied", "local")
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE), errors.Is(err, syscall.ENOMEM), errors.Is(err, syscall.ENOBUFS), errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EADDRINUSE):
		return checkFailure(driver, "check.unavailable", "local")
	}
	var condition *checkConditionError
	if errors.As(err, &condition) {
		return checkFailure(driver, condition.code, "response")
	}
	var access *checkAccessError
	if errors.As(err, &access) {
		if access.code == "authentication_failed" {
			return checkFailure(driver, access.code, "authentication")
		}
		if access.code == "authorization_failed" {
			return checkFailure(driver, access.code, "authorization")
		}
	}
	var httpStatus *HTTPStatusError
	if errors.As(err, &httpStatus) {
		observation := checkFailure(driver, "unexpected_http_status", "response")
		status := httpStatus.Status
		observation.HTTPStatus = &status
		return validatedCheckFailure(driver, observation)
	}
	var grpcStatus *GRPCHealthError
	if errors.As(err, &grpcStatus) {
		if grpcStatus.RPCCode == codes.Canceled {
			return checkFailure(driver, "cancelled", "local")
		}
		if grpcStatus.Status != "" {
			observation := checkFailure(driver, "grpc_health_failed", "response")
			status := api.FailureDiagnosticGrpcStatus(grpcStatus.Status)
			observation.GRPCStatus = &status
			return validatedCheckFailure(driver, observation)
		}
		observation := checkFailure(driver, "grpc.rpc_failed", "check")
		status := grpcStatus.RPCCode.String()
		observation.GRPCCode = &status
		return validatedCheckFailure(driver, observation)
	}
	var expiry *CertificateExpiryError
	if errors.As(err, &expiry) {
		observation := checkFailure(driver, "certificate_expiring", "tls")
		date, days := expiry.NotAfter.UTC(), expiry.DaysRemaining
		observation.CertificateNotAfter, observation.CertificateDaysRemaining = &date, &days
		return validatedCheckFailure(driver, observation)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		code = "dns_failed"
		if dns.IsNotFound {
			code = "dns.not_found"
		} else if dns.IsTimeout {
			code = "dns.timeout"
		}
		// IsTemporary does not identify an RCODE or establish SERVFAIL.
		return checkFailure(driver, code, "resolve")
	}
	var untrusted x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var certificate *tls.CertificateVerificationError
	var protocol tls.RecordHeaderError
	switch {
	case errors.As(err, &untrusted):
		return checkFailure(driver, "tls.cert.untrusted", "tls")
	case errors.As(err, &hostname):
		return checkFailure(driver, "tls.cert.hostname", "tls")
	case errors.As(err, &invalid):
		code = "tls.cert.invalid"
		if invalid.Reason == x509.Expired && invalid.Cert != nil && !at.IsZero() {
			if at.Before(invalid.Cert.NotBefore) {
				code = "tls.cert.not_yet_valid"
			}
			if at.After(invalid.Cert.NotAfter) {
				code = "tls.cert.expired"
			}
		}
		return checkFailure(driver, code, "tls")
	case errors.As(err, &certificate):
		return checkFailure(driver, "tls_failed", "tls")
	case errors.As(err, &protocol):
		return checkFailure(driver, "tls.protocol_error", "tls")
	}
	// Docker daemon availability is not a container state observation. Adapters
	// report a typed container condition only after an actual inspect response.
	if driver == "docker" {
		return checkFailure(driver, "check.unavailable", "local")
	}
	var operation *net.OpError
	if errors.As(err, &operation) {
		switch operation.Op {
		case "dial":
			phase = "connect"
		case "read":
			phase = "response"
		case "write":
			phase = "request"
		}
		if phase != "check" {
			switch {
			case errors.Is(err, syscall.ECONNREFUSED):
				code = "network.connection_refused"
			case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
				code = "network.connection_reset"
			case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
				code = "network.unreachable"
			case operation.Timeout():
				code = "network.timeout"
			default:
				code = "connection_failed"
			}
			return checkFailure(driver, code, phase)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return checkFailure(driver, "deadline_exceeded", "check")
	}
	return checkFailure(driver, "operation_failed", "check")
}

func checkFailure(driver, code, phase string) api.FailureDiagnostic {
	definition, ok := api.FailureCodeDefinition(api.FailureTaxonomyVersion, code)
	if ok {
		for _, supported := range definition.Drivers {
			if supported == driver {
				observation, err := api.CanonicalFailure(api.FailureTaxonomyVersion, code, phase)
				if err == nil {
					return observation
				}
			}
		}
	}
	observation, _ := api.CanonicalFailure(api.FailureTaxonomyVersion, "operation_failed", "check")
	return observation
}

func validatedCheckFailure(driver string, observation api.FailureDiagnostic) api.FailureDiagnostic {
	if api.ValidateFailureObservation(driver, observation) != nil {
		return checkFailure(driver, "operation_failed", "check")
	}
	return observation
}
