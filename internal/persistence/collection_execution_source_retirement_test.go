package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sourceRetirementMarker(s CollectionState) *CollectionExecutionSourceRetirementState {
	at := s.ExecutionRetirement.UpdatedAt.Add(time.Second)
	return &CollectionExecutionSourceRetirementState{Version: 1, InputDigest: s.ProgressDigest, PlanDigest: s.Plan.ProgressDigest,
		ValidationDigest: s.Validation.ProgressDigest, StartedAt: at, UpdatedAt: at}
}

func sourceRetirementRemoveValidation(s *CollectionState) {
	s.Validation.RemovedRows, s.Validation.RemovedBytes = s.Validation.Uploaded, s.Validation.EncodedBytes
	s.ExecutionRetirement.Sources.ValidationDigest = CollectionValidationInitialDigest()
}

func sourceRetirementRemovePlan(s *CollectionState) {
	s.Plan.RemovedFragments, s.Plan.RemovedBytes = s.Plan.UploadedFragments, s.Plan.EncodedBytes
	s.ExecutionRetirement.Sources.PlanDigest = collectionPlanInitialDigest()
}

func TestCollectionExecutionSourceRetirementModelOrderingAndIdentity(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 3)
	original := executionRetirementRecoveryCapture(t, s, head)
	f := executionRetirementRecoveryPrefix(t, original, false, head.Execution.Processed, false)
	base := f.head.Clone()
	base.ExecutionRetirement.Sources = sourceRetirementMarker(base)
	for _, stage := range []string{"initial", "validation-prefix", "validation-empty", "plan-prefix", "plan-empty", "input-prefix", "input-empty"} {
		t.Run(stage, func(t *testing.T) {
			head := base.Clone()
			sources := head.ExecutionRetirement.Sources
			switch stage {
			case "validation-prefix":
				head.Validation.RemovedRows, head.Validation.RemovedBytes = 1, 1
				sources.ValidationDigest = strings.Repeat("a", 64)
			case "validation-empty", "plan-prefix", "plan-empty", "input-prefix", "input-empty":
				sourceRetirementRemoveValidation(&head)
			}
			switch stage {
			case "plan-prefix":
				head.Plan.RemovedFragments, head.Plan.RemovedBytes = 1, 1
				sources.PlanDigest = strings.Repeat("b", 64)
			case "plan-empty", "input-prefix", "input-empty":
				sourceRetirementRemovePlan(&head)
			}
			switch stage {
			case "input-prefix":
				head.RemovedRows, head.RemovedBytes = 1, 1
				sources.InputDigest = strings.Repeat("c", 64)
			case "input-empty":
				head.RemovedRows, head.RemovedBytes = head.Uploaded, head.EncodedBytes
				sources.InputDigest = collectionInitialDigest()
			}
			if err := head.validate(); err != nil || !head.ExecutionRetirement.complete(head) {
				t.Fatal("legal terminal-only source stage rejected", err)
			}
			if !reflect.DeepEqual(head.Activation, base.Activation) || !reflect.DeepEqual(head.Execution, base.Execution) || !reflect.DeepEqual(head.ExecutionResult, base.ExecutionResult) ||
				head.ProgressDigest != base.ProgressDigest || head.Plan.Descriptor != base.Plan.Descriptor || head.Validation.Descriptor != base.Validation.Descriptor {
				t.Fatal("source progress replaced original identities")
			}
		})
	}
	for name, change := range map[string]func(*CollectionState){
		"plan-before-validation": func(s *CollectionState) {
			s.Plan.RemovedFragments, s.Plan.RemovedBytes = 1, 1
			s.ExecutionRetirement.Sources.PlanDigest = strings.Repeat("a", 64)
		},
		"input-before-plan": func(s *CollectionState) {
			sourceRetirementRemoveValidation(s)
			s.RemovedRows, s.RemovedBytes = 1, 1
			s.ExecutionRetirement.Sources.InputDigest = strings.Repeat("a", 64)
		},
		"missing-marker": func(s *CollectionState) { sourceRetirementRemoveValidation(s); s.ExecutionRetirement.Sources = nil },
		"incomplete-execution": func(s *CollectionState) {
			p, _ := NewCollectionExecutionRetirementCheckpoint(s.Execution.Binding, s.ItemCount, s.Execution.StartedAt)
			s.ExecutionRetirement.Checkpoint = &p
		},
		"initial-input-digest":      func(s *CollectionState) { s.ExecutionRetirement.Sources.InputDigest = strings.Repeat("a", 64) },
		"initial-plan-digest":       func(s *CollectionState) { s.ExecutionRetirement.Sources.PlanDigest = strings.Repeat("a", 64) },
		"initial-validation-digest": func(s *CollectionState) { s.ExecutionRetirement.Sources.ValidationDigest = strings.Repeat("a", 64) },
		"empty-prefix-digest": func(s *CollectionState) {
			sourceRetirementRemoveValidation(s)
			s.ExecutionRetirement.Sources.ValidationDigest = s.Validation.ProgressDigest
		},
		"invalid-hash":   func(s *CollectionState) { s.ExecutionRetirement.Sources.InputDigest = "malformed" },
		"future-version": func(s *CollectionState) { s.ExecutionRetirement.Sources.Version++ },
		"start-before-execution-retired": func(s *CollectionState) {
			s.ExecutionRetirement.Sources.StartedAt = s.ExecutionRetirement.UpdatedAt.Add(-time.Nanosecond)
		},
		"backward-source-time": func(s *CollectionState) {
			s.ExecutionRetirement.Sources.UpdatedAt = s.ExecutionRetirement.Sources.StartedAt.Add(-time.Nanosecond)
		},
		"altered-original-plan": func(s *CollectionState) { s.Plan.Descriptor.Digest = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			head := base.Clone()
			change(&head)
			if err := head.validate(); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("invalid source retirement model accepted", err)
			}
		})
	}
	// Later committed history expiry does not rewrite the original source times.
	later := base.Clone()
	later.ExecutionResult.HistoryExpiredAt = later.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)
	if err := later.validate(); err != nil || !collectionExecutionSourcesEqual(later.ExecutionRetirement.Sources, base.ExecutionRetirement.Sources) {
		t.Fatal("later history expiry invalidated original source progress", err)
	}
	clone := base.Clone()
	clone.ExecutionRetirement.Sources.InputDigest = strings.Repeat("a", 64)
	if collectionExecutionRetirementsEqual(clone.ExecutionRetirement, base.ExecutionRetirement) || base.ExecutionRetirement.Sources.InputDigest != base.ProgressDigest {
		t.Fatal("source marker clone/equality lost its committed identity")
	}
	raw, err := json.Marshal(f.head.ExecutionRetirement)
	if err != nil || strings.Contains(string(raw), `"sources"`) {
		t.Fatal("format12 retirement encoding acquired an absent source marker", err)
	}
	fence := CollectionExecutionRetirementFenceFor(base)
	fence.Retirement.Sources.Version++
	if err := fence.validate(base.Execution.Binding, base.ExecutionRetirement.Sources.UpdatedAt); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("command fence accepted malformed nested source marker", err)
	}
}

