package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	validationCrashDirectoryEnv = "CPRA_VALIDATION_STAGING_CRASH_DIRECTORY"
	validationCrashScenarioEnv  = "CPRA_VALIDATION_STAGING_CRASH_SCENARIO"
	validationCrashReadyPrefix  = "CPRA_VALIDATION_STAGING_COMMITTED "
)

// The parent keeps the original descriptor and result bytes, never a freshly
// compiled replacement. IPC deliberately excludes protected inventory envelopes,
// resource bodies, authentication verifiers and bearer credentials.
type validationCrashReport struct {
	OperationID   string                     `json:"operation_id"`
	UploadID      string                     `json:"upload_id"`
	ContentDigest string                     `json:"content_digest"`
	Phase         string                     `json:"phase"`
	ActivityAt    time.Time                  `json:"activity_at"`
	ExpiresAt     time.Time                  `json:"expires_at"`
	Plan          *CollectionPlanState       `json:"plan,omitempty"`
	Validation    CollectionValidationState  `json:"validation"`
	Items         []CollectionValidationItem `json:"items"`
}

func TestCollectionValidationProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(validationCrashDirectoryEnv)
	if directory == "" {
		return
	}
	scenario := os.Getenv(validationCrashScenarioEnv)
	if scenario != "partial-snapshot-and-log" && scenario != "finalized-valid-snapshot" && scenario != "rejected-log" {
		t.Fatal("unknown validation crash scenario")
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	admin, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal("open stopped authentication store", err)
	}
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		_ = admin.Close()
		t.Fatal("provision named operator", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal("release stopped authentication store", err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal("open service store", err)
	}
	// There is intentionally no graceful Close. The parent terminates this
	// process after the command acknowledgement and optional snapshot below.
	head := validationHistoryCrashInput(t, s, 4)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal("observe original named authority", err)
	}
	valid := scenario != "rejected-log"
	head, begin, items := validationApplyIntent(t, s, head, authority, valid)
	if scenario == "rejected-log" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("snapshot before rejection intent", err)
		}
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[:1]))
	if scenario == "partial-snapshot-and-log" {
		if err := s.Snapshot(); err != nil {
			t.Fatal("snapshot partial validation result", err)
		}
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[1:2]))
	} else {
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[1:]))
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
		if scenario == "finalized-valid-snapshot" {
			if err := s.Snapshot(); err != nil {
				t.Fatal("snapshot finalized validation result", err)
			}
		}
	}
	report := validationCrashReport{OperationID: head.ID, UploadID: head.UploadID, ContentDigest: head.ContentDigest,
		Phase: head.Phase, ActivityAt: head.ActivityAt, ExpiresAt: head.ExpiresAt, Plan: head.Clone().Plan,
		Validation: *head.Validation, Items: items}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 1<<20 || bytes.Contains(raw, []byte(authenticationToken)) ||
		bytes.Contains(raw, []byte(authenticationVerifier(authenticationToken))) ||
		bytes.Contains(raw, []byte("never-write-this-collection-plaintext")) {
		t.Fatal("invalid safe bounded validation IPC", err)
	}
	fmt.Println(validationCrashReadyPrefix + string(raw))
	select {}
}

func validationCrashChild(t *testing.T, directory, scenario string) validationCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionValidationProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), validationCrashDirectoryEnv+"="+directory, validationCrashScenarioEnv+"="+scenario)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the existing bounded diagnostic sink; it is inspected only after
	// Wait has joined the child's stderr-copy goroutine.
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
		scanner.Buffer(make([]byte, 4096), (1<<20)+len(validationCrashReadyPrefix)+1)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), validationCrashReadyPrefix) {
				ready <- strings.TrimPrefix(scanner.Text(), validationCrashReadyPrefix)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("validation child did not reach its committed boundary")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("validation child exited before commitment: %s", diagnostics.String())
	}
	var report validationCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.OperationID == "" || len(report.Items) != 4 {
		t.Fatal("invalid validation commitment report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal("force termination", err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("validation child was not forcibly terminated")
	}
	return report
}

