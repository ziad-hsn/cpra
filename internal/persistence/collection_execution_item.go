package persistence

import (
	"bytes"
	"encoding/json"
	"time"
)

const (
	collectionExecutionItemVersion   = 1
	collectionExecutionItemMaxBytes  = 16 << 10
	collectionExecutionItemPageRows  = 256
	collectionExecutionItemPageBytes = 4 << 20
)

// CollectionExecutionItem is the versioned metadata projection of one original
// input. Catalog decisions and controller dispositions are separate facts. It
// carries no resource body, encrypted payload, source path or provider error.
type CollectionExecutionItem struct {
	Version        int                           `json:"version"`
	Binding        CollectionExecutionBinding    `json:"binding"`
	InputOrdinal   uint64                        `json:"input_ordinal"`
	PlanOrdinal    uint64                        `json:"plan_ordinal"`
	Key            CatalogKey                    `json:"key"`
	Source         string                        `json:"source"`
	SourceDocument uint64                        `json:"source_document"`
	SourceItem     uint64                        `json:"source_item"`
	OriginalUID    string                        `json:"original_uid,omitempty"`
	OldVersion     string                        `json:"old_version,omitempty"`
	UID            string                        `json:"uid,omitempty"`
	NewVersion     string                        `json:"new_version,omitempty"`
	Generation     uint64                        `json:"generation,omitempty"`
	Decision       string                        `json:"decision"`
	CommittedIndex uint64                        `json:"committed_index,omitempty"`
	DecidedAt      time.Time                     `json:"decided_at,omitempty"`
	Child          *CollectionExecutionItemChild `json:"child,omitempty"`
}

// CollectionExecutionItemChild contains only the original accepted child's
// terminal disposition. A successful catalog decision is never rewritten by a
// failed projection, supersession or explicit restore.
type CollectionExecutionItemChild struct {
	ID                   string    `json:"id"`
	State                string    `json:"state"`
	Outcome              string    `json:"outcome"`
	UpdatedAt            time.Time `json:"updated_at"`
	InvalidatedByRestore string    `json:"invalidated_by_restore,omitempty"`
}

func (i CollectionExecutionItem) Clone() CollectionExecutionItem {
	if i.Child != nil {
		child := *i.Child
		i.Child = &child
	}
	return i
}

func (i CollectionExecutionItem) validate() error {
	if i.Version != collectionExecutionItemVersion || i.Binding.validate() != nil || !collectionExecutionOrdinals(i.PlanOrdinal, i.InputOrdinal) ||
		i.Key.validate() != nil || bootstrapOrder(i.Key) == "" || !validCollectionSourceCoordinates(i.Source, i.SourceDocument, i.SourceItem) ||
		i.OriginalUID != "" && !catalogIdentifier(i.OriginalUID, 256) || i.OldVersion != "" && !catalogIdentifier(i.OldVersion, 256) ||
		(i.OriginalUID == "") != (i.OldVersion == "") {
		return ErrCollectionInvalid
	}
	if i.Decision == "unattempted" {
		if i.UID != "" || i.NewVersion != "" || i.Generation != 0 || i.CommittedIndex != 0 || !i.DecidedAt.IsZero() || i.Child != nil {
			return ErrCollectionInvalid
		}
		return nil
	}
	if i.CommittedIndex == 0 || i.DecidedAt.IsZero() || i.DecidedAt.Year() < 1 || i.DecidedAt.Year() > 9999 {
		return ErrCollectionInvalid
	}
	switch i.Decision {
	case "accepted", "unchanged":
		if !catalogIdentifier(i.UID, 256) || !catalogIdentifier(i.NewVersion, 256) || i.Generation == 0 {
			return ErrCollectionInvalid
		}
		if i.Decision == "unchanged" {
			if i.Child != nil || i.UID != i.OriginalUID || i.NewVersion != i.OldVersion {
				return ErrCollectionInvalid
			}
			return nil
		}
		if i.OriginalUID != "" && i.UID != i.OriginalUID || i.NewVersion == i.OldVersion || i.OriginalUID == "" && i.Generation != 1 {
			return ErrCollectionInvalid
		}
		c := i.Child
		if c == nil || c.ID == i.Binding.OperationID || c.UpdatedAt.Before(i.DecidedAt) || c.UpdatedAt.Year() < 1 || c.UpdatedAt.Year() > 9999 ||
			c.State != "completed" && c.State != "failed" && c.State != "partial" ||
			c.State == "completed" && c.Outcome != "applied" || c.State == "failed" && c.Outcome != "projection_failed" ||
			c.State == "partial" && c.Outcome != "superseded" ||
			c.InvalidatedByRestore != "" && (!validAuthenticationID(c.InvalidatedByRestore) || c.State != "partial") {
			return ErrCollectionInvalid
		}
		parentEpoch, parentSequence, _ := ParseOperationHandle(i.Binding.OperationID)
		childEpoch, childSequence, err := ParseOperationHandle(c.ID)
		if err != nil || childEpoch != parentEpoch || childSequence <= parentSequence {
			return ErrCollectionInvalid
		}
	case "conflict", "dependencyBlocked":
		if i.UID != "" || i.NewVersion != "" || i.Generation != 0 || i.Child != nil {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

// validateSummary rejects fabricated untouched suffixes and out-of-cohort
// observations independently of the source reader. Publication/history callers
// must additionally bind the canonical row to their exact anchor and ordinal.
func (i CollectionExecutionItem) validateSummary(s CollectionExecutionSummary) error {
	if i.validate() != nil || s.validate() != nil || i.Binding != s.Binding || i.InputOrdinal > s.ItemCount || i.PlanOrdinal > s.ItemCount {
		return ErrCollectionInvalid
	}
	processed := uint64(0)
	if s.Fence.Progress != nil {
		processed = s.Fence.Progress.Processed
	}
	if i.Decision == "unattempted" {
		if i.PlanOrdinal <= processed || s.Unattempted == 0 || s.Fence.Phase != "canceled" && s.Fence.Phase != "invalidated" {
			return ErrCollectionInvalid
		}
		return nil
	}
	if i.PlanOrdinal > processed || i.DecidedAt.Before(s.ActivationAt) || i.DecidedAt.After(s.FinalizedAt) || i.Child != nil && i.Child.UpdatedAt.After(s.FinalizedAt) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionItemEncoding(item CollectionExecutionItem) ([]byte, error) {
	if item.validate() != nil {
		return nil, ErrCollectionInvalid
	}
	raw, err := json.Marshal(item)
	if err != nil || len(raw) > collectionExecutionItemMaxBytes {
		return nil, ErrCollectionInvalid
	}
	return raw, nil
}

func decodeCollectionExecutionItem(raw []byte) (CollectionExecutionItem, error) {
	if len(raw) == 0 || len(raw) > collectionExecutionItemMaxBytes {
		return CollectionExecutionItem{}, ErrCollectionInvalid
	}
	var item CollectionExecutionItem
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&item) != nil {
		return CollectionExecutionItem{}, ErrCollectionInvalid
	}
	canonical, err := collectionExecutionItemEncoding(item)
	if err != nil || !bytes.Equal(raw, canonical) {
		return CollectionExecutionItem{}, ErrCollectionInvalid
	}
	return item, nil
}
