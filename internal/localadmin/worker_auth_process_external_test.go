//go:build externaljobs

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
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	bolt "go.etcd.io/bbolt"
)

func TestWorkerAuthenticationRealStoppedLifecycleAndProcessLock(t *testing.T) {
	directory, operatorOutput := authTestPaths(t)
	if _, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "bootstrap", DataDirectory: directory, PrincipalID: "operator", Role: "operator", TokenOutput: operatorOutput}); err != nil {
		t.Fatal(err)
	}
	grants := writeWorkerGrantFile(t, directory, "empty.json", []persistence.WorkerGrant{})
	output := filepath.Join(filepath.Dir(operatorOutput), "worker.token")
	request := WorkerAuthenticationRequest{Action: "issue", DataDirectory: directory, Actor: "operator", WorkerID: "worker-a", GrantsFile: grants, TokenOutput: output}
	issued, err := AdministerWorkerAuthentication(t.Context(), request)
	if err != nil || issued.Outcome != "committed" || len(issued.Workers) != 1 {
		t.Fatal("real stopped issuance failed", err)
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = directory
	owner, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	identity, err := owner.WorkerProtocolIdentity(t.Context())
	if err != nil || issued.ProtocolServerID == "" || identity.ServerID != issued.ProtocolServerID || identity.OwnerEpoch == "" {
		t.Fatal("running owner did not preserve provisioned protocol identity", err)
	}
	policy, err := owner.WorkerPolicy(t.Context())
	if err != nil || policy.Revision != issued.Revision || policy.Workers[request.WorkerID].UID != issued.Workers[0].UID {
		t.Fatal("normal owner lost committed worker identity", err)
	}
	firstToken := assertAuthToken(t, output, policy.Workers[request.WorkerID].TokenSHA256)
	defer clear(firstToken)
	secondOutput := filepath.Join(filepath.Dir(output), "worker-b.token")
	runWorkerAuthenticationProcess(t, directory, grants, secondOutput, "locked")
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	runWorkerAuthenticationProcess(t, directory, grants, secondOutput, "issue")
	listed, err := AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: directory, Actor: "operator"})
	if err != nil || listed.ProtocolServerID != issued.ProtocolServerID || len(listed.Workers) != 2 || listed.Workers[0].UID != issued.Workers[0].UID {
		t.Fatal("separate stopped process failed to preserve worker identity", err)
	}
	request.Action, request.GrantsFile = "rotate", ""
	request.TokenOutput = filepath.Join(filepath.Dir(output), "worker-next.token")
	rotated, err := AdministerWorkerAuthentication(t.Context(), request)
	if err != nil || rotated.ProtocolServerID != issued.ProtocolServerID || rotated.Workers[0].UID != issued.Workers[0].UID || rotated.Workers[0].CredentialRevision == issued.Workers[0].CredentialRevision || rotated.Workers[0].GrantRevision != issued.Workers[0].GrantRevision {
		t.Fatal("real rotation changed worker identity or failed to replace credentials", err)
	}
	request.Action, request.TokenOutput, request.GrantsFile = "set-grants", "", grants
	unchanged, err := AdministerWorkerAuthentication(t.Context(), request)
	if err != nil || unchanged.Workers[0].GrantRevision != rotated.Workers[0].GrantRevision {
		t.Fatal("identical grant update lost its revision", err)
	}
	request.Action, request.GrantsFile = "revoke", ""
	if _, err := AdministerWorkerAuthentication(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	listed, err = AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: directory, Actor: "operator"})
	if err != nil || !listed.Workers[0].Revoked {
		t.Fatal("revocation did not survive reopening", err)
	}
	request.Action, request.GrantsFile, request.TokenOutput = "issue", grants, filepath.Join(filepath.Dir(output), "reuse.token")
	if _, err := AdministerWorkerAuthentication(t.Context(), request); !errors.Is(err, ErrWorkerAuthenticationState) {
		t.Fatal("revoked worker identity was reused", err)
	}
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, firstToken) {
			return errors.New("raw worker bearer found in durable state")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func runWorkerAuthenticationProcess(t *testing.T, directory, grants, output, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerAuthenticationProcessHelper$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CPRA_WORKER_AUTH_TEST_STATE="+directory, "CPRA_WORKER_AUTH_TEST_GRANTS="+grants, "CPRA_WORKER_AUTH_TEST_OUTPUT="+output, "CPRA_WORKER_AUTH_TEST_MODE="+mode)
	if logs, err := command.CombinedOutput(); err != nil {
		t.Fatalf("worker authentication process boundary failed (%s): %v\n%s", mode, err, logs)
	}
}

func TestWorkerAuthenticationProcessHelper(t *testing.T) {
	directory, grants, output, mode := os.Getenv("CPRA_WORKER_AUTH_TEST_STATE"), os.Getenv("CPRA_WORKER_AUTH_TEST_GRANTS"), os.Getenv("CPRA_WORKER_AUTH_TEST_OUTPUT"), os.Getenv("CPRA_WORKER_AUTH_TEST_MODE")
	if mode == "" {
		t.Skip("separate native process fixture")
	}
	if !filepath.IsAbs(directory) || !filepath.IsAbs(grants) || !filepath.IsAbs(output) || mode != "locked" && mode != "issue" {
		t.Fatal("invalid native fixture input")
	}
	report, err := AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "issue", DataDirectory: directory, Actor: "operator", WorkerID: "worker-b", GrantsFile: grants, TokenOutput: output})
	if mode == "locked" {
		if !errors.Is(err, bolt.ErrTimeout) || report.Outcome == "committed" {
			t.Fatal("live store owner was not rejected by native lock", err)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("locked store published a token")
		}
		return
	}
	if err != nil || report.Outcome != "committed" || len(report.Workers) != 2 {
		t.Fatal("stopped worker issuance failed", err)
	}
}

