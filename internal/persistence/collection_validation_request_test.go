package persistence

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func requestTestBegin(t *testing.T, s *Store, head CollectionState, authority OperatorAuthority) CollectionState {
	t.Helper()
	at := head.ActivityAt.Add(time.Millisecond)
	r := CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), RequestedAt: at}
	return validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: head.ID,
		UploadID: head.UploadID, ValidationRequest: &r}, at))
}

func requestTestClaim(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	at := head.ActivityAt.Add(time.Millisecond)
	claim := CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}
	return validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_claim", OperationID: head.ID,
		UploadID: head.UploadID, ValidationFence: CollectionValidationRequestFenceFor(head), ValidationClaim: &claim}, at))
}

func requestTestInterrupt(head CollectionState, reason string) CollectionCommand {
	p := collectionCleanupFor(head)
	return CollectionCommand{Action: "validation_interrupt", OperationID: head.ID, UploadID: head.UploadID,
		ValidationFence: CollectionValidationRequestFenceFor(head), ValidationProgress: &p,
		ValidationInterruption: &CollectionValidationInterruption{ID: uuid.NewString(), Reason: reason, At: head.ActivityAt.Add(time.Millisecond)}}
}

func requestTestArtifact(t *testing.T, s *Store, head CollectionState, c CollectionCommand) CollectionState {
	t.Helper()
	c.OperationID, c.UploadID, c.ValidationFence = head.ID, head.UploadID, CollectionValidationRequestFenceFor(head)
	return validationApplyAllowed(t, collectionCommand(t, s, c, head.ActivityAt.Add(time.Millisecond)))
}

func TestCollectionValidationRequestImmutableClaimAndRetry(t *testing.T) {
	s := openCatalogMemory(t)
	input, authority := validationApplyInput(t, s, 2)
	requested := requestTestBegin(t, s, input, authority)
	if requested.Phase != "validating" || requested.ValidationRequest.Claim != nil || requested.Plan != nil || requested.Validation != nil ||
		s.fsm.image.Version != CollectionValidationRequestFormatVersion || len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 {
		t.Fatal("request activated resources or changed its initial disposition")
	}
	claimAt := requested.ActivityAt.Add(time.Millisecond)
	first := CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: claimAt}
	c := CollectionCommand{Action: "validation_claim", OperationID: requested.ID, UploadID: requested.UploadID,
		ValidationFence: CollectionValidationRequestFenceFor(requested), ValidationClaim: &first}
	competing := c
	other := first
	other.ID, other.RunID = uuid.NewString(), uuid.NewString()
	competing.ValidationClaim = &other
	results := submit(t, s, Command{Kind: "collection", At: claimAt, Collection: &c}, Command{Kind: "collection", At: claimAt, Collection: &competing})
	claimed := validationApplyAllowed(t, results[0])
	if !errors.Is(results[1].Err, ErrCollectionConflict) || claimed.ValidationRequest.Claim.ID != first.ID {
		t.Fatal("competing attempt took over original claim", results[1].Err)
	}
	// A retry keeps original metadata but supplies its current observation time.
	retry := validationApplyAllowed(t, collectionCommand(t, s, c, claimAt.Add(time.Second)))
	if !reflect.DeepEqual(retry, claimed) {
		t.Fatal("claim reconciliation renewed or replaced state")
	}
	request := requested.ValidationRequest.Clone()
	retry = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: input.ID,
		UploadID: input.UploadID, ValidationRequest: &request}, request.RequestedAt))
	if !reflect.DeepEqual(retry, claimed) {
		t.Fatal("request retry lost its committed attempt")
	}
	request.ID = uuid.NewString()
	if result := collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: input.ID,
		UploadID: input.UploadID, ValidationRequest: &request}, request.RequestedAt); !errors.Is(result.Err, ErrCollectionConflict) {
		t.Fatal("different request replaced original", result.Err)
	}
	clone := claimed.Clone()
	clone.ValidationRequest.Claim.ID = uuid.NewString()
	clone.ValidationRequest.Authority.Actor = "someone-else"
	planApplyStateUnchanged(t, s, claimed)
	if err := s.fsm.checkCollectionValidationRequestFence(input, CollectionCommand{}, input.ActivityAt); err != nil {
		t.Fatal("unrequested historical path changed", err)
	}
	if err := s.fsm.checkCollectionValidationRequestFence(input, CollectionCommand{ValidationFence: CollectionValidationRequestFenceFor(claimed)}, input.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("new fence accepted on historical path", err)
	}
}