func TestCollectionValidationCommittedStateSurvivesProcessKill(t *testing.T) {
	for _, scenario := range []string{"partial-snapshot-and-log", "finalized-valid-snapshot", "rejected-log"} {
		t.Run(scenario, func(t *testing.T) {
			config := testConfig(t)
			report := validationCrashChild(t, config.Storage.Directory, scenario)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal("reopen after validation process termination", err)
			}
			defer s.Close()
			head, exists, err := s.CollectionGet(report.OperationID)
			if err != nil || !exists || head.Validation == nil || head.UploadID != report.UploadID || head.ContentDigest != report.ContentDigest ||
				head.Phase != report.Phase || !reflect.DeepEqual(head.Plan, report.Plan) || !reflect.DeepEqual(*head.Validation, report.Validation) ||
				!head.ActivityAt.Equal(report.ActivityAt) || !head.ExpiresAt.Equal(report.ExpiresAt) {
				t.Fatal("restart changed committed validation identity or progress", err)
			}
			prefix, err := s.fsm.collections.ValidationPage(head.ID, 0, 256)
			if err != nil || !reflect.DeepEqual(prefix, report.Items[:head.Validation.Uploaded]) {
				t.Fatal("restart changed original validation rows", err)
			}
			originalBegin := CollectionValidationBegin{Header: report.Validation.Header, Descriptor: report.Validation.Descriptor}
			retry := validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &originalBegin, nil))
			if !reflect.DeepEqual(retry, head) {
				t.Fatal("original begin retry rebound or renewed the verdict")
			}
			if scenario == "partial-snapshot-and-log" {
				if head.Validation.Uploaded != 2 || !head.Validation.FinalizedAt.IsZero() {
					t.Fatal("fixture did not retain its committed two-row prefix")
				}
				if r := validationApplyCommand(t, s, head, "validation_finalize", nil, nil); !errors.Is(r.Err, ErrCollectionConflict) {
					t.Fatal("partial result finalized", r.Err)
				}
				retry = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, report.Items[:2]))
				if !reflect.DeepEqual(retry, head) {
					t.Fatal("exact prefix retry duplicated rows or renewed expiry")
				}
				// Repeat the original prefix and continue its original suffix in
				// one bounded command; no recompilation occurs after restart.
				head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, report.Items))
			}
			before := head.Clone()
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			if head.Validation.Descriptor != report.Validation.Descriptor || head.Validation.FinalizedAt.IsZero() ||
				head.Validation.Header != report.Validation.Header ||
				scenario != "partial-snapshot-and-log" && !reflect.DeepEqual(head, before) {
				t.Fatal("lost finalization response changed original verdict or lifetime")
			}
			wantPhase := "validated"
			if scenario == "rejected-log" {
				wantPhase = "rejected"
			}
			if head.Phase != wantPhase || head.Validation.Header.Valid != (scenario != "rejected-log") {
				t.Fatal("restart invented a validation outcome")
			}
			all, err := s.fsm.collections.ValidationPage(head.ID, 0, 256)
			if err != nil || !reflect.DeepEqual(all, report.Items) {
				t.Fatal("reconciliation duplicated or changed result rows", err)
			}
			if head.Plan != nil {
				proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
				if err != nil || proof.Descriptor != report.Plan.Descriptor {
					t.Fatal("restart lost original companion plan", err)
				}
			}
			catalog, err := s.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.RLock()
			inactive := catalog.Len() == 0 && len(s.fsm.image.Monitors) == 0 && len(s.fsm.image.Operations) == 0 &&
				s.fsm.image.CatalogMutationSequence == 0 && s.fsm.image.Version == CollectionValidationFormatVersion
			s.fsm.mu.RUnlock()
			if !inactive {
				t.Fatal("crash replay or validation finalization activated resources")
			}
		})
	}
}

