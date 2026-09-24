package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// CollectionFormatVersion adds self-contained, streamed encrypted collection
// rows to snapshots. An older reader must reject this format rather than ignore
// inactive input which a caller has already been told is durable.
const CollectionFormatVersion = 3

const (
	maxCollectionItems                 = 10_000_000
	maxCollectionLedgerBytes     int64 = 1 << 30
	maxCollectionOperations            = 64
	CollectionInactivityLifetime       = 24 * time.Hour
)

var (
	ErrCollectionInvalid     = errors.New("invalid encrypted collection state")
	ErrCollectionConflict    = errors.New("collection identity or uploaded position changed")
	ErrCollectionUnavailable = errors.New("encrypted collection storage unavailable")
	ErrCollectionQuota       = errors.New("encrypted collection storage quota exceeded")
)

// CollectionItem stores exact transmitted resource-object bytes inside Payload.
// The inventory MAC covers the original input, not a re-encoded or secret-filled
// preparation. Source is an opaque token; paths and URLs never enter this row.
type CollectionItem struct {
	Ordinal        uint64                `json:"ordinal"`
	Key            CatalogKey            `json:"key"`
	Source         string                `json:"source"`
	SourceDocument uint64                `json:"source_document"`
	SourceItem     uint64                `json:"source_item"`
	ContentDigest  string                `json:"content_digest"`
	Payload        secureconfig.Envelope `json:"payload"`
}

func (i CollectionItem) Clone() CollectionItem {
	i.Payload = i.Payload.Clone()
	return i
}

func (i CollectionItem) Binding(storeID, uploadID string) secureconfig.Binding {
	return secureconfig.Binding{StoreID: storeID, Kind: "CollectionItem", ID: uploadID, UID: uploadID,
		Revision: strconv.FormatUint(i.Ordinal, 10) + ":" + i.ContentDigest, Purpose: "collection-input"}
}

func (i CollectionItem) validate() error {
	if i.Ordinal == 0 || i.Ordinal > maxCollectionItems || i.Key.validate() != nil ||
		bootstrapOrder(i.Key) == "" || !bootstrapHash(i.ContentDigest) || i.Payload.Validate() != nil {
		return ErrCollectionInvalid
	}
	// ItemMAC validates the public identity/coordinate envelope without using a
	// real inventory key or interpreting ciphertext as a resource.
	var key [commitment.KeyBytes]byte
	_, err := commitment.ItemMAC(key[:], commitment.Position{Ordinal: i.Ordinal, ID: i.Key.Kind + "/" + i.Key.ID,
		Source: commitment.SourcePosition{Token: i.Source, Document: i.SourceDocument, Item: i.SourceItem}}, []byte("{}"))
	if err != nil {
		return ErrCollectionInvalid
	}
	return nil
}

// CollectionState is an inactive upload header. Secret contains the private
// inventory key and source fingerprint. Only bounded headers live in the FSM;
// resource payload rows live in its disk-backed materialization. This foundation
// retains private activation admission separately from any item execution.
type CollectionState struct {
	ExecutionRetirement  *CollectionExecutionRetirementState `json:"execution_retirement,omitempty"`
	ExecutionResult      *CollectionExecutionResultState     `json:"execution_result,omitempty"`
	Execution            *CollectionExecutionProgress        `json:"execution,omitempty"`
	Activation           *CollectionActivation               `json:"activation,omitempty"`
	ID                   string                              `json:"id"`
	UploadID             string                              `json:"upload_id"`
	Actor                string                              `json:"actor"`
	Owner                *OperatorAuthority                  `json:"owner,omitempty"`
	NormalizationProfile string                              `json:"normalization_profile,omitempty"`
	IdentityFormat       string                              `json:"identity_format"`
	ContentDigest        string                              `json:"content_digest"`
	ItemCount            uint64                              `json:"item_count"`
	Uploaded             uint64                              `json:"uploaded"`
	EncodedBytes         int64                               `json:"encoded_bytes"`
	MaxEncodedBytes      int64                               `json:"max_encoded_bytes"`
	ProgressDigest       string                              `json:"progress_digest"`
	RemovedRows          uint64                              `json:"removed_rows,omitempty"`
	RemovedBytes         int64                               `json:"removed_bytes,omitempty"`
	Phase                string                              `json:"phase"`
	TerminalAt           time.Time                           `json:"terminal_at,omitempty"`
	InvalidatedByRestore string                              `json:"invalidated_by_restore,omitempty"`
	Cancellation         *CollectionCancellation             `json:"cancellation,omitempty"`
	Admission            *CollectionAdmissionProof           `json:"admission,omitempty"`
	CreatedAt            time.Time                           `json:"created_at"`
	ActivityAt           time.Time                           `json:"activity_at"`
	ExpiresAt            time.Time                           `json:"expires_at"`
	Secret               secureconfig.Envelope               `json:"secret"`
	Plan                 *CollectionPlanState                `json:"plan,omitempty"`
	Validation           *CollectionValidationState          `json:"validation,omitempty"`
	ValidationRequest    *CollectionValidationRequest        `json:"validation_request,omitempty"`
}

