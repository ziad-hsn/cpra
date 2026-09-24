package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const collectionValidationPageBytes = 4 << 20

func (m *managementHTTP) registerCollectionValidation(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v2/operations/{id}/validate", m.handleCollectionValidation)
	mux.HandleFunc("GET /api/v2/operations/{id}/validation", m.handleCollectionValidationResult)
}

// Validation admits an immutable request; compilation belongs to the process
// coordinator, never to a request goroutine or a retained HTTP credential.
func (m *managementHTTP) handleCollectionValidation(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	access, err := m.auth.Authorize(r, "ValidateOperation")
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "Validation accepts only its original operation path."))
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
	if err := collectionEmptyBody(w, r, "Validation"); err != nil {
		writeManagementError(w, err)
		return
	}
	admit := func(commit func() error) error {
		return m.auth.WithAdmission(r, "ValidateOperation", func(current api.AccessInfo) error {
			if current.PrincipalID != access.PrincipalID {
				return managementFailure(403, "forbidden", "The authenticated principal changed.")
			}
			if err := collectionValidationPermissions(current, true); err != nil {
				return err
			}
			return m.admit(ctx, commit)
		})
	}
	result, err := m.catalog.RequestCollectionValidation(ctx, r.PathValue("id"), access.PrincipalID, m.now, admit)
	if result.ID != "" {
		w.Header().Set("X-Operation-ID", result.ID)
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	current, err := m.auth.Authorize(r, "ValidateOperation")
	if err == nil && current.PrincipalID != access.PrincipalID {
		err = managementFailure(403, "forbidden", "The authenticated principal changed.")
	}
	if err == nil {
		err = collectionValidationPermissions(current, true)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		writeManagementError(w, err)
		return
	}
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Location", "/api/v2/operations/"+result.ID+"/validation")
	writeManagementJSON(w, http.StatusAccepted, result)
}

// The initial committed policy grants complete reader/operator kind sets.
// Require that set before examining collection existence, because a retained
// result may describe both submitted and consulted resources. This conservative
// boundary can be narrowed only alongside a scoped durable authorization model.
func collectionValidationPermissions(access api.AccessInfo, write bool) error {
	permissions := collectionPermissionSet(access)
	for _, kind := range management.ResourceKinds() {
		if !permissions["Get"+kind] || write && (!permissions["Create"+kind] || !permissions["Replace"+kind]) {
			return managementFailure(403, "forbidden", "Collection validation requires access to every supported resource kind.")
		}
	}
	return nil
}

// collectionValidationReadView is immutable metadata plus a protected bounded
// read. It owns neither a cursor lock nor an authorization grant.
type collectionValidationReadView interface {
	Identity() persistence.CollectionValidationResultStatus
	Page(context.Context, uint64, int, time.Time) (persistence.CollectionValidationHistoryPage, error)
}

