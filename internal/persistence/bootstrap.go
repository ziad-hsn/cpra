package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

var ErrBootstrapPending = errors.New("configuration migration is incomplete; resume its original encrypted stage")
var ErrBootstrapConflict = errors.New("configuration migration identity or progress changed")

// BootstrapManifest identifies the fully validated encrypted input. Neither
// digest hashes plaintext: InventoryDigest covers authenticated staging entries,
// CatalogDigest covers exact seed ciphertext in dependency order.
type BootstrapManifest struct {
	StageID         string `json:"stage_id"`
	InventoryDigest string `json:"inventory_digest"`
	CatalogDigest   string `json:"catalog_digest"`
	Count           uint64 `json:"count"`
}

type BootstrapState struct {
	Manifest       BootstrapManifest `json:"manifest"`
	Phase          string            `json:"phase"`
	Applied        uint64            `json:"applied"`
	ProgressDigest string            `json:"progress_digest"`
	LastKey        CatalogKey        `json:"last_key"`
	StartedAt      time.Time         `json:"started_at"`
	ActivatedAt    time.Time         `json:"activated_at,omitempty"`
}

// BootstrapCommand is internal startup coordination, never an HTTP operation.
// Begin and activate carry a manifest; seed carries its original ordinal and
// exact encrypted record. Random IDs/envelopes are generated in staging only.
type BootstrapCommand struct {
	Action   string             `json:"action"`
	StageID  string             `json:"stage_id"`
	Manifest *BootstrapManifest `json:"manifest,omitempty"`
	Ordinal  uint64             `json:"ordinal,omitempty"`
	Record   *CatalogRecord     `json:"record,omitempty"`
}

func bootstrapHash(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == sha256.Size && s == strings.ToLower(s)
}

func (m BootstrapManifest) validate() error {
	if !catalogIdentifier(m.StageID, 256) || !bootstrapHash(m.InventoryDigest) || !bootstrapHash(m.CatalogDigest) || m.Count > 10_000_000 {
		return ErrBootstrapConflict
	}
	if m.Count == 0 && m.CatalogDigest != BootstrapInitialDigest() {
		return ErrBootstrapConflict
	}
	return nil
}

func (b BootstrapCommand) validate() error {
	if !catalogIdentifier(b.StageID, 256) {
		return ErrBootstrapConflict
	}
	switch b.Action {
	case "begin", "activate":
		if b.Manifest == nil || b.Manifest.StageID != b.StageID || b.Manifest.validate() != nil || b.Ordinal != 0 || b.Record != nil {
			return ErrBootstrapConflict
		}
	case "seed":
		if b.Manifest != nil || b.Ordinal == 0 || b.Ordinal > 10_000_000 || b.Record == nil || validateBootstrapRecord(*b.Record) != nil {
			return ErrBootstrapConflict
		}
	default:
		return ErrBootstrapConflict
	}
	return nil
}

func bootstrapOrder(k CatalogKey) string {
	var rank string
	switch k.Kind {
	case "Credential":
		rank = "0"
	case "NotificationEndpoint":
		rank = "1"
	case "Recipient":
		rank = "2"
	case "NotificationGroup":
		rank = "3"
	case "Monitor":
		rank = "4"
	default:
		return ""
	}
	return rank + "\x00" + k.ID
}

func validateBootstrapRecord(r CatalogRecord) error {
	if r.validate() != nil || r.Removed || r.Generation != 1 || r.CommittedIndex != 0 || r.DependentsVersion != 0 || bootstrapOrder(r.Key) == "" {
		return ErrBootstrapConflict
	}
	for _, ref := range r.References {
		if bootstrapOrder(ref) == "" || bootstrapOrder(ref) >= bootstrapOrder(r.Key) {
			return ErrBootstrapConflict
		}
	}
	return nil
}

func BootstrapInitialDigest() string {
	digest := sha256.Sum256([]byte("cpra/bootstrap/catalog/v1\x00"))
	return hex.EncodeToString(digest[:])
}

