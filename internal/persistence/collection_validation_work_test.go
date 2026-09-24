package persistence

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validationWorkObservation(headers ...CollectionState) time.Time {
	var at time.Time
	for _, h := range headers {
		if h.ActivityAt.After(at) {
			at = h.ActivityAt
		}
		if h.TerminalAt.After(at) {
			at = h.TerminalAt
		}
	}
	return at.Add(time.Second)
}

func TestCollectionValidationWorkDetachedMetadataAndNoIO(t *testing.T) {
	s := openCatalogMemory(t)
	partialPlan := requestReceiptPartialPlan(t, s, requestReceiptInput(t, s, 2))
	finalized := requestReceiptResult(t, s, requestReceiptInput(t, s, 2), 2)
	finalized = requestTestArtifact(t, s, finalized, CollectionCommand{Action: "validation_finalize", ValidationID: finalized.Validation.Header.ResultID})
	interrupted := requestReceiptInput(t, s, 1)
	interruption := requestTestInterrupt(interrupted, "validationInterrupted")
	interrupted = validationApplyAllowed(t, collectionCommand(t, s, interruption, interruption.ValidationInterruption.At))
	_ = validationHistoryCrashInput(t, s, 1) // Ordinary upload is omitted.
	at := validationWorkObservation(partialPlan, finalized, interrupted)
	before, err := json.Marshal(s.fsm.image)
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CollectionValidationWork(context.Background(), at)
	if err != nil || len(work) != 3 {
		t.Fatal("bounded request selector", len(work), err)
	}
	if work[0].OperationID != partialPlan.ID || work[0].ResultFinalized || work[0].Progress.Plan == nil ||
		work[1].OperationID != finalized.ID || !work[1].ResultFinalized || work[1].Progress.Validation == nil ||
		work[2].OperationID != interrupted.ID || work[2].Phase != "interrupted" || work[2].Request.Interruption == nil {
		t.Fatal("selector changed original phase/result metadata")
	}
	encoded, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Secret", "Payload", authenticationToken, "private-inventory-key", base64.StdEncoding.EncodeToString(partialPlan.Secret.Ciphertext)} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("work metadata exposed a protected payload")
		}
	}
	work[0].Request.Claim.ID = uuid.NewString()
	work[0].Progress.Plan.PlanID = uuid.NewString()
	work[0].Progress.ValidationRequest.RunID = uuid.NewString()
	work[1].Progress.Validation.ResultID = uuid.NewString()
	work[2].Request.Interruption.Reason = "changed"
	after, err := json.Marshal(s.fsm.image)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("selector or detached metadata mutation changed state")
	}
	planApplyStateUnchanged(t, s, partialPlan)
	planApplyStateUnchanged(t, s, finalized)
	planApplyStateUnchanged(t, s, interrupted)

	// A minimal read-only owner has neither ledger nor history handles. This
	// exercises the real selector without replacing any storage method.
	policy, err := s.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	probe := &Store{stop: make(chan struct{}), fsm: &machine{image: image{
		Version: CollectionValidationRequestFormatVersion, OperationEpoch: s.fsm.image.OperationEpoch,
		OperationHighWater: s.fsm.image.OperationHighWater, Authentication: &policy,
		Collections: map[string]CollectionState{partialPlan.ID: partialPlan.Clone()},
	}}}
	if got, err := probe.CollectionValidationWork(context.Background(), at); err != nil || len(got) != 1 {
		t.Fatal("metadata selector requires payload/history I/O", err)
	}
	if err := probe.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("registration requires payload/history I/O", err)
	}
}

