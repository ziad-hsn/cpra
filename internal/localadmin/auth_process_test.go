package localadmin

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	bolt "go.etcd.io/bbolt"
)

func TestAuthenticationRealStoppedLifecycleAndProcessLock(t *testing.T) {
	directory, output := authTestPaths(t)
	bootstrap, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "bootstrap", DataDirectory: directory, PrincipalID: "team/oncall", Role: "operator", TokenOutput: output})
	if err != nil || bootstrap.Outcome != "committed" {
		t.Fatal("real Raft authentication bootstrap failed", err)
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = directory
	owner, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	apply := func(command persistence.Command) persistence.Result {
		t.Helper()
		results, err := owner.Submit(t.Context(), []persistence.Command{command})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("real lifecycle fixture command failed", err)
		}
		if command.Kind == "start" && !results[0].Allowed {
			t.Fatal("fixture external start was not admitted")
		}
		return results[0]
	}
	at := time.Now().UTC()
	m := persistence.Monitor{ID: "active-incident", Name: "State must survive auth administration", Revision: "revision-1", Policy: persistence.Policy{Interval: time.Second, Unhealthy: 1, Healthy: 2, Enabled: true, RecoveryBypass: true, Endpoints: map[string]int{"red": 1}}}
	apply(persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	apply(persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at.Add(time.Millisecond), Scheduled: at})
	m, _ = owner.Get(m.ID)
	action := ""
	for id := range m.Actions {
		action = id
		break
	}
	if action == "" {
		t.Fatal("fixture did not create an incident action")
	}
	apply(persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action, At: at.Add(2 * time.Millisecond)})
	before, _ := owner.Get(m.ID)
	if before.Actions[action].State != persistence.Started {
		t.Fatal("fixture requires an interrupted started action")
	}
	readerOutput := filepath.Join(filepath.Dir(output), "process-reader.token")
	runAuthenticationProcess(t, directory, readerOutput, "locked")
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	runAuthenticationProcess(t, directory, readerOutput, "issue")
	admin, err := persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := admin.Get(m.ID)
	if !reflect.DeepEqual(before, after) {
		admin.Close()
		t.Fatal("ordinary administrative reopen mutated incident/executor lifecycle")
	}
	policy, err := admin.Authentication()
	if err != nil || len(policy.Principals) != 2 || policy.Epoch != bootstrap.Epoch {
		admin.Close()
		t.Fatal("separate stopped process failed to persist reader policy", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	var readerVerifier, operatorVerifier string
	for _, principal := range policy.Principals {
		switch principal.ID {
		case "team/observer":
			readerVerifier = principal.TokenSHA256
			if principal.Role != "reader" {
				t.Fatal("issued reader changed role")
			}
		case "team/oncall":
			operatorVerifier = principal.TokenSHA256
		}
	}
	readerToken := assertAuthToken(t, readerOutput, readerVerifier)
	operatorToken := assertAuthToken(t, output, operatorVerifier)
	defer clear(readerToken)
	defer clear(operatorToken)
	rotatedOutput := filepath.Join(filepath.Dir(output), "process-reader-rotated.token")
	rotated, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "rotate", DataDirectory: directory, PrincipalID: "team/observer", TokenOutput: rotatedOutput})
	if err != nil || rotated.Epoch != policy.Epoch || rotated.Revision == policy.Revision {
		t.Fatal("real rotation failed", err)
	}
	if _, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "revoke", DataDirectory: directory, PrincipalID: "team/observer"}); err != nil {
		t.Fatal(err)
	}
	listed, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: directory})
	if err != nil || len(listed.Principals) != 2 || listed.Principals[0].ID != "team/observer" || !listed.Principals[0].Revoked || listed.Principals[0].Role != "reader" {
		t.Fatal("real revocation did not survive stopped reopen", err)
	}
	for _, token := range [][]byte{readerToken, operatorToken} {
		if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, token) {
				return errors.New("plaintext bearer found in durable state")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuthenticationNonBootstrapNeverInitializesState(t *testing.T) {
	for _, action := range []string{"list", "issue", "rotate", "revoke", "reprovision"} {
		for _, incomplete := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/empty", true: "/incomplete"}[incomplete], func(t *testing.T) {
				directory, output := authTestPaths(t)
				if incomplete {
					// A partial file is not permission to initialize the remaining
					// store. In particular, no Raft database must be created here.
					if err := os.WriteFile(filepath.Join(directory, "identity.json"), []byte("interrupted bootstrap fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				request := AuthenticationRequest{Action: action, DataDirectory: directory}
				if action != "list" {
					request.PrincipalID = "operator"
				}
				if action == "issue" || action == "reprovision" {
					request.Role = "operator"
				}
				if action != "list" && action != "revoke" {
					request.TokenOutput = output
				}
				if _, err := AdministerAuthentication(t.Context(), request); !errors.Is(err, ErrAuthenticationState) {
					t.Fatal("empty/incomplete store was initialized or opened", err)
				}
				entries, err := os.ReadDir(directory)
				expected := 0
				if incomplete {
					expected = 1
					data, readErr := os.ReadFile(filepath.Join(directory, "identity.json"))
					if readErr != nil || string(data) != "interrupted bootstrap fixture" {
						t.Fatal("incomplete input was changed", readErr)
					}
				}
				if err != nil || len(entries) != expected {
					t.Fatal("non-bootstrap command created durable files", err)
				}
				if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("non-bootstrap rejection published a token")
				}
			})
		}
	}
}

