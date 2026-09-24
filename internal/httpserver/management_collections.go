package httpserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const collectionPreflightBytes = 4 << 20
const collectionPreflightItems = 10000

type collectionPreflightWire struct {
	IdentityFormat    string            `json:"identityFormat"`
	IdentityKey       string            `json:"identityKey"`
	SourceFingerprint string            `json:"sourceFingerprint"`
	ContentDigest     string            `json:"contentDigest"`
	ItemCount         int64             `json:"itemCount"`
	Items             []json.RawMessage `json:"items"`
}
type collectionPreflightItem struct {
	ContentDigest  string          `json:"contentDigest"`
	ID             string          `json:"id"`
	Ordinal        int64           `json:"ordinal"`
	Resource       json.RawMessage `json:"resource"`
	Source         string          `json:"source"`
	SourceDocument int64           `json:"sourceDocument"`
	SourceItem     int64           `json:"sourceItem"`
}

func (m *managementHTTP) handleCollectionPreflight(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	// No body, catalog or source material is read before endpoint authorization.
	if _, err := m.auth.Authorize(r, "PreflightCollection"); err != nil {
		writeManagementError(w, err)
		return
	}
	if r.URL.RawQuery != "" {
		writeManagementError(w, managementFailure(400, "invalidQuery", "Collection preflight does not accept query parameters."))
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
	raw, err := collectionPreflightBody(w, r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	defer clear(raw)
	request, items, err := verifyCollectionPreflight(ctx, raw)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	defer func() {
		for _, raw := range request.Items {
			clear(raw)
		}
		for _, item := range items {
			clear(item.Resource)
		}
	}()
	var result api.Preflight
	err = m.auth.WithPolicyAdmission(r, "PreflightCollection", func(access api.AccessInfo, _ uint64) error {
		result, err = m.validateCollectionPreflight(ctx, request, items, access)
		return err
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	// The admitted callback has returned: do not recursively acquire Authorizer's
	// policy lock. Expired/revoked credentials must not receive a delayed result.
	if _, err := m.auth.Authorize(r, "PreflightCollection"); err != nil {
		writeManagementError(w, err)
		return
	}
	if err := ctx.Err(); err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}

func collectionPreflightBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if encoding := r.Header.Values("Content-Encoding"); len(encoding) > 1 || len(encoding) == 1 && encoding[0] != "identity" {
		return nil, managementFailure(415, "unsupportedEncoding", "Encoded request bodies are not supported.")
	}
	// A context alone does not interrupt a blocked request-body read. Bound
	// the actual connection/stream read too, using the earlier caller deadline.
	deadline, ok := r.Context().Deadline()
	if !ok {
		return nil, management.ErrUnavailable
	}
	control := http.NewResponseController(w)
	if control.SetReadDeadline(deadline) != nil {
		return nil, management.ErrUnavailable
	}
	defer control.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, collectionPreflightBytes)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		clear(raw)
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return nil, managementFailure(413, "collectionTooLarge", "A collection request cannot exceed 4 MiB. Split staged uploads into bounded chunks.")
		}
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, invalidCollection()
	}
	if len(raw) == 0 || !utf8.Valid(raw) {
		clear(raw)
		return nil, invalidCollection()
	}
	return raw, nil
}
func invalidCollection() error {
	return managementFailure(400, "invalidCollection", "Supply one complete collection inventory with valid exact fields and bounded resource objects.")
}
func collectionObject(raw []byte, out any, fields ...string) error {
	var values map[string]json.RawMessage
	if api.StrictDecode(raw, &values) != nil || values == nil || len(values) != len(fields) {
		return invalidCollection()
	}
	for _, field := range fields {
		value, exists := values[field]
		if !exists || string(value) == "null" {
			return invalidCollection()
		}
	}
	if json.Unmarshal(raw, out) != nil {
		return invalidCollection()
	}
	return nil
}
func collectionHex(value string) ([commitment.MACBytes]byte, error) {
	var result [commitment.MACBytes]byte
	if len(value) != 64 {
		return result, invalidCollection()
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return result, invalidCollection()
		}
	}
	n, err := hex.Decode(result[:], []byte(value))
	if err != nil || n != len(result) {
		return result, invalidCollection()
	}
	return result, nil
}

