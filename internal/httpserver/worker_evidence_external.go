//go:build externaljobs

package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (h *workerSessionHTTP) evidence(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	authority, err := h.auth.AuthorizeWorker(r, "WorkerLateEvidence", h.store)
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerEvidence")
		return
	}
	if h.completionReady == nil || h.complete == nil || h.catalog == nil {
		writeWorkerLifecycleError(w, persistence.ErrWorkerSessionUnavailable, "workerEvidence")
		return
	}
	if err := h.completionReady(ctx); err != nil {
		writeWorkerLifecycleError(w, err, "workerEvidence")
		return
	}
	if !h.evidenceRequests.reserve(authority.WorkerUID, 16, 4) {
		w.Header().Set("Retry-After", "1")
		writeWorkerLifecycleError(w, persistence.ErrWorkerExecutionQuota, "workerEvidence")
		return
	}
	defer h.evidenceRequests.release(authority.WorkerUID)
	raw, err := readWorkerProtocolBody(w, r, 128<<10)
	defer clear(raw)
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerEvidence")
		return
	}
	var input api.LateEvidenceRequest
	var required struct {
		Diagnostic *string         `json:"diagnostic"`
		Data       json.RawMessage `json:"data"`
	}
	if api.StrictDecode(raw, &input) != nil || json.Unmarshal(raw, &required) != nil || required.Diagnostic == nil || len(required.Data) == 0 || input.Evidence == nil {
		writeWorkerLifecycleError(w, management.ErrWorkerEvidenceInvalid, "workerEvidence")
		return
	}
	defer clear(input.Data)
	defer clear(required.Data)
	var receipt persistence.WorkerReceipt
	err = h.complete(ctx, func() error {
		var err error
		receipt, err = h.catalog.CommitWorkerEvidence(ctx, authority, input)
		return err
	})
	if err != nil {
		writeWorkerLifecycleError(w, err, "workerEvidence")
		return
	}
	writeManagementJSON(w, http.StatusOK, api.Receipt{ServerID: receipt.ServerID, WorkerUID: receipt.WorkerUID,
		ExecutionID: receipt.ExecutionID, ReceiptID: receipt.ReceiptID, Disposition: api.ReceiptDisposition(receipt.Disposition)})
}
