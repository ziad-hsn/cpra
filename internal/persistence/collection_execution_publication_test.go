package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

func executionPublishStep(t *testing.T, s *Store, head CollectionState, at time.Time) Result {
	t.Helper()
	return executeStoreCommand(t, s, CollectionExecuteCommand{Action: "publish", Binding: head.ExecutionResult.Summary.Binding, Publication: &CollectionExecutionPublication{Published: head.ExecutionResult.Published}}, at)
}

func executionPublicationFixture(t *testing.T, disk bool, count int) (*Store, CollectionState) {
	t.Helper()
	s, head, _ := preparationCacheFixture(t, disk, count)
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	return s, executionItemFinalize(t, s, head)
}

func TestCollectionExecutionPublicationBoundsRetryAndRestart(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 503)
			original := head.Clone()
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			sequence, high := s.fsm.image.CatalogMutationSequence, s.fsm.image.OperationHighWater
			r := executionPublishStep(t, s, head, at)
			head = validationApplyAllowed(t, r)
			if len(r.Events) != 256 || head.ExecutionResult.Published != 256 || head.ExecutionResult.HistorySealed {
				t.Fatal("first page not bounded")
			}
			if head.ExecutionResult.PublishedBytes == 0 || !bootstrapHash(head.ExecutionResult.ProgressDigest) {
				t.Fatal("missing retained prefix commitment")
			}
			retry := executionPublishStep(t, s, original, at.Add(time.Second))
			if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(*retry.Collection, head) {
				t.Fatal("lost reply repeated publication", retry.Err)
			}
			if s.fsm.image.CatalogMutationSequence != sequence || s.fsm.image.OperationHighWater != high {
				t.Fatal("publication mutated catalog or allocated execution")
			}
			partial := captureSnapshotBytes(t, s.fsm)
			if !bytes.HasPrefix(partial, []byte(collectionExecutionPublicationSnapshotMagic)) {
				t.Fatal("missing format11 header")
			}
			f := &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(partial))); err != nil {
				t.Fatal("partial restore", err)
			}
			if err := f.collections.Close(); err != nil {
				t.Fatal(err)
			}
			if disk {
				config := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = Open(context.Background(), config)
				if err != nil {
					t.Fatal("reopen partial result", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				head, _, err = s.CollectionGet(head.ID)
				if err != nil || head.ExecutionResult.Published != 256 {
					t.Fatal("partial publication lost", err)
				}
			}
			r = executionPublishStep(t, s, head, at.Add(2*time.Second))
			head = validationApplyAllowed(t, r)
			if len(r.Events) != 248 || head.ExecutionResult.Published != 503 || !head.ExecutionResult.HistorySealed {
				t.Fatalf("remaining rows/seal: events=%d published=%d sealed=%t", len(r.Events), head.ExecutionResult.Published, head.ExecutionResult.HistorySealed)
			}
			receipt := collectionExecutionReceiptFor(*head.ExecutionResult)
			if receipt.validate() != nil {
				t.Fatal("invalid retained descriptor")
			}
			var got []CollectionExecutionItem
			for after := uint64(0); ; {
				page, err := s.History().collectionExecutionPage(context.Background(), receipt, s.fsm.image.Index, after, 100, at.Add(3*time.Second))
				if err != nil || len(page.Items) > 100 {
					t.Fatal("retained page", err)
				}
				got = append(got, page.Items...)
				if page.NextAfter == 0 {
					break
				}
				after = page.NextAfter
			}
			if len(got) != 503 {
				t.Fatal("lost original input rows")
			}
			digest := collectionExecutionResultInitialDigest()
			var size uint64
			for i, item := range got {
				if item.InputOrdinal != uint64(i+1) || item.Decision != "unattempted" || item.Child != nil {
					t.Fatal("incorrect original result")
				}
				next, n, err := collectionExecutionResultNextDigest(digest, item)
				if err != nil {
					t.Fatal(err)
				}
				digest = next
				size += n
			}
			if receipt.Descriptor.Digest != digest || receipt.Descriptor.Bytes != size {
				t.Fatal("sealed descriptor differs from original rows")
			}
			if _, err := s.History().collectionExecutionPage(context.Background(), receipt, s.fsm.image.Index, 0, 501, at); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("unbounded page accepted", err)
			}
			snapshot := captureSnapshotBytes(t, s.fsm)
			f = &machine{history: s.fsm.history}
			if err := f.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
				t.Fatal("sealed restore", err)
			}
			defer f.collections.Close()
			if !reflect.DeepEqual(f.image.Collections[head.ID].ExecutionResult, head.ExecutionResult) {
				t.Fatal("snapshot lost sealed result")
			}
			if disk {
				if err := validateHistoryOperationsContext(context.Background(), s.History().databases[at.UTC().Format("2006-01-02")]); err != nil {
					t.Fatal("native secondary index", err)
				}
			}
		})
	}
}

func TestCollectionExecutionPublicationExpiredCannotResurrect(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		t.Run(fmt.Sprint(sealed), func(t *testing.T) {
			s, head := executionPublicationFixture(t, false, 1)
			at := head.ExecutionResult.Summary.FinalizedAt
			if sealed {
				head = validationApplyAllowed(t, executionPublishStep(t, s, head, at))
			}
			expiry := at.AddDate(0, 0, 30)
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, expiry))
			if !head.ExecutionResult.HistoryExpiredAt.Equal(expiry) {
				t.Fatal("expiry not committed")
			}
			if s.History().catalog.Cutoff.Before(at) {
				t.Fatal("expiry did not advance monotonic history cutoff")
			}
			before := head.Clone()
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, at))
			if !reflect.DeepEqual(head, before) {
				t.Fatal("backward time resurrected result")
			}
			if sealed {
				receipt := collectionExecutionReceiptFor(*head.ExecutionResult)
				if _, err := s.History().collectionExecutionPage(context.Background(), receipt, s.fsm.image.Index, 0, 1, at); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("backward read resurrected result", err)
				}
			}
		})
	}
}

