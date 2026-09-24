package persistence

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

const authenticationToken = "token-only-held-by-test-caller-not-by-the-durable-store"

func authenticationVerifier(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func authenticationBootstrap() AuthenticationCommand {
	return AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(),
		Principals: []AuthenticationPrincipal{{ID: "oncall", Role: "operator", TokenSHA256: authenticationVerifier(authenticationToken), ExpiresAt: time.Now().UTC().Add(time.Hour)}}}
}

func openAuthenticationAdmin(t *testing.T, config runtimeconfig.Config) *Store {
	t.Helper()
	s, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAuthenticationCommittedPolicyConditionalRotationAndSnapshot(t *testing.T) {
	config := testConfig(t)
	s := openAuthenticationAdmin(t, config)
	command := authenticationBootstrap()
	initial, err := s.CommitAuthentication(context.Background(), command)
	if err != nil || !initial.BootstrapConsumed || initial.ResetRequired {
		t.Fatal(initial, err)
	}
	duplicate, err := s.CommitAuthentication(context.Background(), command)
	if err != nil || !reflect.DeepEqual(initial, duplicate) {
		t.Fatal("exact lost-reply reconciliation changed authority", err)
	}
	changedBootstrap := command
	changedBootstrap.Revision = uuid.NewString()
	if _, err := s.CommitAuthentication(context.Background(), changedBootstrap); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("bootstrap was reusable", err)
	}
	next := AuthenticationCommand{Mode: "replace", Epoch: initial.Epoch, ExpectedEpoch: initial.Epoch, ExpectedRevision: initial.Revision,
		Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(), Principals: initial.Clone().Principals}
	next.Principals[0].TokenSHA256 = authenticationVerifier("replacement-private-token")
	updated, err := s.CommitAuthentication(context.Background(), next)
	if err != nil || updated.Principals[0].TokenSHA256 == initial.Principals[0].TokenSHA256 {
		t.Fatal("rotation failed", err)
	}
	if _, err := s.CommitAuthentication(context.Background(), command); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("old command overwrote rotation", err)
	}
	stale := next
	stale.Revision = uuid.NewString()
	if _, err := s.CommitAuthentication(context.Background(), stale); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("stale revision accepted", err)
	}
	// The snapshot is frozen before the next mutation, including principal slices.
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	revoke := next
	revoke.ExpectedRevision, revoke.Revision, revoke.At = updated.Revision, uuid.NewString(), time.Now().UTC()
	revoke.Principals = updated.Clone().Principals
	revoke.Principals[0].Revoked = true
	revoked, err := s.CommitAuthentication(context.Background(), revoke)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.(*frozenSnapshot).image.Authentication.Principals[0].Revoked {
		t.Fatal("background snapshot shared mutable principal storage")
	}
	initial.Principals[0].Role = "untrusted"
	revoked.Principals[0].TokenSHA256 = "untrusted"
	state, _ := s.Authentication()
	if state.Principals[0].Role != "operator" || !state.Principals[0].Revoked || !validVerifier(state.Principals[0].TokenSHA256) {
		t.Fatal("returned policy aliases state")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAuthenticationAdmin(t, config)
	restored, err := reopened.Authentication()
	if err != nil || !reflect.DeepEqual(state, restored) {
		t.Fatal("snapshot/replay lost revocation or expiry", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(config.Storage.Directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && (bytes.Contains(data, []byte(authenticationToken)) || bytes.Contains(data, []byte("replacement-private-token"))) {
			t.Errorf("plaintext bearer persisted in %s", entry.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationRejectsInvalidPoliciesAndCorruptSnapshot(t *testing.T) {
	s := openAuthenticationAdmin(t, testConfig(t))
	base := authenticationBootstrap()
	for name, mutate := range map[string]func(*AuthenticationCommand){
		"role": func(c *AuthenticationCommand) { c.Principals[0].Role = "administrator" },
		"duplicate id": func(c *AuthenticationCommand) {
			p := c.Principals[0]
			p.TokenSHA256 = authenticationVerifier("other")
			c.Principals = append(c.Principals, p)
		},
		"duplicate digest": func(c *AuthenticationCommand) {
			p := c.Principals[0]
			p.ID = "another"
			p.TokenSHA256 = strings.ToUpper(p.TokenSHA256)
			c.Principals = append(c.Principals, p)
		},
		"raw token":         func(c *AuthenticationCommand) { c.Principals[0].TokenSHA256 = authenticationToken },
		"legacy alias":      func(c *AuthenticationCommand) { c.LegacyTokenSHA256 = c.Principals[0].TokenSHA256 },
		"reserved identity": func(c *AuthenticationCommand) { c.Principals[0].ID = "legacy-read" },
		"missing time":      func(c *AuthenticationCommand) { c.At = time.Time{} },
		"excess principals": func(c *AuthenticationCommand) {
			c.Principals = make([]AuthenticationPrincipal, MaxAuthenticationPrincipals+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			c.Principals = append([]AuthenticationPrincipal(nil), base.Principals...)
			mutate(&c)
			if _, err := s.CommitAuthentication(context.Background(), c); err == nil {
				t.Fatal("invalid policy admitted")
			}
		})
	}
	if _, err := s.CommitAuthentication(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	i := snapshot.(*frozenSnapshot).image
	i.Authentication.BootstrapConsumed = false
	data, _ := json.Marshal(i)
	if _, err := decodeImage(bytes.NewReader(data)); err == nil {
		t.Fatal("corrupt authority lost its bootstrap fence")
	}
}

func TestAuthenticationAnonymousBootstrapIsExplicitAndCannotBeReenabled(t *testing.T) {
	s := openAuthenticationAdmin(t, testConfig(t))
	command := authenticationBootstrap()
	command.Principals = nil
	if _, err := s.CommitAuthentication(context.Background(), command); !errors.Is(err, ErrAuthenticationInvalid) {
		t.Fatal(err)
	}
	command.AnonymousLoopback = true
	initial, err := s.CommitAuthentication(context.Background(), command)
	if err != nil || !initial.AnonymousLoopback {
		t.Fatal(err)
	}
	replace := authenticationBootstrap()
	replace.Mode, replace.Epoch, replace.ExpectedEpoch, replace.ExpectedRevision = "replace", initial.Epoch, initial.Epoch, initial.Revision
	named, err := s.CommitAuthentication(context.Background(), replace)
	if err != nil || named.AnonymousLoopback {
		t.Fatal(err)
	}
	replace.Revision, replace.ExpectedRevision, replace.At = uuid.NewString(), named.Revision, time.Now().UTC()
	replace.Principals, replace.AnonymousLoopback = nil, true
	if _, err := s.CommitAuthentication(context.Background(), replace); !errors.Is(err, ErrAuthenticationConflict) {
		t.Fatal("authenticated store downgraded to anonymous", err)
	}
}

func TestAdministrativeOpenPreservesLifecycleHistoryAndExclusiveOwnership(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
	m, _ := s.Get("stable-one")
	id := sortedActions(m.Actions)[0]
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now})
	before, _ := s.Get(m.ID)
	index, session := s.Status().CommittedIndex, s.fsm.image.LocalExecutorSession
	if _, err := OpenAdministrative(context.Background(), c); err == nil {
		t.Fatal("admin opened live state")
	}
	command := authenticationBootstrap()
	if _, err := s.CommitAuthentication(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Authentication()
	command.Mode, command.Epoch, command.ExpectedEpoch, command.ExpectedRevision, command.Revision = "replace", state.Epoch, state.Epoch, state.Revision, uuid.NewString()
	if _, err := s.CommitAuthentication(context.Background(), command); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("live replacement enabled", err)
	}
	index = s.Status().CommittedIndex
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Administrative inspection must leave pending retention cleanup alone.
	var history historyCatalog
	if err := readOfflineJSON(filepath.Join(c.Storage.Directory, "history", "catalog.json"), &history); err != nil {
		t.Fatal(err)
	}
	history.Segments["2000-01-01"] = false
	if err := atomicJSON(filepath.Join(c.Storage.Directory, "history", "catalog.json"), history); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(c.Storage.Directory, "history", "2000-01-01.db")
	if err := os.WriteFile(retired, []byte("pending-retention-cleanup"), 0600); err != nil {
		t.Fatal(err)
	}
	admin := openAuthenticationAdmin(t, c)
	if _, err := os.Stat(retired); err != nil {
		t.Fatal("administrative open performed retention cleanup", err)
	}
	if !admin.fsm.history.catalog.Cutoff.Equal(history.Cutoff) {
		t.Fatal("administrative open advanced retention cutoff")
	}
	after, _ := admin.Get(m.ID)
	if !reflect.DeepEqual(before, after) || after.Actions[id].State != Started || admin.fsm.image.LocalExecutorSession != session || admin.Status().CommittedIndex != index {
		t.Fatal("administrative opening performed lifecycle work")
	}
	if _, err := admin.Submit(context.Background(), []Command{{Kind: "recover", At: time.Now().UTC()}}); !errors.Is(err, ErrAuthenticationAdminRequired) {
		t.Fatal("admin dispatched lifecycle command", err)
	}
	if _, err := Open(context.Background(), c); err == nil {
		t.Fatal("runtime bypassed administrator lock")
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	after, _ = resumed.Get(m.ID)
	if after.Actions[id].State != Unknown {
		t.Fatal("ordinary restart did not conservatively recover interrupted action")
	}
}

func TestAuthenticationHistoryFailureCannotConfirmAuthority(t *testing.T) {
	s := openAuthenticationAdmin(t, testConfig(t))
	fault := errors.New("history write unavailable")
	s.fsm.history.mu.Lock()
	s.fsm.history.err = fault
	s.fsm.history.mu.Unlock()
	if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err == nil {
		t.Fatal("failed commit reported success")
	}
	if _, err := s.Authentication(); !errors.Is(err, ErrAuthenticationUnavailable) || s.Status().Ready {
		t.Fatal("failed authority exposed as usable", err)
	}
}

func TestAuthenticationSurvivesForcedProcessTermination(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthenticationProcessHelper$")
	command.Env = append(os.Environ(), "CPRA_AUTH_PROCESS_HELPER="+config.Storage.Directory)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "COMMITTED\n" {
		t.Fatal("helper did not establish commit", line, err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	s := openAuthenticationAdmin(t, config)
	state, err := s.Authentication()
	if err != nil || len(state.Principals) != 1 || state.Principals[0].TokenSHA256 != authenticationVerifier(authenticationToken) || !state.BootstrapConsumed {
		t.Fatal("forced termination lost committed verifier", err)
	}
}

func TestAuthenticationProcessHelper(t *testing.T) {
	directory := os.Getenv("CPRA_AUTH_PROCESS_HELPER")
	if directory == "" {
		t.Skip("subprocess fixture")
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = directory
	s, err := OpenAdministrative(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("COMMITTED")
	select {}
}
