package cli

import (
	"fmt"
	"regexp"
	"strings"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

var managementField = regexp.MustCompile(`^(metadata|spec|items|sources|revision|reason|note|duration|incidentID|resolution|evidenceRefs)(\.[A-Za-z][A-Za-z0-9_]*|\[[0-9]+\])*$`)

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
		if len(field.Field) <= 160 && managementField.MatchString(field.Field) {
			name = field.Field
		}
		result.WriteString("; " + name + ": validation failed")
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
