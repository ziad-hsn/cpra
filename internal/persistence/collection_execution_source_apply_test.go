package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

func sourceRetirementCommand(head CollectionState) CollectionExecuteCommand {
	return CollectionExecuteCommand{Action: "retire_sources", Binding: head.ExecutionResult.Summary.Binding, SourceRetirement: CollectionExecutionSourceRetirementFenceFor(head)}
}

func sourceHeaderPresent(t *testing.T, s *Store, id string) bool {
	t.Helper()
	_, found, err := s.CollectionGet(id)
	if found && err == nil {
		return true
	}
	if !found && errors.Is(err, ErrOperationExpired) {
		return false
	}
	t.Fatal("unexpected source header observation", found, err)
	return false
}

func sourceRetirementFixture(t *testing.T, disk bool, count int) (*Store, CollectionState) {
	t.Helper()
	s, head, _ := preparationCacheFixture(t, disk, count)
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionItemFinalize(t, s, head)
	for !head.ExecutionResult.HistorySealed {
		head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)))
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, executionRetireCommand(head), head.ExecutionResult.Summary.FinalizedAt.Add(2*time.Second)))
	return s, head
}

func TestCollectionExecutionSourceRetirementAllStages(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := sourceRetirementFixture(t, disk, 257)
			original := head.Clone()
			at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
			view, status, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil || status.State != "ready" {
				t.Fatal("original result unavailable", err)
			}
			var stages []string
			first := sourceRetirementCommand(head)
			for step := 0; step < 20; step++ {
				before := head.Clone()
				command := sourceRetirementCommand(head)
				result := executeStoreCommand(t, s, command, at)
				head = validationApplyAllowed(t, result)
				if !reflect.DeepEqual(head.Execution, original.Execution) || !reflect.DeepEqual(head.ExecutionResult, original.ExecutionResult) || !reflect.DeepEqual(head.Activation, original.Activation) {
					t.Fatal("cleanup rewrote original execution/result/admission")
				}
				if head.Validation.RemovedRows > before.Validation.RemovedRows {
					stages = append(stages, "validation")
				} else if head.Plan.RemovedFragments > before.Plan.RemovedFragments {
					stages = append(stages, "plan")
				} else if head.RemovedRows > before.RemovedRows {
					stages = append(stages, "input")
				} else {
					t.Fatal("nonterminal cleanup made no progress")
				}
				found := sourceHeaderPresent(t, s, head.ID)
				blob := captureSnapshotBytes(t, s.fsm)
				if !bytes.HasPrefix(blob, []byte(collectionExecutionSourceRetirementSnapshotMagic)) {
					t.Fatal("source cleanup snapshot did not record format13")
				}
				f := &machine{history: s.fsm.history}
				if err := f.Restore(io.NopCloser(bytes.NewReader(blob))); err != nil {
					t.Fatal("restore partially deleted sources", stages, err)
				}
				if f.collectionExecutionIndex != nil || f.collectionOutcomeCommitments[head.ID] != nil || f.collectionTerminalTrees[head.ID] != nil {
					t.Fatal("cleanup recovery installed executable caches")
				}
				if err := f.collections.Close(); err != nil {
					t.Fatal(err)
				}
				retry := executeStoreCommand(t, s, first, original.ExecutionRetirement.UpdatedAt.Add(time.Second))
				if retry.Err != nil || !retry.Allowed || found && !reflect.DeepEqual(*retry.Collection, head) || !found && retry.Collection != nil {
					t.Fatal("old cleanup retry advanced or recreated a header", retry.Err)
				}
				page, err := view.Page(t.Context(), 0, 100, at)
				if err != nil || len(page.Items) != 100 {
					t.Fatal("source deletion invalidated retained result page", err)
				}
				at = at.Add(time.Second)
				if !found {
					break
				}
			}
			if sourceHeaderPresent(t, s, head.ID) {
				t.Fatal("header survived complete source retirement")
			}
			seen := map[string]int{}
			for _, stage := range stages {
				seen[stage]++
			}
			if seen["validation"] < 2 || seen["plan"] < 2 || seen["input"] < 2 {
				t.Fatal("fixture did not cross every namespace page boundary", stages)
			}
			used, err := s.fsm.collections.Bytes()
			if err != nil || used != 0 {
				t.Fatal("source retirement leaked quota", used, err)
			}
			if disk {
				configuration := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				lock, err := LockOffline(configuration.Storage.Directory)
				if err != nil {
					t.Fatal("offline verification after source retirement", err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(t.Context(), configuration)
				if err != nil {
					t.Fatal("native retired-source replay", err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				s = reopened
			}
			_, state, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil || state.State != "ready" || state.ResultID != status.ResultID {
				t.Fatal("retained result was not discoverable after header removal/restart", state, err)
			}
			if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, "another-operator", at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("header removal lost original owner", err)
			}
		})
	}
}

