package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const (
	managementCursorTTL          = 5 * time.Minute
	managementMaxSnapshots       = 64
	managementPrincipalSnapshots = 16
	managementMaxPageBytes       = 8 << 20
	managementMaxMutations       = 8
)

type managementResourceRoute struct{ kind, plural, path string }

var managementResourceRoutes = []managementResourceRoute{
	{"Monitor", "Monitors", "/api/v2/monitors"},
	{"NotificationEndpoint", "NotificationEndpoints", "/api/v2/notification-endpoints"},
	{"Recipient", "Recipients", "/api/v2/recipients"},
	{"NotificationGroup", "NotificationGroups", "/api/v2/notification-groups"},
	{"Credential", "Credentials", "/api/v2/credentials"},
}

type managementSnapshot struct {
	snapshotExtensions
	execution             collectionExecutionReadView
	executionOperation    api.Operation
	validation            collectionValidationReadView
	validationOperation   string
	operations            managementOperationReadView
	operationBytes        int64
	observations          []json.RawMessage
	observationsAvailable bool
	view                  snapshotPageView[api.Resource]
	incidents             snapshotPageView[api.Incident]
	history               snapshotPageView[api.Event]
	actions               snapshotPageView[api.Action]
	monitorID             string
	principal             string
	generation            uint64
	kind                  string
	limit                 int
	created, expires      time.Time
}
type managementHTTP struct {
	managementExtensions
	ready                 func() bool
	admit                 func(context.Context, func() error) error
	mutations             chan struct{}
	validationReads       chan struct{}
	operationReads        chan struct{}
	snapshotReads         chan struct{}
	operationReservations map[string]int64
	catalog               *management.Catalog
	reselection           *management.CollectionReselectionManager
	auth                  *httpauth.Authorizer
	mu                    sync.Mutex
	snapshots             map[string]managementSnapshot
	cursorKey             [32]byte
	cursorReady           bool
	now                   func() time.Time
}

type managementHTTPError struct {
	status       int
	code, detail string
}

func (e *managementHTTPError) Error() string { return e.code }
func managementFailure(status int, code, detail string) error {
	return &managementHTTPError{status, code, detail}
}