func TestCollectionValidationWorkOrderExpiryAndBound(t *testing.T) {
	s := openCatalogMemory(t)
	var headers []CollectionState
	for range 12 {
		headers = append(headers, validationHistoryCrashInput(t, s, 1))
	}
	common := validationWorkObservation(headers...)
	for j := range headers {
		head := headers[j]
		at := common
		if j == len(headers)-1 {
			at = at.Add(-time.Millisecond) // Primary order precedes sequence order.
		}
		r := CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
			Authority: *head.Owner, CapabilitiesDigest: strings.Repeat("c", 64), RequestedAt: at}
		headers[j] = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: head.ID,
			UploadID: head.UploadID, ValidationRequest: &r}, at))
	}
	work, err := s.CollectionValidationWork(context.Background(), common.Add(time.Second))
	if err != nil || len(work) != len(headers) || work[0].OperationID != headers[len(headers)-1].ID {
		t.Fatal("request time order", err)
	}
	for j := 0; j < len(headers)-1; j++ {
		if work[j+1].OperationID != headers[j].ID {
			t.Fatal("operation sequence sorted lexically", j, work[j+1].OperationID, headers[j].ID)
		}
	}
	// Expiry is returned for the coordinator's decision; selection cannot renew it.
	late := headers[0].ExpiresAt.Add(time.Hour)
	if expired, err := s.CollectionValidationWork(context.Background(), late); err != nil || len(expired) != len(headers) ||
		!expired[1].ExpiresAt.Equal(headers[0].ExpiresAt) {
		t.Fatal("expired headers hidden or renewed", err)
	}
	if got, err := s.CollectionValidationWork(context.Background(), common.Add(-time.Nanosecond)); !errors.Is(err, ErrCollectionConflict) || got != nil {
		t.Fatal("backdated work observation", err)
	}
	// Oversized map indicates corruption; it is rejected before any partial list
	// or an unbounded allocation, even if most headers are ordinary uploads.
	s.fsm.mu.Lock()
	for j := len(headers); j <= maxCollectionOperations; j++ {
		head := headers[0].Clone()
		head.ID = operationHandle(s.fsm.image.OperationEpoch, uint64(j+1))
		s.fsm.image.Collections[head.ID] = head
	}
	s.fsm.mu.Unlock()
	if got, err := s.CollectionValidationWork(context.Background(), late); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
		t.Fatal("corrupt oversized map was partially selected", err)
	}
}

func TestCollectionValidationWorkOldEpochAndAuthorityRemainObservations(t *testing.T) {
	s := openCatalogMemory(t)
	head := requestReceiptInput(t, s, 1)
	interruption := requestTestInterrupt(head, "authorityChanged")
	head = validationApplyAllowed(t, collectionCommand(t, s, interruption, interruption.ValidationInterruption.At))
	at := validationWorkObservation(head)
	s.fsm.mu.Lock()
	// Explicit restore keeps terminal facts while changing the active epoch.
	// Selection is metadata-only and must not hide that original terminal entry.
	s.fsm.image.OperationEpoch = uuid.NewString()
	s.fsm.image.Authentication.Revision = uuid.NewString()
	s.fsm.image.Authentication.Principals[0].Revoked = true
	s.fsm.mu.Unlock()
	work, err := s.CollectionValidationWork(context.Background(), at)
	if err != nil || len(work) != 1 || work[0].OperationID != head.ID || work[0].Phase != "interrupted" ||
		!reflect.DeepEqual(work[0].Request, *head.ValidationRequest) {
		t.Fatal("selector hid old terminal/revoked metadata", err)
	}
	if _, err := s.CollectionValidationWork(context.Background(), head.TerminalAt.Add(-time.Nanosecond)); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("terminal observation accepted a stale time", err)
	}
}

func TestCollectionValidationWorkHealthAndRegistration(t *testing.T) {
	for _, condition := range []string{"administrative", "opening", "store-error", "fsm-error", "bootstrap", "restore", "reset", "nil-fsm", "closed"} {
		t.Run(condition, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := requestReceiptInput(t, s, 1)
			at := validationWorkObservation(head)
			if condition == "closed" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.mu.Lock()
				s.fsm.mu.Lock()
				oldFSM := s.fsm
				switch condition {
				case "administrative":
					s.administrative = true
				case "opening":
					s.opening = true
				case "store-error":
					s.err = errors.New("private storage diagnosis")
				case "fsm-error":
					s.fsm.err = errors.New("private state diagnosis")
				case "bootstrap":
					s.fsm.image.Bootstrap = &BootstrapState{Phase: "begin"}
				case "restore":
					s.fsm.image.Restore = &RestoreState{Phase: "begin"}
				case "reset":
					s.fsm.image.Authentication.ResetRequired = true
				case "nil-fsm":
					s.fsm = nil
				}
				oldFSM.mu.Unlock()
				s.mu.Unlock()
				if condition == "nil-fsm" {
					defer func() { s.mu.Lock(); s.fsm = oldFSM; s.mu.Unlock() }()
				}
			}
			if got, err := s.CollectionValidationWork(context.Background(), at); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
				t.Fatal("unavailable store selected metadata", err)
			}
			if err := s.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("unavailable store registered coordinator", err)
			}
			if s.validationRunID != "" {
				t.Fatal("failed health check consumed registration")
			}
		})
	}
	for _, at := range []time.Time{{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		s := openCatalogMemory(t)
		if _, err := s.CollectionValidationWork(context.Background(), at); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("invalid time accepted", err)
		}
	}
}

