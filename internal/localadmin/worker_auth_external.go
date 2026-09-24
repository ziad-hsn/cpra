//go:build externaljobs

package localadmin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

var (
	ErrWorkerAuthenticationInput = errors.New("invalid local worker authentication request")
	ErrWorkerAuthenticationState = errors.New("worker authentication operation is not valid for the current policy")
)

const maxWorkerGrantFileBytes = 1 << 20

// WorkerAuthenticationRequest selects stopped local administration. GrantsFile
// is required for issue, reprovision and set-grants, including an empty grant set.
// TokenOutput is a new protected file; existing tokens are never accepted.
type WorkerAuthenticationRequest struct {
	Action, DataDirectory, Actor, WorkerID, GrantsFile, TokenOutput string
	ExpiresAt                                                       *time.Time
}

type WorkerAuthenticationPrincipalReport struct {
	ID                 string                    `json:"id"`
	UID                string                    `json:"uid"`
	CredentialRevision string                    `json:"credentialRevision"`
	GrantRevision      string                    `json:"grantRevision"`
	Revoked            bool                      `json:"revoked"`
	ResetRequired      bool                      `json:"resetRequired"`
	ExpiresAt          *time.Time                `json:"expiresAt,omitempty"`
	Grants             []persistence.WorkerGrant `json:"grants"`
}

// WorkerAuthenticationReport excludes active and restored token verifiers.
// An unconfirmed commit retains its intended revision and original token file.
type WorkerAuthenticationReport struct {
	ProtocolServerID string                                `json:"protocolServerID,omitempty"`
	Epoch            string                                `json:"epoch,omitempty"`
	Revision         string                                `json:"revision,omitempty"`
	IntendedRevision string                                `json:"intendedRevision,omitempty"`
	ResetRequired    bool                                  `json:"resetRequired"`
	UpdatedAt        *time.Time                            `json:"updatedAt,omitempty"`
	Workers          []WorkerAuthenticationPrincipalReport `json:"workers"`
	Outcome          string                                `json:"outcome"`
	TokenOutput      string                                `json:"tokenOutput,omitempty"`
}

type workerAuthenticationStore interface {
	WorkerProtocolIdentity(context.Context) (persistence.WorkerServerIdentity, error)
	ObserveOperatorAuthority(context.Context, string, time.Time) (persistence.OperatorAuthority, error)
	WorkerPolicy(context.Context) (persistence.WorkerPolicyState, error)
	CommitWorkerPolicy(context.Context, persistence.WorkerPolicyCommand) (persistence.WorkerPolicyState, error)
	Close() error
}

type workerAuthenticationOpener func(context.Context, runtimeconfig.Config) (workerAuthenticationStore, error)

// AdministerWorkerAuthentication exclusively opens an existing stopped store.
// It neither bootstraps management authority nor starts providers or workers.
func AdministerWorkerAuthentication(ctx context.Context, request WorkerAuthenticationRequest) (WorkerAuthenticationReport, error) {
	return administerWorkerAuthentication(ctx, request, func(ctx context.Context, config runtimeconfig.Config) (workerAuthenticationStore, error) {
		if err := requireExistingAuthenticationStore(config.Storage.Directory); err != nil {
			return nil, err
		}
		return persistence.OpenAdministrative(ctx, config)
	})
}