func TestCollectionValidationRequestAuthorityAndExpiry(t *testing.T) {
	for _, mode := range []string{"request-revision", "claim-revision", "claim-principal-expiry", "claim-upload-expiry", "missing-owner", "wrong-owner", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 2)
			if mode == "missing-owner" || mode == "incomplete" {
				// These represent accepted legacy/incomplete uploads, not malformed commands.
				c := collectionCreateFixture(t, s, 2)
				c.Create.Actor = authority.Actor
				if mode == "incomplete" {
					c.Create.Owner = &authority
				}
				head = validationApplyAllowed(t, collectionCommand(t, s, c, c.Create.CreatedAt))
				item := collectionItemFixture(t, s, head, 1, "one")
				head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Millisecond)))
				if mode == "missing-owner" {
					item = collectionItemFixture(t, s, head, 2, "two")
					head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Millisecond)))
				}
			}
			at := head.ActivityAt.Add(time.Millisecond)
			request := CollectionValidationRequest{ID: uuid.NewString(), InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
				Authority: authority, CapabilitiesDigest: strings.Repeat("d", 64), RequestedAt: at}
			c := CollectionCommand{Action: "validation_request", OperationID: head.ID, UploadID: head.UploadID, ValidationRequest: &request}
			want := ErrCollectionConflict
			if strings.HasPrefix(mode, "claim-") {
				head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
				c = CollectionCommand{Action: "validation_claim", OperationID: head.ID, UploadID: head.UploadID,
					ValidationFence: &CollectionValidationRequestFence{RequestID: head.ValidationRequest.ID}, ValidationClaim: head.ValidationRequest.Claim}
				at = head.ActivityAt.Add(time.Millisecond)
			}
			if mode == "request-revision" || mode == "claim-revision" {
				state, err := s.Authentication()
				if err != nil {
					t.Fatal(err)
				}
				replacement := lifecycleReplacement(state)
				replacement.At = at
				if r := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); r.Err != nil {
					t.Fatal(r.Err)
				}
				want = ErrAuthenticationConflict
			}
			if mode == "claim-principal-expiry" {
				state, err := s.Authentication()
				if err != nil {
					t.Fatal(err)
				}
				at, want = state.Principals[0].ExpiresAt, ErrOperatorAuthorityDenied
			}
			if mode == "claim-upload-expiry" {
				at, want = head.ExpiresAt, ErrOperationExpired
			}
			if mode == "wrong-owner" {
				request.Authority.Actor = "another-operator"
				want = ErrOperatorAuthorityDenied
			}
			if mode == "missing-owner" {
				want = ErrOperatorAuthorityDenied
			}
			if r := collectionCommand(t, s, c, at); !errors.Is(r.Err, want) {
				t.Fatal("authority/input fence", r.Err, "want", want)
			}
			planApplyStateUnchanged(t, s, head)
			if s.fsm.err != nil {
				t.Fatal("admission rejection poisoned storage")
			}
		})
	}
}

func TestCollectionValidationRequestInterruptPreservesPartialPlan(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 3)
	head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
	begin, parts := planApplyArtifact(t, s, head)
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_begin", PlanBegin: &begin})
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_append", PlanID: begin.Header.PlanID, PlanFragment: &parts[0]})
	beforeInput, beforePlan, beforeResult := validationLedgerStreams(t, s.fsm.collections)
	interrupt := requestTestInterrupt(head, "authorityChanged")
	stale := *interrupt.ValidationProgress
	stale.ActivityAt = stale.ActivityAt.Add(-time.Millisecond)
	bad := interrupt
	bad.ValidationProgress = &stale
	if r := collectionCommand(t, s, bad, interrupt.ValidationInterruption.At); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("stale progress interrupted current prefix", r.Err)
	}
	state, err := s.Authentication()
	if err != nil {
		t.Fatal(err)
	}
	replacement := lifecycleReplacement(state)
	replacement.At = interrupt.ValidationInterruption.At
	if r := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); r.Err != nil {
		t.Fatal(r.Err)
	}
	stopped := validationApplyAllowed(t, collectionCommand(t, s, interrupt, interrupt.ValidationInterruption.At))
	if stopped.Phase != "interrupted" || stopped.ValidationRequest.Interruption.Reason != "authorityChanged" ||
		!stopped.ActivityAt.Equal(head.ActivityAt) || !stopped.ExpiresAt.Equal(head.ExpiresAt) ||
		!reflect.DeepEqual(stopped.Plan, head.Plan) || !reflect.DeepEqual(stopped.Secret, head.Secret) {
		t.Fatal("interruption replaced original artifact or renewed input")
	}
	input, plan, validation := validationLedgerStreams(t, s.fsm.collections)
	if string(input) != string(beforeInput) || string(plan) != string(beforePlan) || string(validation) != string(beforeResult) {
		t.Fatal("interruption modified a retained namespace")
	}
	r := collectionCommand(t, s, interrupt, interrupt.ValidationInterruption.At)
	if !reflect.DeepEqual(validationApplyAllowed(t, r), stopped) || len(r.Events) != 0 {
		t.Fatal("interruption retry changed state or repeated audit")
	}
	changed := *interrupt.ValidationInterruption
	changed.ID = uuid.NewString()
	bad = interrupt
	bad.ValidationInterruption = &changed
	if r := collectionCommand(t, s, bad, changed.At); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("different interruption overwrote original", r.Err)
	}
	appendCommand := CollectionCommand{Action: "plan_append", OperationID: head.ID, UploadID: head.UploadID,
		PlanID: begin.Header.PlanID, PlanFragment: &parts[1], ValidationFence: CollectionValidationRequestFenceFor(head)}
	if r := collectionCommand(t, s, appendCommand, stopped.TerminalAt.Add(time.Millisecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("interrupted plan continued", r.Err)
	}
	planApplyStateUnchanged(t, s, stopped)
	if len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 {
		t.Fatal("inactive request activated resources")
	}
}