func runAuthenticationProcess(t *testing.T, directory, output, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthenticationProcessHelper$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CPRA_AUTH_TEST_STATE="+directory, "CPRA_AUTH_TEST_OUTPUT="+output, "CPRA_AUTH_TEST_MODE="+mode)
	if logs, err := command.CombinedOutput(); err != nil {
		t.Fatalf("authentication process boundary failed (%s): %v\n%s", mode, err, logs)
	}
}

func TestAuthenticationProcessHelper(t *testing.T) {
	directory, output, mode := os.Getenv("CPRA_AUTH_TEST_STATE"), os.Getenv("CPRA_AUTH_TEST_OUTPUT"), os.Getenv("CPRA_AUTH_TEST_MODE")
	if mode == "" {
		t.Skip("separate native process fixture")
	}
	if !filepath.IsAbs(directory) || !filepath.IsAbs(output) || mode != "locked" && mode != "issue" {
		t.Fatal("invalid native fixture input")
	}
	report, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "issue", DataDirectory: directory, PrincipalID: "team/observer", Role: "reader", TokenOutput: output})
	if mode == "locked" {
		if !errors.Is(err, bolt.ErrTimeout) || report.Outcome == "committed" {
			t.Fatal("active owner was not rejected by the real process lock", err)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("active-store rejection published token material")
		}
		return
	}
	if err != nil || report.Outcome != "committed" || len(report.Principals) != 2 {
		t.Fatal("stopped process did not commit reader issuance", err)
	}
}

func TestAuthenticationRealRestoreNeedsCurrentEpochProvision(t *testing.T) {
	directory, output := authTestPaths(t)
	initial, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "bootstrap", DataDirectory: directory, PrincipalID: "old-operator", Role: "operator", TokenOutput: output})
	if err != nil {
		t.Fatal(err)
	}
	backup, restored := directory+"-backup", directory+"-restored"
	key := backupTestKey(t, filepath.Dir(directory))
	if err := Backup(directory, backup, "", key); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backup, restored, key); err != nil {
		t.Fatal(err)
	}
	reset, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: restored})
	if err != nil || !reset.ResetRequired || reset.Epoch == initial.Epoch || len(reset.Principals) != 0 || reset.LegacyReadEnabled || reset.AnonymousLoopback {
		t.Fatal("restored policy retained stale grants or lost current-epoch reset", err)
	}
	freshOutput := filepath.Join(filepath.Dir(output), "restored.token")
	request := AuthenticationRequest{Action: "bootstrap", DataDirectory: restored, PrincipalID: "fresh-operator", Role: "operator", TokenOutput: freshOutput}
	if _, err := AdministerAuthentication(t.Context(), request); !errors.Is(err, ErrAuthenticationState) {
		t.Fatal("ordinary bootstrap bypassed explicit reprovisioning", err)
	}
	request.Action = "reprovision"
	provisioned, err := AdministerAuthentication(t.Context(), request)
	if err != nil || provisioned.ResetRequired || provisioned.Epoch != reset.Epoch || provisioned.Revision == reset.Revision || len(provisioned.Principals) != 1 || provisioned.Principals[0].ID != "fresh-operator" {
		t.Fatal("explicit provision did not bind the restored current epoch", err)
	}
	listed, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "list", DataDirectory: restored})
	if err != nil || listed.Revision != provisioned.Revision || strings.Contains(listed.Principals[0].ID, "old") {
		t.Fatal("reopen resurrected restored old grant", err)
	}
}
