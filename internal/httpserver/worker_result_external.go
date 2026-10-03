//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const maxWorkerResultBodyBytes = 128 << 10
const maxConcurrentWorkerResults = 32
const maxWorkerResultsPerPrincipal = 8

func (h *workerSessionHTTP) reserveResult(uid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resulting >= maxConcurrentWorkerResults || h.results[uid] >= maxWorkerResultsPerPrincipal {
		return false
	}
	if h.results == nil {
		h.results = make(map[string]int)
	}
	h.results[uid]++
	h.resulting++
	return true
}

func (h *workerSessionHTTP) releaseResult(uid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resulting--
	h.results[uid]--
	if h.results[uid] == 0 {
		delete(h.results, uid)
	}
}

func (h *workerSessionHTTP) result(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	authority, err := h.auth.AuthorizeWorker(r, "WorkerResult", h.store)
	if err != nil {
		writeWorkerResultError(w, err)
		return
	}
	if h.completionReady == nil || h.complete == nil || h.catalog == nil {
		writeWorkerResultError(w, persistence.ErrWorkerSessionUnavailable)
		return
	}
	if err := h.completionReady(ctx); err != nil {
		writeWorkerResultError(w, err)
		return
	}
	if !h.reserveResult(authority.WorkerUID) {
		w.Header().Set("Retry-After", "1")
		writeWorkerResultError(w, persistence.ErrWorkerExecutionQuota)
		return
	}
	defer h.releaseResult(authority.WorkerUID)
	raw, err := readWorkerProtocolBody(w, r, maxWorkerResultBodyBytes)
	defer clear(raw)
	if err != nil {
		switch {
		case errors.Is(err, errWorkerBodyEncoding):
			writeManagementError(w, managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported."))
		case errors.Is(err, errWorkerBodyLimit):
			writeManagementError(w, managementFailure(413, "workerResultTooLarge", "Worker result requests cannot exceed 128 KiB."))
		case errors.Is(err, errWorkerBodyInvalid):
			writeWorkerResultError(w, management.ErrWorkerOutcomeInvalid)
		default:
			writeWorkerResultError(w, err)
		}
		return
	}
	var input api.Outcome
	if api.StrictDecode(raw, &input) != nil {
		writeWorkerResultError(w, management.ErrWorkerOutcomeInvalid)
		return
	}
	defer clear(input.Data)
	var receipt persistence.WorkerReceipt
	err = h.complete(ctx, func() error {
		var err error
		receipt, err = h.catalog.CommitWorkerOutcome(ctx, authority, input)
		return err
	})
	if err != nil {
		writeWorkerResultError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, api.Receipt{ServerID: receipt.ServerID, WorkerUID: receipt.WorkerUID,
		ExecutionID: receipt.ExecutionID, ReceiptID: receipt.ReceiptID, Disposition: api.ReceiptDisposition(receipt.Disposition)})
}

func writeWorkerResultError(w http.ResponseWriter, err error) {
	status, code, detail := 503, "unavailable", "Worker result storage is unavailable. Retain the original outcome."
	switch {
	case errors.Is(err, persistence.ErrCommitUnconfirmed):
		code, detail = "workerResultUnconfirmed", "Result admission is unconfirmed. Retain and retry the identical outcome."
	case errors.Is(err, persistence.ErrWorkerCheckExpired):
		status, code, detail = 410, "workerCheckExpired", "This check record has expired. Its execution cannot be resumed."
	case errors.Is(err, httpauth.ErrWorkerAuthentication), errors.Is(err, persistence.ErrWorkerUnauthorized):
		status, code, detail = 401, "unauthorized", "Supply a current worker bearer token."
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, httpauth.ErrWorkerForbidden), errors.Is(err, persistence.ErrWorkerAuthorityDenied):
		status, code, detail = 403, "forbidden", "Current worker authority and an approved HTTPS origin are required."
	case errors.Is(err, management.ErrWorkerOutcomeInvalid), errors.Is(err, management.ErrJobValueInvalid), errors.Is(err, persistence.ErrWorkerExecutionInvalid):
		status, code, detail = 400, "invalidWorkerResult", "The result does not match the original execution contract."
	case errors.Is(err, persistence.ErrWorkerExecutionConflict), errors.Is(err, persistence.ErrWorkerSessionExpired):
		status, code, detail = 409, "workerResultConflict", "The result conflicts with the original execution or its retained outcome."
	case errors.Is(err, persistence.ErrWorkerSessionIdentity):
		status, code, detail = 409, "workerServerIdentity", "The result does not match the provisioned server identity."
	case errors.Is(err, persistence.ErrWorkerExecutionQuota):
		status, code, detail = 429, "workerResultQuota", "Worker result capacity is exhausted. Retain the original outcome."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, detail = "requestInterrupted", "Result admission was interrupted. Retain and retry the identical outcome."
	}
	writeManagementError(w, managementFailure(status, code, detail))
}