func TestCollectionExecutionSourceRetirementFormatAndFence(t *testing.T) {
	s, head := sourceRetirementFixture(t, false, 1)
	at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	c := sourceRetirementCommand(head)
	command := Command{Kind: "collection_execute", CollectionExecute: &c, At: at}
	if commandWriteFormat(command) != CollectionExecutionSourceRetirementFormatVersion {
		t.Fatal("source deletion selected older storage format")
	}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == CollectionExecutionSourceRetirementFormatVersion || version == CollectionReselectionFormatVersion || externalStorageFormat(version)) {
			t.Fatal("source deletion envelope gate", version, err)
		}
	}
	bad := sourceRetirementCommand(head)
	bad.SourceRetirement.Cleanup.Plan.ProgressDigest = identity("unrelated-plan")
	if r := executeStoreCommand(t, s, bad, at); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("source cleanup accepted a changed original descriptor", r.Err)
	}
	for _, mutate := range []func(*CollectionExecuteCommand){
		func(c *CollectionExecuteCommand) { c.Retirement = CollectionExecutionRetirementFenceFor(head) },
		func(c *CollectionExecuteCommand) { c.Authority = *head.Owner },
		func(c *CollectionExecuteCommand) { c.Ordinal = 1 },
		func(c *CollectionExecuteCommand) { c.Action = "retire" },
	} {
		changed := sourceRetirementCommand(head)
		mutate(&changed)
		if changed.validate(at) == nil {
			t.Fatal("source cleanup accepted foreign command fields")
		}
	}
}

func TestCollectionExecutionRetirementMaintenanceReclaimsTerminalSources(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 1)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(2 * time.Second)
	for step := 0; step < 12; step++ {
		handled, err := s.maintainCollectionExecutionRetirement(at)
		if err != nil {
			t.Fatal("automatic retirement", step, err)
		}
		found := sourceHeaderPresent(t, s, head.ID)
		if !found {
			if !handled {
				t.Fatal("header disappeared without a committed cleanup")
			}
			break
		}
		at = at.Add(time.Second)
	}
	if sourceHeaderPresent(t, s, head.ID) {
		t.Fatal("maintenance left completed source behind")
	}
	if handled, err := s.maintainCollectionExecutionRetirement(at); err != nil || handled {
		t.Fatal("idle maintenance proposed work", err)
	}
	_, state, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
	if err != nil || state.State != "ready" {
		t.Fatal("maintenance lost retained result", err)
	}
}

func TestCollectionExecutionSourceRetirementPartialOutcomePlanSnapshot(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 257)
	if head.Phase != "partial" {
		t.Fatal("fixture did not retain independent failed child dispositions")
	}
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	for head.ExecutionRetirement == nil || !head.ExecutionRetirement.complete(head) {
		head = validationApplyAllowed(t, executeStoreCommand(t, s, executionRetireCommand(head), at))
		at = at.Add(time.Second)
	}
	for head.Plan.RemovedFragments == 0 {
		head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at))
		at = at.Add(time.Second)
	}
	if head.Plan.RemovedFragments >= head.Plan.UploadedFragments || head.Validation.RemovedRows != head.Validation.Uploaded || head.RemovedRows != 0 {
		t.Fatal("fixture failed to leave a partial original plan")
	}
	f := &machine{history: s.fsm.history}
	if err := f.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal("partial-outcome shortened-plan snapshot", err)
	}
	defer f.collections.Close()
	if !reflect.DeepEqual(f.image.Collections[head.ID], head) || f.collectionOutcomeCommitments[head.ID] != nil || f.collectionTerminalTrees[head.ID] != nil {
		t.Fatal("terminal plan recovery changed outcome or installed caches")
	}
}
