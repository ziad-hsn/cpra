//go:build externaljobs

package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// WorkerPolicyFormatVersion adds provisioned worker credentials and scopes, not
// assignments, start grants, or an external execution adapter.
const WorkerPolicyFormatVersion = 17
const workerPolicySnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-17\n"
const WorkerPolicyVersion = 1
const (
	MaxWorkers              = 1024
	MaxWorkerGrants         = 64
	MaxWorkerGrantResources = 64
	MaxWorkerPolicyBytes    = 16 << 20
)

var (
	ErrWorkerPolicyInvalid       = errors.New("invalid worker policy")
	ErrWorkerPolicyConflict      = errors.New("worker policy epoch or revision conflict")
	ErrWorkerPolicyQuota         = errors.New("worker policy quota exceeded")
	ErrWorkerPolicyAdminRequired = errors.New("worker policy changes require stopped local administration")
	ErrWorkerPolicyUnavailable   = errors.New("worker policy unavailable")
	ErrWorkerPolicyResetRequired = fmt.Errorf("%w: explicit worker provisioning required", ErrWorkerPolicyUnavailable)
	ErrWorkerUnauthorized        = errors.New("worker credential denied")
	ErrWorkerAuthorityDenied     = errors.New("worker scope denied")
)

type WorkerGrant struct {
	JobTypeID    string   `json:"job_type_id"`
	JobTypeUID   string   `json:"job_type_uid"`
	Version      string   `json:"version"`
	Category     string   `json:"category"`
	ResourceKind string   `json:"resource_kind"`
	ResourceIDs  []string `json:"resource_ids"`
}

func (g WorkerGrant) Clone() WorkerGrant { g.ResourceIDs = slices.Clone(g.ResourceIDs); return g }
func (g WorkerGrant) Validate() error {
	if !catalogIdentifier(g.JobTypeID, 256) || !catalogIdentifier(g.JobTypeUID, 256) || !jobTypeSelector(g.Version) || len(g.ResourceIDs) == 0 || len(g.ResourceIDs) > MaxWorkerGrantResources {
		return ErrWorkerPolicyInvalid
	}
	if (g.Category == "check" || g.Category == "recovery") && g.ResourceKind != "Monitor" || g.Category == "notification" && g.ResourceKind != "NotificationEndpoint" || g.Category != "check" && g.Category != "recovery" && g.Category != "notification" {
		return ErrWorkerPolicyInvalid
	}
	seen := make(map[string]bool, len(g.ResourceIDs))
	for _, id := range g.ResourceIDs {
		if !catalogIdentifier(id, 256) || seen[id] || strings.Contains(id, "*") && (id != "*" || len(g.ResourceIDs) != 1) {
			return ErrWorkerPolicyInvalid
		}
		seen[id] = true
	}
	return nil
}

type WorkerPrincipal struct {
	ID                 string `json:"id"`
	UID                string `json:"uid"`
	CredentialRevision string `json:"credential_revision"`
	GrantRevision      string `json:"grant_revision"`
	TokenSHA256        string `json:"token_sha256"`
	// RestoredTokenSHA256 blocks reuse of the immediately restored verifier in
	// this epoch. It is not an unbounded archive of historical credentials.
	RestoredTokenSHA256 string        `json:"restored_token_sha256,omitempty"`
	Revoked             bool          `json:"revoked,omitempty"`
	ExpiresAt           time.Time     `json:"expires_at,omitempty"`
	ResetRequired       bool          `json:"reset_required,omitempty"`
	Grants              []WorkerGrant `json:"grants,omitempty"`
}

func (p WorkerPrincipal) Clone() WorkerPrincipal {
	p.Grants = slices.Clone(p.Grants)
	for n := range p.Grants {
		p.Grants[n] = p.Grants[n].Clone()
	}
	return p
}
func (p WorkerPrincipal) Validate() error {
	if !validAuthenticationID(p.ID) || !validAuthenticationID(p.UID) || !validAuthenticationID(p.CredentialRevision) || !validAuthenticationID(p.GrantRevision) || !validVerifier(p.TokenSHA256) || p.RestoredTokenSHA256 != "" && !validVerifier(p.RestoredTokenSHA256) || len(p.Grants) > MaxWorkerGrants || !p.ExpiresAt.IsZero() && (p.ExpiresAt.Year() < 1 || p.ExpiresAt.Year() > 9999) || p.ResetRequired && len(p.Grants) != 0 {
		return ErrWorkerPolicyInvalid
	}
	seen := make(map[string]bool, len(p.Grants))
	for _, g := range p.Grants {
		if g.Validate() != nil {
			return ErrWorkerPolicyInvalid
		}
		key := g.JobTypeID + "\x00" + g.JobTypeUID + "\x00" + g.Version + "\x00" + g.Category + "\x00" + g.ResourceKind
		if seen[key] {
			return ErrWorkerPolicyInvalid
		}
		seen[key] = true
	}
	if !p.ResetRequired && p.RestoredTokenSHA256 != "" && workerVerifier(p.TokenSHA256) == workerVerifier(p.RestoredTokenSHA256) {
		return ErrWorkerPolicyInvalid
	}
	return nil
}