func TestCollectionExecutionPublicationHistoryFailureStopsAdmission(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	if err := s.History().Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Submit(context.Background(), []Command{{Kind: "collection_execute", At: head.ExecutionResult.Summary.FinalizedAt, CollectionExecute: &CollectionExecuteCommand{Action: "publish", Binding: head.ExecutionResult.Summary.Binding, Publication: &CollectionExecutionPublication{}}}})
	if err == nil || s.fsm.err == nil {
		t.Fatal("failed history acknowledged or left healthy", err)
	}
}

func TestCollectionExecutionPublicationFormatsAndCommandIsolation(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	c := Command{Kind: "collection_execute", At: head.ExecutionResult.Summary.FinalizedAt, CollectionExecute: &CollectionExecuteCommand{Action: "publish", Binding: head.ExecutionResult.Summary.Binding, Publication: &CollectionExecutionPublication{}}}
	for _, version := range []int{9, 10, 11, 12, 13, 14} {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == 11 || version == 12 || version == 13 || version == 14) {
			t.Fatal("format gate", version, err)
		}
	}
	c.CollectionExecute.Authority = OperatorAuthority{Actor: "injected"}
	if c.CollectionExecute.validate(c.At) == nil {
		t.Fatal("publication accepted execution authority")
	}
	c.CollectionExecute.Authority = OperatorAuthority{}
	raw, _ := json.Marshal(envelope{Version: 11, Commands: []Command{c, c}})
	if _, err := decodeEnvelope(raw); err == nil {
		t.Fatal("nonisolated publication admitted")
	}
	head = validationApplyAllowed(t, executionPublishStep(t, s, head, c.At))
	corrupted := s.fsm.image
	corrupted.Version = 10
	if validateCollectionHeaders(corrupted) == nil {
		t.Fatal("format10 accepted publication metadata")
	}
	// An exact old prefix retries without executing the original resource again.
	raw, _ = json.Marshal(envelope{Version: 11, Commands: []Command{c}})
	got := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw})
	rows, ok := got.([]Result)
	if !ok || len(rows) != 1 || rows[0].Err != nil || len(rows[0].Events) != 0 {
		t.Fatal("sealed replay retry", got)
	}
}

func TestCollectionExecutionPublicationNativeCorruption(t *testing.T) {
	for _, kind := range []string{"missing item", "missing index", "missing anchor", "bad seal"} {
		t.Run(kind, func(t *testing.T) {
			s, head := executionPublicationFixture(t, true, 1)
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt))
			h := s.History()
			day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
			err := h.databases[day].Update(func(tx *bolt.Tx) error {
				root := tx.Bucket(collectionExecutionPublicationBucket)
				b := root.Bucket([]byte(head.ID))
				switch kind {
				case "missing item":
					return b.Bucket(collectionExecutionPublicationItemsKey).Delete(collectionOrdinal(1))
				case "missing index":
					return tx.DeleteBucket(collectionExecutionPublicationBucket)
				case "missing anchor":
					return tx.Bucket(collectionExecutionAnchorBucket).Delete([]byte(head.ID))
				case "bad seal":
					return b.Put(collectionExecutionPublicationSummaryKey, []byte(`{}`))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateHistoryOperationsContext(context.Background(), h.databases[day]); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("startup accepted broken history", err)
			}
			if _, err := h.collectionExecutionPage(context.Background(), collectionExecutionReceiptFor(*head.ExecutionResult), s.fsm.image.Index, 0, 100, head.ExecutionResult.Summary.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("reader accepted broken history", err)
			}
		})
	}
}

func TestCollectionExecutionPublicationPreservesFormat10StateEncoding(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	r := head.ExecutionResult
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(struct {
		Summary CollectionExecutionSummary `json:"summary"`
	}{Summary: r.Summary})
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("unpublished format10 result gained format11 fields", string(got), err)
	}
	head = validationApplyAllowed(t, executionPublishStep(t, s, head, r.Summary.FinalizedAt))
	for _, change := range []func(*CollectionExecutionResultState){
		func(r *CollectionExecutionResultState) { r.HistorySealed = false },
		func(r *CollectionExecutionResultState) {
			r.PublishedBytes = (collectionExecutionItemMaxBytes+4)*r.Published + 1
		},
	} {
		copy := head.Clone()
		change(copy.ExecutionResult)
		if copy.validate() == nil {
			t.Fatal("impossible published descriptor accepted")
		}
	}
}

func TestCollectionExecutionPublicationMaintenanceUsesObservedTimeForRetainedCohort(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(-time.Second)
	before := s.fsm.image.Index
	handled, err := s.maintainCollectionExecutionHistory(at)
	if err != nil || handled || s.fsm.image.Index != before {
		t.Fatal("maintenance advanced an unexpired future cohort", handled, err)
	}
	if s.fsm.image.Collections[head.ID].ExecutionResult.Published != 0 {
		t.Fatal("maintenance fabricated a future publication observation")
	}
}
