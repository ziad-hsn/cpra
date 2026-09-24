package localadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type authStoreFixture struct {
	state         persistence.AuthenticationState
	commands      []persistence.AuthenticationCommand
	commitErr     error
	closeErr      error
	commitDespite bool
	opened        int
	closed        int
	beforeCommit  func(persistence.AuthenticationCommand)
}

func (s *authStoreFixture) Authentication() (persistence.AuthenticationState, error) {
	state := s.state
	state.Principals = append([]persistence.AuthenticationPrincipal(nil), s.state.Principals...)
	return state, nil
}

func (s *authStoreFixture) CommitAuthentication(_ context.Context, c persistence.AuthenticationCommand) (persistence.AuthenticationState, error) {
	s.commands = append(s.commands, c)
	if s.beforeCommit != nil {
		s.beforeCommit(c)
	}
	if s.commitErr == nil || s.commitDespite {
		s.state = persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: c.Epoch, Revision: c.Revision, BootstrapConsumed: true, AnonymousLoopback: c.AnonymousLoopback, Principals: c.Principals, LegacyTokenSHA256: c.LegacyTokenSHA256, UpdatedAt: c.At}
	}
	return s.state, s.commitErr
}

func (s *authStoreFixture) Close() error { s.closed++; return s.closeErr }

func (s *authStoreFixture) open(_ context.Context, config runtimeconfig.Config) (authenticationStore, error) {
	s.opened++
	if config.Storage.Mode != "raft" || !filepath.IsAbs(config.Storage.Directory) {
		return nil, errors.New("incorrect administrative storage configuration")
	}
	return s, nil
}

func authTestPaths(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native Linux ownership fixture; Windows protected publication is tested separately")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return state, filepath.Join(root, "tokens", "initial.token")
}

func assertAuthToken(t *testing.T, path, verifier string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil || len(contents) != 44 || contents[len(contents)-1] != '\n' {
		t.Fatal("token was not published as one complete encoded bearer", err)
	}
	bearer := contents[:len(contents)-1]
	entropy, err := base64.RawURLEncoding.DecodeString(string(bearer))
	if err != nil || len(entropy) != 32 {
		t.Fatal("token did not carry 256 random bits", err)
	}
	clear(entropy)
	digest := sha256.Sum256(bearer)
	if hex.EncodeToString(digest[:]) != verifier {
		t.Fatal("durable verifier does not authenticate exactly the file bearer")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("token output is not owner-only", err)
	}
	return append([]byte(nil), bearer...)
}

func TestAuthenticationIssuanceRotationRevocationAndSafeReports(t *testing.T) {
	state, output := authTestPaths(t)
	fixture := &authStoreFixture{}
	request := AuthenticationRequest{Action: "bootstrap", DataDirectory: state, PrincipalID: "team/oncall", Role: "operator", TokenOutput: output}
	fixture.beforeCommit = func(c persistence.AuthenticationCommand) {
		assertAuthToken(t, request.TokenOutput, c.Principals[len(c.Principals)-1].TokenSHA256)
	}
	first, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || first.Outcome != "committed" || first.Epoch == "" || first.Revision != first.IntendedRevision || !first.BootstrapConsumed {
		t.Fatal("bootstrap did not commit one bounded policy", err)
	}
	firstToken := assertAuthToken(t, output, fixture.state.Principals[0].TokenSHA256)
	defer clear(firstToken)
	expiry := time.Now().UTC().Add(time.Hour)
	request = AuthenticationRequest{Action: "issue", DataDirectory: state, PrincipalID: "team/observer", Role: "reader", TokenOutput: filepath.Join(filepath.Dir(output), "reader.token"), ExpiresAt: &expiry}
	second, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || len(second.Principals) != 2 || fixture.commands[1].ExpectedEpoch != first.Epoch || fixture.commands[1].ExpectedRevision != first.Revision || second.Epoch != first.Epoch {
		t.Fatal("issue lost existing policy or its exact precondition", err)
	}
	oldReader := fixture.state.Principals[1]
	request = AuthenticationRequest{Action: "rotate", DataDirectory: state, PrincipalID: "team/observer", TokenOutput: filepath.Join(filepath.Dir(output), "reader-next.token")}
	rotated, err := administerAuthentication(t.Context(), request, fixture.open)
	newReader := fixture.state.Principals[1]
	if err != nil || newReader.Role != "reader" || !newReader.ExpiresAt.Equal(expiry) || newReader.TokenSHA256 == oldReader.TokenSHA256 || len(fixture.state.Principals) != 2 || rotated.Epoch != first.Epoch {
		t.Fatal("rotation changed role/expiry, retained old grant overlap or lost epoch", err)
	}
	fixture.beforeCommit = nil
	request = AuthenticationRequest{Action: "revoke", DataDirectory: state, PrincipalID: "team/observer"}
	revoked, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || !fixture.state.Principals[1].Revoked || revoked.TokenOutput != "" {
		t.Fatal("revoke failed or generated a new token", err)
	}
	observed, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: state}, fixture.open)
	if err != nil || len(fixture.commands) != 4 || fixture.closed != 5 || !reflect.DeepEqual(revoked.Principals, observed.Principals) {
		t.Fatal("list mutated policy or failed to close the administrative store", err)
	}
	encoded, _ := json.Marshal([]AuthenticationReport{first, second, rotated, revoked, observed})
	if bytes.Contains(encoded, firstToken) || bytes.Contains(encoded, []byte(newReader.TokenSHA256)) || strings.Contains(string(encoded), "TokenSHA256") || strings.Contains(string(encoded), "token_sha256") {
		t.Fatal("authentication report exposed a token or verifier")
	}
	for _, cmd := range fixture.commands {
		encoded, _ := json.Marshal(cmd)
		if bytes.Contains(encoded, firstToken) {
			t.Fatal("plaintext token entered durable command")
		}
	}
}