func (s CollectionState) Clone() CollectionState {
	if s.ExecutionRetirement != nil {
		r := s.ExecutionRetirement.Clone()
		s.ExecutionRetirement = &r
	}
	if s.ExecutionResult != nil {
		r := s.ExecutionResult.Clone()
		s.ExecutionResult = &r
	}
	if s.Execution != nil {
		copy := s.Execution.Clone()
		s.Execution = &copy
	}
	if s.Activation != nil {
		copy := s.Activation.Clone()
		s.Activation = &copy
	}
	s.Secret = s.Secret.Clone()
	if s.ValidationRequest != nil {
		copy := s.ValidationRequest.Clone()
		s.ValidationRequest = &copy
	}
	if s.Owner != nil {
		copy := *s.Owner
		s.Owner = &copy
	}
	if s.Validation != nil {
		copy := *s.Validation
		s.Validation = &copy
	}
	if s.Plan != nil {
		copy := *s.Plan
		s.Plan = &copy
	}
	if s.Admission != nil {
		copy := *s.Admission
		s.Admission = &copy
	}
	if s.Cancellation != nil {
		copy := *s.Cancellation
		s.Cancellation = &copy
	}
	return s
}

func (s CollectionState) Binding(storeID string) secureconfig.Binding {
	revision := s.ContentDigest
	if s.NormalizationProfile != "" {
		revision = "cpra.collection.profile.v1:" + s.NormalizationProfile + ":" + s.ContentDigest
	}
	return secureconfig.Binding{StoreID: storeID, Kind: "Collection", ID: s.UploadID, UID: s.UploadID,
		Revision: revision, Purpose: "collection-inventory"}
}

func (s CollectionState) validate() error {
	if s.ExecutionRetirement != nil && s.ExecutionRetirement.validateState(s) != nil {
		return ErrCollectionInvalid
	}
	if s.ExecutionResult != nil && s.ExecutionResult.validateState(s) != nil || collectionExecutionCompleted(s.Phase) && s.ExecutionResult == nil {
		return ErrCollectionInvalid
	}
	if s.Execution != nil && s.Execution.validateState(s) != nil {
		return ErrCollectionInvalid
	}
	if s.Activation != nil && s.Activation.validateState(s) != nil || s.Phase == "applying" && s.Activation == nil {
		return ErrCollectionInvalid
	}
	if s.Phase == "interrupted" && s.ValidationRequest == nil {
		return ErrCollectionInvalid
	}
	if s.ValidationRequest != nil && s.ValidationRequest.validateState(s) != nil {
		return ErrCollectionInvalid
	}
	if s.Owner != nil && (s.Owner.validate() != nil || s.Owner.Actor != s.Actor) {
		return ErrCollectionInvalid
	}
	if s.Admission != nil && s.Admission.validate() != nil {
		return ErrCollectionInvalid
	}
	if _, _, err := ParseOperationHandle(s.ID); err != nil || !validOperationEpoch(s.UploadID) ||
		!catalogIdentifier(s.Actor, 128) || s.IdentityFormat != commitment.Format || !bootstrapHash(s.ContentDigest) ||
		s.ItemCount == 0 || s.ItemCount > maxCollectionItems || s.Uploaded > s.ItemCount || !validCollectionNormalizationProfile(s.NormalizationProfile) ||
		s.MaxEncodedBytes < 1 || s.MaxEncodedBytes > maxCollectionLedgerBytes || s.EncodedBytes < 0 || s.EncodedBytes > s.MaxEncodedBytes ||
		!bootstrapHash(s.ProgressDigest) || !collectionLive(s.Phase) && s.Phase != "invalidated" && s.Phase != "expired" && s.Phase != "canceled" && s.Phase != "interrupted" && !collectionExecutionCompleted(s.Phase) || s.CreatedAt.IsZero() || s.ActivityAt.Before(s.CreatedAt) ||
		!s.ExpiresAt.Equal(s.ActivityAt.Add(CollectionInactivityLifetime)) || s.Secret.Validate() != nil || len(s.Secret.Ciphertext) > 1024 {
		return ErrCollectionInvalid
	}
	if s.Plan != nil && s.Plan.validate(s) != nil || s.Validation != nil && s.Validation.validate(s) != nil ||
		s.Plan == nil && s.Validation == nil && (s.Phase == "validating" && s.ValidationRequest == nil || s.Phase == "validated" || s.Phase == "rejected") {
		return ErrCollectionInvalid
	}
	if s.Phase == "rejected" && (s.Plan != nil || s.Validation == nil || s.Validation.Header.Valid || s.Validation.FinalizedAt.IsZero()) {
		return ErrCollectionInvalid
	}
	if s.Phase == "canceled" {
		if s.Cancellation == nil || s.Cancellation.validate() != nil || s.Cancellation.Actor != s.Actor ||
			s.Cancellation.At.Before(s.ActivityAt) || s.Activation == nil && !s.Cancellation.At.Before(s.ExpiresAt) || s.Activation != nil && s.Cancellation.At.Before(s.Activation.At) {
			return ErrCollectionInvalid
		}
	} else if s.Cancellation != nil {
		return ErrCollectionInvalid
	}
	// Older private format-3 terminal headers lack receipt timestamps. They
	// remain readable for cleanup; their historical audit cannot be promoted
	// into a complete receipt without the missing committed metadata.
	if !s.TerminalAt.IsZero() || s.InvalidatedByRestore != "" {
		if collectionReceiptFor(s).validate() != nil || collectionLive(s.Phase) ||
			s.Cancellation != nil && !s.TerminalAt.Equal(s.Cancellation.At) {
			return ErrCollectionInvalid
		}
	}
	if s.Uploaded == 0 && (s.EncodedBytes != 0 || s.ProgressDigest != collectionInitialDigest()) ||
		s.Uploaded > 0 && s.EncodedBytes == 0 {
		return ErrCollectionInvalid
	}
	if s.RemovedRows > s.Uploaded || s.RemovedBytes < 0 || s.RemovedBytes > s.EncodedBytes ||
		(s.RemovedRows == 0) != (s.RemovedBytes == 0) ||
		(s.RemovedRows == s.Uploaded) != (s.RemovedBytes == s.EncodedBytes) ||
		collectionLive(s.Phase) && (s.RemovedRows != 0 || s.RemovedBytes != 0) {
		return ErrCollectionInvalid
	}
	return nil
}

// CollectionCleanup fences one bounded removal step to its observed header.
// A rejected stale step cannot remove rows added by a renewed upload. It carries
// no keys or payload and cannot activate an operation.
type CollectionCleanup struct {
	Activation        *CollectionActivationFence        `json:"activation,omitempty"`
	Uploaded          uint64                            `json:"uploaded"`
	EncodedBytes      int64                             `json:"encoded_bytes"`
	RemovedRows       uint64                            `json:"removed_rows"`
	RemovedBytes      int64                             `json:"removed_bytes"`
	ActivityAt        time.Time                         `json:"activity_at"`
	Plan              *CollectionPlanCleanup            `json:"plan,omitempty"`
	Validation        *CollectionValidationCleanup      `json:"validation,omitempty"`
	ValidationRequest *CollectionValidationRequestFence `json:"validation_request,omitempty"`
}

