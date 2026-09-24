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
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	activationCrashDirectoryEnv = "CPRA_ACTIVATION_ADMISSION_CRASH_DIRECTORY"
	activationCrashScenarioEnv  = "CPRA_ACTIVATION_ADMISSION_CRASH_SCENARIO"
	activationCrashReadyPrefix  = "CPRA_ACTIVATION_ADMISSION_COMMITTED "
)

// IPC contains original allowlisted identities and ciphertext-namespace digests,
// never the protected inventory header, provider configuration or credentials.
type activationCrashReport struct {
	OperationID       string                       `json:"operation_id"`
	UploadID          string                       `json:"upload_id"`
	ContentDigest     string                       `json:"content_digest"`
	ProgressDigest    string                       `json:"progress_digest"`
	ItemCount         uint64                       `json:"item_count"`
	Phase             string                       `json:"phase"`
	ActivityAt        time.Time                    `json:"activity_at"`
	ExpiresAt         time.Time                    `json:"expires_at"`
	TerminalAt        time.Time                    `json:"terminal_at"`
	Activation        CollectionActivation         `json:"activation"`
	Cancellation      *CollectionCancellation      `json:"cancellation,omitempty"`
	Plan              CollectionPlanState          `json:"plan"`
	Validation        CollectionValidationState    `json:"validation"`
	ValidationRequest *CollectionValidationRequest `json:"validation_request,omitempty"`
	NamespaceDigests  [3]string                    `json:"namespace_digests"`
}

func activationCrashNamespaceDigests(t *testing.T, s *Store) [3]string {
	t.Helper()
	a, b, c := validationLedgerStreams(t, s.fsm.collections)
	var digests [3]string
	for n, data := range [][]byte{a, b, c} {
		sum := sha256.Sum256(data)
		digests[n] = hex.EncodeToString(sum[:])
	}
	return digests
}

func TestCollectionActivationProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(activationCrashDirectoryEnv)
	if directory == "" {
		return
	}
	scenario := os.Getenv(activationCrashScenarioEnv)
	if scenario != "pre-admission-snapshot-and-log" && scenario != "applying-snapshot" && scenario != "applying-snapshot-cancel-log" {
		t.Fatal("unknown activation process fixture")
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	admin, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	policy := authenticationBootstrap()
	policy.Principals[0].ExpiresAt = policy.At.Add(90 * 24 * time.Hour)
	if _, err := admin.CommitAuthentication(context.Background(), policy); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	// No Close or defer: the parent kills the running owner after this IPC.
	head := activationSnapshotInput(t, s, true)
	if scenario == "pre-admission-snapshot-and-log" {
		if s.fsm.image.Version != CollectionValidationRequestFormatVersion {
			t.Fatal("pre-admission image was not format7")
		}
		if err := s.Snapshot(); err != nil {
			t.Fatal("pre-admission snapshot", err)
		}
	}
	head, _ = activationSnapshotAdmit(t, s, head)
	if scenario != "pre-admission-snapshot-and-log" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("admitted snapshot", err)
		}
	}
	if scenario == "applying-snapshot-cancel-log" {
		head, _ = activationSnapshotCancel(t, s, head, head.ExpiresAt.Add(time.Hour))
	}
	activationSnapshotNoExecution(t, s)
	report := activationCrashReport{OperationID: head.ID, UploadID: head.UploadID, ContentDigest: head.ContentDigest,
		ProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Phase: head.Phase, ActivityAt: head.ActivityAt, ExpiresAt: head.ExpiresAt,
		TerminalAt: head.TerminalAt, Activation: head.Activation.Clone(), Cancellation: head.Cancellation, Plan: *head.Plan, Validation: *head.Validation,
		ValidationRequest: head.Clone().ValidationRequest, NamespaceDigests: activationCrashNamespaceDigests(t, s)}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 1<<20 || bytes.Contains(raw, []byte(authenticationToken)) || bytes.Contains(raw, []byte(authenticationVerifier(authenticationToken))) ||
		bytes.Contains(raw, []byte("never-write-this-collection-plaintext")) {
		t.Fatal("invalid safe bounded activation IPC", err)
	}
	fmt.Println(activationCrashReadyPrefix + string(raw))
	select {}
}

func activationCrashChild(t *testing.T, directory, scenario string) activationCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionActivationProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), activationCrashDirectoryEnv+"="+directory, activationCrashScenarioEnv+"="+scenario)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics planCrashDiagnostics
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), (1<<20)+len(activationCrashReadyPrefix)+1)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), activationCrashReadyPrefix) {
				ready <- strings.TrimPrefix(scanner.Text(), activationCrashReadyPrefix)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("activation child did not reach committed boundary")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("activation child exited before commitment: %s", diagnostics.String())
	}
	var report activationCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.OperationID == "" || report.ItemCount != 2 || report.Activation.ID == "" {
		t.Fatal("invalid original activation report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal("force termination", err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("activation child was not forcibly terminated")
	}
	return report
}