func administerWorkerAuthentication(ctx context.Context, request WorkerAuthenticationRequest, open workerAuthenticationOpener) (report WorkerAuthenticationReport, err error) {
	if ctx == nil {
		return report, ErrWorkerAuthenticationInput
	}
	if err := ctx.Err(); err != nil {
		return report, &authenticationError{"local worker authentication request canceled", err}
	}
	if err := validateWorkerAuthenticationRequest(request, time.Now().UTC()); err != nil {
		return report, err
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = filepath.Clean(request.DataDirectory)
	store, err := open(ctx, config)
	if err != nil {
		return report, &authenticationError{"cannot administer workers: stop the current owner and verify the existing state directory", err}
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, &authenticationError{"worker policy store did not close cleanly; inspect the reported revision before another action", closeErr})
		}
	}()
	at := time.Now().UTC()
	authority, err := store.ObserveOperatorAuthority(ctx, request.Actor, at)
	if err != nil {
		return report, &authenticationError{"worker administration requires the selected current named operator; provision management authority first if needed", err}
	}
	current, err := store.WorkerPolicy(ctx)
	if err != nil {
		return report, &authenticationError{"cannot read the current worker policy", err}
	}
	report = workerAuthenticationReport(current)
	if current.Epoch != "" {
		identity, identityErr := store.WorkerProtocolIdentity(ctx)
		if identityErr != nil {
			return report, &authenticationError{"cannot read worker protocol server identity", identityErr}
		}
		report.ProtocolServerID = identity.ServerID
	}
	if request.Action == "list" {
		report.Outcome = "observed"
		return report, nil
	}
	var grants []persistence.WorkerGrant
	if request.GrantsFile != "" {
		grants, err = readWorkerGrants(ctx, request.GrantsFile, config.Storage.Directory)
		if err != nil {
			return report, err
		}
	}
	command, err := prepareWorkerAuthenticationCommand(current, request, grants, authority, at)
	if err != nil {
		return report, err
	}
	report.IntendedRevision = command.Revision
	var output []byte
	if request.TokenOutput != "" {
		var entropy [32]byte
		defer clear(entropy[:])
		if _, err := rand.Read(entropy[:]); err != nil {
			return report, &authenticationError{"could not generate worker authentication token", err}
		}
		bearer := make([]byte, base64.RawURLEncoding.EncodedLen(len(entropy)))
		defer clear(bearer)
		base64.RawURLEncoding.Encode(bearer, entropy[:])
		digest := sha256.Sum256(bearer)
		command.Worker.TokenSHA256 = hex.EncodeToString(digest[:])
		output = make([]byte, len(bearer)+1)
		defer clear(output)
		copy(output, bearer)
		output[len(bearer)] = '\n'
	}
	if err := command.Validate(); err != nil {
		return report, &authenticationError{"invalid worker policy or grant selection; verifier was not submitted", err}
	}
	if output != nil {
		if err := secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: request.TokenOutput, DataDirectory: config.Storage.Directory, MaxBytes: 128}, output); err != nil {
			report.Outcome = "not_submitted"
			return report, &authenticationError{"worker token publication failed; verifier was not submitted; existing output is never overwritten", err}
		}
		report.TokenOutput = filepath.Clean(request.TokenOutput)
	}
	// Apply rechecks the original authority fence at this fresh observation time.
	// Slow grant/token I/O must not preserve an operator's expired eligibility.
	command.At = time.Now().UTC()
	updated, err := store.CommitWorkerPolicy(ctx, command)
	if err != nil {
		report.Outcome = "not_committed"
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			report.Outcome = "unconfirmed"
			return report, &authenticationError{"worker policy commit is unconfirmed; retain the token file and inspect the intended revision with local worker-auth list; do not repeat issuance", err}
		}
		return report, &authenticationError{"worker policy change was rejected; retain any token file and inspect the current policy before another action", err}
	}
	outputPath, intended := report.TokenOutput, report.IntendedRevision
	report = workerAuthenticationReport(updated)
	report.TokenOutput, report.IntendedRevision, report.Outcome = outputPath, intended, "committed"
	identity, identityErr := store.WorkerProtocolIdentity(ctx)
	if identityErr != nil {
		return report, &authenticationError{"worker policy committed but protocol server identity is unavailable; inspect local worker-auth list before configuring a worker", identityErr}
	}
	report.ProtocolServerID = identity.ServerID
	return report, nil
}