func TestCollectionValidationCoordinatorSingleRegistrationAndCancellation(t *testing.T) {
	s := openCatalogMemory(t)
	before := s.Status().CommittedIndex
	var wg sync.WaitGroup
	results := make(chan error, 24)
	for range cap(results) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString())
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrCollectionCoordinatorRegistered) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || s.Status().CommittedIndex != before {
		t.Fatal("registration was not one-shot/local", succeeded)
	}
	if err := s.RegisterCollectionValidationCoordinator(context.Background(), s.validationRunID); !errors.Is(err, ErrCollectionCoordinatorRegistered) {
		t.Fatal("same run reacquired registration", err)
	}
	s.MarkUnavailable(errors.New("private error"))
	if err := s.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("duplicate registration bypassed current health", err)
	}
	for _, operation := range []string{"read", "register"} {
		for _, owner := range []string{"store", "fsm"} {
			t.Run(operation+"/"+owner, func(t *testing.T) {
				s := openCatalogMemory(t)
				base, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &authenticationWaitingContext{Context: base, waiting: make(chan struct{})}
				lock := &s.mu
				if owner == "fsm" {
					lock = &s.fsm.mu
				}
				lock.Lock()
				defer lock.Unlock()
				done := make(chan error, 1)
				go func() {
					if operation == "register" {
						done <- s.RegisterCollectionValidationCoordinator(ctx, uuid.NewString())
					} else {
						_, err := s.CollectionValidationWork(ctx, time.Now().UTC())
						done <- err
					}
				}()
				select {
				case <-ctx.waiting:
				case <-time.After(time.Second):
					t.Fatal("did not reach ownership wait")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("canceled ownership wait blocked")
				}
				if s.validationRunID != "" {
					t.Fatal("canceled registration consumed lifetime slot")
				}
			})
		}
	}
}

func TestCollectionValidationWorkRealRaftAndReopen(t *testing.T) {
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CollectionValidationWork(context.Background(), time.Now().UTC()); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("administrative owner admitted service work", err)
	}
	if err := admin.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("administrative owner registered service coordinator", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head := requestReceiptInput(t, s, 2)
	at := validationWorkObservation(head)
	beforeIndex := s.Status().CommittedIndex
	beforeTransaction := ledgerTestTransactionID(t, s.fsm.collections)
	work, err := s.CollectionValidationWork(context.Background(), at)
	if err != nil || len(work) != 1 || work[0].OperationID != head.ID {
		t.Fatal("Raft metadata observation", err)
	}
	if err := s.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if s.Status().CommittedIndex != beforeIndex || ledgerTestTransactionID(t, s.fsm.collections) != beforeTransaction {
		t.Fatal("metadata observation/registration committed durable changes")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if again, err := reopened.CollectionValidationWork(context.Background(), at); err != nil || !reflect.DeepEqual(again, work) {
		t.Fatal("restart changed work metadata", err)
	}
	if err := reopened.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("new Store inherited old in-process registration", err)
	}
	if err := reopened.raft.Shutdown().Error(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.CollectionValidationWork(context.Background(), at); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("non-leader selected work", err)
	}
	if err := reopened.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("non-leader registration bypassed health", err)
	}
}

func TestCollectionValidationWorkRejectsMalformedHeadersWithoutPartialResults(t *testing.T) {
	for _, mutation := range []string{"id", "request", "claim-time"} {
		t.Run(mutation, func(t *testing.T) {
			s := openCatalogMemory(t)
			first := requestReceiptInput(t, s, 1)
			second := requestReceiptInput(t, s, 1)
			s.fsm.mu.Lock()
			bad := second.Clone()
			switch mutation {
			case "id":
				bad.ID = operationHandle(s.fsm.image.OperationEpoch, 999)
			case "request":
				bad.ValidationRequest.ID = "malformed"
			case "claim-time":
				bad.ValidationRequest.Claim.At = bad.ActivityAt.Add(time.Hour)
			}
			s.fsm.image.Collections[second.ID] = bad
			s.fsm.mu.Unlock()
			if work, err := s.CollectionValidationWork(context.Background(), validationWorkObservation(first, second)); !errors.Is(err, ErrCollectionUnavailable) || work != nil {
				t.Fatal("malformed selected header returned partial work", fmt.Sprint(err))
			}
		})
	}
}
