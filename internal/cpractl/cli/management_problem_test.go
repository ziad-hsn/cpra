package cli

import (
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestManagementProblemDetails(t *testing.T) {
	problem := &cpra.Error{StatusCode: 422, RequestID: cliOperationID, OperationID: cliOperationID, Problem: api.Problem{
		Code: "invalidResource", Detail: cliSecretValue,
		Errors: []api.FieldError{{Field: "spec.check.driver.config", Reason: "invalidResource", Message: cliSecretValue}},
	}}
	err := managementFailure(problem)
	for _, want := range []string{"HTTP 422", "code invalidResource", "request " + cliOperationID, "operation " + cliOperationID, "spec.check.driver.config: The resource schema, compiled driver or configuration is invalid. (invalidResource)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing safe problem detail %q in %q", want, err)
		}
	}
	if strings.Contains(err.Error(), cliSecretValue) || !errors.Is(err, cpra.ErrInvalid) {
		t.Fatal("raw detail leaked or error classification changed")
	}
}

func TestManagementTokenFileErrorsCrossSDKBoundary(t *testing.T) {
	for _, empty := range []bool{false, true} {
		fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("token failure sent an authenticated request") })
		want := "API token file does not exist"
		if empty {
			if err := os.WriteFile(fixture.token, []byte("\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want = "API token file is empty"
		} else if err := os.Remove(fixture.token); err != nil {
			t.Fatal(err)
		}
		out, stderr, err := fixture.run(t, nil, "get", "monitors")
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(out+stderr+err.Error(), fixture.token) || errors.Is(err, cpra.ErrAmbiguous) {
			t.Fatal("local authentication failure lost its safe cause", out, stderr, err)
		}
	}
}

func TestManagementFailureCausesRemainDistinctAndSafe(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"missing file", localFileFailure("API token file", &os.PathError{Path: cliSecretValue, Err: os.ErrNotExist}), "API token file does not exist"},
		{"unreadable file", localFileFailure("selected input file", &os.PathError{Path: cliSecretValue, Err: os.ErrPermission}), "not readable by the current user"},
		{"wrong file type", localFileFailure("API CA file", errInputNotRegular), "must be a regular file"},
		{"large file", localFileFailure("API token file", errInputTooLarge), "exceeds its byte limit"},
		{"empty token", &managementCommandError{"API token file is empty", cpra.ErrInvalid}, "API token file is empty"},
		{"trust", &cpra.TransportError{Cause: x509.UnknownAuthorityError{Cert: &x509.Certificate{DNSNames: []string{cliSecretValue}}}}, "TLS certificate is not trusted"},
		{"hostname", &cpra.TransportError{Cause: x509.HostnameError{Host: cliSecretValue}}, "does not match the configured server name"},
		{"validity", &cpra.TransportError{Cause: x509.CertificateInvalidError{Reason: x509.Expired}}, "certificate verification failed"},
		{"network", &cpra.TransportError{Cause: &net.OpError{Op: "dial", Err: errors.New(cliSecretValue)}}, "could not communicate with the API"},
		{"authentication", &cpra.Error{StatusCode: 401}, "API authentication failed"},
		{"authorization", &cpra.Error{StatusCode: 403}, "authenticated identity is not authorized"},
		{"throttled", &cpra.Error{StatusCode: 429}, "API request throttled"},
		{"unavailable", &cpra.Error{StatusCode: 503}, "reachable but unavailable"},
		{"ambiguous", &cpra.AmbiguousError{Cause: &cpra.TransportError{Cause: x509.UnknownAuthorityError{}}}, "mutation outcome is unconfirmed"},
		{"server path", &os.PathError{Path: cliSecretValue, Err: os.ErrNotExist}, "management request failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := managementFailure(test.err)
			if !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), cliSecretValue) || !errors.Is(err, test.err) {
				t.Fatalf("wrong or unsafe classification: %v", err)
			}
		})
	}
}

func TestCollectionInputAndPrivateStagingErrorsAreDistinct(t *testing.T) {
	f := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("input failure reached API") })
	missing := filepath.Join(t.TempDir(), "missing")
	_, _, err := f.run(t, nil, "apply", "-f", missing)
	if err == nil || !strings.Contains(err.Error(), "input does not exist") || strings.Contains(err.Error(), missing) {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	for _, profile := range []string{"", collection.FileNormalizationProfile} {
		args := []string{"apply", "-f", "-"}
		if profile != "" {
			args = append(args, "--file-profile", profile)
		}
		_, _, err := f.run(t, []byte(`{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"oncall"},"spec":{"endpointRefs":["mail"]}}`), args...)
		if err == nil || !strings.Contains(err.Error(), "private collection staging is unavailable") || strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), "input does not exist") {
			t.Fatal(err)
		}
	}
}

func TestManagementProblemDetailsRejectUntrustedTextAndBoundFields(t *testing.T) {
	problem := &cpra.Error{StatusCode: 999999, RequestID: "\x1b[31m" + cliSecretValue, OperationID: cliSecretValue,
		Problem: api.Problem{Code: cliSecretValue, Title: cliSecretValue, Detail: cliSecretValue, Instance: "/private/state"}}
	for range 12 {
		problem.Problem.Errors = append(problem.Problem.Errors, api.FieldError{Field: "/private/" + cliSecretValue, Message: cliSecretValue, Reason: cliSecretValue})
	}
	message := managementFailure(problem).Error()
	if strings.Contains(message, cliSecretValue) || strings.Contains(message, "/private") || strings.Contains(message, "\x1b") || strings.Contains(message, "999999") {
		t.Fatal("untrusted metadata reached terminal output")
	}
	if strings.Count(message, "; resource field:") != 10 || !strings.Contains(message, "additional field errors omitted") {
		t.Fatal("field output is not bounded")
	}
}
