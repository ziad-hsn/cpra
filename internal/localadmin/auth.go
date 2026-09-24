package localadmin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

var (
	ErrAuthenticationInput = errors.New("invalid local authentication request")
	ErrAuthenticationState = errors.New("local authentication operation is not valid for the current policy")
)

// AuthenticationRequest is stopped local administration, not an HTTP request.
// Rotate preserves the existing role and expiry; a non-nil ExpiresAt explicitly
// changes expiry, with the zero time removing it. TokenOutput is always a new,
// protected file outside DataDirectory. No command accepts an existing token.
type AuthenticationRequest struct {
	Action, DataDirectory, PrincipalID, Role, TokenOutput string
	ExpiresAt                                             *time.Time
}

// AuthenticationPrincipalReport omits token verifiers as well as bearer values.
type AuthenticationPrincipalReport struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	Revoked   bool       `json:"revoked"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// AuthenticationReport can safely be displayed after success or an uncertain
// commit. IntendedRevision identifies the original attempt; the retained token
// file must not be regenerated merely because its commit reply was lost.
type AuthenticationReport struct {
	Epoch             string                          `json:"epoch,omitempty"`
	Revision          string                          `json:"revision,omitempty"`
	IntendedRevision  string                          `json:"intendedRevision,omitempty"`
	BootstrapConsumed bool                            `json:"bootstrapConsumed"`
	ResetRequired     bool                            `json:"resetRequired"`
	AnonymousLoopback bool                            `json:"anonymousLoopback"`
	LegacyReadEnabled bool                            `json:"legacyReadEnabled"`
	UpdatedAt         *time.Time                      `json:"updatedAt,omitempty"`
	Principals        []AuthenticationPrincipalReport `json:"principals"`
	Outcome           string                          `json:"outcome"`
	TokenOutput       string                          `json:"tokenOutput,omitempty"`
}

// authenticationError retains errors.Is/As without printing paths, filesystem
// errors or durable payloads. The report separately carries permitted metadata.
type authenticationError struct {
	message string
	cause   error
}

func (e *authenticationError) Error() string { return e.message }
func (e *authenticationError) Unwrap() error { return e.cause }

type authenticationStore interface {
	Authentication() (persistence.AuthenticationState, error)
	CommitAuthentication(context.Context, persistence.AuthenticationCommand) (persistence.AuthenticationState, error)
	Close() error
}

type authenticationOpener func(context.Context, runtimeconfig.Config) (authenticationStore, error)

// AdministerAuthentication exclusively opens a stopped local store without
// loading a monitor configuration, decryption backend, controller or provider.
func AdministerAuthentication(ctx context.Context, request AuthenticationRequest) (AuthenticationReport, error) {
	return administerAuthentication(ctx, request, func(ctx context.Context, config runtimeconfig.Config) (authenticationStore, error) {
		if request.Action != "bootstrap" {
			if err := requireExistingAuthenticationStore(config.Storage.Directory); err != nil {
				return nil, err
			}
		}
		return persistence.OpenAdministrative(ctx, config)
	})
}

// This bounded preflight prevents a read or a rejected individual mutation from
// initializing an empty/incomplete store. Raft still validates contents under
// its exclusive lock. Do not use LockOffline here: explicit reprovisioning must
// be able to finish a valid pending restore reset.
func requireExistingAuthenticationStore(directory string) error {
	for _, name := range []string{"identity.json", "raft.db", "history/catalog.json", "history", "snapshots"} {
		path := filepath.Join(directory, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return &authenticationError{"authentication requires an existing complete store; only explicit bootstrap may initialize state", ErrAuthenticationState}
		}
		isDirectory := name == "history" || name == "snapshots"
		if (isDirectory && !info.IsDir()) || (!isDirectory && !info.Mode().IsRegular()) {
			return &authenticationError{"authentication state must contain real directories and regular files without links", ErrAuthenticationState}
		}
	}
	return nil
}

func administerAuthentication(ctx context.Context, request AuthenticationRequest, open authenticationOpener) (report AuthenticationReport, err error) {
	if ctx == nil {
		return report, ErrAuthenticationInput
	}
	if err := ctx.Err(); err != nil {
		return report, &authenticationError{"local authentication request canceled", err}
	}
	at := time.Now().UTC()
	if err := validateAuthenticationRequest(request, at); err != nil {
		return report, err
	}
	config := runtimeconfig.Default()
	config.Storage.Mode, config.Storage.Directory = "raft", filepath.Clean(request.DataDirectory)
	store, err := open(ctx, config)
	if err != nil {
		return report, &authenticationError{"cannot administer authentication: stop the current owner and verify the selected state directory", err}
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, &authenticationError{"authentication store did not close cleanly; inspect the reported revision before another action", closeErr})
		}
	}()
	current, err := store.Authentication()
	if err != nil {
		return report, &authenticationError{"cannot read the current authentication policy", err}
	}
	report = authenticationReport(current)
	if request.Action == "list" {
		report.Outcome = "observed"
		return report, nil
	}
	command, index, err := prepareAuthenticationCommand(current, request, at)
	if err != nil {
		return report, err
	}
	report.IntendedRevision = command.Revision
	if request.Action != "revoke" {
		var entropy [32]byte
		defer clear(entropy[:])
		if _, err := rand.Read(entropy[:]); err != nil {
			return report, &authenticationError{"could not generate authentication token", err}
		}
		bearer := make([]byte, base64.RawURLEncoding.EncodedLen(len(entropy)))
		defer clear(bearer)
		base64.RawURLEncoding.Encode(bearer, entropy[:])
		digest := sha256.Sum256(bearer)
		command.Principals[index].TokenSHA256 = hex.EncodeToString(digest[:])
		output := make([]byte, len(bearer)+1)
		defer clear(output)
		copy(output, bearer)
		output[len(bearer)] = '\n'
		if err := secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: request.TokenOutput, DataDirectory: config.Storage.Directory, MaxBytes: 128}, output); err != nil {
			report.Outcome = "not_submitted"
			return report, &authenticationError{"token publication failed; verifier was not submitted; an existing output is never overwritten", err}
		}
		report.TokenOutput = filepath.Clean(request.TokenOutput)
	}
	// The token file has already been durably published. A failed commit keeps
	// that exact private file, including on cancellation or uncertain admission.
	updated, err := store.CommitAuthentication(ctx, command)
	if err != nil {
		report.Outcome = "not_committed"
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			report.Outcome = "unconfirmed"
			return report, &authenticationError{"authentication commit is unconfirmed; retain the token file and inspect the intended revision with local auth list; do not repeat issuance", err}
		}
		return report, &authenticationError{"authentication change was rejected; retain any token file and inspect the current policy before another action", err}
	}
	output, intended := report.TokenOutput, report.IntendedRevision
	report = authenticationReport(updated)
	report.TokenOutput, report.IntendedRevision, report.Outcome = output, intended, "committed"
	return report, nil
}

func validateAuthenticationRequest(r AuthenticationRequest, at time.Time) error {
	if !filepath.IsAbs(r.DataDirectory) || strings.ContainsAny(r.DataDirectory, "\x00\r\n") || rejectSymlinkAncestors(r.DataDirectory) != nil {
		return &authenticationError{"--data-dir must identify an explicit absolute directory without symbolic links", ErrAuthenticationInput}
	}
	info, err := os.Lstat(r.DataDirectory)
	if err != nil || !info.IsDir() {
		return &authenticationError{"--data-dir must select an existing stopped state directory", ErrAuthenticationInput}
	}
	if r.Action == "list" {
		if r.PrincipalID != "" || r.Role != "" || r.TokenOutput != "" || r.ExpiresAt != nil {
			return ErrAuthenticationInput
		}
		return nil
	}
	if !validAuthenticationPrincipal(r.PrincipalID) {
		return &authenticationError{"principal ID must contain 1..128 printable UTF-8 bytes without whitespace", ErrAuthenticationInput}
	}
	if r.PrincipalID == "legacy-read" && r.Action != "revoke" {
		return &authenticationError{"legacy-read is reserved; issue a named identity or explicitly revoke the legacy credential", ErrAuthenticationInput}
	}
	switch r.Action {
	case "bootstrap", "issue", "reprovision":
		if r.Role != "reader" && r.Role != "operator" {
			return &authenticationError{"--role must explicitly select reader or operator", ErrAuthenticationInput}
		}
	case "rotate":
		if r.Role != "" {
			return &authenticationError{"rotation preserves the principal role", ErrAuthenticationInput}
		}
	case "revoke":
		if r.Role != "" || r.TokenOutput != "" || r.ExpiresAt != nil {
			return ErrAuthenticationInput
		}
		return nil
	default:
		return ErrAuthenticationInput
	}
	if r.ExpiresAt != nil && !r.ExpiresAt.IsZero() && (!r.ExpiresAt.After(at) || r.ExpiresAt.Year() > 9999) {
		return &authenticationError{"explicit expiry must be in the future", ErrAuthenticationInput}
	}
	if !filepath.IsAbs(r.TokenOutput) || strings.ContainsAny(r.TokenOutput, "\x00\r\n") || rejectSymlinkAncestors(r.TokenOutput) != nil {
		return &authenticationError{"--token-output must select a new absolute protected file outside state without symbolic links", ErrAuthenticationInput}
	}
	if _, err := os.Lstat(r.TokenOutput); !errors.Is(err, os.ErrNotExist) {
		return &authenticationError{"token output already exists or cannot be inspected; choose an absent path", ErrAuthenticationInput}
	}
	return nil
}

func validAuthenticationPrincipal(id string) bool {
	return len(id) > 0 && len(id) <= 128 && utf8.ValidString(id) && strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

func prepareAuthenticationCommand(current persistence.AuthenticationState, request AuthenticationRequest, at time.Time) (persistence.AuthenticationCommand, int, error) {
	command := persistence.AuthenticationCommand{Version: persistence.AuthenticationLifecycleFormatVersion, Mode: "replace", ExpectedEpoch: current.Epoch, ExpectedRevision: current.Revision, Epoch: current.Epoch, Revision: uuid.NewString(), Actor: "local-administrator", At: at, Principals: append([]persistence.AuthenticationPrincipal(nil), current.Principals...), LegacyTokenSHA256: current.LegacyTokenSHA256}
	if len(command.Principals) > persistence.MaxAuthenticationPrincipals {
		return command, -1, ErrAuthenticationState
	}
	if request.Action == "bootstrap" || request.Action == "reprovision" {
		if request.Action == "bootstrap" {
			if current.BootstrapConsumed || current.Epoch != "" || current.ResetRequired {
				return command, -1, &authenticationError{"bootstrap is only valid for a never-initialized policy; use issue or explicit post-restore reprovisioning", ErrAuthenticationState}
			}
			command.Mode, command.Epoch = "bootstrap", uuid.NewString()
		} else {
			if !current.ResetRequired || current.Epoch == "" {
				return command, -1, &authenticationError{"reprovision requires a restored store awaiting its explicit current-epoch provisioning", ErrAuthenticationState}
			}
			command.Mode = "provision"
		}
		command.Principals = []persistence.AuthenticationPrincipal{{ID: request.PrincipalID, Role: request.Role}}
		command.LegacyTokenSHA256 = ""
		if request.ExpiresAt != nil {
			command.Principals[0].ExpiresAt = request.ExpiresAt.UTC()
		}
		return command, 0, nil
	}
	if !current.BootstrapConsumed || current.Epoch == "" || current.ResetRequired {
		return command, -1, &authenticationError{"policy needs explicit bootstrap or post-restore reprovisioning before individual changes", ErrAuthenticationState}
	}
	if request.PrincipalID == "legacy-read" {
		if request.Action != "revoke" || current.LegacyTokenSHA256 == "" {
			return command, -1, &authenticationError{"legacy read credential is absent or already revoked", ErrAuthenticationState}
		}
		command.LegacyTokenSHA256 = ""
		return command, -1, nil
	}
	index := -1
	for i := range command.Principals {
		if command.Principals[i].ID == request.PrincipalID {
			index = i
			break
		}
	}
	if request.Action == "issue" {
		if index >= 0 || len(command.Principals) >= persistence.MaxAuthenticationPrincipals {
			return command, -1, &authenticationError{"principal already exists or the policy has reached its principal limit", ErrAuthenticationState}
		}
		index = len(command.Principals)
		command.Principals = append(command.Principals, persistence.AuthenticationPrincipal{ID: request.PrincipalID, Role: request.Role})
	} else if index < 0 {
		return command, -1, &authenticationError{"principal does not exist", ErrAuthenticationState}
	} else if command.Principals[index].Revoked {
		return command, -1, &authenticationError{"principal is already revoked; choose an explicit new principal identity", ErrAuthenticationState}
	}
	if request.Action == "revoke" {
		command.Principals[index].Revoked = true
	} else if request.ExpiresAt != nil {
		command.Principals[index].ExpiresAt = request.ExpiresAt.UTC()
	}
	return command, index, nil
}

func authenticationReport(state persistence.AuthenticationState) AuthenticationReport {
	report := AuthenticationReport{Epoch: state.Epoch, Revision: state.Revision, BootstrapConsumed: state.BootstrapConsumed, ResetRequired: state.ResetRequired, AnonymousLoopback: state.AnonymousLoopback, LegacyReadEnabled: state.LegacyTokenSHA256 != "", Principals: make([]AuthenticationPrincipalReport, 0, len(state.Principals))}
	if !state.UpdatedAt.IsZero() {
		at := state.UpdatedAt
		report.UpdatedAt = &at
	}
	for _, principal := range state.Principals {
		item := AuthenticationPrincipalReport{ID: principal.ID, Role: principal.Role, Revoked: principal.Revoked}
		if !principal.ExpiresAt.IsZero() {
			at := principal.ExpiresAt
			item.ExpiresAt = &at
		}
		report.Principals = append(report.Principals, item)
	}
	sort.Slice(report.Principals, func(i, j int) bool { return report.Principals[i].ID < report.Principals[j].ID })
	return report
}
