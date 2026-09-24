package management

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const validationWorkerCrashDirectory = "CPRA_VALIDATION_WORKER_CRASH_DIRECTORY"
const validationWorkerCrashTarget = "CPRA_VALIDATION_WORKER_CRASH_TARGET"
const validationWorkerCrashPrefix = "CPRA_VALIDATION_WORKER_COMMITTED "

type validationWorkerCrashReport struct {
	IDs map[string]string `json:"ids"`
	At  time.Time         `json:"at"`
}

func validationWorkerRaft(t *testing.T, directory string) (*Catalog, *persistence.Store, runtimeconfig.Config) {
	t.Helper()
	config := runtimeconfig.Default()
	config.Storage.Mode, config.Storage.Directory = "raft", directory
	store, err := persistence.Open(context.Background(), config)
	if err != nil {
		t.Fatal("open real coordinator store", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(context.Background()); err != nil {
		t.Fatal("verify reopened catalog", err)
	}
	return catalog, store, config
}

// The child reports only operation handles and an observation time. No
// protected headers, provider bodies, encryption keys or frozen artifacts cross
// the pipe. Recovery therefore cannot reuse parent-retained compiler output.
func TestCollectionValidationWorkerRestartChild(t *testing.T) {
	directory := os.Getenv(validationWorkerCrashDirectory)
	if directory == "" {
		t.Skip("separate-process coordinator fixture")
	}
	target := os.Getenv(validationWorkerCrashTarget)
	if !strings.HasPrefix(target, "http://127.0.0.1:") {
		t.Fatal("expected parent-owned loopback target")
	}
	catalog, store, _ := validationWorkerRaft(t, directory)
	policy, err := store.CommitAuthentication(context.Background(), collectionOwnerBootstrap(time.Now().UTC().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := make(map[string]collectionSourceFixture)
	report := validationWorkerCrashReport{IDs: make(map[string]string)}
	for _, name := range []string{"queued", "claimed", "partial-plan", "finalized", "canceled"} {
		input := collectionMonitor(name, target)
		f := stagedSourceFixtureStore(t, catalog, store, 1, func(int) (persistence.CatalogKey, []byte) {
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			return persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}, raw
		}, func(head *persistence.CollectionState) {
			head.Actor = "team/operator"
			head.Owner = &persistence.OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: head.Actor}
		}, nil)
		f.head = coordinatorRequest(t, &f)
		fixtures[name], report.IDs[name] = f, f.head.ID
		if f.at.After(report.At) {
			report.At = f.at
		}
	}
	// Complete one result and cancel another before the snapshot; both original
	// dispositions must survive the later restart sweep without a rewrite.
	at := report.At.Add(3 * time.Second)
	finalized, err := catalog.runCollectionValidation(context.Background(), report.IDs["finalized"], uuid.NewString(), collectionClock(at))
	if err != nil || finalized.Validation == nil || finalized.Validation.FinalizedAt.IsZero() {
		t.Fatal("finalized fixture", err)
	}
	finalized = submitStagedSource(t, store, persistence.CollectionCommand{Action: "validation_publish", OperationID: finalized.ID,
		UploadID: finalized.UploadID, ValidationID: finalized.Validation.Header.ResultID}, at)
	if !finalized.Validation.HistorySealed {
		t.Fatal("finalized fixture lacks original retained seal")
	}
	if _, err := catalog.CancelCollection(context.Background(), report.IDs["canceled"], "team/operator", collectionClock(at), allowCollectionCommit); err != nil {
		t.Fatal("canceled fixture", err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal("preclaim snapshot", err)
	}
	// These abandoned attempts exist only in the later log: one has a claim
	// without artifacts, the other has the real runner's committed plan Begin.
	claimed := fixtures["claimed"].head
	submitStagedSource(t, store, persistence.CollectionCommand{Action: "validation_claim", OperationID: claimed.ID,
		UploadID: claimed.UploadID, ValidationFence: &persistence.CollectionValidationRequestFence{RequestID: claimed.ValidationRequest.ID},
		ValidationClaim: &persistence.CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}}, at)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partial, err := catalog.runCollectionValidation(ctx, report.IDs["partial-plan"], uuid.NewString(), func() time.Time {
		head, _, err := store.CollectionGet(report.IDs["partial-plan"])
		if err != nil {
			t.Fatal(err)
		}
		if head.Plan != nil {
			cancel()
		}
		return at
	})
	if !errors.Is(err, context.Canceled) || partial.Plan == nil || partial.Plan.UploadedFragments != 0 || partial.Validation != nil {
		t.Fatal("partial original artifact fixture", err)
	}
	report.At = at.Add(time.Second)
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 4096 {
		t.Fatal("invalid bounded child report", err)
	}
	fmt.Println(validationWorkerCrashPrefix + string(raw))
	select {} // Parent kills without Close, preserving the actual crash boundary.
}

type validationWorkerDiagnostics struct{ bytes.Buffer }

func (b *validationWorkerDiagnostics) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := (32 << 10) - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(remaining, n)])
	}
	return n, nil
}

