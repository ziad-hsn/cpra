package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func (o *options) mustClient() (*cpra.Client, error) {
	if o.apiClient != nil {
		return o.apiClient, nil
	}
	config := cpra.Config{BaseURL: o.server, Timeout: o.timeout, AllowInsecureHTTP: o.insecureHTTP, ReadAttempts: 1}
	if o.tokenFile != "" {
		config.TokenSource = func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			data, err := readManagementFile(o.tokenFile, 16<<10)
			if err != nil {
				return "", localFileFailure("API token file", err)
			}
			defer clear(data)
			token := strings.TrimSpace(string(data))
			if token == "" {
				return "", &managementCommandError{"API token file is empty", cpra.ErrInvalid}
			}
			return token, nil
		}
	} else {
		config.AuthToken = os.Getenv("CPRA_AUTH_TOKEN")
	}
	roots, err := loadAPIRoots(o.caFile)
	if err != nil {
		return nil, err
	}
	config.RootCAs = roots
	client, err := cpra.New(config)
	if err != nil {
		return nil, err
	}
	o.apiClient = client
	return client, nil
}

func loadAPIRoots(path string) (*x509.CertPool, error) {
	if path != "" {
		data, err := readManagementFile(path, 4<<20)
		if err != nil {
			return nil, localFileFailure("API CA file", err)
		}
		defer clear(data)
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, &managementCommandError{"system trust roots are unavailable", err}
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, &managementCommandError{"API CA file contains no certificates", cpra.ErrInvalid}
		}
		return roots, nil
	}
	return nil, nil
}

func readManagementFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errInputNotRegular
	}
	data, readErr := readManagementInput(file, limit)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		clear(data)
		return nil, closeErr
	}
	return data, nil
}

func readManagementInput(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		clear(data)
		return nil, err
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, errInputTooLarge
	}
	return data, nil
}

var (
	errInputNotRegular = errors.New("selected input must be a regular file")
	errInputTooLarge   = errors.New("input exceeds its byte limit")
)

// Only call this at local file boundaries. A server or provider PathError must
// never be mistaken for a user-selected file or printed with its path.
func localFileFailure(subject string, err error) error {
	message := subject + " could not be read"
	switch {
	case errors.Is(err, os.ErrNotExist):
		message = subject + " does not exist; check the selected path"
	case errors.Is(err, os.ErrPermission):
		message = subject + " is not readable by the current user"
	case errors.Is(err, errInputNotRegular):
		message = subject + " must be a regular file"
	case errors.Is(err, errInputTooLarge):
		message = subject + " exceeds its byte limit"
	}
	return &managementCommandError{message, err}
}

type managementCommandError struct {
	message string
	cause   error
}

func (e *managementCommandError) Error() string { return e.message }
func (e *managementCommandError) Unwrap() error { return e.cause }

func managementFailure(err error) error {
	if err == nil {
		return nil
	}
	var sourceAccess *collection.SourceAccessError
	if errors.As(err, &sourceAccess) && !errors.Is(err, cpra.ErrAmbiguous) {
		return localFileFailure("input", err)
	}
	message := "management request failed"
	var problem *cpra.Error
	var local *managementCommandError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var transport *cpra.TransportError
	var network *net.OpError
	errors.As(err, &problem)
	switch {
	case errors.Is(err, cpra.ErrAmbiguous):
		message = "mutation outcome is unconfirmed; inspect the original operation or resource before retrying"
	case errors.Is(err, cpra.ErrNotAdmitted):
		message = "no resource or action mutation was submitted by this request; inspect any original operation before explicit continuation; request was not retried"
	case errors.Is(err, cpra.ErrConflict):
		message = "resource version conflict; inspect the current resource and resolve the intended change"
	case problem != nil && problem.StatusCode == http.StatusUnauthorized:
		message = "API authentication failed; check the selected token and current authority"
	case problem != nil && problem.StatusCode == http.StatusForbidden:
		message = "permission denied; the authenticated identity is not authorized for this operation"
	case problem != nil && problem.StatusCode == http.StatusTooManyRequests:
		message = "API request throttled; wait before making an explicit request; no mutation was retried"
	case errors.Is(err, cpra.ErrUnauthorized):
		message = "management authentication or permission denied; use a named identity authorized for this operation"
	case errors.Is(err, cpra.ErrNotFound):
		message = "management resource was not found"
	case errors.Is(err, cpra.ErrExpired):
		message = "operation or cursor expired; request a fresh observation"
	case errors.Is(err, cpra.ErrExecutionResultExpired):
		message = "the original operation's retained execution result has expired"
	case errors.Is(err, cpra.ErrExecutionResultUnsupported):
		message = "the server did not report supported execution result availability for the original operation"
	case errors.Is(err, cpra.ErrFeatureUnavailable):
		message = "the server does not support this operation"
	case errors.Is(err, cpra.ErrUnavailable):
		message = "API is reachable but unavailable for this request; inspect readiness and storage state"
	case errors.As(err, &local):
		message = local.message
	case errors.Is(err, cpra.ErrInvalid):
		message = "management request was rejected; inspect resource fields and version preconditions"
	case errors.Is(err, cpra.ErrResponseTooLarge):
		message = "management response exceeded the configured byte limit"
	case errors.Is(err, context.Canceled):
		message = "management request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "management request deadline exceeded"
	case errors.As(err, &unknownAuthority):
		message = "API TLS certificate is not trusted; select the server CA with --ca-file"
	case errors.As(err, &hostname):
		message = "API TLS certificate does not match the configured server name"
	case errors.As(err, &certificate):
		message = "API TLS certificate verification failed; check certificate validity and trust configuration"
	case errors.As(err, &transport), errors.As(err, &network):
		message = "could not communicate with the API; check the server address, listener and network"
	}
	var ambiguous *cpra.AmbiguousError
	operationID := ""
	if errors.As(err, &ambiguous) {
		operationID = ambiguous.OperationID
	}
	if errors.As(err, &problem) && operationID == "" {
		operationID = problem.OperationID
	}
	var source *collection.SourceError
	errors.As(err, &source)
	if field, ok := api.ValidationFieldError(err); ok {
		message += "; " + field.Message
		path := field.Field
		if source != nil && api.SafeValidationField(source.Field) != "" {
			path = source.Field
		}
		if path != "" {
			message += " (" + path + ")"
		}
	}
	if source != nil {
		message += "; " + managementDisplay(source.Location.String())
	}
	if problem != nil {
		message += managementProblemDetails(problem)
	}
	if safeManagementHandle(operationID) {
		message += "; operation " + operationID
	}
	return &managementCommandError{message, err}
}