func TestCollectionValidationRequestVerdictProfileAndFinalOutcome(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 2)
	head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
	_, begin, items := validationApplyIntent(t, s, head, authority, false)
	bad := begin
	bad.Header.CapabilitiesDigest = strings.Repeat("b", 64)
	c := CollectionCommand{Action: "validation_begin", OperationID: head.ID, UploadID: head.UploadID,
		ValidationBegin: &bad, ValidationFence: CollectionValidationRequestFenceFor(head)}
	if r := collectionCommand(t, s, c, head.ActivityAt.Add(time.Millisecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("changed profile accepted", r.Err)
	}
	c.ValidationBegin = &begin
	c.ValidationFence = nil
	if r := collectionCommand(t, s, c, head.ActivityAt.Add(time.Millisecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("claim-free artifact write accepted", r.Err)
	}
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_begin", ValidationBegin: &begin})
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_append", ValidationID: begin.Header.ResultID, ValidationItems: items})
	head = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_finalize", ValidationID: begin.Header.ResultID})
	claimRetry := CollectionCommand{Action: "validation_claim", OperationID: head.ID, UploadID: head.UploadID,
		ValidationFence: &CollectionValidationRequestFence{RequestID: head.ValidationRequest.ID}, ValidationClaim: head.ValidationRequest.Claim}
	if r := collectionCommand(t, s, claimRetry, head.ActivityAt.Add(time.Millisecond)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("completed verdict accepted a new execution claim", r.Err)
	}
	interrupt := requestTestInterrupt(head, "coordinatorRestarted")
	if r := collectionCommand(t, s, interrupt, interrupt.ValidationInterruption.At); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("completed verdict overwritten by interruption", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
	// Original request observation is still a read-only fact after completion.
	request := head.ValidationRequest.Clone()
	request.Claim = nil
	r := collectionCommand(t, s, CollectionCommand{Action: "validation_request", OperationID: head.ID,
		UploadID: head.UploadID, ValidationRequest: &request}, request.RequestedAt)
	if !reflect.DeepEqual(validationApplyAllowed(t, r), head) {
		t.Fatal("request retry changed completed outcome")
	}
}

func TestCollectionValidationRequestCommandAndStateValidation(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 1)
	head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
	original := head.Clone()
	for name, mutate := range map[string]func(*CollectionState){
		"missing owner":         func(s *CollectionState) { s.Owner = nil },
		"wrong input":           func(s *CollectionState) { s.ValidationRequest.InputProgressDigest = strings.Repeat("a", 64) },
		"wrong actor":           func(s *CollectionState) { s.ValidationRequest.Authority.Actor = "another" },
		"before request":        func(s *CollectionState) { s.ValidationRequest.Claim.At = s.CreatedAt },
		"unknown phase":         func(s *CollectionState) { s.Phase = "uploading" },
		"invalid run":           func(s *CollectionState) { s.ValidationRequest.Claim.RunID = "invalid" },
		"unmarked interruption": func(s *CollectionState) { s.Phase = "interrupted" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := head.Clone()
			mutate(&bad)
			if bad.validate() == nil {
				t.Fatal("invalid state accepted")
			}
		})
	}
	planApplyStateUnchanged(t, s, original)
	interrupt := requestTestInterrupt(head, "validationInterrupted")
	for name, mutate := range map[string]func(*CollectionCommand){
		"mixed claim":      func(c *CollectionCommand) { c.ValidationClaim = head.ValidationRequest.Claim },
		"mixed create":     func(c *CollectionCommand) { c.Create = &head },
		"missing progress": func(c *CollectionCommand) { c.ValidationProgress = nil },
		"foreign fence": func(c *CollectionCommand) {
			c.ValidationFence = &CollectionValidationRequestFence{RequestID: uuid.NewString()}
		},
		"mixed result":      func(c *CollectionCommand) { c.ValidationID = uuid.NewString() },
		"mixed plan":        func(c *CollectionCommand) { c.PlanID = uuid.NewString() },
		"foreign operation": func(c *CollectionCommand) { c.OperationID = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := interrupt
			mutate(&bad)
			if bad.validate(interrupt.ValidationInterruption.At) == nil {
				t.Fatal("mixed/invalid command accepted")
			}
		})
	}
	for _, reason := range []string{"", "arbitrary-user-reason", "private-secret"} {
		bad := *interrupt.ValidationInterruption
		bad.Reason = reason
		if bad.validate() == nil {
			t.Fatal("unbounded interruption reason accepted")
		}
	}
	if _, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt); err != nil {
		t.Fatal("read-only tests changed authority", err)
	}
}
