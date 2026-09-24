//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
)

// CatalogJobTypeFormatVersion pins external configuration to immutable encrypted
// JobType versions. It adds no execution adapter or worker start permission.
const CatalogJobTypeFormatVersion = 18
const catalogJobTypeSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-18\n"
const MaxCatalogJobTypeReferences = 64

// JobTypeReference contains no parameters or worker credential aliases. Revision
// identifies the retained version record, not the editable current descriptor.
type JobTypeReference struct {
	JobTypeID  string `json:"job_type_id"`
	JobTypeUID string `json:"job_type_uid"`
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	Category   string `json:"category"`
}

func (r JobTypeReference) Validate() error {
	if !catalogIdentifier(r.JobTypeID, 256) || !catalogIdentifier(r.JobTypeUID, 256) || !jobTypeSelector(r.Version) || !catalogIdentifier(r.Revision, 256) || r.Category != "check" && r.Category != "recovery" && r.Category != "notification" {
		return ErrJobTypeInvalid
	}
	return nil
}
func jobTypeReferenceKey(r JobTypeReference) string {
	return r.JobTypeID + "\x00" + r.JobTypeUID + "\x00" + r.Version + "\x00" + r.Revision + "\x00" + r.Category
}

// CanonicalJobTypeReferences detaches, sorts and deduplicates a bounded set.
func CanonicalJobTypeReferences(refs []JobTypeReference) ([]JobTypeReference, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]JobTypeReference, 0, min(len(refs), MaxCatalogJobTypeReferences))
	seen := make(map[JobTypeReference]bool, min(len(refs), MaxCatalogJobTypeReferences))
	for _, r := range refs {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if seen[r] {
			continue
		}
		if len(out) == MaxCatalogJobTypeReferences {
			return nil, ErrJobTypeQuota
		}
		seen[r] = true
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b JobTypeReference) int {
		x, y := jobTypeReferenceKey(a), jobTypeReferenceKey(b)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	})
	return slices.Compact(out), nil
}

type catalogRecordExtensions struct {
	jobTypeReferencesPresent bool
	JobTypeReferences        []JobTypeReference `json:"job_type_references,omitempty"`
}

func cloneCatalogRecordExtensions(r CatalogRecord) catalogRecordExtensions {
	return catalogRecordExtensions{JobTypeReferences: slices.Clone(r.JobTypeReferences), jobTypeReferencesPresent: r.jobTypeReferencesPresent}
}
func validateCatalogRecordExtensions(r CatalogRecord) error {
	if len(r.JobTypeReferences) == 0 {
		if r.jobTypeReferencesPresent && r.Key.Kind != "Monitor" && r.Key.Kind != "NotificationEndpoint" {
			return ErrJobTypeInvalid
		}
		// An empty tombstone has no outbound edge. Preserve explicit presence for
		// the enclosing format gate even when trusted preparation clears the slice.
		return nil
	}
	if r.Removed || r.Key.Kind != "Monitor" && r.Key.Kind != "NotificationEndpoint" || len(r.JobTypeReferences) > MaxCatalogJobTypeReferences {
		return ErrJobTypeInvalid
	}
	previous := ""
	for _, ref := range r.JobTypeReferences {
		key := jobTypeReferenceKey(ref)
		if ref.Validate() != nil || key <= previous || r.Key.Kind == "NotificationEndpoint" && ref.Category != "notification" {
			return ErrJobTypeInvalid
		}
		previous = key
	}
	return nil
}
func catalogRecordMinimumFormat(r CatalogRecord) int {
	if len(r.JobTypeReferences) != 0 || r.jobTypeReferencesPresent {
		return CatalogJobTypeFormatVersion
	}
	return FormatVersion
}
func catalogReferenceCommandFormat(c Command) int {
	version := FormatVersion
	if c.Catalog != nil {
		version = max(version, catalogRecordMinimumFormat(c.Catalog.Record))
	}
	if c.Bootstrap != nil && c.Bootstrap.Record != nil {
		version = max(version, catalogRecordMinimumFormat(*c.Bootstrap.Record))
	}
	if c.CollectionExecute != nil && c.CollectionExecute.Prepared != nil {
		version = max(version, catalogRecordMinimumFormat(c.CollectionExecute.Prepared.Record))
	}
	return version
}

