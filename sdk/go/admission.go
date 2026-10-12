package cpra

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Only this complete server contract proves that target admission did not occur.
// All malformed, truncated or contradictory 5xx responses remain ambiguous.
func allocationNotSubmitted(response *http.Response, raw []byte) bool {
	if response.StatusCode != http.StatusServiceUnavailable || !utf8.Valid(raw) {
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
	if api.StrictDecode(raw, &problem) != nil || problem.Type != "about:blank" || problem.Status != 503 || strings.TrimSpace(problem.Title) == "" {
		return false
	}
	if problem.Code == "operationAllocationUnconfirmed" {
		if len(response.Header.Values("X-Operation-ID")) != 0 {
			return false
		}
	} else if problem.Code == "admissionUnavailable" {
		retry := response.Header.Values("Retry-After")
		if len(retry) != 1 {
			return false
		}
		seconds, err := strconv.Atoi(retry[0])
		if err != nil || seconds < 1 || seconds > 60 || strconv.Itoa(seconds) != retry[0] || !originalAdmissionHandle(response) {
			return false
		}
	} else {
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

func originalAdmissionHandle(response *http.Response) bool {
	handles := response.Header.Values("X-Operation-ID")
	if len(handles) == 0 {
		return true
	}
	if len(handles) != 1 || !canonicalAdmissionHandle(handles[0]) || response.Request == nil || response.Request.URL == nil {
		return false
	}
	path := strings.TrimPrefix(response.Request.URL.Path, "/api/v2/operations/")
	if path == response.Request.URL.Path {
		return true // A resource mutation can retain a newly allocated handle.
	}
	id, _, _ := strings.Cut(path, "/")
	return id == handles[0]
}

func canonicalAdmissionHandle(id string) bool {
	parts := strings.Split(id, ".")
	if len(parts) != 3 || parts[0] != "op" || len(parts[1]) != 36 || len(parts[2]) != 20 {
		return false
	}
	for i, c := range parts[1] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	sequence, err := strconv.ParseUint(parts[2], 10, 64)
	return err == nil && sequence != 0
}