const (
	validationHistoryCrashDirectoryEnv = "CPRA_VALIDATION_HISTORY_CRASH_DIRECTORY"
	validationHistoryCrashScenarioEnv  = "CPRA_VALIDATION_HISTORY_CRASH_SCENARIO"
	validationHistoryCrashReadyPrefix  = "CPRA_VALIDATION_HISTORY_COMMITTED "
)

// Use a future command observation time to isolate explicit publication steps
// from the production one-second maintenance ticker. This changes no process
// clock or production timer, and every input/result remains genuinely committed.
func validationHistoryCrashInput(t *testing.T, s *Store, count int) CollectionState {
	t.Helper()
	if s.fsm.image.Authentication == nil {
		if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC().Add(10 * time.Minute)
	c := collectionCreateFixture(t, s, uint64(count))
	c.Create.Actor = "oncall"
	c.Create.CreatedAt, c.Create.ActivityAt, c.Create.ExpiresAt = at, at, at.Add(CollectionInactivityLifetime)
	owner, err := s.ObserveCollectionOwner(context.Background(), c.Create.Actor, at)
	if err != nil || owner == nil {
		t.Fatal("future fixture has no named owner", err)
	}
	c.Create.Owner = owner
	head := validationApplyAllowed(t, collectionCommand(t, s, c, at))
	for first := 1; first <= count; first += 256 {
		commands := make([]Command, 0, 256)
		for ordinal := first; ordinal <= count && ordinal < first+256; ordinal++ {
			item := collectionItemFixture(t, s, head, uint64(ordinal), fmt.Sprintf("item-%05d", ordinal))
			at = at.Add(time.Millisecond)
			commands = append(commands, Command{Kind: "collection", At: at, Collection: &CollectionCommand{
				Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}})
		}
		results, err := s.Submit(context.Background(), commands)
		if err != nil || len(results) != len(commands) {
			t.Fatal("future input batch", err)
		}
		for _, result := range results {
			head = validationApplyAllowed(t, result)
		}
	}
	return head
}

func TestCollectionValidationHistoryProcessCrashHelper(t *testing.T) {
	directory := os.Getenv(validationHistoryCrashDirectoryEnv)
	if directory == "" {
		return
	}
	scenario := os.Getenv(validationHistoryCrashScenarioEnv)
	if scenario != "sealed-log" && scenario != "partial-snapshot" {
		t.Fatal("unknown history crash scenario")
	}
	config := testConfig(t)
	config.Storage.Directory = directory
	admin, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	policy := authenticationBootstrap()
	policy.Principals[0].ExpiresAt = policy.At.Add(24 * time.Hour)
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
	// Deliberately no service Close: the parent performs a forced termination.
	head := validationHistoryCrashInput(t, s, 257)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	for first := 0; first < len(items); first += 256 {
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[first:min(first+256, len(items))]))
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	at := head.ActivityAt.Add(time.Second)
	r := validationPublishStep(t, s, head, at)
	head = validationApplyAllowed(t, r)
	if len(r.Events) != 256 || head.Validation.Published != 256 || head.Validation.HistorySealed {
		t.Fatal("first publication was not exactly one bounded page")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("snapshot first publication page", err)
	}
	if scenario == "sealed-log" {
		r = validationPublishStep(t, s, head, at.Add(time.Second))
		head = validationApplyAllowed(t, r)
		if len(r.Events) != 2 || !head.Validation.HistorySealed || head.Validation.Published != 257 {
			t.Fatal("last row and seal were not committed together")
		}
	}
	report := validationCrashReport{OperationID: head.ID, UploadID: head.UploadID, ContentDigest: head.ContentDigest,
		Phase: head.Phase, ActivityAt: head.ActivityAt, ExpiresAt: head.ExpiresAt, Validation: *head.Validation, Items: items}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 1<<20 || bytes.Contains(raw, []byte(authenticationToken)) ||
		bytes.Contains(raw, []byte(authenticationVerifier(authenticationToken))) || bytes.Contains(raw, []byte("never-write-this-collection-plaintext")) {
		t.Fatal("invalid safe publication IPC", err)
	}
	fmt.Println(validationHistoryCrashReadyPrefix + string(raw))
	select {}
}

