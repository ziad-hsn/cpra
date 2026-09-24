// Package cprafixture is an in-memory HTTP contract fixture for SDK examples.
// It is not the CPRa server: it has no Raft, scheduler, providers, or durability.
package cprafixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// Token is a public fixture token. Never use it in a configured CPRa server.
const Token = "sdk-example-token"

// Server owns a loopback-only fixture. Close it when the demo finishes.
type Server struct {
	*httptest.Server
	handler *Handler
}

func New() *Server                         { h := NewHandler(); return &Server{httptest.NewServer(h), h} }
func (s *Server) Snapshot() []api.Resource { return s.handler.Snapshot() }

type staged struct {
	identity   api.CollectionPrepareRequest
	operation  api.Operation
	count      int64
	items      map[string]api.ApplyItem
	versions   map[string]string
	validation *api.ValidationResultPage
}

// Handler supports only the routes used by these examples. State resets on exit.
type Handler struct {
	mu         sync.Mutex
	resources  map[string]api.Resource
	operations map[string]*staged
	admissions map[string]*prepared
	version    uint64
}

func NewHandler() *Handler {
	return &Handler{resources: map[string]api.Resource{}, operations: map[string]*staged{}, admissions: map[string]*prepared{}}
}
func (h *Handler) Snapshot() []api.Resource {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.resources))
	for k := range h.resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]api.Resource, 0, len(keys))
	for _, k := range keys {
		var r api.Resource
		b, _ := json.Marshal(h.resources[k])
		_ = json.Unmarshal(b, &r)
		out = append(out, r)
	}
	return out
}

