//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func writeJobTypeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, persistence.ErrJobTypeConflict):
		err = persistence.ErrCatalogConflict
	case errors.Is(err, persistence.ErrJobTypeVersionConflict):
		err = managementFailure(409, "immutableVersion", "The implementation contract changed. Use a new version name while preserving its category.")
	case errors.Is(err, persistence.ErrJobTypeQuota):
		err = managementFailure(429, "jobTypeQuota", "Retained JobType capacity is exhausted. Existing versions have been preserved.")
	case errors.Is(err, persistence.ErrJobTypeUnavailable):
		err = management.ErrUnavailable
	case errors.Is(err, persistence.ErrJobTypeInvalid), errors.Is(err, management.ErrJobSchemaInvalid), errors.Is(err, management.ErrJobSchemaBudget), errors.Is(err, management.ErrJobValueInvalid):
		err = managementFailure(422, "invalidJobType", "The JobType or its schema is invalid or exceeds the supported limits.")
	case errors.Is(err, persistence.ErrOperatorAuthorityDenied), errors.Is(err, persistence.ErrAuthenticationConflict):
		err = managementFailure(403, "forbidden", "Current committed operator authority is required.")
	}
	writeManagementError(w, err)
}

func jobTypeHeaders(w http.ResponseWriter, r api.JobType) {
	w.Header().Set("ETag", `"`+r.Metadata.ResourceVersion+`"`)
	w.Header().Set("X-Resource-Version", r.Metadata.ResourceVersion)
}

func (m *managementHTTP) handleJobType(w http.ResponseWriter, r *http.Request, operation string) {
	managementHeaders(w)
	access, err := m.auth.Authorize(r, operation)
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if r.PathValue("id") == "" {
			m.handleJobTypeList(w, r)
			return
		}
		var resource api.JobType
		err = m.withObservationRead(r, operation, func(ctx context.Context) error {
			var err error
			resource, err = m.catalog.GetJobType(ctx, r.PathValue("id"))
			return err
		})
		if err != nil {
			writeJobTypeError(w, err)
			return
		}
		jobTypeHeaders(w, resource)
		writeManagementJSON(w, http.StatusOK, resource)
		return
	}
	if !m.ready() {
		writeJobTypeError(w, mutationUnavailable())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	select {
	case m.mutations <- struct{}{}:
		defer func() { <-m.mutations }()
	default:
		writeJobTypeError(w, managementFailure(429, "admissionBusy", "Management validation capacity is busy."))
		return
	}
	version, err := managementPrecondition(r)
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	var prepared *management.PreparedJobType
	if r.Method == http.MethodDelete {
		prepared, err = m.catalog.PrepareDeleteJobType(ctx, r.PathValue("id"), version, access.PrincipalID)
	} else {
		raw, readErr := managementBody(w, r)
		if readErr != nil {
			writeJobTypeError(w, readErr)
			return
		}
		defer clear(raw)
		var resource api.JobType
		if api.StrictDecode(raw, &resource) != nil {
			writeJobTypeError(w, managementFailure(400, "invalidResource", "The body must contain one valid resource without duplicate or unknown fields."))
			return
		}
		if resource.Kind != "JobType" || r.Method == http.MethodPut && resource.Metadata.ID != r.PathValue("id") {
			writeJobTypeError(w, managementFailure(400, "identityMismatch", "The resource kind and identity must match the request route."))
			return
		}
		prepared, err = m.catalog.PrepareJobType(ctx, resource, version, r.Method == http.MethodPost, access.PrincipalID)
	}
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	var result management.JobTypeMutationResult
	err = m.admit(ctx, func() error {
		return m.auth.WithAdmission(r, operation, func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "Authorization changed during preparation.")
			}
			var err error
			result, err = m.catalog.CommitJobTypeMutation(ctx, prepared)
			if id := prepared.OperationID(); id != "" {
				w.Header().Set("X-Operation-ID", id)
			}
			return err
		})
	})
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	jobTypeHeaders(w, result.Resource)
	w.Header().Set("X-CPRa-Admission", "committed")
	w.Header().Set("X-Commit-Index", strconv.FormatUint(result.CommittedIndex, 10))
	if r.Method == http.MethodDelete {
		writeManagementJSON(w, http.StatusOK, result.Operation)
		return
	}
	writeManagementJSON(w, http.StatusOK, result.Resource)
}

func (m *managementHTTP) handleJobTypeList(w http.ResponseWriter, r *http.Request) {
	query, err := managementListQuery(r)
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	var result api.JobTypeList
	err = m.withSnapshotRead(r, "ListJobTypes", "JobType", query, func(ctx context.Context, read *snapshotRead) error {
		if read.fresh {
			view, err := m.catalog.JobTypeSnapshot(ctx)
			if err != nil {
				return err
			}
			if err = read.entry.jobTypeReservation.shrink(view.EstimatedBytes()); err != nil {
				return err
			}
			read.entry.jobTypes = view
		}
		items, after, err := read.entry.jobTypes.Page(ctx, "JobType", read.cursor.After, read.entry.limit)
		if err != nil {
			return err
		}
		read.after = after
		result.Items = items
		if after != "" {
			result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: after})
		}
		return nil
	})
	if err != nil {
		writeJobTypeError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}
