//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const (
	maxWorkerStartBodyBytes     = 8 << 10
	maxConcurrentWorkerStarts   = 64
	maxWorkerStartsPerPrincipal = 16
)

func (h *workerSessionHTTP) reserveStart(uid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.starting >= maxConcurrentWorkerStarts || h.starts[uid] >= maxWorkerStartsPerPrincipal {
		return false
	}
	if h.starts == nil {
		h.starts = make(map[string]int)
	}
	h.starts[uid]++
	h.starting++
	return true
}

func (h *workerSessionHTTP) releaseStart(uid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starting--
	h.starts[uid]--
	if h.starts[uid] == 0 {
		delete(h.starts, uid)
	}
}

func (h *workerSessionHTTP) start(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	authority, err := h.auth.AuthorizeWorker(r, "WorkerStart", h.store)
	if err != nil {
		writeWorkerStartError(w, err)
		return
	}
	if h.ready == nil || h.admit == nil {
		writeWorkerStartError(w, persistence.ErrWorkerSessionUnavailable)
		return
	}
	if err := h.ready(ctx); err != nil {
		writeWorkerStartError(w, err)
		return
	}
	if !h.reserveStart(authority.WorkerUID) {
		w.Header().Set("Retry-After", "1")
		writeWorkerStartError(w, persistence.ErrWorkerExecutionQuota)
		return
	}
	defer h.releaseStart(authority.WorkerUID)
	raw, err := readWorkerProtocolBody(w, r, maxWorkerStartBodyBytes)
	defer clear(raw)
	if err != nil {
		switch {
		case errors.Is(err, errWorkerBodyEncoding):
			writeManagementError(w, managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported."))
		case errors.Is(err, errWorkerBodyLimit):
			writeManagementError(w, managementFailure(413, "workerStartTooLarge", "Worker start requests cannot exceed 8 KiB."))
		case errors.Is(err, errWorkerBodyInvalid):
			writeWorkerStartError(w, persistence.ErrWorkerExecutionInvalid)
		default:
			writeWorkerStartError(w, err)
		}
		return
	}
	var request api.StartRequest
	if api.StrictDecode(raw, &request) != nil || request.Mode != api.StartModeBegin && request.Mode != api.StartModeReconcile {
		writeWorkerStartError(w, persistence.ErrWorkerExecutionInvalid)
		return
	}
	command := persistence.WorkerStartRequest{ServerID: request.ServerID, SessionID: request.SessionID,
		ExecutionID: request.ExecutionID, ExecutionRevision: request.ExecutionRevision, LeaseID: request.LeaseID, Mode: string(request.Mode)}
	var response persistence.WorkerStartResponse
	err = h.admit(ctx, func() error {
		var err error
		response, err = h.store.CommitWorkerStart(ctx, authority, command)
		return err
	})
	if err != nil {
		writeWorkerStartError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, api.StartResponse{ServerID: response.ServerID, WorkerUID: response.WorkerUID,
		SessionID: response.SessionID, ExecutionID: response.ExecutionID, ExecutionRevision: response.ExecutionRevision,
		LeaseID: response.LeaseID, Disposition: api.StartDisposition(response.Disposition), GrantID: response.GrantID,
		ReceiptID: response.ReceiptID, Deadline: response.Deadline})
}

func writeWorkerStartError(w http.ResponseWriter, err error) {
	status, code, detail := 503, "unavailable", "Worker execution storage is unavailable."
	switch {
	case errors.Is(err, persistence.ErrCommitUnconfirmed):
		code, detail = "workerStartUnconfirmed", "Start admission is unconfirmed. Reconcile the original execution; do not repeat begin."
	case errors.Is(err, httpauth.ErrWorkerAuthentication), errors.Is(err, persistence.ErrWorkerUnauthorized):
		status, code, detail = 401, "unauthorized", "Supply a current worker bearer token."
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, httpauth.ErrWorkerForbidden), errors.Is(err, persistence.ErrWorkerAuthorityDenied):
		status, code, detail = 403, "forbidden", "Current worker authority and an approved HTTPS origin are required."
	case errors.Is(err, persistence.ErrWorkerExecutionInvalid):
		status, code, detail = 400, "invalidWorkerStart", "The worker start request is invalid."
	case errors.Is(err, persistence.ErrWorkerExecutionConflict), errors.Is(err, persistence.ErrWorkerSessionConflict), errors.Is(err, persistence.ErrWorkerSessionExpired), errors.Is(err, persistence.ErrWorkerExecutionExpired):
		status, code, detail = 409, "workerExecutionConflict", "The request conflicts with the original execution binding. Preserve its reconciliation record."
	case errors.Is(err, persistence.ErrWorkerSessionIdentity):
		status, code, detail = 409, "workerServerIdentity", "The request does not match the provisioned server identity."
	case errors.Is(err, persistence.ErrWorkerExecutionQuota):
		status, code, detail = 429, "workerExecutionQuota", "Worker execution capacity is exhausted."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, detail = "requestInterrupted", "Start was interrupted. Reconcile the original execution; do not repeat begin."
	}
	writeManagementError(w, managementFailure(status, code, detail))
}
