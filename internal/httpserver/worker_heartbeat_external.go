//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Separate limits keep heartbeats and supplemental evidence from consuming
// admission reserved for original outcome delivery.
type workerIngressLimit struct {
	mu         sync.Mutex
	active     int
	principals map[string]int
}

func (l *workerIngressLimit) reserve(uid string, maximum, perPrincipal int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= maximum || l.principals[uid] >= perPrincipal {
		return false
	}
	if l.principals == nil {
		l.principals = make(map[string]int)
	}
	l.active++
	l.principals[uid]++
	return true
}

func (l *workerIngressLimit) release(uid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active--
	l.principals[uid]--
	if l.principals[uid] == 0 {
		delete(l.principals, uid)
	}
}

func (h *workerSessionHTTP) heartbeat(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	authority, err := h.auth.AuthorizeWorker(r, "WorkerHeartbeat", h.store)
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerHeartbeat")
		return
	}
	if h.completionReady == nil || h.complete == nil {
		writeWorkerLifecycleError(w, persistence.ErrWorkerSessionUnavailable, "workerHeartbeat")
		return
	}
	if err := h.completionReady(ctx); err != nil {
		writeWorkerLifecycleError(w, err, "workerHeartbeat")
		return
	}
	if !h.heartbeats.reserve(authority.WorkerUID, 64, 16) {
		w.Header().Set("Retry-After", "1")
		writeWorkerLifecycleError(w, persistence.ErrWorkerExecutionQuota, "workerHeartbeat")
		return
	}
	defer h.heartbeats.release(authority.WorkerUID)
	raw, err := readWorkerProtocolBody(w, r, 8<<10)
	defer clear(raw)
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerHeartbeat")
		return
	}
	var input api.HeartbeatRequest
	if api.StrictDecode(raw, &input) != nil || input.WorkerID == "" {
		writeWorkerLifecycleError(w, persistence.ErrWorkerExecutionInvalid, "workerHeartbeat")
		return
	}
	if input.WorkerID != authority.WorkerID {
		writeWorkerLifecycleError(w, persistence.ErrWorkerAuthorityDenied, "workerHeartbeat")
		return
	}
	var response persistence.WorkerHeartbeatResponse
	err = h.complete(ctx, func() error {
		var err error
		response, err = h.store.CommitWorkerHeartbeat(ctx, authority, persistence.WorkerHeartbeatRequest{
			ServerID: input.ServerID, SessionID: input.SessionID, ExecutionID: input.ExecutionID, GrantID: input.GrantID})
		return err
	})
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerHeartbeat")
		return
	}
	writeManagementJSON(w, http.StatusOK, api.HeartbeatResponse{ServerID: response.ServerID, WorkerUID: response.WorkerUID,
		SessionID: response.SessionID, ExecutionID: response.ExecutionID, GrantID: response.GrantID, Accepted: response.Accepted})
}

func writeWorkerLifecycleError(w http.ResponseWriter, err error, operation string) {
	status, code, detail := 503, "unavailable", "Worker protocol storage is unavailable. Retain pending reports."
	switch {
	case errors.Is(err, persistence.ErrCommitUnconfirmed):
		code, detail = operation+"Unconfirmed", "Admission is unconfirmed. Retry only the identical request."
	case errors.Is(err, httpauth.ErrWorkerAuthentication), errors.Is(err, persistence.ErrWorkerUnauthorized):
		status, code, detail = 401, "unauthorized", "Supply a current worker bearer token."
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, httpauth.ErrWorkerForbidden), errors.Is(err, persistence.ErrWorkerAuthorityDenied):
		status, code, detail = 403, "forbidden", "Current worker authority and an approved HTTPS origin are required."
	case errors.Is(err, errWorkerBodyEncoding):
		status, code, detail = 415, "unsupportedEncoding", "Encoded request bodies are not supported."
	case errors.Is(err, errWorkerBodyLimit):
		status, code, detail = 413, operation+"TooLarge", "The request exceeds the worker protocol byte limit."
	case errors.Is(err, persistence.ErrWorkerExecutionInvalid), errors.Is(err, errWorkerBodyInvalid), errors.Is(err, management.ErrWorkerEvidenceInvalid), errors.Is(err, management.ErrWorkerOutcomeInvalid), errors.Is(err, management.ErrJobValueInvalid):
		status, code, detail = 400, operation+"Invalid", "The request does not match the original execution contract."
	case errors.Is(err, persistence.ErrWorkerExecutionConflict), errors.Is(err, persistence.ErrWorkerSessionExpired):
		status, code, detail = 409, operation+"Conflict", "The request conflicts with the original execution or retained evidence."
	case errors.Is(err, persistence.ErrWorkerSessionIdentity):
		status, code, detail = 409, "workerServerIdentity", "The request does not match the provisioned server identity."
	case errors.Is(err, persistence.ErrWorkerExecutionQuota):
		status, code, detail = 429, operation+"Quota", "Worker protocol capacity is exhausted. Retain pending evidence."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, detail = "requestInterrupted", "Admission was interrupted. Retain pending reports."
	}
	writeManagementError(w, managementFailure(status, code, detail))
}