func TestCollectionActivationCommittedAdmissionSurvivesProcessKill(t *testing.T) {
	for _, scenario := range []string{"pre-admission-snapshot-and-log", "applying-snapshot", "applying-snapshot-cancel-log"} {
		t.Run(scenario, func(t *testing.T) {
			config := testConfig(t)
			report := activationCrashChild(t, config.Storage.Directory, scenario)
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("offline validation after process kill", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal("restart after activation process kill", err)
			}
			defer s.Close()
			head, exists, err := s.CollectionGet(report.OperationID)
			if err != nil || !exists || head.Activation == nil || head.Plan == nil || head.Validation == nil ||
				head.UploadID != report.UploadID || head.ContentDigest != report.ContentDigest || head.ProgressDigest != report.ProgressDigest || head.ItemCount != report.ItemCount ||
				head.Phase != report.Phase || !head.ActivityAt.Equal(report.ActivityAt) || !head.ExpiresAt.Equal(report.ExpiresAt) || !head.TerminalAt.Equal(report.TerminalAt) ||
				!reflect.DeepEqual(*head.Activation, report.Activation) || !reflect.DeepEqual(head.Cancellation, report.Cancellation) ||
				!reflect.DeepEqual(*head.Plan, report.Plan) || !reflect.DeepEqual(*head.Validation, report.Validation) || !reflect.DeepEqual(head.ValidationRequest, report.ValidationRequest) ||
				activationCrashNamespaceDigests(t, s) != report.NamespaceDigests {
				t.Fatal("restart changed original activation, result, plan or namespace", err)
			}
			if s.fsm.image.Version != CollectionActivationFormatVersion {
				t.Fatal("replay lost format8")
			}
			// The original upload lifetime never becomes a new applying deadline.
			at := head.ExpiresAt.Add(2 * time.Hour)
			authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			c := CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID,
				Activation: &report.Activation, ActivationAuthority: &authority}
			retry := collectionCommand(t, s, c, at)
			if retry.Err != nil || !reflect.DeepEqual(validationApplyAllowed(t, retry), head) || len(retry.Events) != 0 {
				t.Fatal("lost admission reply renewed or repeated activation", retry.Err)
			}
			receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || receipt.Phase != report.Phase {
				t.Fatal("original admission expired at upload deadline", err)
			}
			page, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at)
			if err != nil || page.Receipt.Descriptor != report.Validation.Descriptor || page.Receipt.Header != report.Validation.Header ||
				!page.Receipt.FinalizedAt.Equal(report.Validation.FinalizedAt) || len(page.Items) != int(report.ItemCount) {
				t.Fatal("admission lost original sealed result", err)
			}
			if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, report.Validation.FinalizedAt.AddDate(0, 0, 30)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("original result retention did not expire independently", err)
			}
			if report.Phase == "applying" {
				late, err := s.CollectionReceipt(context.Background(), head.ID, report.Validation.FinalizedAt.AddDate(0, 0, 31))
				if err != nil || late.Phase != "applying" {
					t.Fatal("result retention erased applying admission", err)
				}
			} else {
				cancelCommand := CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: report.Cancellation,
					ActivationFence: CollectionActivationFenceFor(head), ActivationAuthority: &authority}
				retry := collectionCommand(t, s, cancelCommand, at)
				if retry.Err != nil || !reflect.DeepEqual(validationApplyAllowed(t, retry), head) || len(retry.Events) != 0 {
					t.Fatal("lost cancel reply changed original outcome", retry.Err)
				}
			}
			history, err := s.History().Page("collection/"+head.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			admissionHistory, err := s.History().Page("collection-activation/"+head.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			admissions, cancellations := 0, 0
			for _, event := range append(history.Events, admissionHistory.Events...) {
				switch event.Type {
				case "collection_activation_admitted":
					admissions++
					if event.ActionID != report.Activation.ID {
						t.Fatal("changed admission event identity")
					}
				case "collection_canceled":
					cancellations++
				}
			}
			wantCancellations := 0
			if report.Cancellation != nil {
				wantCancellations = 1
			}
			if admissions != 1 || cancellations != wantCancellations {
				t.Fatal("replay or retry duplicated admission/cancellation evidence", admissions, cancellations)
			}
			activationSnapshotNoExecution(t, s)
		})
	}
}
