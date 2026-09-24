package cprafixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// The fixture completes children synchronously in memory. These records model
// the public result shape, not production scheduling, retention or durability.
func (h *Handler) activateCollection(o *staged) {
	keys := make([]string, 0, len(o.items))
	for key := range o.items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := order(o.items[keys[i]].Resource.Kind), order(o.items[keys[j]].Resource.Kind)
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	at := time.Now().UTC()
	summary := api.ExecutionResultSummary{ResultID: o.operation.ID + ".activation", UploadID: o.operation.ID + ".upload", PlanID: o.validation.Summary.PlanID, PlanDigest: o.validation.Summary.PlanDigest,
		Outcome: "completed", ItemCount: o.count, Processed: o.count, FinalizedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
	for ordinal, id := range keys {
		item := o.items[id]
		key := item.Resource.Kind + "/" + item.Resource.Metadata.ID
		original := h.resources[key]
		result := api.ApplyResult{ID: key, Kind: item.Resource.Kind, InputOrdinal: api.Pointer(item.Ordinal), PlanOrdinal: api.Pointer(int64(ordinal + 1)), Source: item.Source,
			SourceDocument: api.Pointer(item.SourceDocument), SourceItem: api.Pointer(item.SourceItem), OldVersion: o.versions[key], OriginalUID: original.Metadata.UID,
			DecidedAt: &at, Committed: api.Pointer(false)}
		if original.Metadata.ResourceVersion != o.versions[key] {
			result.Outcome, result.CatalogDecision = "conflict", "conflict"
			summary.Conflicts++
		} else {
			resource := h.put(key, item.Resource)
			result.Outcome, result.CatalogDecision = "accepted", "accepted"
			result.Applied, result.Committed = api.Pointer(true), api.Pointer(true)
			result.NewVersion, result.UID = resource.Metadata.ResourceVersion, resource.Metadata.UID
			result.Generation = api.Pointer(resource.Metadata.Generation)
			result.ChildDisposition = &api.ExecutionChildDisposition{OperationID: fmt.Sprintf("%s.child.%d", o.operation.ID, ordinal+1), State: "completed", Outcome: "applied", UpdatedAt: &at}
			summary.Accepted++
			summary.ChildApplied++
			(*o.operation.Applied)++
			(*o.operation.Committed)++
		}
		result.CommittedIndex = api.Pointer(int64(max(1, h.version)))
		o.operation.Items = append(o.operation.Items, result)
	}
	sort.Slice(o.operation.Items, func(i, j int) bool { return *o.operation.Items[i].InputOrdinal < *o.operation.Items[j].InputOrdinal })
	if summary.Conflicts != 0 {
		summary.Outcome = "partial"
		if summary.Accepted == 0 {
			summary.Outcome = "failed"
		}
	}
	raw, _ := json.Marshal(o.operation.Items)
	digest := sha256.Sum256(raw)
	summary.Digest, summary.Bytes = hex.EncodeToString(digest[:]), int64(len(raw))
	o.operation.State = summary.Outcome
	o.operation.ExecutionResult = &api.ExecutionResultAvailability{State: "ready", Summary: &summary}
}

func fixtureActivationReceipt(operation api.Operation) api.Operation {
	operation.Items, operation.NextCursor = nil, ""
	return operation
}

func writeFixtureExecutionPage(w http.ResponseWriter, r *http.Request, operation api.Operation) {
	if operation.ExecutionResult == nil {
		write(w, 200, operation)
		return
	}
	after, limit := 0, 100
	var err error
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		after, err = strconv.Atoi(cursor)
		if err != nil || after < 0 || after >= len(operation.Items) {
			problem(w, 400, "invalid fixture result cursor")
			return
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			problem(w, 400, "invalid fixture result limit")
			return
		}
	}
	end := min(after+limit, len(operation.Items))
	operation.Items = operation.Items[after:end]
	if end < int(*operation.ItemCount) {
		operation.NextCursor = strconv.Itoa(end)
	}
	write(w, 200, operation)
}