func (s *Server) registerManagement(mux *http.ServeMux) {
	if s.cfg.Management == nil || s.cfg.ManagementAuth == nil {
		return
	}
	s.managementOnce.Do(func() {
		m := &managementHTTP{ready: s.mutationReady, admit: s.withMutationAdmission, mutations: make(chan struct{}, managementMaxMutations), validationReads: make(chan struct{}, 8), operationReads: make(chan struct{}, 8), snapshotReads: make(chan struct{}, 8), operationReservations: make(map[string]int64), catalog: s.cfg.Management, auth: s.cfg.ManagementAuth, snapshots: make(map[string]managementSnapshot), now: time.Now}
		_, err := rand.Read(m.cursorKey[:])
		m.cursorReady = err == nil
		m.reselection = s.cfg.Reselection
		s.managementHTTP = m
	})
	m := s.managementHTTP
	s.registerManagementExtensions(mux, m)
	m.registerControls(mux)
	m.registerHistory(mux)
	m.registerActions(mux)
	s.registerManagementObservations(mux, m)
	for _, route := range managementResourceRoutes {
		for _, entry := range []struct{ method, operation, path string }{
			{http.MethodGet, "List" + route.plural, route.path},
			{http.MethodPost, "Create" + route.kind, route.path},
			{http.MethodGet, "Get" + route.kind, route.path + "/{id}"},
			{http.MethodPut, "Replace" + route.kind, route.path + "/{id}"},
			{http.MethodPatch, "Patch" + route.kind, route.path + "/{id}"},
			{http.MethodDelete, "Delete" + route.kind, route.path + "/{id}"},
		} {
			mux.HandleFunc(entry.method+" "+entry.path, func(w http.ResponseWriter, r *http.Request) { m.handleResource(w, r, route, entry.operation) })
		}
	}
	mux.HandleFunc("POST /api/v2/collections/preflight", m.handleCollectionPreflight)
	mux.HandleFunc("POST /api/v2/collections/prepare", func(w http.ResponseWriter, r *http.Request) { m.handleCollectionAdmission(w, r, "PrepareCollection") })
	mux.HandleFunc("POST /api/v2/operations", func(w http.ResponseWriter, r *http.Request) { m.handleCollectionAdmission(w, r, "CreateOperation") })
	mux.HandleFunc("PUT /api/v2/operations/{id}/items", func(w http.ResponseWriter, r *http.Request) { m.handleCollectionAdmission(w, r, "UploadOperation") })
	m.registerCollectionCancellation(mux)
	m.registerCollectionValidation(mux)
	m.registerCollectionActivation(mux)
	m.registerCollectionReselection(mux)
	mux.HandleFunc("GET /api/v2/operations", m.handleOperations)
	mux.HandleFunc("GET /api/v2/operations/{id}", m.handleOperation)
	mux.HandleFunc("GET /api/v2/self", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		access, err := m.auth.Authorize(r, "GetAccess")
		if err != nil {
			writeManagementError(w, err)
			return
		}
		// Only actually registered operations are advertised as actionable access.
		access.Permissions = m.managementPermissions(access.Permissions)
		writeManagementJSON(w, http.StatusOK, access)
	})
	mux.HandleFunc("GET /api/v2/discovery", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if _, err := m.auth.Authorize(r, "GetCapabilities"); err != nil {
			writeManagementError(w, err)
			return
		}
		result := api.Capabilities{APIVersions: []string{"v1", "v2"}, Drivers: map[string][]string{"check": {}, "recovery": {}, "notification": {}}, Resources: []string{}, PatchTypes: []string{"application/merge-patch+json"}, ResourceOperations: map[string][]string{}}
		for _, capability := range jobs.Capabilities() {
			if capability.Available {
				result.Drivers[capability.Kind] = append(result.Drivers[capability.Kind], capability.Driver)
			}
		}
		for _, route := range managementResourceRoutes {
			result.Resources = append(result.Resources, route.kind)
			result.ResourceOperations[route.kind] = []string{"List" + route.plural, "Create" + route.kind, "Get" + route.kind, "Replace" + route.kind, "Patch" + route.kind, "Delete" + route.kind}
		}
		result.Resources = append(result.Resources, "Incident")
		result.ResourceOperations["Incident"] = []string{"ListIncidents", "GetIncident", "AcknowledgeIncident", "DismissIncident", "ReopenIncident"}
		result.ResourceOperations["Monitor"] = append(result.ResourceOperations["Monitor"], "SnoozeMonitor", "UnsnoozeMonitor")
		result.Resources = append(result.Resources, "Event")
		result.ResourceOperations["Event"] = []string{"GetHistory"}
		result.Resources = append(result.Resources, "Action")
		result.ResourceOperations["Action"] = []string{"ListActions", "GetAction", "ReviewAction"}
		result.Resources = append(result.Resources, "Operation")
		result.ResourceOperations["Operation"] = []string{"ListOperations", "GetOperation", "CreateOperation", "UploadOperation", "CancelOperation", "ValidateOperation", "GetOperationValidation", "ActivateOperation"}
		if m.reselection != nil {
			for _, route := range reselectionOperations {
				result.ResourceOperations["Operation"] = append(result.ResourceOperations["Operation"], route.name)
			}
		}
		result.Resources = append(result.Resources, "Collection")
		result.ResourceOperations["Collection"] = []string{"PreflightCollection", "PrepareCollection"}
		result.ResourceOperations["Monitor"] = append(result.ResourceOperations["Monitor"], "RecoverMonitor")
		for _, observation := range observationRoutes {
			result.Resources = append(result.Resources, observation.kind)
			result.ResourceOperations[observation.kind] = []string{observation.operation}
		}
		m.discoverExtensions(&result)
		writeManagementJSON(w, http.StatusOK, result)
	})
}