func validateWorkerAuthenticationRequest(r WorkerAuthenticationRequest, at time.Time) error {
	invalid := func(message string) error { return &authenticationError{message, ErrWorkerAuthenticationInput} }
	if !filepath.IsAbs(r.DataDirectory) || strings.ContainsAny(r.DataDirectory, "\x00\r\n") || rejectSymlinkAncestors(r.DataDirectory) != nil {
		return invalid("--data-dir must select an explicit absolute directory without symbolic links")
	}
	if info, err := os.Lstat(r.DataDirectory); err != nil || !info.IsDir() {
		return invalid("--data-dir must select an existing stopped state directory")
	}
	if !validAuthenticationPrincipal(r.Actor) || r.Actor == "legacy-read" {
		return invalid("--actor must explicitly identify a current named operator")
	}
	if r.Action == "list" {
		if r.WorkerID != "" || r.GrantsFile != "" || r.TokenOutput != "" || r.ExpiresAt != nil {
			return ErrWorkerAuthenticationInput
		}
		return nil
	}
	if !validAuthenticationPrincipal(r.WorkerID) {
		return invalid("worker ID must contain 1..128 printable UTF-8 bytes without whitespace")
	}
	needsGrants, needsToken := false, false
	switch r.Action {
	case "issue", "reprovision":
		needsGrants, needsToken = true, true
	case "rotate":
		needsToken = true
	case "set-grants":
		needsGrants = true
	case "revoke":
	default:
		return ErrWorkerAuthenticationInput
	}
	if needsGrants != (r.GrantsFile != "") || needsToken != (r.TokenOutput != "") || !needsToken && r.ExpiresAt != nil {
		return invalid("action requires its explicit grants and token output flags; unrelated flags are not accepted")
	}
	if needsGrants && (!filepath.IsAbs(r.GrantsFile) || strings.ContainsAny(r.GrantsFile, "\x00\r\n") || rejectSymlinkAncestors(r.GrantsFile) != nil) {
		return invalid("--grants-file must select an absolute protected JSON file outside state without symbolic links")
	}
	if r.ExpiresAt != nil && !r.ExpiresAt.IsZero() && (!r.ExpiresAt.After(at) || r.ExpiresAt.Year() > 9999) {
		return invalid("explicit expiry must be in the future")
	}
	if needsToken {
		if !filepath.IsAbs(r.TokenOutput) || strings.ContainsAny(r.TokenOutput, "\x00\r\n") || rejectSymlinkAncestors(r.TokenOutput) != nil {
			return invalid("--token-output must select a new absolute protected file outside state without symbolic links")
		}
		if _, err := os.Lstat(r.TokenOutput); !errors.Is(err, os.ErrNotExist) {
			return invalid("worker token output already exists or cannot be inspected; choose an absent path")
		}
	}
	return nil
}

func prepareWorkerAuthenticationCommand(current persistence.WorkerPolicyState, request WorkerAuthenticationRequest, grants []persistence.WorkerGrant, authority persistence.OperatorAuthority, at time.Time) (persistence.WorkerPolicyCommand, error) {
	command := persistence.WorkerPolicyCommand{Mode: "upsert", ExpectedEpoch: current.Epoch, ExpectedRevision: current.Revision, Epoch: current.Epoch, Revision: uuid.NewString(), Actor: request.Actor, Authority: authority, At: at}
	invalid := func(message string) (persistence.WorkerPolicyCommand, error) {
		return command, &authenticationError{message, ErrWorkerAuthenticationState}
	}
	previous, exists := current.Workers[request.WorkerID]
	if exists && previous.Revoked {
		return invalid("worker identity is revoked; it cannot be rotated, reprovisioned or reused")
	}
	switch request.Action {
	case "issue":
		if exists || current.ResetRequired || current.RestoreID != "" {
			return invalid("issue requires an absent worker in an unrestored policy; restored state requires explicit reprovision")
		}
		if current.Epoch == "" {
			command.Mode, command.Epoch = "bootstrap", uuid.NewString()
		}
	case "reprovision":
		if current.RestoreID == "" || current.Epoch == "" || exists && !previous.ResetRequired {
			return invalid("reprovision requires restored state and a new or reset worker identity")
		}
		command.Mode = "provision"
	case "rotate", "set-grants":
		if !exists || current.ResetRequired || previous.ResetRequired {
			return invalid("worker is absent or requires explicit post-restore reprovisioning")
		}
	case "revoke":
		if !exists {
			return invalid("worker does not exist")
		}
	default:
		return invalid("unsupported worker policy action")
	}
	if !exists {
		if len(current.Workers) >= persistence.MaxWorkers {
			return invalid("worker policy has reached its identity limit")
		}
		command.Worker = persistence.WorkerPrincipal{ID: request.WorkerID, UID: uuid.NewString(), CredentialRevision: uuid.NewString(), GrantRevision: uuid.NewString()}
	} else {
		command.Worker = previous.Clone()
	}
	if request.Action == "issue" || request.Action == "reprovision" || request.Action == "set-grants" {
		command.Worker.Grants = grants
		if request.Action != "set-grants" || !(len(previous.Grants) == 0 && len(grants) == 0 || reflect.DeepEqual(previous.Grants, grants)) {
			command.Worker.GrantRevision = uuid.NewString()
		}
	}
	if request.Action == "rotate" || request.Action == "reprovision" || request.Action == "revoke" {
		command.Worker.CredentialRevision = uuid.NewString()
	}
	if request.Action == "reprovision" {
		command.Worker.ResetRequired = false
	}
	if request.Action == "revoke" {
		command.Worker.Revoked = true
	}
	if request.ExpiresAt != nil {
		command.Worker.ExpiresAt = request.ExpiresAt.UTC()
	}
	return command, nil
}

