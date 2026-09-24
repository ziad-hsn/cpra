package api

import "fmt"

// Collection identity requests include private key material and may also include
// provider configuration. Ordinary Go formatting deliberately omits the whole
// request. JSON encoding remains available for the authenticated wire protocol;
// callers must never log or persist that encoding as plaintext.
func (PreflightRequest) String() string               { return "private collection preflight (input omitted)" }
func (r PreflightRequest) GoString() string           { return r.String() }
func (r PreflightRequest) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }

// String omits the write-only collection key and source commitment.
func (OperationCreateRequest) String() string               { return "private collection creation (input omitted)" }
func (r OperationCreateRequest) GoString() string           { return r.String() }
func (r OperationCreateRequest) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }

// String omits the private key and source commitment used to prepare a ticket.
func (CollectionPrepareRequest) String() string {
	return "private collection preparation (input omitted)"
}
func (r CollectionPrepareRequest) GoString() string           { return r.String() }
func (r CollectionPrepareRequest) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }

// String omits the opaque creation ticket, including under nested formatting.
func (CollectionAdmission) String() string               { return "private collection admission (input omitted)" }
func (r CollectionAdmission) GoString() string           { return r.String() }
func (r CollectionAdmission) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }
