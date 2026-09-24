package persistence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// CollectionValidationFormatVersion adds retained, source-attributed validation
// results and the durable named-operator lifecycle boundary. Older command and
// snapshot formats retain their literal interpretations during replay.
const CollectionValidationFormatVersion = 6

const (
	CollectionValidationResultMaxBytes = 32 << 20
	CollectionValidationItemMaxBytes   = 4096
)

// CollectionValidationItem is an allowlisted observation of one original input,
// in input order. It carries no resource body, provider diagnostic or source path.
// A successful individual classification cannot authorize collection activation.
type CollectionValidationItem struct {
	Ordinal         uint64     `json:"ordinal"`
	Key             CatalogKey `json:"key"`
	Source          string     `json:"source"`
	Document        uint64     `json:"document"`
	Item            uint64     `json:"item"`
	Change          string     `json:"change,omitempty"`
	Issue           string     `json:"issue,omitempty"`
	UID             string     `json:"uid,omitempty"`
	ResourceVersion string     `json:"resource_version,omitempty"`
}

// CollectionValidationDescriptor commits to the entire ordered result inventory.
// Bytes counts framed canonical item JSON, excluding database wrappers.
type CollectionValidationDescriptor struct {
	Count  uint64 `json:"count"`
	Bytes  uint64 `json:"bytes"`
	Digest string `json:"digest"`
}

func CollectionValidationInitialDigest() string {
	sum := sha256.Sum256([]byte("cpra/collection/validation-result/v1\x00"))
	return hex.EncodeToString(sum[:])
}

// CollectionValidationItemEncoding validates before producing canonical bytes.
// Callers retain ownership of the returned buffer; no encryption keys are used.
func CollectionValidationItemEncoding(item CollectionValidationItem) ([]byte, error) {
	if item.Ordinal == 0 || item.Ordinal > maxCollectionItems || item.Key.validate() != nil ||
		bootstrapOrder(item.Key) == "" ||
		(item.UID == "") != (item.ResourceVersion == "") ||
		item.UID != "" && (!catalogIdentifier(item.UID, 256) || !catalogIdentifier(item.ResourceVersion, 256)) {
		return nil, ErrCollectionInvalid
	}
	var key [commitment.KeyBytes]byte
	if _, err := commitment.ItemMAC(key[:], commitment.Position{Ordinal: item.Ordinal, ID: item.Key.Kind + "/" + item.Key.ID,
		Source: commitment.SourcePosition{Token: item.Source, Document: item.Document, Item: item.Item}}, []byte("{}")); err != nil {
		return nil, ErrCollectionInvalid
	}
	switch item.Change {
	case "", "create", "update", "unchanged":
	default:
		return nil, ErrCollectionInvalid
	}
	if !CollectionValidationIssue(item.Issue) || item.Change == "" && item.Issue == "" ||
		item.Change == "create" && item.UID != "" {
		return nil, ErrCollectionInvalid
	}
	raw, err := json.Marshal(item)
	if err != nil || len(raw) > CollectionValidationItemMaxBytes {
		return nil, ErrCollectionInvalid
	}
	return raw, nil
}

// CollectionValidationIssue is deliberately a fixed vocabulary. Provider errors
// and arbitrary messages must never become persisted diagnostics.
func CollectionValidationIssue(issue string) bool {
	switch issue {
	case "", "invalidSource", "invalidResource", "duplicateIdentity", "invalidGraph", "validationLimit", "conflict", "missingReference", "unsafePrefix", "notEvaluated", "validationInterrupted":
		return true
	default:
		return false
	}
}

func CollectionValidationNextDigest(previous string, item CollectionValidationItem) (string, uint64, error) {
	if !bootstrapHash(previous) {
		return "", 0, ErrCollectionInvalid
	}
	raw, err := CollectionValidationItemEncoding(item)
	if err != nil {
		return "", 0, err
	}
	prior, _ := hex.DecodeString(previous)
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/validation-prefix/v1\x00"))
	_, _ = h.Write(prior)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), uint64(len(raw)) + 4, nil
}
