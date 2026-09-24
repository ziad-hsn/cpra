//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const workerOutcomeBytes = 128 << 10

var ErrWorkerOutcomeInvalid = errors.New("invalid worker outcome")

// WorkerOutcomeBinding must originate from the retained execution. Preparation
// checks the pinned contract; it does not authorize delivery or commit a result.
// Result admission must independently check the authenticated original worker.
type WorkerOutcomeBinding struct {
	ServerID          string                       `json:"server_id"`
	WorkerUID         string                       `json:"worker_uid"`
	ExecutionID       string                       `json:"execution_id"`
	ExecutionRevision string                       `json:"execution_revision"`
	GrantID           string                       `json:"grant_id"`
	JobType           persistence.JobTypeReference `json:"job_type"`
}

func (b WorkerOutcomeBinding) validate() error {
	for _, id := range []string{b.ServerID, b.WorkerUID, b.ExecutionID, b.ExecutionRevision, b.GrantID} {
		if id == "" || !workerOutcomeText(id, 256) {
			return ErrWorkerOutcomeInvalid
		}
	}
	if b.JobType.Validate() != nil {
		return ErrWorkerOutcomeInvalid
	}
	return nil
}

func (b WorkerOutcomeBinding) encryptionBinding(storeID string) secureconfig.Binding {
	raw, _ := json.Marshal(b)
	digest := sha256.Sum256(append([]byte("cpra/worker/outcome-binding/v1\x00"), raw...))
	return secureconfig.Binding{StoreID: storeID, Kind: "WorkerExecution", ID: b.ExecutionID,
		UID: b.WorkerUID, Revision: hex.EncodeToString(digest[:]), Purpose: "worker-outcome-v1"}
}

// WorkerOutcomeDisposition contains only server-owned lifecycle classifications.
// Accepted, completed and delivered remain distinct worker reports. Observation
// is true only for an actual success/failure check, never for missing data.
type WorkerOutcomeDisposition struct {
	Status      string
	Observation bool
	Retryable   bool
}

// PreparedWorkerOutcome retains only ciphertext and non-secret identity/status.
// The commit path must retain Digest as the original result's replay identity.
type PreparedWorkerOutcome struct {
	binding     WorkerOutcomeBinding
	disposition WorkerOutcomeDisposition
	digest      string
	payload     secureconfig.Envelope
}

func (p PreparedWorkerOutcome) Binding() WorkerOutcomeBinding         { return p.binding }
func (p PreparedWorkerOutcome) Disposition() WorkerOutcomeDisposition { return p.disposition }
func (p PreparedWorkerOutcome) Digest() string                        { return p.digest }
func (p PreparedWorkerOutcome) Envelope() secureconfig.Envelope       { return p.payload.Clone() }
func (PreparedWorkerOutcome) String() string                          { return "prepared worker outcome" }
func (p PreparedWorkerOutcome) Format(w fmt.State, _ rune)            { _, _ = w.Write([]byte(p.String())) }
func (PreparedWorkerOutcome) MarshalJSON() ([]byte, error) {
	return nil, errors.New("prepared worker outcome cannot be serialized")
}

// PrepareWorkerOutcome validates supplemental data against the original immutable
// JobType version and encrypts the full report before durable submission. Current
// metadata, new versions and revoked new-work grants cannot redefine old results.
func (c *Catalog) PrepareWorkerOutcome(ctx context.Context, binding WorkerOutcomeBinding, input api.Outcome) (PreparedWorkerOutcome, error) {
	var out PreparedWorkerOutcome
	if ctx == nil || binding.validate() != nil {
		return out, ErrWorkerOutcomeInvalid
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := validateWorkerOutcomeEnvelope(binding, input); err != nil {
		return out, err
	}
	// Freeze caller-owned buffers before validation and sealing. Strings are
	// immutable; the two slices otherwise permit validation/sealing divergence.
	input.Data = slices.Clone(input.Data)
	defer clear(input.Data)
	input.Evidence = slices.Clone(input.Evidence)
	if err := c.readyContext(ctx); err != nil {
		return out, err
	}
	selected, ok, err := c.store.LookupJobTypeVersion(ctx, binding.JobType.JobTypeID, binding.JobType.Version)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, ErrUnavailable
	}
	if !ok {
		return out, persistence.ErrCatalogDependency
	}
	version := selected.Version
	if version.Record.UID != binding.JobType.JobTypeUID || version.Record.Revision != binding.JobType.Revision || version.Category != binding.JobType.Category {
		return out, persistence.ErrCatalogDependency
	}
	contract, err := c.openJobType(ctx, version)
	if err != nil {
		return out, err
	}
	disposition, encoded, err := validateWorkerOutcome(ctx, contract, binding, input)
	if err != nil {
		return out, err
	}
	defer clear(encoded)
	payload, err := c.sealer.Seal(ctx, binding.encryptionBinding(c.storeID), encoded)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	identity, _ := json.Marshal(binding)
	hash := sha256.New()
	_, _ = hash.Write([]byte("cpra/worker/outcome/v1\x00"))
	_, _ = hash.Write(identity)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return PreparedWorkerOutcome{binding: binding, disposition: disposition, digest: hex.EncodeToString(hash.Sum(nil)), payload: payload}, nil
}