func validationWorkerKillChild(t *testing.T, directory, target string) validationWorkerCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionValidationWorkerRestartChild$", "-test.count=1")
	command.Env = append(os.Environ(), validationWorkerCrashDirectory+"="+directory, validationWorkerCrashTarget+"="+target)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics validationWorkerDiagnostics
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	type childMessage struct{ report, diagnostic string }
	ready := make(chan childMessage, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 8192)
		var diagnostic validationWorkerDiagnostics
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), validationWorkerCrashPrefix) {
				ready <- childMessage{report: strings.TrimPrefix(scanner.Text(), validationWorkerCrashPrefix)}
				return
			}
			_, _ = diagnostic.Write(scanner.Bytes())
			_, _ = diagnostic.Write([]byte{'\n'})
		}
		ready <- childMessage{diagnostic: diagnostic.String()}
	}()
	var message childMessage
	select {
	case message = <-ready:
	case <-ctx.Done():
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal("kill child", err)
	}
	waitErr := command.Wait()
	waited = true
	if message.report == "" || waitErr == nil || command.ProcessState == nil || command.ProcessState.Success() {
		t.Fatal("child did not reach and crash after acknowledged original state", waitErr, diagnostics.String(), message.diagnostic)
	}
	var report validationWorkerCrashReport
	if err := json.Unmarshal([]byte(message.report), &report); err != nil || len(report.IDs) != 5 || report.At.IsZero() {
		t.Fatal("invalid committed fixture identity", err)
	}
	return report
}

func validationWorkerStop(t *testing.T, coordinator *CollectionValidationCoordinator) {
	t.Helper()
	coordinator.BeginStop()
	coordinator.BeginStop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := coordinator.Wait(ctx); err != nil {
		t.Error("coordinator did not join", err)
	}
	select {
	case <-coordinator.Done():
	default:
		t.Error("successful Wait left worker running")
	}
	if err := coordinator.Err(); err != nil {
		t.Error("healthy coordinator stopped with fatal error", err)
	}
}

func TestCollectionValidationWorkerProcessCrashRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux forced-termination evidence")
	}
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer target.Close()
	directory := t.TempDir()
	report := validationWorkerKillChild(t, directory, target.URL)
	original := make(map[string]persistence.CollectionReceipt)
	retained := make(map[string]persistence.CollectionReceipt)
	for restart := range 2 {
		catalog, store, _ := validationWorkerRaft(t, directory)
		at := report.At.Add(time.Duration(restart) * time.Second)
		if restart == 0 {
			for name, id := range report.IDs {
				r, err := store.CollectionReceipt(context.Background(), id, at)
				if err != nil || r.ValidationRequest == nil {
					t.Fatal("recovery lost original request", name, err)
				}
				original[name] = r
			}
			if original["queued"].ValidationRequest.Claim != nil || original["claimed"].ValidationRequest.Claim == nil || original["partial-plan"].ValidationRequest.Claim == nil ||
				original["finalized"].Phase != "validated" || original["canceled"].Phase != "canceled" {
				t.Fatal("fixture did not recover distinct queued/claimed/terminal states")
			}
		}
		wrapper := collectionBlockSealing(t, catalog)
		var prematureReads atomic.Int64
		sealer, err := secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: wrapper, before: func(ctx context.Context) {
			for _, name := range []string{"claimed", "partial-plan"} {
				receipt, readErr := store.CollectionReceipt(ctx, report.IDs[name], at)
				if readErr != nil || receipt.Phase != "interrupted" {
					prematureReads.Add(1)
				}
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		catalog.sealer = sealer
		coordinator, err := catalog.StartCollectionValidationCoordinator(context.Background(), collectionClock(at))
		if err != nil || coordinator == nil {
			t.Fatal("start reopened coordinator", err)
		}
		t.Cleanup(func() { validationWorkerStop(t, coordinator) })
		if !coordinator.Ready() {
			t.Fatal("successful Start returned before startup retirement completed")
		}
		// Startup retirement is synchronous, not an eventual polling assertion.
		for _, name := range []string{"claimed", "partial-plan"} {
			r, err := store.CollectionReceipt(context.Background(), report.IDs[name], at)
			if err != nil || r.Phase != "interrupted" || r.ValidationRequest.Interruption == nil || r.ValidationRequest.Interruption.Reason != "coordinatorRestarted" ||
				r.ValidationRequest.ID != original[name].ValidationRequest.ID || !reflect.DeepEqual(r.ValidationRequest.Claim, original[name].ValidationRequest.Claim) || r.Validation != nil {
				t.Fatal("startup resumed/replaced abandoned original attempt", name, err)
			}
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			r, err := store.CollectionReceipt(context.Background(), report.IDs["queued"], at)
			if err == nil && r.Phase == "validated" {
				if r.Validation == nil || !r.Validation.Header.Valid || r.ValidationRequest.ID != original["queued"].ValidationRequest.ID || r.ValidationRequest.Claim == nil {
					t.Fatal("queued original request lost identity or result")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("queued request did not publish original validation", err, coordinator.Err())
			}
			time.Sleep(10 * time.Millisecond)
		}
		validationWorkerStop(t, coordinator)
		for name, id := range report.IDs {
			r, err := store.CollectionReceipt(context.Background(), id, at)
			if err != nil {
				t.Fatal("retained result disappeared", name, err)
			}
			if name == "finalized" || name == "canceled" {
				if !reflect.DeepEqual(r, original[name]) {
					t.Fatal("startup rewrote an original terminal disposition", name)
				}
			}
			if restart == 0 {
				retained[name] = r
			} else if !reflect.DeepEqual(r, retained[name]) {
				t.Fatal("second startup renewed/replaced retained receipt", name)
			}
			if name == "claimed" || name == "partial-plan" {
				page, err := store.History().Page("collection/"+id, "", 100)
				if err != nil || len(page.Events) != 1 || page.Events[0].Type != "collection_interrupted" || page.Events[0].Reason != "coordinatorRestarted" ||
					page.Events[0].ActionID != r.ValidationRequest.Interruption.ID {
					t.Fatal("startup interruption audit missing or repeated", name, err)
				}
			}
		}
		view, err := store.CatalogSnapshot()
		if err != nil || view.Len() != 0 || providerCalls.Load() != 0 || wrapper.wraps.Load() != 0 || prematureReads.Load() != 0 ||
			restart == 0 && wrapper.opens.Load() == 0 || restart == 1 && wrapper.opens.Load() != 0 {
			t.Fatal("restart activated resources, called provider, sealed mutation, or recompiled retained result", err)
		}
		if err := store.Snapshot(); err != nil {
			t.Fatal("post-coordinator snapshot", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal("close joined store", err)
		}
	}
}