// CollectionCommand carries only ciphertext and nonsecret structural metadata.
// Create allocates a handle from the ordinary operation epoch/high-water mark.
// Upload accepts precisely the next original item or an identical retry. All
// timestamps and encryption randomness are supplied before durable submission.
type CollectionCommand struct {
	Activation             *CollectionActivation             `json:"activation,omitempty"`
	ActivationFence        *CollectionActivationFence        `json:"activation_fence,omitempty"`
	ActivationAuthority    *OperatorAuthority                `json:"activation_authority,omitempty"`
	ValidationPublished    uint64                            `json:"validation_published,omitempty"`
	Action                 string                            `json:"action"`
	Epoch                  string                            `json:"epoch,omitempty"`
	OperationID            string                            `json:"operation_id,omitempty"`
	UploadID               string                            `json:"upload_id,omitempty"`
	Create                 *CollectionState                  `json:"create,omitempty"`
	Item                   *CollectionItem                   `json:"item,omitempty"`
	UploadFence            *CollectionUploadFence            `json:"upload_fence,omitempty"`
	Cleanup                *CollectionCleanup                `json:"cleanup,omitempty"`
	Cancel                 *CollectionCancellation           `json:"cancel,omitempty"`
	PlanID                 string                            `json:"plan_id,omitempty"`
	PlanBegin              *CollectionPlanBegin              `json:"plan_begin,omitempty"`
	PlanFragment           *CollectionPlanLedgerFragment     `json:"plan_fragment,omitempty"`
	PlanFinalize           *CollectionPlanVerification       `json:"plan_finalize,omitempty"`
	ValidationID           string                            `json:"validation_id,omitempty"`
	ValidationBegin        *CollectionValidationBegin        `json:"validation_begin,omitempty"`
	ValidationItems        []CollectionValidationItem        `json:"validation_items,omitempty"`
	ValidationRequest      *CollectionValidationRequest      `json:"validation_request,omitempty"`
	ValidationClaim        *CollectionValidationClaim        `json:"validation_claim,omitempty"`
	ValidationInterruption *CollectionValidationInterruption `json:"validation_interruption,omitempty"`
	ValidationFence        *CollectionValidationRequestFence `json:"validation_fence,omitempty"`
	ValidationProgress     *CollectionCleanup                `json:"validation_progress,omitempty"`
}