var kinds = map[string]string{"monitors": "Monitor", "notification-endpoints": "NotificationEndpoint", "notification-groups": "NotificationGroup", "credentials": "Credential"}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-CPRa-Fixture", "in-memory-example-only")
	if r.Header.Get("Authorization") != "Bearer "+Token {
		problem(w, 401, "fixture token required")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	w.Header().Set("X-Request-ID", "fixture-request")
	if r.URL.Path == "/api/v2/readyz" {
		write(w, 200, api.Health{Available: true})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "v2" {
		problem(w, 404, "fixture route absent")
		return
	}
	if parts[2] == "operations" {
		h.operation(w, r, parts[3:])
		return
	}
	if len(parts) == 4 && parts[2] == "collections" && parts[3] == "prepare" {
		h.prepare(w, r)
		return
	}
	kind, ok := kinds[parts[2]]
	if !ok {
		kind, ok = extensionKind(parts[2])
	}
	if !ok {
		problem(w, 404, "fixture route absent")
		return
	}
	id := ""
	if len(parts) == 4 {
		id = parts[3]
	} else if len(parts) > 4 {
		problem(w, 404, "fixture route absent")
		return
	}
	key := kind + "/" + id
	current, exists := h.resources[key]
	switch r.Method {
	case http.MethodGet:
		if id != "" {
			if !exists {
				problem(w, 404, "resource absent")
				return
			}
			w.Header().Set("ETag", strconv.Quote(current.Metadata.ResourceVersion))
			write(w, 200, current)
			return
		}
		limit := 100
		if text := r.URL.Query().Get("limit"); text != "" {
			n, err := strconv.Atoi(text)
			if err != nil || n < 1 || n > 500 {
				problem(w, 400, "invalid limit")
				return
			}
			limit = n
		}
		keys := make([]string, 0)
		for k, v := range h.resources {
			if v.Kind == kind {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		cursor := r.URL.Query().Get("cursor")
		out := make([]api.Resource, 0, limit)
		next := ""
		for _, k := range keys {
			if k <= cursor {
				continue
			}
			if len(out) == limit {
				next = kind + "/" + out[len(out)-1].Metadata.ID
				break
			}
			out = append(out, h.resources[k])
		}
		write(w, 200, map[string]any{"items": out, "nextCursor": next})
		return
	case http.MethodPost:
		if id != "" {
			problem(w, 405, "collection POST required")
			return
		}
		var value api.Resource
		if !decode(w, r, &value, api.MaxResourceBytes) {
			return
		}
		if value.Kind != kind || value.Metadata.ID == "" || strings.ContainsAny(value.Metadata.ID, "/\\") {
			problem(w, 400, "invalid resource identity")
			return
		}
		if err := api.ValidateResource(value); err != nil {
			problem(w, 422, "resource invalid for this fixture build")
			return
		}
		key = kind + "/" + value.Metadata.ID
		if _, ok := h.resources[key]; ok {
			problem(w, 412, "resource already exists")
			return
		}
		if r.Header.Get("If-None-Match") != "*" {
			problem(w, 428, "creation precondition required")
			return
		}
		value = h.put(key, value)
		w.Header().Set("ETag", strconv.Quote(value.Metadata.ResourceVersion))
		write(w, 201, value)
	case http.MethodPut, http.MethodPatch:
		if !exists {
			problem(w, 404, "resource absent")
			return
		}
		if r.Header.Get("If-Match") != strconv.Quote(current.Metadata.ResourceVersion) {
			problem(w, 412, "resource version changed")
			return
		}
		var value api.Resource
		if r.Method == http.MethodPut {
			if !decode(w, r, &value, api.MaxResourceBytes) {
				return
			}
		} else {
			var patch map[string]any
			if !decode(w, r, &patch, api.MaxResourceBytes) {
				return
			}
			b, _ := json.Marshal(current)
			var original map[string]any
			_ = json.Unmarshal(b, &original)
			merged := merge(original, patch)
			b, _ = json.Marshal(merged)
			if json.Unmarshal(b, &value) != nil {
				problem(w, 400, "invalid merged resource")
				return
			}
		}
		if value.Metadata.ID != id || value.Kind != kind || api.ValidateResource(value) != nil {
			problem(w, 422, "invalid replacement")
			return
		}
		value = h.put(key, value)
		w.Header().Set("ETag", strconv.Quote(value.Metadata.ResourceVersion))
		write(w, 200, value)
	default:
		problem(w, 405, "fixture method absent")
	}
}
func (h *Handler) put(key string, r api.Resource) api.Resource {
	h.version++
	old, ok := h.resources[key]
	if ok {
		r.Metadata.UID = old.Metadata.UID
		r.Metadata.Generation = old.Metadata.Generation + 1
	} else {
		r.Metadata.UID = fmt.Sprintf("fixture-uid-%d", h.version)
		r.Metadata.Generation = 1
	}
	r.Metadata.ResourceVersion = fmt.Sprintf("fixture-rv-%d", h.version)
	h.resources[key] = r
	return r
}
func (h *Handler) operation(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 && r.Method == http.MethodPost {
		var req api.OperationCreateRequest
		if !decode(w, r, &req, 132<<10) {
			return
		}
		admission, ok := h.admissions[req.AdmissionTicket]
		if !ok || !time.Now().Before(admission.expires) {
			problem(w, 410, "original fixture admission expired or absent")
			return
		}
		identity := creationIdentity(req)
		if !sameIdentity(identity, admission.identity) {
			problem(w, 409, "fixture admission identity changed")
			return
		}
		if admission.operationID != "" {
			write(w, 200, h.operations[admission.operationID].operation)
			return
		}
		id := fmt.Sprintf("fixture-op-%d", len(h.operations)+1)
		o := &staged{identity: identity, operation: api.Operation{ID: id, ContentDigest: req.ContentDigest, IdentityFormat: commitment.Format, ItemCount: api.Pointer(req.ItemCount), Uploaded: api.Pointer(int64(0)), Applied: api.Pointer(int64(0)), Committed: api.Pointer(int64(0)), State: "staging"}, count: req.ItemCount, items: map[string]api.ApplyItem{}, versions: map[string]string{}}
		h.operations[id] = o
		admission.operationID = id
		w.Header().Set("X-Operation-ID", id)
		write(w, 201, o.operation)
		return
	}
	if len(parts) == 0 {
		problem(w, 405, "method absent")
		return
	}
	o, ok := h.operations[parts[0]]
	if !ok {
		problem(w, 404, "operation absent")
		return
	}
	w.Header().Set("X-Operation-ID", o.operation.ID)
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeFixtureExecutionPage(w, r, o.operation)
		return
	}
	if len(parts) != 2 {
		problem(w, 404, "operation route absent")
		return
	}
	switch parts[1] {
	case "items":
		if r.Method != http.MethodPut || o.operation.State != "staging" {
			problem(w, 409, "not staging")
			return
		}
		var req api.UploadRequest
		if !decode(w, r, &req, 4<<20) {
			return
		}
		if len(req.Items) > 256 {
			problem(w, 413, "chunk too large")
			return
		}
		chunkIDs := map[string]bool{}
		newCount := len(o.items)
		for _, item := range req.Items {
			if item.ID == "" || chunkIDs[item.ID] {
				problem(w, 422, "duplicate or empty item ID in chunk")
				return
			}
			chunkIDs[item.ID] = true
			if _, exists := o.items[item.ID]; !exists {
				newCount++
			}
			if int64(newCount) > o.count {
				problem(w, 413, "upload exceeds declared item count")
				return
			}
			raw, _ := json.Marshal(item.Resource)
			key, _ := hex.DecodeString(*o.identity.IdentityKey)
			mac, _ := hex.DecodeString(item.ContentDigest)
			position := itemPosition(item)
			valid := commitment.VerifyItem(key, position, raw, mac) == nil
			clear(key)
			if !valid || item.ID != item.Resource.Kind+"/"+item.Resource.Metadata.ID || api.ValidateResource(item.Resource) != nil {
				problem(w, 422, "invalid resource/digest")
				return
			}
			if old, ok := o.items[item.ID]; ok && old.ContentDigest != item.ContentDigest {
				problem(w, 409, "changed item replay")
				return
			}
		}
		o.operation.Validated = api.Pointer(false)
		o.versions = map[string]string{}
		for _, item := range req.Items {
			o.items[item.ID] = item
		}
		o.operation.Uploaded = api.Pointer(int64(len(o.items)))
		write(w, 200, o.operation)
	case "validate":
		if r.Method != http.MethodPost {
			problem(w, 405, "POST required")
			return
		}
		if o.validation != nil {
			write(w, 202, o.operation)
			return
		}
		if o.operation.State != "staging" {
			problem(w, 409, "not staging")
			return
		}
		if int64(len(o.items)) != o.count || !validInventory(o) {
			problem(w, 409, "complete input required")
			return
		}
		// This fixture seals immediately. Production admission runs asynchronously.
		o.operation.State = "validated"
		o.operation.Validated = api.Pointer(true)
		at := time.Now().UTC()
		rows := make([]api.ValidationResultItem, 0, len(o.items))
		for _, item := range o.items {
			key := item.Resource.Kind + "/" + item.Resource.Metadata.ID
			old := h.resources[key]
			o.versions[key] = old.Metadata.ResourceVersion
			change := "create"
			uid, version := "", ""
			if old.Metadata.ResourceVersion != "" {
				change = "update"
				uid = old.Metadata.UID
				version = old.Metadata.ResourceVersion
			}
			rows = append(rows, api.ValidationResultItem{Ordinal: item.Ordinal, Kind: item.Resource.Kind, ID: item.Resource.Metadata.ID, Source: item.Source, SourceDocument: item.SourceDocument, SourceItem: item.SourceItem, Change: change, UID: uid, ResourceVersion: version})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Ordinal < rows[j].Ordinal })
		encoded, _ := json.Marshal(rows)
		digest := sha256.Sum256(encoded)
		profile := sha256.Sum256([]byte("example-only-fixture"))
		o.validation = &api.ValidationResultPage{OperationID: o.operation.ID, IdentityFormat: commitment.Format, ContentDigest: o.operation.ContentDigest, ItemCount: o.count, Items: rows, Summary: api.ValidationResultSummary{ResultID: o.operation.ID + "-result", Valid: true, Count: o.count, Digest: hex.EncodeToString(digest[:]), PlanID: o.operation.ID + "-plan", PlanDigest: hex.EncodeToString(digest[:]), CapabilitiesDigest: hex.EncodeToString(profile[:]), FinalizedAt: at, ExpiresAt: at.Add(30 * 24 * time.Hour)}}
		write(w, 202, o.operation)
	case "validation":
		if r.Method != http.MethodGet {
			problem(w, 405, "GET required")
			return
		}
		if o.validation == nil {
			w.Header().Set("Retry-After", "5")
			write(w, 409, api.Problem{Code: "validationPending", OperationID: o.operation.ID})
			return
		}
		limit := 100
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 500 {
				problem(w, 400, "invalid page limit")
				return
			}
			limit = n
		}
		after := 0
		if value := r.URL.Query().Get("cursor"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n >= len(o.validation.Items) {
				problem(w, 400, "invalid fixture cursor")
				return
			}
			after = n
		}
		page := *o.validation
		end := min(after+limit, len(page.Items))
		page.Items = page.Items[after:end]
		if end < len(o.validation.Items) {
			page.NextCursor = strconv.Itoa(end)
		}
		write(w, 200, page)
	case "activate":
		if r.Method != http.MethodPost {
			problem(w, 405, "POST required")
			return
		}
		if o.operation.ExecutionResult != nil {
			write(w, 200, fixtureActivationReceipt(o.operation))
			return
		}
		if o.operation.Validated == nil || !*o.operation.Validated || o.operation.State != "validated" {
			problem(w, 409, "full validation required")
			return
		}
		h.activateCollection(o)
		write(w, 200, fixtureActivationReceipt(o.operation))
	case "cancel":
		if r.Method != http.MethodPost {
			problem(w, 405, "POST required")
			return
		}
		if o.operation.State == "staging" || o.operation.State == "validated" {
			o.operation.State = "cancelled"
		}
		write(w, 200, o.operation)
	default:
		problem(w, 404, "operation route absent")
	}
}
func order(kind string) int {
	switch kind {
	case "Credential", "JobType":
		return 0
	case "NotificationEndpoint":
		return 1
	case "NotificationGroup":
		return 2
	default:
		return 3
	}
}
func decode(w http.ResponseWriter, r *http.Request, out any, limit int) bool {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	raw, err := io.ReadAll(r.Body)
	if err != nil || api.StrictDecode(raw, out) != nil {
		problem(w, 400, "invalid or oversized JSON")
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, detail string) {
	write(w, status, api.Problem{Status: int64(status), Title: detail})
}
func merge(dst, patch map[string]any) map[string]any {
	for key, value := range patch {
		if value == nil {
			delete(dst, key)
			continue
		}
		m, ok := value.(map[string]any)
		if !ok {
			dst[key] = value
			continue
		}
		old, _ := dst[key].(map[string]any)
		if old == nil {
			old = map[string]any{}
		}
		dst[key] = merge(old, m)
	}
	return dst
}
