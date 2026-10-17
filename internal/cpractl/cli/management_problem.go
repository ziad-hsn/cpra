package cli

import (
	"fmt"
	"strings"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Problem detail and field messages are server-controlled text. Show only
// bounded correlation identities and reviewed codes, never raw detail/values.
func managementProblemDetails(problem *cpra.Error) string {
	var result strings.Builder
	if problem.StatusCode >= 100 && problem.StatusCode <= 599 {
		fmt.Fprintf(&result, "; HTTP %d", problem.StatusCode)
	}
	if managementProblemCode(problem.Problem.Code) {
		result.WriteString("; code " + problem.Problem.Code)
	}
	requestID := problem.RequestID
	if requestID == "" {
		requestID = problem.Problem.RequestID
	}
	if safeManagementHandle(requestID) {
		result.WriteString("; request " + requestID)
	}
	for i, field := range problem.Problem.Errors {
		if i == 10 {
			result.WriteString("; additional field errors omitted")
			break
		}
		name := "resource field"
		if safe := api.SafeValidationField(field.Field); safe != "" {
			name = safe
		}
		message := api.ValidationMessage(field.Reason)
		if message == "" {
			message = "validation failed"
		}
		result.WriteString("; " + name + ": " + message)
		if managementProblemCode(field.Reason) {
			result.WriteString(" (" + field.Reason + ")")
		}
	}
	return result.String()
}

func managementProblemCode(code string) bool {
	switch code {
	case "internalError", "unauthorized", "forbidden", "admissionBusy", "admissionUnavailable", "outcomeUnconfirmed", "operationAllocationUnconfirmed", "requestInterrupted", "unavailable",
		"collectionQuota", "collectionConflict", "invalidCollection", "collectionUnavailable", "operationExpired", "operationConflict", "operationNotFound", "historyUnavailable", "cursorExpired", "notFound", "versionConflict",
		"evidenceConflict", "recoveryIneligible", "recoveryRateLimited", "executorUnfenced", "invalidControl", "resourceReferenced", "dependencyConflict", "validationGraphLimit", "invalidResource", "invalidGraph",
		"validationPending", "validationInterrupted", "validationCanceled", "validationNotRequested", "featureUnavailable", "preconditionRequired", "invalidPrecondition", "invalidJSON", "bodyTooLarge":
		return true
	}
	return false
}