// BootstrapDigest chains an exact seed-ready ciphertext record. Callers must use
// dependency/ID order, never re-encrypt or regenerate metadata during resume.
func BootstrapDigest(previous string, r CatalogRecord) (string, error) {
	if previous == "" {
		previous = BootstrapInitialDigest()
	}
	if !bootstrapHash(previous) || validateBootstrapRecord(r) != nil {
		return "", ErrBootstrapConflict
	}
	prior, _ := hex.DecodeString(previous)
	raw, err := json.Marshal(r)
	if err != nil {
		return "", ErrBootstrapConflict
	}
	h := sha256.New()
	_, _ = h.Write(prior)
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (f *machine) bootstrapPending() bool {
	return f.image.Bootstrap != nil && f.image.Bootstrap.Phase != "active"
}

func (f *machine) applyBootstrap(b BootstrapCommand, index uint64, at time.Time, format int) Result {
	current := f.image.Bootstrap
	if b.Action == "begin" {
		if current != nil {
			if current.Manifest == *b.Manifest {
				return Result{Allowed: true}
			}
			return Result{Err: ErrBootstrapConflict}
		}
		if len(f.image.Catalog) != 0 || f.pendingOperationCount() != 0 {
			return Result{Err: ErrBootstrapConflict}
		}
		f.image.Bootstrap = &BootstrapState{Manifest: *b.Manifest, Phase: "seeding", ProgressDigest: BootstrapInitialDigest(), StartedAt: at}
		f.image.Version = max(f.image.Version, CatalogFormatVersion)
		return Result{Allowed: true}
	}
	if current == nil || current.Manifest.StageID != b.StageID || at.Before(current.StartedAt) {
		return Result{Err: ErrBootstrapConflict}
	}
	if b.Action == "activate" {
		if current.Manifest != *b.Manifest || current.Applied != current.Manifest.Count || current.ProgressDigest != current.Manifest.CatalogDigest {
			return Result{Err: ErrBootstrapConflict}
		}
		if current.Phase != "active" {
			current.Phase, current.ActivatedAt = "active", at
		}
		return Result{Allowed: true}
	}
	if current.Phase != "seeding" || b.Ordinal != current.Applied+1 || b.Ordinal > current.Manifest.Count ||
		bootstrapOrder(b.Record.Key) <= bootstrapOrder(current.LastKey) || len(f.image.Catalog) != int(current.Applied) {
		return Result{Err: ErrBootstrapConflict}
	}
	conditions := make([]CatalogCondition, 0, len(b.Record.References))
	for _, ref := range b.Record.References {
		r, ok := f.image.Catalog[ref.indexKey()]
		if !ok || r.Removed {
			return Result{Err: ErrCatalogDependency}
		}
		conditions = append(conditions, CatalogCondition{Key: ref, UID: r.UID, Revision: r.Revision})
	}
	digest, err := BootstrapDigest(current.ProgressDigest, *b.Record)
	if err != nil {
		return Result{Err: err}
	}
	// Seed admission uses the same immutable catalog/index machinery. The public
	// catalog path is blocked while seeding, so partial input is never editable.
	result := f.applyCatalog(CatalogMutation{Record: *b.Record, Create: true, Conditions: conditions}, index, at, format)
	if result.Err != nil || !result.Allowed {
		return result
	}
	current.Applied, current.LastKey, current.ProgressDigest = b.Ordinal, b.Record.Key, digest
	return result
}

func validateBootstrapImage(i image) error {
	b := i.Bootstrap
	if b == nil {
		return nil
	}
	if !catalogFormat(i.Version) || b.Manifest.validate() != nil || b.StartedAt.IsZero() || b.Applied > b.Manifest.Count || !bootstrapHash(b.ProgressDigest) {
		return ErrBootstrapConflict
	}
	if b.Phase == "active" {
		if b.Applied != b.Manifest.Count || b.ProgressDigest != b.Manifest.CatalogDigest || b.ActivatedAt.Before(b.StartedAt) {
			return ErrBootstrapConflict
		}
		return nil // Later edits legitimately change the catalog's count/payloads.
	}
	if b.Phase != "seeding" || !b.ActivatedAt.IsZero() || len(i.Catalog) != int(b.Applied) || len(i.Operations) != 0 {
		return ErrBootstrapConflict
	}
	records := make([]CatalogRecord, 0, len(i.Catalog))
	for _, r := range i.Catalog {
		r.CommittedIndex, r.DependentsVersion = 0, 0
		records = append(records, r)
	}
	slices.SortFunc(records, func(a, b CatalogRecord) int { return strings.Compare(bootstrapOrder(a.Key), bootstrapOrder(b.Key)) })
	digest, last := BootstrapInitialDigest(), (CatalogKey{})
	for _, r := range records {
		var err error
		digest, err = BootstrapDigest(digest, r)
		if err != nil {
			return err
		}
		last = r.Key
	}
	if digest != b.ProgressDigest || last != b.LastKey {
		return ErrBootstrapConflict
	}
	return nil
}

// Bootstrap returns an immutable progress record, including incomplete startup
// migrations. It remains available while ordinary catalog reads are unavailable.
func (s *Store) Bootstrap() (BootstrapState, bool) {
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.image.Bootstrap == nil {
		return BootstrapState{}, false
	}
	return *s.fsm.image.Bootstrap, true
}
