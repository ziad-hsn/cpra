package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func mainAuthStatus(t *testing.T, f mainManagementFixture, path, token string, basic bool) int {
	t.Helper()
	r, err := http.NewRequest("GET", "https://"+f.options.webAddr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if basic {
		r.SetBasicAuth("cpra", token)
	} else if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20)); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

func TestMainCommittedAuthoritySkipsRemovedAndChangedBootstrapFiles(t *testing.T) {
	f := newMainManagementFixture(t)
	f.options.webAuth = "legacy-initial-fixture"
	_, stop := startMainManagement(t, f)
	if got := mainAuthStatus(t, f, "/api/v1/state", f.options.webAuth, true); got != 200 {
		t.Fatal("legacy bootstrap", got)
	}
	stop()
	if err := os.Remove(f.settings.Management.PolicyFile); err != nil {
		t.Fatal(err)
	}
	f.options.webAuth = "legacy-overwrite-fixture"
	f.options.webAuthFile = f.settings.Management.PolicyFile + ".absent-token"
	_, stop = startMainManagement(t, f)
	if got := mainAuthStatus(t, f, "/api/v2/self", startupOperatorToken, false); got != 200 {
		t.Fatal("retained named policy", got)
	}
	if got := mainAuthStatus(t, f, "/api/v1/state", "legacy-initial-fixture", true); got != 200 {
		t.Fatal("retained legacy", got)
	}
	if got := mainAuthStatus(t, f, "/api/v1/state", f.options.webAuth, true); got != 401 {
		t.Fatal("old source override", got)
	}
	stop()

	// A stopped administrator revokes the old named and legacy credentials.
	store, err := persistence.OpenAdministrative(context.Background(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Principals {
		state.Principals[i].Revoked = true
	}
	newToken := "new-committed-fixture-012345678901234567890123456789"
	verifier, _ := httpauth.HashToken(newToken)
	state.Principals = append(state.Principals, persistence.AuthenticationPrincipal{ID: "replacement", Role: httpauth.Operator, TokenSHA256: verifier})
	_, err = store.CommitAuthentication(context.Background(), persistence.AuthenticationCommand{Mode: "replace", ExpectedEpoch: state.Epoch, ExpectedRevision: state.Revision,
		Epoch: state.Epoch, Revision: uuid.NewString(), Actor: "local-test", At: time.Now().UTC(), Principals: state.Principals})
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	// Recreate an obsolete valid policy source and legacy token file. Neither
	// may override the durable revocation or reintroduce a Basic fallback.
	oldVerifier, _ := httpauth.HashToken(startupOperatorToken)
	if err := os.WriteFile(f.settings.Management.PolicyFile, []byte("principals:\n  - id: obsolete\n    role: operator\n    token_sha256: "+oldVerifier+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.options.webAuthFile, []byte("legacy-initial-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, stop = startMainManagement(t, f)
	defer stop()
	if got := mainAuthStatus(t, f, "/api/v2/self", newToken, false); got != 200 {
		t.Fatal("new committed authority", got)
	}
	for _, path := range []string{"/api/v2/self", "/api/v1/state"} {
		for _, token := range []string{startupOperatorToken, "legacy-initial-fixture", f.options.webAuth} {
			for _, basic := range []bool{false, true} {
				if got := mainAuthStatus(t, f, path, token, basic); got != 401 {
					t.Fatalf("revoked access accepted on %s basic=%v: %d", path, basic, got)
				}
			}
		}
	}
	stop()
	// Explicit restore invalidates all credential sources before any listener
	// or operational owner can start, even with the old bootstrap files present.
	if err := persistence.MarkRestored(f.settings.Storage.Directory, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	err = runCPRa(context.Background(), f.options, func() { t.Error("restored unprovisioned owner became ready") })
	if !errors.Is(err, persistence.ErrAuthenticationResetRequired) {
		t.Fatal("restore did not fail closed", err)
	}
	connection, dialErr := net.DialTimeout("tcp", f.options.webAddr, 100*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		t.Fatal("unprovisioned restore opened listener")
	}
	store, err = persistence.OpenAdministrative(context.Background(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	restoredToken := "restored-principal-fixture-012345678901234567890123456789"
	restoredVerifier, _ := httpauth.HashToken(restoredToken)
	_, err = store.CommitAuthentication(context.Background(), persistence.AuthenticationCommand{Mode: "provision", ExpectedEpoch: state.Epoch, ExpectedRevision: state.Revision,
		Epoch: state.Epoch, Revision: uuid.NewString(), Actor: "local-test", At: time.Now().UTC(),
		Principals: []persistence.AuthenticationPrincipal{{ID: "restored-operator", Role: httpauth.Operator, TokenSHA256: restoredVerifier}}})
	closeErr = store.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	_, stop = startMainManagement(t, f)
	defer stop()
	if got := mainAuthStatus(t, f, "/api/v2/self", restoredToken, false); got != 200 {
		t.Fatal("reprovisioned authority", got)
	}
	for _, token := range []string{startupOperatorToken, newToken, "legacy-initial-fixture"} {
		if got := mainAuthStatus(t, f, "/api/v2/self", token, false); got != 401 {
			t.Fatal("restore resurrected old grant", got)
		}
		if got := mainAuthStatus(t, f, "/api/v1/state", token, true); got != 401 {
			t.Fatal("restore resurrected Basic grant", got)
		}
	}
}

func TestMainValidationWithoutBootstrapPolicyHasNoStorageSideEffects(t *testing.T) {
	f := newMainManagementFixture(t)
	f.settings.Management.PolicyFile = ""
	writeMainRuntime(t, f.options.runtimeFile, f.settings)
	f.options.validate = true
	if err := runCPRa(context.Background(), f.options, func() { t.Error("validation started owner") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.settings.Storage.Directory); !os.IsNotExist(err) {
		t.Fatal("validation opened state", err)
	}
}

func TestMainLegacyAndAnonymousAuthorityCannotBeOverriddenOnRestart(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-verifier", true: "explicit-anonymous"}[anonymous], func(t *testing.T) {
			f := newMainManagementFixture(t)
			f.settings.Management = runtimeconfig.Management{}
			f.client = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
			f.options.webAuth = "legacy-startup-token"
			if anonymous {
				f.options.webAuth = ""
			}
			writeMainRuntime(t, f.options.runtimeFile, f.settings)
			_, stop := startMainManagement(t, f)
			stop()
			store, err := persistence.OpenAdministrative(context.Background(), f.settings)
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.Authentication()
			if err != nil || !state.BootstrapConsumed || state.AnonymousLoopback != anonymous {
				t.Fatal("initial authority", state.Version, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			f.options.webAuth = "ignored-new-runtime-token"
			f.options.webAuthFile = f.options.runtimeFile + ".missing-token"
			_, stop = startMainManagement(t, f)
			for _, token := range []string{"", "legacy-startup-token", "ignored-new-runtime-token"} {
				r, _ := http.NewRequest("GET", "http://"+f.options.webAddr+"/api/v1/state", nil)
				if token != "" {
					r.SetBasicAuth("cpra", token)
				}
				response, err := f.client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				response.Body.Close()
				want := 401
				if anonymous || token == "legacy-startup-token" {
					want = 200
				}
				if response.StatusCode != want {
					t.Fatal("retained legacy policy", response.StatusCode, want)
				}
			}
			stop()
			if anonymous {
				f.options.webAddr = "0.0.0.0:8060"
				if err := runCPRa(context.Background(), f.options, func() { t.Error("anonymous public listener started") }); err == nil {
					t.Fatal("anonymous authority escaped loopback")
				}
			}
		})
	}
}
