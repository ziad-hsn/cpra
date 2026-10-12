package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
)

var ErrJobPanic = errors.New("job panicked")

// HTTPStatusError retains an observed status without a response body. It does
// not establish rejection of an external effect or authorize another attempt.
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("received unexpected status code: %d", e.Status)
}

// FailureCode classifies typed causes without inspecting arbitrary error text.
// This is an explanation only; effect rejection and retry permission are
// determined independently by the controller's delivery contract.
func FailureCode(err error) string {
	if err == nil {
		return ""
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
	var rejection *DeliveryError
	var httpStatus *HTTPStatusError
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var network net.Error
	switch {
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
