package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (m *managementHTTP) registerCollectionActivation(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v2/operations/{id}/activate", m.handleCollectionActivation)
}

func (m *managementHTTP) handleCollectionActivation(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	access, err := m.auth.Authorize(r, "ActivateOperation")
	if err == nil {
		err = collectionActivationPermissions(access)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "Activation accepts only its original operation path."))
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
	if err := collectionEmptyBody(w, r, "Activation"); err != nil {
		writeManagementError(w, err)
		return
	}
	admit := func(commit func() error) error {
		return m.auth.WithAdmission(r, "ActivateOperation", func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "The authenticated principal changed.")
			}
			if err := collectionActivationPermissions(current); err != nil {
				return err
			}
			return m.admit(ctx, commit)
		})
	}
	result, err := m.catalog.ActivateCollection(ctx, r.PathValue("id"), access.PrincipalID, m.now, admit)
	if result.ID != "" {
		w.Header().Set("X-Operation-ID", result.ID)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	// Capture only the original protected receipt outside policy admission.
	// Pending and retained results both carry a final metadata fence.
	observation, err := m.catalog.CollectionOperationObservation(ctx, result.ID, access.PrincipalID, m.now())
	if err != nil {
		writeManagementError(w, err)
		return
	}
	m.publishCollectionActivation(w, r, access.PrincipalID, observation)
}

func (m *managementHTTP) publishCollectionActivation(w http.ResponseWriter, r *http.Request, actor string, observation management.CollectionOperationObservation) {
	err := m.auth.WithAdmission(r, "ActivateOperation", func(current api.AccessInfo) error {
		if current.PrincipalID != actor {
			return managementFailure(403, "forbidden", "The authenticated principal changed.")
		}
		if err := collectionActivationPermissions(current); err != nil {
			return err
		}
		return observation.Recheck(r.Context(), m.now())
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	result := observation.Operation
	w.Header().Set("Location", "/api/v2/operations/"+result.ID)
	if result.State == "applying" {
		w.Header().Set("Retry-After", "5")
	}
	writeManagementJSON(w, http.StatusOK, result)
}

func collectionActivationPermissions(access api.AccessInfo) error {
	permissions := collectionPermissionSet(access)
	for _, kind := range management.ResourceKinds() {
		if !permissions["Get"+kind] || !permissions["Create"+kind] || !permissions["Replace"+kind] {
			return managementFailure(403, "forbidden", "Activation requires read and write permission for every supported resource kind.")
		}
	}
	return nil
}