// Verify exact raw resource spans before DecodeResource or any normalization.
// Outer JSON syntax/duplicate checks do not interpret desired resource semantics.
func verifyCollectionPreflight(ctx context.Context, raw []byte) (collectionPreflightWire, []collectionPreflightItem, error) {
	var request collectionPreflightWire
	if err := collectionObject(raw, &request, "identityFormat", "identityKey", "sourceFingerprint", "contentDigest", "itemCount", "items"); err != nil {
		return request, nil, err
	}
	if request.IdentityFormat != commitment.Format {
		return request, nil, managementFailure(400, "unsupportedIdentityFormat", "The collection identity format is unsupported.")
	}
	if request.ItemCount < 0 || request.ItemCount != int64(len(request.Items)) || len(request.Items) > collectionPreflightItems || request.Items == nil {
		return request, nil, invalidCollection()
	}
	key, err := collectionHex(request.IdentityKey)
	if err != nil {
		return request, nil, err
	}
	defer clear(key[:])
	fingerprint, err := collectionHex(request.SourceFingerprint)
	if err != nil {
		return request, nil, err
	}
	defer clear(fingerprint[:])
	expected, err := collectionHex(request.ContentDigest)
	if err != nil {
		return request, nil, err
	}
	accumulator, err := commitment.NewAccumulator(key[:], uint64(request.ItemCount), fingerprint)
	if err != nil {
		return request, nil, invalidCollection()
	}
	defer accumulator.Close()
	items := make([]collectionPreflightItem, 0, len(request.Items))
	ids := map[string]bool{}
	for i, rawItem := range request.Items {
		if err := ctx.Err(); err != nil {
			return request, nil, err
		}
		var item collectionPreflightItem
		if err := collectionObject(rawItem, &item, "contentDigest", "id", "ordinal", "resource", "source", "sourceDocument", "sourceItem"); err != nil {
			return request, nil, err
		}
		if item.Ordinal != int64(i+1) || item.SourceDocument < 1 || item.SourceItem < 1 || ids[item.ID] {
			return request, nil, invalidCollection()
		}
		if len(item.Resource) > commitment.MaxResourceBytes {
			return request, nil, managementFailure(413, "resourceTooLarge", "A resource cannot exceed 1 MiB.")
		}
		if len(item.Resource) == 0 || item.Resource[0] != '{' {
			return request, nil, invalidCollection()
		}
		itemMAC, err := collectionHex(item.ContentDigest)
		if err != nil {
			return request, nil, err
		}
		position := commitment.Position{Ordinal: uint64(item.Ordinal), ID: item.ID, Source: commitment.SourcePosition{Token: item.Source, Document: uint64(item.SourceDocument), Item: uint64(item.SourceItem)}}
		if commitment.VerifyItem(key[:], position, item.Resource, itemMAC[:]) != nil || accumulator.Add(position, itemMAC) != nil {
			return request, nil, managementFailure(400, "inventoryMismatch", "The submitted resource bytes, identities, positions, or inventory commitment do not match.")
		}
		ids[item.ID] = true
		items = append(items, item)
	}
	if accumulator.Verify(expected[:]) != nil {
		return request, nil, managementFailure(400, "inventoryMismatch", "The submitted resource bytes, identities, positions, or inventory commitment do not match.")
	}
	return request, items, nil
}

func (m *managementHTTP) validateCollectionPreflight(ctx context.Context, request collectionPreflightWire, items []collectionPreflightItem, access api.AccessInfo) (api.Preflight, error) {
	result := api.Preflight{IdentityFormat: commitment.Format, ContentDigest: request.ContentDigest, ItemCount: api.Pointer(request.ItemCount), Items: make([]api.ApplyResult, len(items))}
	permissions := make(map[string]bool, len(access.Permissions))
	for _, permission := range access.Permissions {
		permissions[permission] = true
	}
	inputs := make([]management.CollectionInput, len(items))
	defer func() {
		for _, item := range inputs {
			clear(item.Resource.Spec)
		}
	}()
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Items[i] = api.ApplyResult{ID: item.ID, Outcome: "notValidated", Committed: api.Pointer(false), Applied: api.Pointer(false)}
		resource, err := api.DecodeResource(item.Resource)
		if err != nil {
			result.Items[i].Outcome = "invalid"
			result.Errors = append(result.Errors, api.FieldError{Field: fmt.Sprintf("items[%d].resource", i), Reason: "invalidResource", Message: "The resource schema or configuration is invalid."})
			continue
		}
		if item.ID != resource.Kind+"/"+resource.Metadata.ID {
			return result, managementFailure(400, "identityMismatch", "Each inventory identity must match its resource kind and ID.")
		}
		if !permissions["Get"+resource.Kind] || !permissions["Create"+resource.Kind] || !permissions["Replace"+resource.Kind] {
			return result, managementFailure(403, "forbidden", "Preflight requires read and write access to every submitted resource kind.")
		}
		inputs[i] = management.CollectionInput{Resource: resource, SourceID: item.Source, ItemID: fmt.Sprintf("item.%020d", item.Ordinal)}
	}
	if len(result.Errors) > 0 {
		return result, nil
	}
	view, err := m.catalog.Snapshot()
	if err != nil {
		return result, err
	}
	validation, err := m.catalog.ValidateCollection(ctx, view, inputs, management.CollectionValidationOptions{CanRead: func(key persistence.CatalogKey) bool { return permissions["Get"+key.Kind] }})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, management.ErrUnavailable) {
		return result, err
	}
	if errors.Is(err, management.ErrGraphLimit) {
		return result, managementFailure(413, "validationLimit", "The collection exceeds bounded synchronous graph validation. Larger collections require staged validation.")
	}
	result.Valid = err == nil && validation.Valid
	for i, item := range validation.Items {
		if i >= len(result.Items) {
			break
		}
		result.Items[i].OldVersion = item.ResourceVersion
		if item.Change != "" {
			result.Items[i].Outcome = item.Change
		}
		if item.Issue != "" {
			if item.Issue == "readDenied" {
				return api.Preflight{}, managementFailure(403, "forbidden", "Preflight requires read access to every referenced and affected resource kind.")
			}
			result.Items[i].Outcome = "invalid"
			result.Errors = append(result.Errors, api.FieldError{Field: fmt.Sprintf("items[%d].resource", i), Reason: item.Issue, Message: collectionIssueMessage(item.Issue)})
		}
	}
	if err != nil && len(result.Errors) == 0 {
		result.Errors = []api.FieldError{{Field: "items", Reason: "invalidGraph", Message: "The collection cannot be validated against the observed catalog."}}
	}
	return result, nil
}
func collectionIssueMessage(code string) string {
	switch code {
	case "conflict":
		return "A supplied or observed resource version changed. Refresh and validate the original inputs again."
	case "unsafePrefix":
		return "Dependency-first application would invalidate an existing consumer before its later update."
	case "missingReference":
		return "A required reference is unavailable in the submitted and authorized retained resources."
	case "duplicateIdentity":
		return "Each resource identity must occur once."
	default:
		return "The resource or its affected configuration graph is invalid."
	}
}