func TestCollectionExecutionSourceRetirementUnbegunAndPreparedGuards(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Minute)
	head.ExecutionRetirement = &CollectionExecutionRetirementState{Version: 1, Result: head.ExecutionResult.Clone(), StartedAt: at, UpdatedAt: at}
	head.ExecutionRetirement.Sources = sourceRetirementMarker(head)
	if head.Execution != nil || head.ExecutionRetirement.Checkpoint != nil || head.validate() != nil {
		t.Fatal("canceled-before-begin cannot enter terminal-only source cleanup")
	}
	original := executionRetirementRecoverySource(t)
	f := executionRetirementRecoveryPrefix(t, original, false, original.head.Execution.Processed, false)
	f.head.ExecutionRetirement.Sources = sourceRetirementMarker(f.head)
	if f.head.Execution.Prepared == nil || f.head.ExecutionRetirement.PreparedRemoved {
		t.Fatal("fixture lacks original unaccepted prepared record")
	}
	if err := f.head.validate(); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("source cleanup accepted an unretired prepared record", err)
	}
}

func TestCollectionExecutionSourceRetirementRecoveryEmptyNamespaces(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 3)
	original := executionRetirementRecoveryCapture(t, s, head)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRetirementRecoveryPrefix(t, original, disk, head.Execution.Processed, false)
			f.head.ExecutionRetirement.Sources = sourceRetirementMarker(f.head)
			v := f.head.Validation
			removed, err := f.ledger.DeleteValidationPage(f.head.ID, v.Uploaded, v.EncodedBytes)
			if err != nil || removed.More || removed.Rows != v.Uploaded || removed.EncodedBytes != v.EncodedBytes {
				t.Fatal("validation fixture deletion", removed, err)
			}
			sourceRetirementRemoveValidation(&f.head)
			for p := f.head.Plan; p.RemovedFragments < p.UploadedFragments; {
				removed, err = f.ledger.DeletePlanPage(f.head.ID, p.UploadedFragments-p.RemovedFragments, p.EncodedBytes-p.RemovedBytes)
				if err != nil || removed.Rows == 0 {
					t.Fatal("plan fixture deletion", removed, err)
				}
				p.RemovedFragments += removed.Rows
				p.RemovedBytes += removed.EncodedBytes
			}
			f.head.ExecutionRetirement.Sources.PlanDigest = collectionPlanInitialDigest()
			removed, err = f.ledger.DeletePage(f.head.ID, f.head.Uploaded, f.head.EncodedBytes)
			if err != nil || removed.More || removed.Rows != f.head.Uploaded || removed.EncodedBytes != f.head.EncodedBytes {
				t.Fatal("input fixture deletion", removed, err)
			}
			f.head.RemovedRows, f.head.RemovedBytes = removed.Rows, removed.EncodedBytes
			f.head.ExecutionRetirement.Sources.InputDigest = collectionInitialDigest()
			f.image.Version = CollectionExecutionSourceRetirementFormatVersion
			f.image.Collections[f.head.ID] = f.head
			view, err := f.ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if err := validateCollectionRows(f.image, view); err != nil {
				t.Fatal("terminal-only empty source snapshot rejected", err)
			}
			got, err := validateCollectionExecutionInventory(context.Background(), f.image, view)
			if err != nil || len(got.trees) != 0 || len(got.commitments) != 0 || len(got.children) != 0 {
				t.Fatal("empty sources recreated executable evidence", err)
			}
			if _, err := buildCollectionExecutionAuditIndex(context.Background(), f.head, view, defaultCollectionExecutionIndexLimits()); err == nil {
				t.Fatal("shortened original input became a complete audit index")
			}
			if used, err := f.ledger.Bytes(); err != nil || used != 0 {
				t.Fatal("source cleanup left logical charge", used, err)
			}
			f.image.Version = CollectionExecutionRetirementFormatVersion
			if err := validateCollectionHeaders(f.image); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("format12 accepted source-retirement state", err)
			}
		})
	}
}
