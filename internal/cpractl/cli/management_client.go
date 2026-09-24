package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"strings"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
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
				return "", &managementCommandError{"read API token file failed", err}
			}
			defer clear(data)
			token := strings.TrimSpace(string(data))
			if token == "" {
				return "", errors.New("API token file is empty")
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
			return nil, &managementCommandError{"read API CA file failed", err}
		}
		defer clear(data)
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("system trust roots are unavailable")
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, errors.New("API CA file contains no certificates")
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
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("selected input must be a regular file")
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
		return nil, errors.New("input exceeds its byte limit")
	}
	return data, nil
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
	message := "management request failed"
	switch {
	case errors.Is(err, cpra.ErrAmbiguous):
		message = "mutation outcome is unconfirmed; inspect the original operation or resource before retrying"
	case errors.Is(err, cpra.ErrNotAdmitted):
		message = "operation allocation is unconfirmed; no resource or action mutation was submitted; request was not retried"
	case errors.Is(err, cpra.ErrConflict):
		message = "resource version conflict; inspect the current resource and resolve the intended change"
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
		message = "management API is unavailable"
	case errors.Is(err, cpra.ErrInvalid):
		message = "management request was rejected; inspect resource fields and version preconditions"
	case errors.Is(err, cpra.ErrResponseTooLarge):
		message = "management response exceeded the configured byte limit"
	case errors.Is(err, context.Canceled):
		message = "management request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "management request deadline exceeded"
	}
	var ambiguous *cpra.AmbiguousError
	var problem *cpra.Error
	operationID := ""
	if errors.As(err, &ambiguous) {
		operationID = ambiguous.OperationID
	}
	if errors.As(err, &problem) && operationID == "" {
		operationID = problem.OperationID
	}
	if safeManagementHandle(operationID) {
		message += "; operation " + operationID
	}
	return &managementCommandError{message, err}
}
