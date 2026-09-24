package cpra

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Only this complete server contract proves that target admission did not occur.
// All malformed, truncated or contradictory 5xx responses remain ambiguous.
func allocationNotSubmitted(response *http.Response, raw []byte) bool {
	if response.StatusCode != http.StatusServiceUnavailable || len(response.Header.Values("X-Operation-ID")) != 0 || !utf8.Valid(raw) {
		return false
	}
	admission := response.Header.Values("X-CPRa-Admission")
	contentTypes := response.Header.Values("Content-Type")
	if len(admission) != 1 || admission[0] != "not-submitted" || len(contentTypes) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/problem+json" {
		return false
	}
	var problem api.Problem
	if api.StrictDecode(raw, &problem) != nil || problem.Type != "about:blank" || problem.Status != 503 || problem.Code != "operationAllocationUnconfirmed" || strings.TrimSpace(problem.Title) == "" {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	// encoding/json otherwise accepts case-insensitive struct field names. This
	// exception must match the exact public problem contract, not an alias or
	// an ambiguous pair such as status and Status.
	for field, encoded := range fields {
		switch field {
		case "status":
		case "type", "title", "detail", "instance", "code", "requestID":
			if !problemString(encoded) {
				return false
			}
		case "errors":
			var items []map[string]json.RawMessage
			if !bytes.HasPrefix(bytes.TrimSpace(encoded), []byte("[")) || json.Unmarshal(encoded, &items) != nil {
				return false
			}
			for _, item := range items {
				if !problemString(item["field"]) || !problemString(item["message"]) {
					return false
				}
				for name, value := range item {
					if name != "field" && name != "message" && name != "reason" || !problemString(value) {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return true
}

func problemString(raw json.RawMessage) bool {
	var value *string
	return json.Unmarshal(raw, &value) == nil && value != nil
}
