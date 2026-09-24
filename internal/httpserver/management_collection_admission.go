package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// handleCollectionAdmission keeps parsing, encryption and durable admission
// within bounded request/worker budgets. It never invokes an external provider.
func (m *managementHTTP) handleCollectionAdmission(w http.ResponseWriter, r *http.Request, operation string) {
	managementHeaders(w)
	access, err := m.auth.Authorize(r, operation)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "Collection writes do not accept query parameters."))
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
	raw, err := collectionPreflightBody(w, r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	defer clear(raw)
	var result any
	var operationID string
	// Preparation never retains a policy grant across encryption or KMS I/O.
	// Every actual Submit rechecks the same authenticated principal and kinds.
	keys := make(map[string]bool)
	admit := func(commit func() error) error {
		return m.auth.WithAdmission(r, operation, func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "The authenticated principal changed.")
			}
			permissions := collectionPermissionSet(current)
			for kind := range keys {
				if !collectionKindPermission(permissions, persistence.CatalogKey{Kind: kind}) {
					return managementFailure(403, "forbidden", "Upload requires current read and write permission for every resource kind.")
				}
			}
			return m.admit(ctx, commit)
		})
	}
	switch operation {
	case "PrepareCollection":
		var req api.CollectionPrepareRequest
		if api.StrictDecode(raw, &req) != nil || !validCollectionAdmissionProfile(raw) {
			err = management.ErrValidation
			break
		}
		result, err = m.catalog.PrepareCollection(ctx, req, access.PrincipalID, m.now, admit)
	case "CreateOperation":
		var req api.OperationCreateRequest
		if api.StrictDecode(raw, &req) != nil || !validCollectionAdmissionProfile(raw) {
			err = management.ErrValidation
			break
		}
		var value api.Operation
		value, err = m.catalog.CreateCollection(ctx, req, access.PrincipalID, m.now, admit)
		result, operationID = value, value.ID
	case "UploadOperation":
		permissions := collectionPermissionSet(access)
		canWrite := func(key persistence.CatalogKey) bool { return collectionKindPermission(permissions, key) }
		var items []management.CollectionUploadItem
		items, err = collectionUploadBody(raw, canWrite)
		if err != nil {
			break
		}
		defer func() {
			for _, item := range items {
				clear(item.Resource)
			}
		}()
		for _, item := range items {
			keys[item.Key.Kind] = true
		}
		var value api.Operation
		value, err = m.catalog.UploadCollection(ctx, r.PathValue("id"), access.PrincipalID, items, canWrite, m.now, admit)
		result, operationID = value, value.ID
	default:
		err = management.ErrUnavailable
	}

	current, authErr := m.auth.Authorize(r, operation)
	if authErr == nil && current.PrincipalID != access.PrincipalID {
		authErr = managementFailure(403, "forbidden", "The authenticated principal changed.")
	}
	if authErr != nil {
		writeManagementError(w, authErr)
		return
	}
	if operationID != "" {
		w.Header().Set("X-Operation-ID", operationID)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if err := ctx.Err(); err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}

// A missing profile preserves the legacy contract. Explicit null and empty
// strings must not be silently collapsed into absence by encoding/json.
func validCollectionAdmissionProfile(raw []byte) bool {
	var field struct {
		Profile json.RawMessage `json:"normalizationProfile"`
	}
	if json.Unmarshal(raw, &field) != nil {
		return false
	}
	defer clear(field.Profile)
	if field.Profile == nil {
		return true
	}
	var profile string
	return json.Unmarshal(field.Profile, &profile) == nil && profile == "cpra.file.base.v1"
}

func collectionPermissionSet(access api.AccessInfo) map[string]bool {
	permissions := make(map[string]bool, len(access.Permissions))
	for _, value := range access.Permissions {
		permissions[value] = true
	}
	return permissions
}

func collectionKindPermission(permissions map[string]bool, key persistence.CatalogKey) bool {
	return permissions["Get"+key.Kind] && permissions["Create"+key.Kind] && permissions["Replace"+key.Kind]
}

func collectionUploadBody(raw []byte, canWrite func(persistence.CatalogKey) bool) (items []management.CollectionUploadItem, err error) {
	var wire struct {
		Items []json.RawMessage `json:"items"`
	}
	defer func() {
		for _, item := range wire.Items {
			clear(item)
		}
	}()
	if api.StrictDecode(raw, &wire) != nil || len(wire.Items) == 0 || len(wire.Items) > 256 {
		return nil, management.ErrValidation
	}
	// Keep ownership even when a later item fails; returned nil must not hide
	// already decoded private buffers from cleanup.
	owned := make([][]byte, 0, len(wire.Items))
	defer func() {
		if err != nil {
			for _, body := range owned {
				clear(body)
			}
		}
	}()
	items = make([]management.CollectionUploadItem, 0, len(wire.Items))
	for _, rawItem := range wire.Items {
		var item collectionPreflightItem
		decodeErr := api.StrictDecode(rawItem, &item)
		owned = append(owned, item.Resource)
		if decodeErr != nil || item.Ordinal <= 0 || item.SourceDocument <= 0 || item.SourceItem <= 0 {
			return nil, management.ErrValidation
		}
		kind, id, found := strings.Cut(item.ID, "/")
		if !found || strings.Contains(id, "/") {
			return nil, management.ErrValidation
		}
		key := persistence.CatalogKey{Kind: kind, ID: id}
		// No resource decode, catalog lookup or key unwrap before kind permission.
		if !canWrite(key) {
			return nil, managementFailure(403, "forbidden", "Upload requires read and write access to each resource kind.")
		}
		items = append(items, management.CollectionUploadItem{Ordinal: uint64(item.Ordinal), Key: key, Source: item.Source,
			SourceDocument: uint64(item.SourceDocument), SourceItem: uint64(item.SourceItem), ContentDigest: item.ContentDigest, Resource: item.Resource})
	}
	return items, nil
}