func (m *managementHTTP) handleCollectionValidationResult(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var owner string
	var authorization uint64
	err := m.auth.WithPolicyAdmission(r, "GetOperationValidation", func(access api.AccessInfo, generation uint64) error {
		if err := collectionValidationPermissions(access, false); err != nil {
			return err
		}
		owner, authorization = access.PrincipalID, generation
		return ctx.Err()
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := collectionValidationQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	// Bound in-flight detached pages separately from the retained cursor quota.
	// Neither policy nor cursor locks are held during a history read.
	select {
	case m.validationReads <- struct{}{}:
		defer func() { <-m.validationReads }()
	default:
		w.Header().Set("Retry-After", "1")
		writeManagementError(w, managementFailure(429, "readBusy", "Validation-result read capacity is busy."))
		return
	}
	read, err := m.collectionValidationPage(ctx, r.PathValue("id"), query, owner, authorization)
	// Recheck even a pending/error disposition before disclosing its existence.
	permissionErr := m.auth.WithPolicyAdmission(r, "GetOperationValidation", func(access api.AccessInfo, generation uint64) error {
		if access.PrincipalID != owner || generation != authorization {
			return managementFailure(410, "cursorExpired", "Authorization changed. Start a new validation-result query.")
		}
		if err := collectionValidationPermissions(access, false); err != nil {
			return err
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return nil
		}
		return m.publishCollectionValidationPage(ctx, read)
	})
	if permissionErr != nil {
		err = permissionErr
	}
	if err != nil {
		if failure, ok := err.(*managementHTTPError); ok && failure.code == "validationPending" {
			w.Header().Set("X-Operation-ID", r.PathValue("id"))
			w.Header().Set("Retry-After", "5")
		}
		writeManagementError(w, err)
		return
	}
	w.Header().Set("X-Operation-ID", read.result.OperationID)
	writeManagementJSON(w, http.StatusOK, read.result)
}

func collectionValidationQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, managementFailure(400, "invalidQuery", "The query is too large.")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managementFailure(400, "invalidQuery", "The query is invalid.")
	}
	for key, values := range query {
		if len(values) != 1 || key != "cursor" && key != "limit" {
			return nil, managementFailure(400, "invalidQuery", "Use one cursor and one limit parameter only.")
		}
		if values[0] == "" {
			return nil, managementFailure(400, "invalidQuery", "Empty query parameters are not accepted.")
		}
	}
	return query, nil
}

func validationResultStatusError(status persistence.CollectionValidationResultStatus) error {
	switch status.State {
	case "pending":
		return managementFailure(409, "validationPending", "Original validation is pending. Read this result again after the stated interval.")
	case "notRequested":
		return managementFailure(409, "validationNotRequested", "Validation has not been requested for this operation.")
	case "noVerdict":
		switch status.TerminalPhase {
		case "interrupted":
			return managementFailure(409, "validationInterrupted", "The original validation attempt was interrupted. Preserve this operation's evidence; new intent requires a new operation.")
		case "canceled":
			return managementFailure(409, "validationCanceled", "This operation was canceled before a validation verdict was finalized.")
		case "expired", "invalidated":
			return persistence.ErrOperationExpired
		}
	}
	return management.ErrUnavailable
}

type collectionValidationRead struct {
	result    api.ValidationResultPage
	entry     managementSnapshot
	cursor    managementCursor
	continued bool
	observed  time.Time
}

