package httpserver

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var reselectionOperations = []struct{ method, path, name string }{
	{"POST", "/api/v2/operations/{id}/reselection", "CreateCollectionReselection"},
	{"GET", "/api/v2/operations/{id}/reselection/{attempt}", "GetCollectionReselection"},
	{"DELETE", "/api/v2/operations/{id}/reselection/{attempt}", "DiscardCollectionReselection"},
	{"PUT", "/api/v2/operations/{id}/reselection/{attempt}/sources/{source}", "UploadCollectionReselectionSource"},
	{"POST", "/api/v2/operations/{id}/reselection/{attempt}/verify", "VerifyCollectionReselection"},
	{"POST", "/api/v2/operations/{id}/reselection/{attempt}/resume", "ResumeCollectionReselection"},
}

func (m *managementHTTP) registerCollectionReselection(mux *http.ServeMux) {
	if m.reselection == nil {
		return
	}
	for _, route := range reselectionOperations {
		mux.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) { m.handleCollectionReselection(w, r, route.name) })
	}
}

func (m *managementHTTP) handleCollectionReselection(w http.ResponseWriter, r *http.Request, operation string) {
	managementHeaders(w)
	authRequest, mediaErr := reselectionAuthorizationRequest(r, operation)
	access, err := m.auth.Authorize(authRequest, operation)
	if err == nil {
		err = collectionActivationPermissions(access)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if mediaErr != nil {
		writeManagementError(w, mediaErr)
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
		w.Header().Set("Retry-After", "5")
		writeManagementError(w, managementFailure(429, "admissionBusy", "Collection reselection capacity is busy."))
		return
	}
	id, attempt := r.PathValue("id"), r.PathValue("attempt")
	// Parsing and source reads happen outside the policy lock. Admission checks
	// the same principal again and joins the server's bounded shutdown accounting.
	// These callbacks stage local bytes or schedule work; they run no provider.
	admit := func(work func() error) error {
		return m.auth.WithAdmission(r, operation, func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "The authenticated principal changed.")
			}
			if err := collectionActivationPermissions(current); err != nil {
				return err
			}
			return m.admit(ctx, work)
		})
	}
	var result management.CollectionReselectionStatus
	status := http.StatusOK
	if operation != "UploadCollectionReselectionSource" && r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "This operation does not accept query parameters."))
		return
	}
	switch operation {
	case "CreateCollectionReselection":
		var raw []byte
		err = m.reselection.CheckCreate(ctx, id, access.PrincipalID)
		if err == nil {
			raw, err = reselectionBody(w, r, 4096)
		}
		defer clear(raw)
		if err == nil {
			var req api.CollectionReselectionCreateRequest
			if api.StrictDecode(raw, &req) != nil || req.SourceCount < 1 || req.SourceCount > 1000 {
				err = management.ErrValidation
			} else {
				err = admit(func() error {
					var createErr error
					result, createErr = m.reselection.Create(ctx, id, access.PrincipalID, req.NormalizationProfile, int(req.SourceCount))
					return createErr
				})
			}
		}
		status = http.StatusCreated
	case "UploadCollectionReselectionSource":
		var source, offset uint64
		var end bool
		source, offset, end, err = reselectionPartPosition(r)
		if err == nil {
			_, err = m.reselection.Get(ctx, id, attempt, access.PrincipalID)
		}
		if err == nil {
			var raw []byte
			raw, err = reselectionBody(w, r, 1<<20)
			defer clear(raw)
			if err == nil {
				err = admit(func() error {
					var uploadErr error
					result, uploadErr = m.reselection.Upload(ctx, id, attempt, access.PrincipalID, source, offset, end, raw)
					return uploadErr
				})
			}
		}
	default:
		result, err = m.reselection.Get(ctx, id, attempt, access.PrincipalID)
		if err == nil {
			var raw []byte
			raw, err = reselectionBody(w, r, 0)
			clear(raw)
		}
		if err == nil {
			switch operation {
			case "GetCollectionReselection":
				// The owner-scoped read above is refreshed before publication.
			case "VerifyCollectionReselection":
				err = admit(func() error {
					var verifyErr error
					result, verifyErr = m.reselection.Verify(ctx, id, attempt, access.PrincipalID)
					return verifyErr
				})
				status = http.StatusAccepted
			case "ResumeCollectionReselection":
				err = admit(func() error {
					var resumeErr error
					result, resumeErr = m.reselection.Resume(ctx, id, attempt, access.PrincipalID)
					return resumeErr
				})
				status = http.StatusAccepted
			case "DiscardCollectionReselection":
				err = admit(func() error { return m.reselection.Discard(ctx, id, attempt, access.PrincipalID) })
				status = http.StatusNoContent
			default:
				err = management.ErrUnavailable
			}
		}
	}
	// The original request may finish after a policy update. Do not publish its
	// private attempt handle to a newly authenticated owner of the same bearer.
	current, authErr := m.auth.Authorize(r, operation)
	if authErr == nil && current.PrincipalID != access.PrincipalID {
		authErr = managementFailure(403, "forbidden", "The authenticated principal changed.")
	}
	if authErr == nil {
		authErr = collectionActivationPermissions(current)
	}
	if authErr == nil {
		authErr = ctx.Err()
	}
	if authErr != nil {
		writeManagementError(w, authErr)
		return
	}
	if err != nil {
		writeReselectionError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	result, err = m.reselection.Get(ctx, id, result.ID, access.PrincipalID)
	if err != nil {
		writeReselectionError(w, err)
		return
	}
	current, err = m.auth.Authorize(r, operation)
	if err == nil && current.PrincipalID != access.PrincipalID {
		err = managementFailure(403, "forbidden", "The authenticated principal changed.")
	}
	if err == nil {
		err = collectionActivationPermissions(current)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	w.Header().Set("X-Operation-ID", id)
	if result.Phase == "verifying" || result.Phase == "transferring" {
		w.Header().Set("Retry-After", "5")
	}
	writeManagementJSON(w, status, reselectionResponse(result))
}

// The common authorizer rejects media errors as forbidden. For these explicit
// body contracts, authenticate an otherwise identical request before returning
// 415. The header-adjusted clone is never used to read a body or admit work.
func reselectionAuthorizationRequest(r *http.Request, operation string) (*http.Request, error) {
	expected := ""
	switch operation {
	case "CreateCollectionReselection":
		expected = "application/json"
	case "UploadCollectionReselectionSource":
		expected = "application/octet-stream"
	default:
		return r, nil
	}
	values := r.Header.Values("Content-Type")
	if len(values) == 1 && len(values[0]) <= 256 {
		media, _, err := mime.ParseMediaType(values[0])
		if err == nil && media == expected {
			return r, nil
		}
	}
	authRequest := r.Clone(r.Context())
	authRequest.Header.Set("Content-Type", expected)
	return authRequest, managementFailure(415, "unsupportedMediaType", "The request content type does not match this operation.")
}

func reselectionResponse(s management.CollectionReselectionStatus) api.CollectionReselectionAttempt {
	r := api.CollectionReselectionAttempt{ID: s.ID, OperationID: s.OperationID, NormalizationProfile: s.NormalizationProfile, Phase: api.CollectionReselectionAttemptPhase(s.Phase), SourceCount: int64(s.SourceCount), SourcesCompleted: int64(s.SourcesCompleted), RawBytes: s.RawBytes, NextSource: int64(s.NextSource), NextOffset: s.NextOffset, ExpiresAt: s.ExpiresAt, OperationUploaded: s.OperationUploaded}
	if s.ErrorCode != "" {
		code := api.CollectionReselectionAttemptErrorCode(s.ErrorCode)
		r.ErrorCode = &code
	}
	return r
}

func reselectionPartPosition(r *http.Request) (uint64, uint64, bool, error) {
	if len(r.URL.RawQuery) > 128 {
		return 0, 0, false, management.ErrValidation
	}
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	source, err := strconv.ParseUint(r.PathValue("source"), 10, 64)
	if err != nil || queryErr != nil || source < 1 || source > 1000 || strconv.FormatUint(source, 10) != r.PathValue("source") || len(q) != 2 || len(q["offset"]) != 1 || len(q["end"]) != 1 {
		return 0, 0, false, management.ErrValidation
	}
	offset, err := strconv.ParseUint(q.Get("offset"), 10, 64)
	if err != nil || offset > 64<<20 || strconv.FormatUint(offset, 10) != q.Get("offset") || q.Get("end") != "true" && q.Get("end") != "false" {
		return 0, 0, false, management.ErrValidation
	}
	return source, offset, q.Get("end") == "true", nil
}

func reselectionBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if encoding := r.Header.Values("Content-Encoding"); len(encoding) > 1 || len(encoding) == 1 && encoding[0] != "identity" {
		return nil, managementFailure(415, "unsupportedEncoding", "Encoded source bodies are not supported.")
	}
	if r.ContentLength > limit {
		return nil, reselectionBodyTooLarge(limit)
	}
	deadline, ok := r.Context().Deadline()
	if !ok {
		return nil, management.ErrUnavailable
	}
	control := http.NewResponseController(w)
	if control.SetReadDeadline(deadline) != nil {
		return nil, management.ErrUnavailable
	}
	defer control.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err == nil {
		if err := r.Context().Err(); err != nil {
			clear(raw)
			return nil, err
		}
		return raw, nil
	}
	clear(raw)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, reselectionBodyTooLarge(limit)
	}
	if r.Context().Err() != nil {
		return nil, r.Context().Err()
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		// The socket deadline can fire before the context timer is delivered.
		return nil, context.DeadlineExceeded
	}
	return nil, management.ErrValidation
}

func reselectionBodyTooLarge(limit int64) error {
	if limit == 0 {
		return managementFailure(413, "unexpectedBody", "Collection reselection does not accept a request body for this operation.")
	}
	return managementFailure(413, "sourcePartTooLarge", "The request exceeds the collection reselection body limit.")
}

func writeReselectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, management.ErrReselectionNotFound):
		err = managementFailure(404, "reselectionNotFound", "The original owner's attempt is unavailable. Inspect the original operation before starting a new attempt.")
	case errors.Is(err, management.ErrReselectionConflict):
		err = managementFailure(409, "reselectionConflict", "The attempt input or phase changed. Inspect its original progress.")
	case errors.Is(err, management.ErrReselectionBusy):
		w.Header().Set("Retry-After", "5")
		err = managementFailure(429, "reselectionBusy", "Reselection capacity is busy. Reuse the existing attempt or wait before creating one.")
	case errors.Is(err, persistence.ErrOperatorAuthorityDenied), errors.Is(err, persistence.ErrAuthenticationConflict):
		err = managementFailure(403, "forbidden", "Current authority for the original operator is required.")
	}
	writeManagementError(w, err)
}