func validationHistoryCrashChild(t *testing.T, directory, scenario string) validationCrashReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCollectionValidationHistoryProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), validationHistoryCrashDirectoryEnv+"="+directory, validationHistoryCrashScenarioEnv+"="+scenario)
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
		scanner.Buffer(make([]byte, 4096), (1<<20)+len(validationHistoryCrashReadyPrefix)+1)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), validationHistoryCrashReadyPrefix) {
				ready <- strings.TrimPrefix(scanner.Text(), validationHistoryCrashReadyPrefix)
				return
			}
		}
		ready <- ""
	}()
	var message string
	select {
	case message = <-ready:
	case <-ctx.Done():
		t.Fatal("publication child did not reach its committed boundary")
	}
	if message == "" {
		_ = cmd.Wait()
		waited = true
		t.Fatalf("publication child exited before commitment: %s", diagnostics.String())
	}
	var report validationCrashReport
	if err := json.Unmarshal([]byte(message), &report); err != nil || report.OperationID == "" || len(report.Items) != 257 ||
		report.Validation.Descriptor.Count != 257 || report.Validation.FinalizedAt.IsZero() {
		t.Fatal("invalid history commitment report", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatal("publication child was not forcibly terminated")
	}
	return report
}

func validationHistoryCrashAssertResult(t *testing.T, s *Store, report validationCrashReport, at time.Time) []Event {
	t.Helper()
	expected := CollectionValidationReceipt{Header: report.Validation.Header, Descriptor: report.Validation.Descriptor, FinalizedAt: report.Validation.FinalizedAt}
	var items []CollectionValidationItem
	for after := uint64(0); ; {
		page, err := s.CollectionValidationPage(context.Background(), report.OperationID, after, 100, at)
		if err != nil || !collectionValidationReceiptsEqual(page.Receipt, expected) || len(page.Items) > 100 {
			t.Fatal("original retained summary/page changed", err)
		}
		items = append(items, page.Items...)
		if page.NextAfter == 0 {
			break
		}
		if page.NextAfter <= after || page.NextAfter > 257 {
			t.Fatal("invalid result pagination")
		}
		after = page.NextAfter
	}
	if !reflect.DeepEqual(items, report.Items) {
		t.Fatal("retained rows were replaced, duplicated or lost")
	}
	history, err := s.History().Page("collection-validation/"+report.OperationID, "", 500)
	if err != nil || len(history.Events) != 258 || history.NextCursor != "" {
		t.Fatal("publication replay duplicated or lost history events", err, len(history.Events))
	}
	seen := make(map[string]bool, len(history.Events))
	for _, event := range history.Events {
		if seen[event.ID] || event.CollectionValidation == nil || !event.At.Equal(expected.FinalizedAt) ||
			event.CollectionValidation.OperationID != report.OperationID || event.CollectionValidation.ResultID != expected.Header.ResultID {
			t.Fatal("publication changed the original cohort or event identities")
		}
		seen[event.ID] = true
	}
	return history.Events
}