func workerOutcomeText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validateWorkerOutcomeEnvelope(binding WorkerOutcomeBinding, input api.Outcome) error {
	if input.ServerID != binding.ServerID || input.WorkerUID != binding.WorkerUID || input.ExecutionID != binding.ExecutionID || input.GrantID != binding.GrantID || input.Kind != binding.JobType.Category ||
		len(input.Data) > workerOutcomeBytes || !utf8.Valid(input.Data) || len(input.Diagnostic) > 64<<10 || !utf8.ValidString(input.Diagnostic) ||
		input.Evidence == nil || len(input.Evidence) > 64 || !workerOutcomeText(input.RejectionCode, 128) || len(input.Status) > 16 {
		return ErrWorkerOutcomeInvalid
	}
	for _, evidence := range input.Evidence {
		if !workerOutcomeText(evidence, 4096) {
			return ErrWorkerOutcomeInvalid
		}
	}
	return nil
}

func validateWorkerOutcome(ctx context.Context, contract api.JobType, binding WorkerOutcomeBinding, input api.Outcome) (WorkerOutcomeDisposition, []byte, error) {
	var out WorkerOutcomeDisposition
	if ctx == nil || binding.validate() != nil || validateWorkerOutcomeEnvelope(binding, input) != nil ||
		contract.Metadata.ID != binding.JobType.JobTypeID || contract.Metadata.UID != binding.JobType.JobTypeUID || contract.Metadata.ResourceVersion != binding.JobType.Revision || contract.Spec.Version != binding.JobType.Version || contract.Spec.Kind != binding.JobType.Category {
		return out, nil, ErrWorkerOutcomeInvalid
	}
	if err := ctx.Err(); err != nil {
		return out, nil, err
	}
	out.Status = input.Status
	switch input.Kind {
	case "check":
		if input.Status != "success" && input.Status != "failure" && input.Status != "noData" {
			return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
		}
		out.Observation = input.Status != "noData"
	case "recovery", "notification":
		if input.Status != "accepted" && input.Status != "rejected" && input.Status != "unknown" &&
			!(input.Kind == "recovery" && input.Status == "completed") && !(input.Kind == "notification" && input.Status == "delivered") {
			return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
		}
	default:
		return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
	}
	if input.Status == "rejected" {
		if input.RejectionCode == "" || !slices.Contains(contract.Spec.RejectionCodes, input.RejectionCode) {
			return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
		}
		out.Retryable = true
	} else if input.RejectionCode != "" {
		return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
	}
	compiled, err := compileJobType(ctx, contract)
	if err != nil {
		return WorkerOutcomeDisposition{}, nil, err
	}
	data := input.Data
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	// Interrupted handlers have no supplemental result. They must still be able
	// to report unknown/noData when the handler's normal result schema is strict.
	absent := bytes.Equal(bytes.TrimSpace(data), []byte("null"))
	if !absent || input.Status != "unknown" && input.Status != "noData" {
		if err := compiled.results.validate(ctx, data); err != nil {
			return WorkerOutcomeDisposition{}, nil, err
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > workerOutcomeBytes {
		clear(encoded)
		return WorkerOutcomeDisposition{}, nil, ErrWorkerOutcomeInvalid
	}
	if err := ctx.Err(); err != nil {
		clear(encoded)
		return WorkerOutcomeDisposition{}, nil, err
	}
	return out, encoded, nil
}
