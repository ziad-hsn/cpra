package cli

import (
	"errors"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
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