func TestCollectionValidationHistorySurvivesProcessKillAndCleanup(t *testing.T) {
	for _, scenario := range []string{"sealed-log", "partial-snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			config := testConfig(t)
			report := validationHistoryCrashChild(t, config.Storage.Directory, scenario)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal("reopen publication crash", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			head, exists, err := s.CollectionGet(report.OperationID)
			if err != nil || !exists || head.Validation == nil || !reflect.DeepEqual(*head.Validation, report.Validation) ||
				head.UploadID != report.UploadID || head.ContentDigest != report.ContentDigest ||
				!head.ActivityAt.Equal(report.ActivityAt) || !head.ExpiresAt.Equal(report.ExpiresAt) {
				t.Fatal("restart changed committed publication progress", err)
			}
			at := head.ActivityAt.Add(5 * time.Second)
			if scenario == "partial-snapshot" {
				if head.Validation.Published != 256 || head.Validation.HistorySealed {
					t.Fatal("partial publication advanced without explicit maintenance")
				}
				if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("unsealed publication claimed a complete result", err)
				}
			} else {
				_ = validationHistoryCrashAssertResult(t, s, report, at)
			}
			// Cancellation cannot discard a finalized result that still needs
			// its final history page; maintenance must publish before cleanup.
			cancel := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}
			head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID,
				UploadID: head.UploadID, Cancel: &cancel}, at))
			if scenario == "partial-snapshot" {
				fence := collectionCleanupFor(head)
				if r := collectionCommand(t, s, CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &fence}, at); !errors.Is(r.Err, ErrCollectionConflict) {
					t.Fatal("cleanup discarded unpublished results", r.Err)
				}
				if err := s.maintainCollections(at); err != nil {
					t.Fatal("resume original publication through maintenance", err)
				}
				head, exists, err = s.CollectionGet(head.ID)
				if err != nil || !exists || !head.Validation.HistorySealed || head.Validation.Published != 257 || head.Validation.RemovedRows != 0 {
					t.Fatal("maintenance did not publish exactly the remaining page before cleanup", err)
				}
			}
			// A lost final publication reply reconciles without adding any new
			// event IDs, replacing the verdict, or renewing the original cohort.
			priorPage := head.Clone()
			priorPage.Validation.Published = 256
			r := validationPublishStep(t, s, priorPage, at)
			if r.Err != nil || len(r.Events) != 0 || r.Collection == nil || !reflect.DeepEqual(*r.Collection, head) {
				t.Fatal("publication retry changed the original result", r.Err)
			}
			events := validationHistoryCrashAssertResult(t, s, report, at)
			for step := 0; step < 4; step++ {
				if err := s.maintainCollections(at); err != nil {
					t.Fatal("bounded canceled cleanup", err)
				}
			}
			if _, exists, err := s.CollectionGet(head.ID); exists || !errors.Is(err, ErrOperationExpired) {
				t.Fatal("canceled staging was not reclaimed", err)
			}
			receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || receipt.Phase != "canceled" || receipt.Validation == nil ||
				!collectionValidationReceiptsEqual(*receipt.Validation, CollectionValidationReceipt{Header: report.Validation.Header,
					Descriptor: report.Validation.Descriptor, FinalizedAt: report.Validation.FinalizedAt}) {
				t.Fatal("terminal receipt lost original validation expectation", err)
			}
			if !reflect.DeepEqual(events, validationHistoryCrashAssertResult(t, s, report, at)) {
				t.Fatal("staging cleanup changed retained history")
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal("snapshot after canceled cleanup", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal("reopen retained results after staging cleanup", err)
			}
			reopened, err := s.CollectionReceipt(context.Background(), report.OperationID, at)
			if err != nil || !collectionReceiptsEqual(reopened, receipt) || !reflect.DeepEqual(events, validationHistoryCrashAssertResult(t, s, report, at)) {
				t.Fatal("restart lost terminal receipt or retained result identity", err)
			}
			view, err := s.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.RLock()
			inactive := view.Len() == 0 && len(s.fsm.image.Monitors) == 0 && len(s.fsm.image.Operations) == 0 && s.fsm.image.CatalogMutationSequence == 0
			s.fsm.mu.RUnlock()
			if !inactive {
				t.Fatal("publication/cleanup/restart activated work")
			}
		})
	}
}