func TestAuthenticationUnconfirmedCommitKeepsOriginalOutputWithoutRetry(t *testing.T) {
	state, output := authTestPaths(t)
	const sensitiveFailure = "provider-secret-must-not-enter-auth-errors"
	fixture := &authStoreFixture{commitErr: fmt.Errorf("%w: %s", persistence.ErrCommitUnconfirmed, sensitiveFailure), commitDespite: true}
	request := AuthenticationRequest{Action: "bootstrap", DataDirectory: state, PrincipalID: "operator", Role: "operator", TokenOutput: output}
	report, err := administerAuthentication(t.Context(), request, fixture.open)
	if !errors.Is(err, persistence.ErrCommitUnconfirmed) || report.Outcome != "unconfirmed" || report.IntendedRevision != fixture.state.Revision || report.TokenOutput != output || len(fixture.commands) != 1 || strings.Contains(err.Error(), sensitiveFailure) {
		t.Fatal("uncertain commit lost reconciliation identity, leaked error or retried", err)
	}
	token := assertAuthToken(t, output, fixture.state.Principals[0].TokenSHA256)
	defer clear(token)
	if _, err := administerAuthentication(t.Context(), request, fixture.open); err == nil || len(fixture.commands) != 1 || fixture.opened != 1 {
		t.Fatal("repeated issuance overwrote or resubmitted original private output")
	}
	listed, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: state}, fixture.open)
	if err != nil || listed.Revision != report.IntendedRevision || listed.Outcome != "observed" {
		t.Fatal("read-only reconciliation could not recognize the committed original attempt", err)
	}
}

func TestAuthenticationInputsAndPermissionsNeverCommit(t *testing.T) {
	state, output := authTestPaths(t)
	base := AuthenticationRequest{Action: "bootstrap", DataDirectory: state, PrincipalID: "operator", Role: "operator", TokenOutput: output}
	for _, mutate := range []func(*AuthenticationRequest){
		func(r *AuthenticationRequest) { r.DataDirectory = "relative" },
		func(r *AuthenticationRequest) { r.PrincipalID = "two people" },
		func(r *AuthenticationRequest) { r.PrincipalID = strings.Repeat("é", 65) },
		func(r *AuthenticationRequest) { r.Role = "admin" },
		func(r *AuthenticationRequest) { r.Role = "" },
		func(r *AuthenticationRequest) { r.TokenOutput = "-" },
		func(r *AuthenticationRequest) {
			r.ExpiresAt = func() *time.Time { at := time.Now().Add(-time.Minute); return &at }()
		},
	} {
		request := base
		mutate(&request)
		fixture := &authStoreFixture{}
		if _, err := administerAuthentication(t.Context(), request, fixture.open); !errors.Is(err, ErrAuthenticationInput) || fixture.opened != 0 {
			t.Fatal("invalid authentication input opened or modified state", err)
		}
	}
	if err := os.Mkdir(filepath.Dir(output), 0755); err != nil {
		t.Fatal(err)
	}
	fixture := &authStoreFixture{}
	if _, err := administerAuthentication(t.Context(), base, fixture.open); err == nil || len(fixture.commands) != 0 {
		t.Fatal("unsafe output permissions admitted a verifier")
	}
	if info, err := os.Stat(filepath.Dir(output)); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("authentication issuance silently fixed directory permissions")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe publication left a token")
	}
	inside := base
	inside.TokenOutput = filepath.Join(state, "forbidden.token")
	if _, err := administerAuthentication(t.Context(), inside, fixture.open); err == nil || len(fixture.commands) != 0 {
		t.Fatal("token inside ordinary backup state was admitted")
	}
}

