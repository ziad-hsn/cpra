package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (m *managementHTTP) registerCollectionCancellation(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v2/operations/{id}/cancel", m.handleCollectionCancellation)
}

func (m *managementHTTP) handleCollectionCancellation(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	access, err := m.auth.Authorize(r, "CancelOperation")
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "Cancellation accepts only its original operation path."))
		return
	}
	if !m.ready() {
		writeManagementError(w, mutationUnavailable())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	select {
	case m.mutations <- struct{}{}:
		defer func() { <-m.mutations }()
	default:
		w.Header().Set("Retry-After", "1")
		writeManagementError(w, managementFailure(429, "admissionBusy", "Management admission capacity is busy."))
		return
	}
	if err := collectionCancellationBody(w, r); err != nil {
		writeManagementError(w, err)
		return
	}
	admit := func(commit func() error) error {
		return m.auth.WithAdmission(r, "CancelOperation", func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "The authenticated principal changed.")
			}
			return m.admit(ctx, commit)
		})
	}
	result, err := m.catalog.CancelCollection(ctx, r.PathValue("id"), access.PrincipalID, m.now, admit)
	if result.ID != "" {
		w.Header().Set("X-Operation-ID", result.ID)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	current, err := m.auth.Authorize(r, "CancelOperation")
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if current.PrincipalID != access.PrincipalID {
		writeManagementError(w, managementFailure(403, "forbidden", "The authenticated principal changed."))
		return
	}
	if err := ctx.Err(); err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}

// The wire contract has no request body. Bound even an unexpected/chunked stream
// at its actual read, so a payload cannot be ignored or held across admission.
func collectionCancellationBody(w http.ResponseWriter, r *http.Request) error {
	return collectionEmptyBody(w, r, "Cancellation")
}

func collectionEmptyBody(w http.ResponseWriter, r *http.Request, action string) error {
	if encoding := r.Header.Values("Content-Encoding"); len(encoding) > 1 || len(encoding) == 1 && encoding[0] != "identity" {
		return managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported.")
	}
	deadline, ok := r.Context().Deadline()
	if !ok {
		return mutationUnavailable()
	}
	control := http.NewResponseController(w)
	if control.SetReadDeadline(deadline) != nil {
		return mutationUnavailable()
	}
	defer control.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, 0)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	defer clear(raw)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return managementFailure(413, "unexpectedBody", action+" does not accept a request body.")
		}
		return managementFailure(400, "invalidBody", "The request body could not be read.")
	}
	return nil
}