func readWorkerGrants(ctx context.Context, path, directory string) ([]persistence.WorkerGrant, error) {
	raw, err := secureconfig.ReadProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: path, DataDirectory: directory, MaxBytes: maxWorkerGrantFileBytes})
	if err != nil {
		return nil, &authenticationError{"cannot read protected worker grants file", err}
	}
	defer clear(raw)
	invalid := func() ([]persistence.WorkerGrant, error) {
		return nil, &authenticationError{"worker grants require one bounded JSON object with an explicit grants array, known fields and no duplicate keys", ErrWorkerAuthenticationInput}
	}
	if !utf8.Valid(raw) || !workerGrantJSONShape(raw) {
		return invalid()
	}
	var input struct {
		Grants *[]persistence.WorkerGrant `json:"grants"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Grants == nil || len(*input.Grants) > persistence.MaxWorkerGrants {
		return invalid()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return invalid()
	}
	if err := ctx.Err(); err != nil {
		return nil, &authenticationError{"worker grant loading canceled", err}
	}
	return *input.Grants, nil
}

// The document is byte-bounded before this structural pass. Limit nesting and
// reject duplicate keys before the typed decoder can discard earlier values.
func workerGrantJSONShape(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) bool
	read = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delimiter {
		case '{':
			if depth != 0 && depth != 2 {
				return false
			}
			seen := make(map[string]bool)
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				known := depth == 0 && name == "grants" || depth == 2 && (name == "job_type_id" || name == "job_type_uid" || name == "version" || name == "category" || name == "resource_kind" || name == "resource_ids")
				if err != nil || !ok || !known || seen[name] {
					return false
				}
				seen[name] = true
				if !read(depth + 1) {
					return false
				}
			}
		case '[':
			limit := persistence.MaxWorkerGrants
			if depth == 3 {
				limit = persistence.MaxWorkerGrantResources
			} else if depth != 1 {
				return false
			}
			count := 0
			for decoder.More() {
				count++
				if count > limit || !read(depth+1) {
					return false
				}
			}
		default:
			return false
		}
		end, err := decoder.Token()
		return err == nil && (delimiter == '{' && end == json.Delim('}') || delimiter == '[' && end == json.Delim(']'))
	}
	if !read(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func workerAuthenticationReport(state persistence.WorkerPolicyState) WorkerAuthenticationReport {
	report := WorkerAuthenticationReport{Epoch: state.Epoch, Revision: state.Revision, ResetRequired: state.ResetRequired, Workers: make([]WorkerAuthenticationPrincipalReport, 0, len(state.Workers))}
	if !state.UpdatedAt.IsZero() {
		at := state.UpdatedAt
		report.UpdatedAt = &at
	}
	for _, worker := range state.Workers {
		worker = worker.Clone()
		item := WorkerAuthenticationPrincipalReport{ID: worker.ID, UID: worker.UID, CredentialRevision: worker.CredentialRevision, GrantRevision: worker.GrantRevision, Revoked: worker.Revoked, ResetRequired: worker.ResetRequired, Grants: worker.Grants}
		if item.Grants == nil {
			item.Grants = []persistence.WorkerGrant{}
		}
		if !worker.ExpiresAt.IsZero() {
			at := worker.ExpiresAt
			item.ExpiresAt = &at
		}
		report.Workers = append(report.Workers, item)
	}
	sort.Slice(report.Workers, func(i, j int) bool { return report.Workers[i].ID < report.Workers[j].ID })
	return report
}