func TestWorkerAuthenticationRealRestoreRequiresReprovision(t *testing.T) {
	directory, operatorOutput := authTestPaths(t)
	if _, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "bootstrap", DataDirectory: directory, PrincipalID: "operator", Role: "operator", TokenOutput: operatorOutput}); err != nil {
		t.Fatal(err)
	}
	grants := writeWorkerGrantFile(t, directory, "empty.json", []persistence.WorkerGrant{})
	request := WorkerAuthenticationRequest{Action: "issue", DataDirectory: directory, Actor: "operator", WorkerID: "worker-a", GrantsFile: grants, TokenOutput: filepath.Join(filepath.Dir(operatorOutput), "worker.token")}
	initial, err := AdministerWorkerAuthentication(t.Context(), request)
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
	if _, err := AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: restored, Actor: "operator"}); err == nil {
		t.Fatal("restored management authority authorized worker inspection")
	}
	if _, err := AdministerAuthentication(t.Context(), AuthenticationRequest{Action: "reprovision", DataDirectory: restored, PrincipalID: "new-operator", Role: "operator", TokenOutput: filepath.Join(filepath.Dir(operatorOutput), "restored-operator.token")}); err != nil {
		t.Fatal(err)
	}
	listed, err := AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: restored, Actor: "new-operator"})
	if err != nil || listed.ProtocolServerID == "" || listed.ProtocolServerID == initial.ProtocolServerID || !listed.ResetRequired || len(listed.Workers) != 1 || !listed.Workers[0].ResetRequired || listed.Workers[0].UID != initial.Workers[0].UID || len(listed.Workers[0].Grants) != 0 || listed.Epoch == initial.Epoch {
		t.Fatal("restore lost stable identity or retained prior authority", err)
	}
	request.Action, request.DataDirectory, request.Actor = "reprovision", restored, "new-operator"
	request.TokenOutput = filepath.Join(filepath.Dir(operatorOutput), "restored-worker.token")
	provisioned, err := AdministerWorkerAuthentication(t.Context(), request)
	if err != nil || provisioned.ProtocolServerID != listed.ProtocolServerID || provisioned.ResetRequired || provisioned.Workers[0].ResetRequired || provisioned.Workers[0].UID != initial.Workers[0].UID || provisioned.Workers[0].CredentialRevision == initial.Workers[0].CredentialRevision || provisioned.Workers[0].GrantRevision == initial.Workers[0].GrantRevision {
		t.Fatal("explicit reprovision failed", err)
	}
	listed, err = AdministerWorkerAuthentication(t.Context(), WorkerAuthenticationRequest{Action: "list", DataDirectory: restored, Actor: "new-operator"})
	if err != nil || !reflect.DeepEqual(listed.Workers, provisioned.Workers) || listed.Revision != provisioned.Revision {
		t.Fatal("reopening resurrected pre-restore worker authority", err)
	}
}

func TestWorkerAuthenticationNeverInitializesMissingStore(t *testing.T) {
	directory, output := authTestPaths(t)
	grants := writeWorkerGrantFile(t, directory, "empty.json", []persistence.WorkerGrant{})
	for _, action := range []string{"list", "issue", "rotate", "set-grants", "revoke", "reprovision"} {
		request := WorkerAuthenticationRequest{Action: action, DataDirectory: directory, Actor: "operator"}
		if action != "list" {
			request.WorkerID = "worker"
		}
		if action == "issue" || action == "reprovision" || action == "set-grants" {
			request.GrantsFile = grants
		}
		if action == "issue" || action == "rotate" || action == "reprovision" {
			request.TokenOutput = output
		}
		if _, err := AdministerWorkerAuthentication(t.Context(), request); !errors.Is(err, ErrAuthenticationState) {
			t.Fatal("worker command initialized an empty store", action, err)
		}
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatal("worker administration created state", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("rejected worker administration published a token")
	}
}
