//go:build externaljobs

package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const maxWorkerPollBodyBytes = 64 << 10
const maxConcurrentWorkerPolls = 32

type workerSessionStore interface {
	httpauth.WorkerPolicyReader
	CommitWorkerPoll(context.Context, persistence.WorkerAuthority, persistence.WorkerPollRequest) (persistence.WorkerPollResponse, error)
	CommitWorkerStart(context.Context, persistence.WorkerAuthority, persistence.WorkerStartRequest) (persistence.WorkerStartResponse, error)
}

// workerSessionHTTP adapts committed worker protocol operations. Production route
// registration remains closed until result handling and dispatch are qualified.
type workerSessionHTTP struct {
	auth     *httpauth.Authorizer
	store    workerSessionStore
	ready    func(context.Context) error
	admit    func(context.Context, func() error) error
	mu       sync.Mutex
	polls    map[string]struct{}
	catalog  *management.Catalog
	starts   map[string]int
	starting int
}

func (h *workerSessionHTTP) reserve(uid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.polls[uid]; exists || len(h.polls) >= maxConcurrentWorkerPolls {
		return false
	}
	if h.polls == nil {
		h.polls = make(map[string]struct{})
	}
	h.polls[uid] = struct{}{}
	return true
}

func (h *workerSessionHTTP) release(uid string) {
	h.mu.Lock()
	delete(h.polls, uid)
	h.mu.Unlock()
}

func (h *workerSessionHTTP) poll(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	authority, err := h.auth.AuthorizeWorker(r, "WorkerPoll", h.store)
	if err != nil {
		writeWorkerSessionError(w, err)
		return
	}
	if h.ready == nil || h.admit == nil {
		writeWorkerSessionError(w, persistence.ErrWorkerSessionUnavailable)
		return
	}
	if err := h.ready(ctx); err != nil {
		writeWorkerSessionError(w, err)
		return
	}
	if !h.reserve(authority.WorkerUID) {
		w.Header().Set("Retry-After", "1")
		writeWorkerSessionError(w, persistence.ErrWorkerSessionQuota)
		return
	}
	defer h.release(authority.WorkerUID)
	raw, err := readWorkerProtocolBody(w, r, maxWorkerPollBodyBytes)
	defer clear(raw)
	if err != nil {
		switch {
		case errors.Is(err, errWorkerBodyEncoding):
			writeManagementError(w, managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported."))
		case errors.Is(err, errWorkerBodyLimit):
			writeManagementError(w, managementFailure(413, "workerPollTooLarge", "Worker polling requests cannot exceed 64 KiB."))
		case errors.Is(err, errWorkerBodyInvalid):
			writeWorkerSessionError(w, persistence.ErrWorkerSessionInvalid)
		default:
			writeWorkerSessionError(w, err)
		}
		return
	}
	var request api.PollRequest
	if api.StrictDecode(raw, &request) != nil || request.PollSequence < 1 || request.WorkerID != authority.WorkerID ||
		len(request.Capabilities) < 1 || len(request.Capabilities) > persistence.MaxWorkerSessionCapabilities ||
		request.Capacity < 1 || request.Capacity > 100 || request.Limit < 1 || request.Limit > request.Capacity ||
		request.WaitSeconds < 0 || request.WaitSeconds > 25 {
		writeWorkerSessionError(w, persistence.ErrWorkerSessionInvalid)
		return
	}
	var required struct {
		SessionID   *string `json:"sessionID"`
		WaitSeconds *int64  `json:"waitSeconds"`
	}
	if json.Unmarshal(raw, &required) != nil || required.SessionID == nil || required.WaitSeconds == nil {
		writeWorkerSessionError(w, persistence.ErrWorkerSessionInvalid)
		return
	}
	capabilities := make([]persistence.WorkerSessionCapability, len(request.Capabilities))
	for i, capability := range request.Capabilities {
		capabilities[i] = persistence.WorkerSessionCapability{JobTypeID: capability.JobTypeID, Version: capability.Version, Category: string(capability.Kind)}
	}
	command := persistence.WorkerPollRequest{
		ServerID: request.ServerID, WorkerID: request.WorkerID, ClientSessionID: request.ClientSessionID,
		SessionID: request.SessionID, PollSequence: uint64(request.PollSequence), Capabilities: capabilities,
		Capacity: int(request.Capacity), Limit: int(request.Limit), WaitSeconds: int(request.WaitSeconds),
	}
	var response persistence.WorkerPollResponse
	err = h.admit(ctx, func() error {
		var err error
		response, err = h.store.CommitWorkerPoll(ctx, authority, command)
		return err
	})
	if err != nil {
		writeWorkerSessionError(w, err)
		return
	}
	assignments := api.Assignments{
		ServerID: response.ServerID, WorkerUID: response.WorkerUID, ClientSessionID: response.ClientSessionID,
		SessionID: response.SessionID, PollSequence: int64(response.PollSequence), SessionExpiresAt: response.SessionExpiresAt,
		Items: []api.Assignment{},
	}
	if h.catalog != nil {
		assignments, err = h.catalog.WorkerAssignments(ctx, authority, response)
	} else if len(response.Offers) != 0 {
		err = persistence.ErrWorkerSessionUnavailable
	}
	if err != nil {
		writeWorkerSessionError(w, err)
		return
	}
	defer func() {
		for i := range assignments.Items {
			clear(assignments.Items[i].Parameters)
		}
	}()
	writeManagementJSON(w, http.StatusOK, assignments)
}

func writeWorkerSessionError(w http.ResponseWriter, err error) {
	status, code, detail := 503, "unavailable", "Worker session storage is unavailable."
	switch {
	case errors.Is(err, httpauth.ErrWorkerAuthentication), errors.Is(err, persistence.ErrWorkerUnauthorized):
		status, code, detail = 401, "unauthorized", "Supply a current worker bearer token."
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, httpauth.ErrWorkerForbidden), errors.Is(err, persistence.ErrWorkerAuthorityDenied):
		status, code, detail = 403, "forbidden", "Current worker authority and an approved HTTPS origin are required."
	case errors.Is(err, persistence.ErrWorkerSessionInvalid):
		status, code, detail = 400, "invalidWorkerPoll", "The worker polling request is invalid."
	case errors.Is(err, persistence.ErrWorkerSessionExpired):
		status, code, detail = 409, "workerSessionExpired", "The polling session expired. Preserve original execution records when opening a new session."
	case errors.Is(err, persistence.ErrWorkerSessionIdentity):
		status, code, detail = 409, "workerServerIdentity", "The request does not match the provisioned server identity."
	case errors.Is(err, persistence.ErrWorkerSessionSequence):
		status, code, detail = 409, "workerPollSequence", "The polling sequence or frozen request does not match the session."
	case errors.Is(err, persistence.ErrWorkerSessionConflict):
		status, code, detail = 409, "workerSessionConflict", "A conflicting worker session is active."
	case errors.Is(err, persistence.ErrWorkerSessionQuota):
		status, code, detail = 429, "workerSessionQuota", "Worker session capacity is exhausted."
	case errors.Is(err, persistence.ErrCommitUnconfirmed):
		code, detail = "workerPollUnconfirmed", "Polling admission is unconfirmed. Retain the original session and frozen request."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, detail = "requestInterrupted", "Polling was interrupted. Retain the original session and frozen request."
	}
	writeManagementError(w, managementFailure(status, code, detail))
}