func (m *managementHTTP) managementPermissions(permissions []string) []string {
	supported := map[string]bool{"PrepareCollection": true, "CreateOperation": true, "UploadOperation": true, "CancelOperation": true, "ValidateOperation": true, "GetOperationValidation": true, "ActivateOperation": true, "PreflightCollection": true, "GetAccess": true, "GetCapabilities": true, "GetOperation": true, "ListOperations": true,
		"ListIncidents": true, "GetIncident": true, "AcknowledgeIncident": true, "DismissIncident": true,
		"ReopenIncident": true, "SnoozeMonitor": true, "UnsnoozeMonitor": true, "GetHistory": true, "GetAction": true, "ListActions": true, "ReviewAction": true, "RecoverMonitor": true}
	for _, observation := range observationRoutes {
		supported[observation.operation] = true
	}
	if m.reselection != nil {
		for _, route := range reselectionOperations {
			supported[route.name] = true
		}
	}
	for _, route := range managementResourceRoutes {
		for _, operation := range []string{"List" + route.plural, "Create" + route.kind, "Get" + route.kind, "Replace" + route.kind, "Patch" + route.kind, "Delete" + route.kind} {
			supported[operation] = true
		}
	}
	m.extensionPermissions(supported)
	result := make([]string, 0, len(permissions))
	for _, operation := range permissions {
		if supported[operation] {
			result = append(result, operation)
		}
	}
	return result
}

func managementHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Request-ID", uuid.NewString())
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func writeManagementJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		writeManagementError(w, management.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}
func writeManagementError(w http.ResponseWriter, err error) {
	status, code, title, detail := http.StatusInternalServerError, "internalError", "Request failed", "The request could not be completed."
	var authError *httpauth.Error
	var failure *managementHTTPError
	switch {
	case errors.As(err, &failure):
		status, code, detail = failure.status, failure.code, failure.detail
		title = http.StatusText(status)
	case errors.As(err, &authError):
		status = authError.StatusCode()
		title = http.StatusText(status)
		if status == 401 {
			code = "unauthorized"
			detail = "Supply an authorized bearer token."
			w.Header().Set("WWW-Authenticate", "Bearer")
		} else {
			code = "forbidden"
			detail = "This operation requires permission and an approved HTTPS origin."
		}
	case errors.Is(err, persistence.ErrCatalogBusy):
		status, code, title, detail = 429, "admissionBusy", "Too Many Requests", "Pending management operations are at capacity. Wait for reconciliation before retrying."
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, persistence.ErrCollectionQuota):
		status, code, title, detail = 429, "collectionQuota", "Too Many Requests", "Encrypted collection capacity is exhausted. Retain the original ticket and operation; do not create replacement input automatically."
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, persistence.ErrCollectionConflict):
		status, code, title, detail = 409, "collectionConflict", "Conflict", "Collection identity or progress changed. Reconcile the original operation before resuming."
	case errors.Is(err, persistence.ErrCollectionInvalid):
		status, code, title, detail = 422, "invalidCollection", "Unprocessable Content", "Collection input or admission identity is invalid."
	case errors.Is(err, persistence.ErrCollectionUnavailable):
		status, code, title, detail = 503, "collectionUnavailable", "Service Unavailable", "Encrypted collection storage is unavailable."
	case errors.Is(err, persistence.ErrOperationAllocationUnconfirmed):
		status, code, title, detail = 503, "operationAllocationUnconfirmed", "Service Unavailable", "Operation allocation is unconfirmed. No resource change or action was submitted. No automatic retry was made."
		w.Header().Set("X-CPRa-Admission", "not-submitted")
		w.Header().Del("X-Operation-ID")
	case errors.Is(err, persistence.ErrOperationExpired):
		status, code, title, detail = 410, "operationExpired", "Gone", "This operation expired or belongs to an earlier storage epoch. It cannot be resumed or used to repeat an action."
	case errors.Is(err, persistence.ErrOperationReservation):
		status, code, title, detail = 409, "operationConflict", "Conflict", "The operation does not match its original reserved content and identity. Inspect the original operation before submitting new work."
	case errors.Is(err, persistence.ErrHistoryUnavailable):
		status, code, title, detail = 503, "historyUnavailable", "Service Unavailable", "Retained operation history is unavailable. Do not repeat a mutation automatically."
	case errors.Is(err, persistence.ErrHistoryCursorExpired):
		status, code, title, detail = 410, "cursorExpired", "Gone", "History retention advanced. Start a new timeline query."
	case errors.Is(err, persistence.ErrOperationNotFound):
		status, code, title, detail = 404, "operationNotFound", "Not Found", "The operation is not available. Do not repeat a mutation automatically."
	case errors.Is(err, persistence.ErrCatalogNotFound):
		status, code, title, detail = 404, "notFound", "Not Found", "The resource does not exist."
	case errors.Is(err, persistence.ErrCatalogConflict):
		status, code, title, detail = 412, "versionConflict", "Precondition Failed", "The resource version or incarnation changed. Reload before editing."
	case errors.Is(err, persistence.ErrLateEvidenceConflict):
		status, code, title, detail = 409, "evidenceConflict", "Conflict", "The proposed review contradicts retained action evidence. Original facts remain unchanged."
	case errors.Is(err, persistence.ErrActionReviewConflict):
		status, code, title, detail = 412, "versionConflict", "Precondition Failed", "The action observation or review changed. Reload before reviewing."
	case errors.Is(err, persistence.ErrRecoveryIneligible):
		status, code, title, detail = 409, "recoveryIneligible", "Conflict", "Recovery is not eligible under the current monitor state and configured limits."
	case errors.Is(err, persistence.ErrRecoveryRateLimited):
		status, code, title, detail = 429, "recoveryRateLimited", "Too Many Requests", "The configured manual recovery limit has been reached."
	case errors.Is(err, persistence.ErrExecutorUnfenced):
		status, code, title, detail = 409, "executorUnfenced", "Conflict", "The original executor is active or is not verified fenced. Its action hold cannot be cleared."
	case errors.Is(err, persistence.ErrControlConflict):
		status, code, title, detail = 412, "versionConflict", "Precondition Failed", "The control version, incident, or monitor incarnation changed. Reload before trying again."
	case errors.Is(err, persistence.ErrIncidentNotActive):
		status, code, title, detail = 409, "incidentNotActive", "Conflict", "This incident is closed or has been replaced."
	case errors.Is(err, persistence.ErrControlInvalid):
		status, code, title, detail = 422, "invalidControl", "Unprocessable Content", "Check the control revision, incident, reason, note, and duration. Snooze requires a positive duration of at most 30 days."
	case errors.Is(err, persistence.ErrCatalogReferenced):
		status, code, title, detail = 409, "resourceReferenced", "Conflict", "Change references before deleting this resource."
	case errors.Is(err, persistence.ErrCatalogDependency):
		status, code, title, detail = 409, "dependencyConflict", "Conflict", "A referenced resource changed. Repeat validation against current resources."
	case errors.Is(err, management.ErrGraphLimit):
		status, code, title, detail = 422, "validationGraphLimit", "Unprocessable Content", "The affected graph exceeds synchronous validation capacity."
	case errors.Is(err, management.ErrValidation):
		status, code, title, detail = 422, "invalidResource", "Unprocessable Content", "Check resource fields, compiled drivers, references, and credential bindings."
	case errors.Is(err, management.ErrOutcomeUnconfirmed):
		status, code, title, detail = 503, "outcomeUnconfirmed", "Service Unavailable", "The mutation outcome is unconfirmed. Reconcile the original resource; do not repeat the write automatically."
	case errors.Is(err, management.ErrUnavailable):
		status, code, title, detail = 503, "unavailable", "Service Unavailable", "Durable management is unavailable."
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		status, code, title, detail = 503, "requestInterrupted", "Service Unavailable", "The request was interrupted. Reconcile any submitted mutation before trying again."
	}
	problem := api.Problem{Type: "about:blank", Title: title, Status: int64(status), Code: code, Detail: detail, RequestID: w.Header().Get("X-Request-ID")}
	raw, _ := json.Marshal(problem)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}