// retained permits an old prepared candidate or detached historical read to
// authenticate its original version after the current incarnation is removed.
func validateCatalogJobTypeReferences(i image, r CatalogRecord, retained bool) error {
	if err := validateCatalogRecordExtensions(r); err != nil {
		return err
	}
	for _, ref := range r.JobTypeReferences {
		if i.JobTypes == nil {
			return ErrCatalogDependency
		}
		state, ok := i.JobTypes.Records[ref.JobTypeID]
		if !ok || !retained && (state.Current.Record.Removed || state.Current.Record.UID != ref.JobTypeUID) {
			return ErrCatalogDependency
		}
		version, ok := state.Versions[ref.Version]
		if !ok || version.Record.UID != ref.JobTypeUID || version.Record.Revision != ref.Revision || version.Category != ref.Category {
			return ErrCatalogDependency
		}
	}
	return nil
}
func validateCatalogRecordImageExtensions(i image, r CatalogRecord, retained bool) error {
	if i.Version < catalogRecordMinimumFormat(r) {
		return ErrJobTypeInvalid
	}
	return validateCatalogJobTypeReferences(i, r, retained)
}
func catalogJobTypeIncomingKey(id, uid string) string { return "JobType\x00" + id + "\x00" + uid }
func addCatalogRecordExtensions(incoming map[string]map[string]struct{}, r CatalogRecord) {
	for _, ref := range r.JobTypeReferences {
		key := catalogJobTypeIncomingKey(ref.JobTypeID, ref.JobTypeUID)
		if incoming[key] == nil {
			incoming[key] = make(map[string]struct{})
		}
		incoming[key][r.Key.indexKey()] = struct{}{}
	}
}
func (f *machine) installCatalogRecordExtensions(old catalogRecordExtensions, r CatalogRecord) {
	for _, ref := range old.JobTypeReferences {
		key := catalogJobTypeIncomingKey(ref.JobTypeID, ref.JobTypeUID)
		delete(f.catalogIncoming[key], r.Key.indexKey())
		if len(f.catalogIncoming[key]) == 0 {
			delete(f.catalogIncoming, key)
		}
	}
	if !r.Removed {
		addCatalogRecordExtensions(f.catalogIncoming, r)
	}
}
func (f *machine) jobTypeCatalogReferenced(id, uid string) bool {
	if f.catalog == nil {
		f.rebuildCatalogIndexes()
	}
	return len(f.catalogIncoming[catalogJobTypeIncomingKey(id, uid)]) != 0
}
func operationDigestEncoding(c Command) ([]byte, error) {
	legacy := legacyOperationCommandDigest(c)
	if c.Catalog == nil || len(c.Catalog.Record.JobTypeReferences) == 0 {
		return json.Marshal(legacy)
	}
	return json.Marshal(struct {
		Format     string                   `json:"format"`
		Command    operationCommandDigestV1 `json:"command"`
		References []JobTypeReference       `json:"job_type_references"`
	}{"cpra/catalog-job-type-operation/v1", legacy, c.Catalog.Record.JobTypeReferences})
}

// JobTypeVersionView captures one immutable version and its current incarnation
// metadata atomically; it does not copy the retained version inventory.
type JobTypeVersionView struct {
	Version        JobTypeVersion
	CurrentUID     string
	CurrentRemoved bool
}

func (s *Store) LookupJobTypeVersion(ctx context.Context, id, version string) (JobTypeVersionView, bool, error) {
	if !catalogIdentifier(id, 256) || !jobTypeSelector(version) {
		return JobTypeVersionView{}, false, ErrJobTypeInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return JobTypeVersionView{}, false, err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return JobTypeVersionView{}, false, nil
	}
	state, ok := s.fsm.image.JobTypes.Records[id]
	if !ok {
		return JobTypeVersionView{}, false, nil
	}
	value, ok := state.Versions[version]
	if !ok {
		return JobTypeVersionView{}, false, nil
	}
	out := JobTypeVersionView{Version: value.Clone(), CurrentUID: state.Current.Record.UID, CurrentRemoved: state.Current.Record.Removed}
	if err := ctx.Err(); err != nil {
		return JobTypeVersionView{}, false, err
	}
	return out, true, nil
}

// UnmarshalJSON preserves strict nested decoding and field presence. A raw
// null/empty extension must not disappear before the enclosing format gate.
// The second shape-only decode copies just this bounded metadata, not Payload.
func (r *CatalogRecord) UnmarshalJSON(raw []byte) error {
	type wire CatalogRecord
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if len(value.JobTypeReferences) == 0 {
		var presence struct {
			References json.RawMessage `json:"job_type_references"`
		}
		if err := json.Unmarshal(raw, &presence); err != nil {
			return err
		}
		value.jobTypeReferencesPresent = len(presence.References) != 0
	}
	*r = CatalogRecord(value)
	return nil
}