type WorkerPolicyState struct {
	Version       int                        `json:"version"`
	Epoch         string                     `json:"epoch"`
	Revision      string                     `json:"revision"`
	ResetRequired bool                       `json:"reset_required,omitempty"`
	RestoreID     string                     `json:"restore_id,omitempty"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	CommandDigest string                     `json:"command_digest"`
	Workers       map[string]WorkerPrincipal `json:"workers"`
	EncodedBytes  int64                      `json:"encoded_bytes"`
}

func (s WorkerPolicyState) Clone() WorkerPolicyState {
	workers := make(map[string]WorkerPrincipal, len(s.Workers))
	for id, p := range s.Workers {
		workers[id] = p.Clone()
	}
	s.Workers = workers
	return s
}

type WorkerPolicyCommand struct {
	Mode             string            `json:"mode"`
	ExpectedEpoch    string            `json:"expected_epoch,omitempty"`
	ExpectedRevision string            `json:"expected_revision,omitempty"`
	Epoch            string            `json:"epoch"`
	Revision         string            `json:"revision"`
	Actor            string            `json:"actor"`
	At               time.Time         `json:"at"`
	Authority        OperatorAuthority `json:"authority"`
	Worker           WorkerPrincipal   `json:"worker"`
}

// Validate checks bounded command shape before local tooling publishes a token.
// Current authority, retained identities, grant references and quotas are also
// checked inside Apply; this method grants no permission by itself.
func (c WorkerPolicyCommand) Validate() error {
	if !validAuthenticationID(c.Epoch) || !validAuthenticationID(c.Revision) || !validAuthenticationID(c.Actor) || c.At.IsZero() || c.At.Year() < 1 || c.At.Year() > 9999 || c.Authority.validate() != nil || c.Actor != c.Authority.Actor || c.Worker.Validate() != nil {
		return ErrWorkerPolicyInvalid
	}
	switch c.Mode {
	case "bootstrap":
		if c.ExpectedEpoch != "" || c.ExpectedRevision != "" || c.Worker.ResetRequired || c.Worker.RestoredTokenSHA256 != "" {
			return ErrWorkerPolicyInvalid
		}
	case "upsert", "provision":
		if c.ExpectedEpoch != c.Epoch || !validAuthenticationID(c.ExpectedRevision) || c.ExpectedRevision == c.Revision || c.Mode == "provision" && (c.Worker.ResetRequired || c.Worker.Revoked) {
			return ErrWorkerPolicyInvalid
		}
	default:
		return ErrWorkerPolicyInvalid
	}
	if _, err := encodedBound(c); err != nil {
		return ErrWorkerPolicyQuota
	}
	return nil
}
func (c WorkerPolicyCommand) digest() string {
	raw, _ := json.Marshal(c)
	return identity("worker-policy-command/v1/" + string(raw))
}
func workerVerifier(value string) (out [32]byte) {
	_, _ = hex.Decode(out[:], []byte(value))
	return out
}

// workerPolicyCost counts one canonical map entry at a time, including framing,
// so neither Apply nor recovery creates a second whole-policy encoding.
func workerPolicyCost(state WorkerPolicyState) (int64, error) {
	workers := state.Workers
	if len(workers) > MaxWorkers {
		return 0, ErrWorkerPolicyQuota
	}
	used := int64(2)
	first := true
	for id, p := range workers {
		key, _ := json.Marshal(id)
		raw, err := json.Marshal(p)
		if err != nil {
			return 0, ErrWorkerPolicyInvalid
		}
		cost := int64(len(key) + 1 + len(raw))
		if !first {
			cost++
		}
		first = false
		if cost > MaxWorkerPolicyBytes-used {
			return 0, ErrWorkerPolicyQuota
		}
		used += cost
	}
	metadata := state
	metadata.Workers = nil
	metadata.EncodedBytes = 0
	raw, err := json.Marshal(metadata)
	if err != nil {
		return 0, ErrWorkerPolicyInvalid
	}
	base := used + int64(len(raw)) - 4
	total := base
	for {
		next := base + int64(len(strconv.FormatInt(total, 10))) - 1
		if next == total {
			break
		}
		total = next
	}
	if total > MaxWorkerPolicyBytes {
		return 0, ErrWorkerPolicyQuota
	}
	return total, nil
}
func workerCredentialsEqual(a, b WorkerPrincipal) bool {
	return workerVerifier(a.TokenSHA256) == workerVerifier(b.TokenSHA256) && a.Revoked == b.Revoked && a.ExpiresAt.Equal(b.ExpiresAt)
}
func workerGrantsEqual(a, b []WorkerGrant) bool {
	return reflect.DeepEqual(a, b) || len(a) == 0 && len(b) == 0
}

// WorkerPolicy returns a detached bounded policy for stopped provisioning and
// private authentication. Callers must never expose its verifiers in reports.
func (s *Store) WorkerPolicy(ctx context.Context) (WorkerPolicyState, error) {
	var out WorkerPolicyState
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		if policy := workerPolicyForProvision(f.image); policy != nil {
			out = policy.Clone()
		}
		return nil
	})
	if err != nil {
		return WorkerPolicyState{}, workerReadError(err)
	}
	return out, nil
}
func (s *Store) CommitWorkerPolicy(ctx context.Context, c WorkerPolicyCommand) (WorkerPolicyState, error) {
	if !s.administrative {
		return WorkerPolicyState{}, ErrWorkerPolicyAdminRequired
	}
	if err := c.Validate(); err != nil {
		return WorkerPolicyState{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_policy", At: c.At, commandExtensions: commandExtensions{WorkerPolicy: &c}}})
	if err != nil {
		return WorkerPolicyState{}, err
	}
	if len(results) != 1 {
		return WorkerPolicyState{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerPolicyUnavailable)
	}
	if results[0].Err != nil {
		return WorkerPolicyState{}, results[0].Err
	}
	if !results[0].Allowed || results[0].WorkerPolicy == nil {
		return WorkerPolicyState{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerPolicyUnavailable)
	}
	return *results[0].WorkerPolicy, nil
}