func TestAuthenticationRestoreProvisionAndExplicitExpiry(t *testing.T) {
	state, output := authTestPaths(t)
	epoch, revision := uuid.NewString(), uuid.NewString()
	fixture := &authStoreFixture{state: persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: epoch, Revision: revision, ResetRequired: true, BootstrapConsumed: true, Principals: []persistence.AuthenticationPrincipal{{ID: "old", Role: "operator", TokenSHA256: strings.Repeat("a", 64)}}, LegacyTokenSHA256: strings.Repeat("b", 64)}}
	for _, action := range []string{"bootstrap", "issue", "rotate", "revoke"} {
		request := AuthenticationRequest{Action: action, DataDirectory: state, PrincipalID: "old"}
		if action != "revoke" {
			request.TokenOutput = output
		}
		if action == "bootstrap" || action == "issue" {
			request.Role = "operator"
		}
		if _, err := administerAuthentication(t.Context(), request, fixture.open); !errors.Is(err, ErrAuthenticationState) {
			t.Fatal("ordinary mutation bypassed restore provisioning", err)
		}
	}
	request := AuthenticationRequest{Action: "reprovision", DataDirectory: state, PrincipalID: "new", Role: "reader", TokenOutput: output}
	report, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || report.Epoch != epoch || report.ResetRequired || len(report.Principals) != 1 || report.Principals[0].ID != "new" || report.Principals[0].Role != "reader" || fixture.commands[0].ExpectedRevision != revision || fixture.commands[0].LegacyTokenSHA256 != "" {
		t.Fatal("restored verifier grants escaped explicit current-epoch provisioning", err)
	}
	expiry := time.Now().Add(time.Hour)
	fixture.state.Principals[0].ExpiresAt = expiry
	clearExpiry := time.Time{}
	request = AuthenticationRequest{Action: "rotate", DataDirectory: state, PrincipalID: "new", TokenOutput: filepath.Join(filepath.Dir(output), "without-expiry.token"), ExpiresAt: &clearExpiry}
	if _, err := administerAuthentication(t.Context(), request, fixture.open); err != nil || !fixture.state.Principals[0].ExpiresAt.IsZero() || fixture.state.Principals[0].Role != "reader" {
		t.Fatal("explicit no-expiry rotation failed or changed reader role", err)
	}
}

func TestAuthenticationLegacyRevocationAndNamedTransitionAreExplicit(t *testing.T) {
	state, output := authTestPaths(t)
	fixture := &authStoreFixture{state: persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: uuid.NewString(), Revision: uuid.NewString(), BootstrapConsumed: true, AnonymousLoopback: true}}
	listed, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: state}, fixture.open)
	if err != nil || !listed.AnonymousLoopback || listed.LegacyReadEnabled {
		t.Fatal("safe policy inspection hid anonymous access", err)
	}
	request := AuthenticationRequest{Action: "issue", DataDirectory: state, PrincipalID: "reader", Role: "reader", TokenOutput: output}
	issued, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || issued.AnonymousLoopback || fixture.commands[0].AnonymousLoopback || len(issued.Principals) != 1 {
		t.Fatal("first named issuance kept anonymous access", err)
	}
	fixture.state.LegacyTokenSHA256 = strings.Repeat("a", 64)
	request = AuthenticationRequest{Action: "rotate", DataDirectory: state, PrincipalID: "reader", TokenOutput: filepath.Join(filepath.Dir(output), "reader-next.token")}
	rotated, err := administerAuthentication(t.Context(), request, fixture.open)
	if err != nil || !rotated.LegacyReadEnabled || fixture.state.LegacyTokenSHA256 == "" {
		t.Fatal("unrelated rotation silently changed the legacy grant", err)
	}
	oldPrincipals := append([]persistence.AuthenticationPrincipal(nil), fixture.state.Principals...)
	revoked, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "revoke", DataDirectory: state, PrincipalID: "legacy-read"}, fixture.open)
	if err != nil || revoked.LegacyReadEnabled || fixture.state.LegacyTokenSHA256 != "" || !reflect.DeepEqual(oldPrincipals, fixture.state.Principals) {
		t.Fatal("explicit legacy revoke changed named grants or left legacy access", err)
	}
	if _, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "revoke", DataDirectory: state, PrincipalID: "legacy-read"}, fixture.open); !errors.Is(err, ErrAuthenticationState) {
		t.Fatal("already-revoked legacy grant silently accepted")
	}
}

func TestAuthenticationPrincipalLimitFailsBeforePublication(t *testing.T) {
	state, output := authTestPaths(t)
	fixture := &authStoreFixture{state: persistence.AuthenticationState{Version: persistence.AuthenticationFormatVersion, Epoch: uuid.NewString(), Revision: uuid.NewString(), BootstrapConsumed: true}}
	for i := range persistence.MaxAuthenticationPrincipals {
		fixture.state.Principals = append(fixture.state.Principals, persistence.AuthenticationPrincipal{ID: fmt.Sprintf("reader-%04d", i), Role: "reader", TokenSHA256: strings.Repeat("a", 64)})
	}
	if _, err := administerAuthentication(t.Context(), AuthenticationRequest{Action: "issue", DataDirectory: state, PrincipalID: "overflow", Role: "reader", TokenOutput: output}, fixture.open); !errors.Is(err, ErrAuthenticationState) || len(fixture.commands) != 0 {
		t.Fatal("principal quota allowed another verifier", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quota failure published token material")
	}
}