func (c CollectionCommand) validate(at time.Time) error {
	if c.UploadFence != nil && (c.Action != "upload" || c.UploadFence.validate() != nil || c.Item == nil || c.Item.Ordinal != c.UploadFence.Uploaded+1) {
		return ErrCollectionInvalid
	}
	if c.Action == "activation_admit" {
		return c.validateActivation(at)
	}
	if c.Activation != nil || (c.ActivationAuthority != nil || c.ActivationFence != nil) && c.Action != "cancel" {
		return ErrCollectionInvalid
	}
	if c.Action == "cancel" && ((c.ActivationFence == nil) != (c.ActivationAuthority == nil) || c.ActivationFence != nil && c.ActivationFence.validate() != nil || c.ActivationAuthority != nil && c.ActivationAuthority.validate() != nil) {
		return ErrCollectionInvalid
	}
	if collectionValidationRequestAction(c.Action) {
		return c.validateValidationRequest(at)
	}
	if c.ValidationRequest != nil || c.ValidationClaim != nil || c.ValidationInterruption != nil || c.ValidationProgress != nil {
		return ErrCollectionInvalid
	}
	if c.ValidationFence != nil {
		if c.ValidationFence.validate() != nil || (c.Action != "plan_begin" && c.Action != "plan_append" && c.Action != "plan_finalize" &&
			c.Action != "validation_begin" && c.Action != "validation_append" && c.Action != "validation_finalize") {
			return ErrCollectionInvalid
		}
	}
	if collectionValidationAction(c.Action) {
		return c.validateValidation()
	}
	if c.ValidationID != "" || c.ValidationBegin != nil || len(c.ValidationItems) != 0 || c.ValidationPublished != 0 {
		return ErrCollectionInvalid
	}
	if c.Action == "plan_begin" || c.Action == "plan_append" || c.Action == "plan_finalize" {
		return c.validatePlan(at)
	}
	if c.PlanID != "" || c.PlanBegin != nil || c.PlanFragment != nil || c.PlanFinalize != nil {
		return ErrCollectionInvalid
	}
	switch c.Action {
	case "epoch":
		if !validOperationEpoch(c.Epoch) || c.OperationID != "" || c.UploadID != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil || c.Cancel != nil {
			return ErrCollectionInvalid
		}
		return nil
	case "create":
		if c.Create == nil || !validOperationEpoch(c.Epoch) || c.OperationID != "" || c.UploadID != "" || c.Item != nil || c.Cleanup != nil || c.Cancel != nil {
			return ErrCollectionInvalid
		}
		s := *c.Create
		if s.ID != "" || s.Phase != "uploading" || s.Activation != nil || s.Plan != nil || s.Validation != nil || s.ValidationRequest != nil || s.Uploaded != 0 || s.EncodedBytes != 0 || s.RemovedRows != 0 || s.RemovedBytes != 0 || !s.CreatedAt.Equal(at) || !s.ActivityAt.Equal(at) {
			return ErrCollectionInvalid
		}
		s.ID = operationHandle(c.Epoch, 1) // Structural validation only; Apply assigns the real sequence.
		return s.validate()
	case "upload":
		if _, _, err := ParseOperationHandle(c.OperationID); err != nil || c.Epoch != "" || c.Create != nil ||
			!validOperationEpoch(c.UploadID) || c.Item == nil || c.Item.validate() != nil || c.Cleanup != nil || c.Cancel != nil {
			return ErrCollectionInvalid
		}
		return nil
	case "cancel":
		if _, _, err := ParseOperationHandle(c.OperationID); err != nil || c.Epoch != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil ||
			!validOperationEpoch(c.UploadID) || c.Cancel == nil || c.Cancel.validate() != nil || c.ActivationFence == nil && !c.Cancel.At.Equal(at) || c.ActivationFence != nil && c.Cancel.At.After(at) {
			return ErrCollectionInvalid
		}
		return nil
	case "cleanup":
		if _, _, err := ParseOperationHandle(c.OperationID); err != nil || c.Epoch != "" || c.Create != nil || c.Item != nil ||
			!validOperationEpoch(c.UploadID) || c.Cleanup == nil || c.Cancel != nil {
			return ErrCollectionInvalid
		}
		p := c.Cleanup
		if p.Activation != nil && p.Activation.validate() != nil {
			return ErrCollectionInvalid
		}
		if p.Plan != nil && p.Plan.validate() != nil || p.Validation != nil && p.Validation.validate() != nil || p.ValidationRequest != nil && p.ValidationRequest.validate() != nil {
			return ErrCollectionInvalid
		}
		if p.Uploaded > maxCollectionItems || p.EncodedBytes < 0 || p.EncodedBytes > maxCollectionLedgerBytes ||
			p.RemovedRows > p.Uploaded || p.RemovedBytes < 0 || p.RemovedBytes > p.EncodedBytes || p.ActivityAt.IsZero() ||
			(p.Uploaded == 0) != (p.EncodedBytes == 0) || (p.RemovedRows == 0) != (p.RemovedBytes == 0) {
			return ErrCollectionInvalid
		}
		return nil
	default:
		return ErrCollectionInvalid
	}
}

func collectionInitialDigest() string {
	h := sha256.Sum256([]byte("cpra/collection/encrypted-rows/v1\x00"))
	return hex.EncodeToString(h[:])
}

// CollectionInitialDigest is the progress identity of an empty encrypted upload.
// Application admission uses it when constructing a new CollectionState; it is
// separate from the client commitment to the complete plaintext inventory.
func CollectionInitialDigest() string { return collectionInitialDigest() }

func collectionNextDigest(previous string, item CollectionItem) (string, error) {
	if !bootstrapHash(previous) || item.validate() != nil {
		return "", ErrCollectionInvalid
	}
	prior, _ := hex.DecodeString(previous)
	raw, err := json.Marshal(item)
	if err != nil {
		return "", ErrCollectionInvalid
	}
	h := sha256.New()
	_, _ = h.Write(prior)
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func catalogFormat(version int) bool {
	return version == CatalogFormatVersion || collectionFormat(version)
}

func collectionFormat(version int) bool {
	return version == CollectionFormatVersion || version == CatalogMutationFormatVersion || version == CollectionPlanFormatVersion || version == CollectionValidationFormatVersion || collectionValidationRequestStorageFormat(version)
}

func supportedFormat(version int) bool { return version == FormatVersion || catalogFormat(version) }