func (m *managementHTTP) collectionValidationPage(ctx context.Context, id string, query url.Values, principal string, generation uint64) (collectionValidationRead, error) {
	var read collectionValidationRead
	limit := 0
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			return read, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500.")
		}
		limit = parsed
	}
	if err := m.lockSnapshots(ctx); err != nil {
		return read, err
	}
	var after uint64
	err := func() error {
		defer m.mu.Unlock()
		if !m.cursorReady {
			return management.ErrUnavailable
		}
		now := m.now().UTC()
		read.observed = now
		m.pruneSnapshots(now, principal, generation)
		if token := query.Get("cursor"); token != "" {
			cursor, err := m.decodeCursor(token)
			if err != nil {
				return err
			}
			entry, ok := m.snapshots[cursor.View]
			if !ok || entry.principal != principal || entry.generation != generation || entry.kind != "CollectionValidation" || entry.validation == nil {
				return managementFailure(410, "cursorExpired", "The validation cursor expired or belongs to another access context.")
			}
			if entry.validationOperation != id || limit != 0 && limit != entry.limit {
				return managementFailure(400, "cursorMismatch", "Continue with the original operation and page limit.")
			}
			if now.Before(entry.created) {
				return management.ErrUnavailable
			}
			after, err = strconv.ParseUint(cursor.After, 10, 64)
			if err != nil || after == 0 || strconv.FormatUint(after, 10) != cursor.After {
				return managementFailure(400, "invalidCursor", "The validation cursor is invalid.")
			}
			read.entry, read.cursor, read.continued = entry, cursor, true
			return nil
		}
		return m.snapshotCountQuota(principal)
	}()
	if err != nil {
		return read, err
	}
	if !read.continued {
		if limit == 0 {
			limit = 100
		}
		view, status, err := m.catalog.CollectionValidationResultView(ctx, id, principal, read.observed)
		if err != nil {
			return read, err
		}
		if status.State != "ready" || view == nil {
			return read, validationResultStatusError(status)
		}
		read.cursor.View = uuid.NewString()
		read.entry = managementSnapshot{validation: view, validationOperation: id, principal: principal, generation: generation,
			kind: "CollectionValidation", limit: limit, created: read.observed, expires: read.observed.Add(managementCursorTTL)}
	}
	page, err := read.entry.validation.Page(ctx, after, read.entry.limit, read.observed)
	if err != nil {
		return read, err
	}
	identity := read.entry.validation.Identity()
	header, descriptor := page.Receipt.Header, page.Receipt.Descriptor
	result := api.ValidationResultPage{OperationID: identity.OperationID, IdentityFormat: identity.IdentityFormat,
		ContentDigest: identity.ContentDigest, ItemCount: int64(identity.ItemCount),
		Summary: api.ValidationResultSummary{ResultID: header.ResultID, Valid: header.Valid, Issue: header.Issue,
			SummaryOnly: header.SummaryOnly, Count: int64(descriptor.Count), Digest: descriptor.Digest,
			PlanID: header.PlanID, PlanDigest: header.PlanDigest, CapabilitiesDigest: header.CapabilitiesDigest,
			FinalizedAt: page.Receipt.FinalizedAt, ExpiresAt: page.Receipt.FinalizedAt.AddDate(0, 0, 30)},
		Items: make([]api.ValidationResultItem, 0, len(page.Items))}
	for _, item := range page.Items {
		result.Items = append(result.Items, api.ValidationResultItem{Ordinal: int64(item.Ordinal), Kind: item.Key.Kind, ID: item.Key.ID,
			Source: item.Source, SourceDocument: int64(item.Document), SourceItem: int64(item.Item), Change: item.Change, Issue: item.Issue,
			UID: item.UID, ResourceVersion: item.ResourceVersion})
	}
	if result.Summary.ExpiresAt.Before(read.entry.expires) {
		read.entry.expires = result.Summary.ExpiresAt
	}
	if page.NextAfter != 0 {
		result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: strconv.FormatUint(page.NextAfter, 10)})
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded)+1 > collectionValidationPageBytes {
		return read, managementFailure(503, "pageUnavailable", "The original validation page exceeds its response bound. Reduce the page limit.")
	}
	read.result = result
	return read, ctx.Err()
}

// Called inside a short current-policy admission callback after all disk work.
// A stale read cannot publish a cursor under a new generation or expired clock.
func (m *managementHTTP) publishCollectionValidationPage(ctx context.Context, read collectionValidationRead) error {
	if err := m.lockSnapshots(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	now := m.now().UTC()
	if now.Before(read.observed) || now.Before(read.entry.created) {
		return management.ErrUnavailable
	}
	if !now.Before(read.result.Summary.ExpiresAt) {
		return persistence.ErrOperationExpired
	}
	if !now.Before(read.entry.expires) {
		return managementFailure(410, "cursorExpired", "The validation cursor expired. Start a new validation-result query.")
	}
	m.pruneSnapshots(now, read.entry.principal, read.entry.generation)
	if read.continued {
		current, ok := m.snapshots[read.cursor.View]
		if !ok || current.principal != read.entry.principal || current.generation != read.entry.generation || current.validationOperation != read.entry.validationOperation {
			return managementFailure(410, "cursorExpired", "The validation cursor expired. Start a new validation-result query.")
		}
	} else if read.result.NextCursor != "" {
		if err := m.snapshotCountQuota(read.entry.principal); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if read.result.NextCursor != "" {
		m.snapshots[read.cursor.View] = read.entry
	}
	return nil
}