func (m *managementHTTP) handleResource(w http.ResponseWriter, r *http.Request, route managementResourceRoute, operation string) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, operation); err != nil {
		writeManagementError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if r.PathValue("id") == "" {
			m.handleList(w, r, route, operation)
			return
		}
		var resource api.Resource
		err := m.withObservationRead(r, operation, func(ctx context.Context) error {
			var err error
			resource, err = m.catalog.Get(ctx, route.kind, r.PathValue("id"))
			return err
		})
		if err != nil {
			writeManagementError(w, err)
			return
		}
		resourceHeaders(w, resource)
		writeManagementJSON(w, http.StatusOK, resource)
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
		writeManagementError(w, managementFailure(429, "admissionBusy", "Management validation capacity is busy. Retry after the stated interval."))
		return
	}
	version, err := managementPrecondition(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var raw []byte
	if r.Method != http.MethodDelete {
		raw, err = managementBody(w, r)
		if err != nil {
			writeManagementError(w, err)
			return
		}
		defer clear(raw)
	}
	prepared, err := m.prepareChange(r.Context(), r.Method, route.kind, r.PathValue("id"), version, raw)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result management.MutationResult
	err = m.admit(r.Context(), func() error {
		return m.auth.WithAdmission(r, operation, func(access api.AccessInfo) error {
			var err error
			result, err = m.catalog.CommitAs(r.Context(), prepared, access.PrincipalID)
			// Admission assigns the server-owned handle. Retain it on uncertain
			// outcomes too, before any success or problem response is written.
			if id := prepared.OperationID(); id != "" {
				w.Header().Set("X-Operation-ID", id)
			}
			return err
		})
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	resourceHeaders(w, result.Resource)
	w.Header().Set("X-CPRa-Admission", "committed")
	w.Header().Set("X-Commit-Index", strconv.FormatUint(result.CommittedIndex, 10))
	if r.Method == http.MethodDelete {
		writeManagementJSON(w, http.StatusOK, result.Operation)
		return
	}
	writeManagementJSON(w, http.StatusOK, result.Resource)
}

func (m *managementHTTP) prepareChange(ctx context.Context, method, kind, id, version string, raw []byte) (*management.PreparedChange, error) {
	switch method {
	case http.MethodPatch:
		return m.catalog.PreparePatch(ctx, kind, id, version, raw)
	case http.MethodDelete:
		return m.catalog.PrepareDelete(ctx, kind, id, version)
	case http.MethodPost, http.MethodPut:
		resource, err := api.DecodeResource(raw)
		if err != nil {
			return nil, managementFailure(400, "invalidResource", "The body must contain one valid resource without duplicate or unknown fields.")
		}
		if resource.Kind != kind || (method == http.MethodPut && resource.Metadata.ID != id) {
			return nil, managementFailure(400, "identityMismatch", "The resource kind and identity must match the request route.")
		}
		return m.catalog.Prepare(ctx, resource, version, method == http.MethodPost)
	default:
		return nil, managementFailure(405, "methodNotAllowed", "This method is not supported.")
	}
}
func resourceHeaders(w http.ResponseWriter, resource api.Resource) {
	w.Header().Set("ETag", `"`+resource.Metadata.ResourceVersion+`"`)
	w.Header().Set("X-Resource-Version", resource.Metadata.ResourceVersion)
}
func managementPrecondition(r *http.Request) (string, error) {
	matches, none := r.Header.Values("If-Match"), r.Header.Values("If-None-Match")
	if r.Method == http.MethodPost {
		if len(none) == 0 {
			return "", managementFailure(428, "preconditionRequired", "Creation requires If-None-Match: *.")
		}
		if len(none) != 1 || none[0] != "*" || len(matches) != 0 {
			return "", managementFailure(400, "invalidPrecondition", "Creation requires exactly one absence precondition.")
		}
		return "", nil
	}
	return managementVersionPrecondition(r)
}
func managementVersionPrecondition(r *http.Request) (string, error) {
	matches, none := r.Header.Values("If-Match"), r.Header.Values("If-None-Match")
	if len(matches) == 0 {
		return "", managementFailure(428, "preconditionRequired", "Updates require one strong If-Match resource version.")
	}
	if len(matches) != 1 || len(none) != 0 {
		return "", managementFailure(400, "invalidPrecondition", "Supply one strong If-Match resource version.")
	}
	tag := matches[0]
	if len(tag) < 3 || len(tag) > 258 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return "", managementFailure(400, "invalidPrecondition", "Weak tags, wildcard tags and tag lists are not accepted.")
	}
	version := tag[1 : len(tag)-1]
	for _, b := range []byte(version) {
		if b < 0x21 || b > 0x7e || b == '"' || b == '\\' || b == ',' || b == '*' {
			return "", managementFailure(400, "invalidPrecondition", "Supply one opaque resource version.")
		}
	}
	return version, nil
}
func managementBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if encoding := r.Header.Values("Content-Encoding"); len(encoding) > 1 || (len(encoding) == 1 && encoding[0] != "identity") {
		return nil, managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, api.MaxResourceBytes)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		clear(raw)
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return nil, managementFailure(413, "resourceTooLarge", "A resource cannot exceed 1 MiB.")
		}
		return nil, managementFailure(400, "invalidBody", "The request body could not be read.")
	}
	if len(raw) == 0 {
		return nil, managementFailure(400, "invalidBody", "A resource body is required.")
	}
	return raw, nil
}

// Each cursor is an HMAC-authenticated immutable position in one retained view.
// Reading the same cursor does not consume it or advance shared mutable state.
type managementCursor struct {
	View  string `json:"v"`
	After string `json:"a"`
}
type managementList struct {
	Items       []api.Resource `json:"items"`
	NextCursor  string         `json:"nextCursor,omitempty"`
	Snapshot    string         `json:"snapshot"`
	GeneratedAt time.Time      `json:"generatedAt"`
}

func (m *managementHTTP) handleList(w http.ResponseWriter, r *http.Request, route managementResourceRoute, operation string) {
	query, err := managementListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result managementList
	err = m.withSnapshotRead(r, operation, route.kind, query, func(ctx context.Context, read *snapshotRead) error {
		entry := &read.entry
		if read.fresh {
			view, err := m.catalog.Snapshot()
			if err != nil {
				return err
			}
			entry.view = view
		}
		items, after, err := managementPage(ctx, entry.view, route.kind, read.cursor.After, entry.limit)
		read.after = after
		result = managementList{Items: items, Snapshot: read.cursor.View, GeneratedAt: entry.created}
		if after != "" {
			result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: after})
		}
		return err
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}
func managementListQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, managementFailure(400, "invalidQuery", "The query is too large.")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managementFailure(400, "invalidQuery", "The query is invalid.")
	}
	for name, values := range query {
		if len(values) != 1 {
			return nil, managementFailure(400, "invalidQuery", "Duplicate query parameters are not accepted.")
		}
		switch name {
		case "cursor", "limit":
		case "selector", "monitorID":
			if values[0] != "" {
				return nil, managementFailure(501, "featureUnavailable", "Filtered catalog navigation is not enabled.")
			}
		default:
			return nil, managementFailure(400, "invalidQuery", "The query contains an unsupported parameter.")
		}
	}
	return query, nil
}
func managementPage(ctx context.Context, view snapshotPageView[api.Resource], kind, after string, limit int) ([]api.Resource, string, error) {
	items := make([]api.Resource, 0, min(limit, 16))
	size := 1024
	boundary := after
	for len(items) < limit {
		batch, next, err := view.Page(ctx, kind, boundary, min(16, limit-len(items)))
		if err != nil {
			return nil, "", err
		}
		for _, resource := range batch {
			raw, err := json.Marshal(resource)
			if err != nil {
				return nil, "", management.ErrUnavailable
			}
			if size+len(raw)+1 > managementMaxPageBytes && len(items) > 0 {
				return items, boundary, nil
			}
			size += len(raw) + 1
			items = append(items, resource)
			boundary = resource.Metadata.ID
		}
		if next == "" {
			return items, "", nil
		}
	}
	return items, boundary, nil
}
func (m *managementHTTP) encodeCursor(cursor managementCursor) string {
	raw, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, m.cursorKey[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(raw, mac.Sum(nil)...))
}
func (m *managementHTTP) decodeCursor(value string) (managementCursor, error) {
	var cursor managementCursor
	invalid := managementFailure(400, "invalidCursor", "The catalog cursor is invalid.")
	if len(value) > 4096 {
		return cursor, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) <= sha256.Size {
		return cursor, invalid
	}
	payload, signature := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, m.cursorKey[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) || api.StrictDecode(payload, &cursor) != nil || cursor.View == "" || cursor.After == "" {
		return managementCursor{}, invalid
	}
	return cursor, nil
}
