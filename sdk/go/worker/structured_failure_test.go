//go:build externaljobs

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestStructuredFailureLostReceiptIsIdentical(t *testing.T) {
	c := testConfig(t)
	f := c.Client.(*fakeProtocol)
	f.loseFirstReceipt = true
	var calls atomic.Int64
	if err := c.Registry.RegisterCheck("example", "v1", []string{"custom.cache.full"}, func(context.Context, Job) (api.Outcome, error) {
		calls.Add(1)
		return api.Outcome{Status: "failure", Failure: &api.WorkerFailure{Code: "custom.cache.full", Phase: "check"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	a := assignment("structured", "check")
	a.ProtocolVersion = "2"
	a.FailureCodes = []string{"custom.cache.full"}
	f.assignments <- a
	_, _, _ = runWorker(t, c)
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.results) >= 2 })
	f.mu.Lock()
	defer f.mu.Unlock()
	first, _ := json.Marshal(f.results[0])
	second, _ := json.Marshal(f.results[1])
	if calls.Load() != 1 || string(first) != string(second) || f.results[0].Failure == nil {
		t.Fatal("replayed handler or changed structured result")
	}
}

func TestJournalConversionRetainsOutcomesAndUnknownEvidence(t *testing.T) {
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"check", "recovery", "notification"} {
		if err = j.reserve(assignment(kind, kind)); err != nil {
			t.Fatal(err)
		}
		if err = j.update(kind, func(r *journalRecord) error {
			r.State, r.GrantID = "outcome", "grant-"+kind
			out := unknownOutcome(kind, "retained diagnostic")
			out.ServerID, out.WorkerUID, out.ExecutionID, out.GrantID = c.ServerID, c.WorkerUID, kind, r.GrantID
			r.Outcome = &out
			if kind == "recovery" {
				r.State, r.ReceiptID = "unknown", "receipt-recovery"
				r.Evidence = &api.LateEvidenceRequest{ServerID: c.ServerID, WorkerUID: c.WorkerUID, ExecutionID: kind, OriginalReceiptID: r.ReceiptID, EvidenceID: "evidence-original"}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := j.records()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ConvertJournal(t.Context(), c); err == nil {
		t.Fatal("conversion acquired running journal")
	}
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	if _, err = New(c); !errors.Is(err, ErrJournalVersion) {
		t.Fatal("implicit conversion", err)
	}
	backup, err := ConvertJournal(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	after, err := r.journal.records()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("conversion changed evidence", err)
	}
	// The original remains independently readable as format 2 with the same key.
	old := c
	old.StateDir = filepath.Join(filepath.Dir(c.StateDir), "old")
	if err = makePrivateDir(old.StateDir); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(backup, filepath.Join(old.StateDir, "worker.db")); err != nil {
		t.Fatal(err)
	}
	original, err := openJournalFormat(old, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	defer original.close()
	restored, err := original.records()
	if err != nil || !reflect.DeepEqual(before, restored) {
		t.Fatal("backup not intact", err)
	}
}

// Large unknown outcomes and pending late evidence are both protected records.
// Conversion quotas count authenticated plaintext, not its larger base64 frame.
func TestJournalConversionProtectedEvidenceAtLimits(t *testing.T) {
	for _, size := range []int{4096, 126 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, err := testConfig(t).validate()
			if err != nil {
				t.Fatal(err)
			}
			c.Limits.Records = 1
			j, err := openJournalFormat(c, 2, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = j.reserve(assignment("protected", "recovery")); err != nil {
				t.Fatal(err)
			}
			err = j.update("protected", func(r *journalRecord) error {
				r.State, r.GrantID, r.ReceiptID = "unknown", "grant", "receipt"
				out := unknownOutcome("recovery", "")
				out.ServerID, out.WorkerUID, out.ExecutionID, out.GrantID = c.ServerID, c.WorkerUID, r.ExecutionID, r.GrantID
				out.Data = json.RawMessage(`{"padding":"` + strings.Repeat("x", size) + `"}`)
				r.Outcome = &out
				r.Evidence = &api.LateEvidenceRequest{ServerID: c.ServerID, WorkerUID: c.WorkerUID, ExecutionID: r.ExecutionID, OriginalReceiptID: r.ReceiptID, EvidenceID: "late", Data: append(json.RawMessage(nil), out.Data...)}
				for _, v := range []any{out, r.Evidence} {
					b, _ := json.Marshal(v)
					if len(b) > c.Limits.OutcomeBytes {
						t.Fatal("fixture exceeds envelope contract")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := j.records()
			if err != nil {
				t.Fatal(err)
			}
			// Exact plaintext limit: a wire-byte quota incorrectly rejects this journal.
			c.Limits.LiveBytes = j.accounting.liveBytes
			if err = j.close(); err != nil {
				t.Fatal(err)
			}
			backup, err := ConvertJournal(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("preserved backup %s", filepath.Base(backup))
			next, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			defer next.close()
			after, err := next.records()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("lost protected evidence", err)
			}
		})
	}
}

func TestJournalConversionQuotaAndCancellationPreserveOriginal(t *testing.T) {
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.reserve(assignment("reserved", "check")); err != nil {
		t.Fatal(err)
	}
	before, err := j.records()
	if err != nil {
		t.Fatal(err)
	}
	path := j.path
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tooSmall := c
	tooSmall.Limits.LiveBytes = 1
	if _, err = ConvertJournal(t.Context(), tooSmall); !errors.Is(err, ErrCapacity) {
		t.Fatal("quota", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = ConvertJournal(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("rejected conversion changed source", err)
	}
	reopened, err := openJournalFormat(c, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	after, err := reopened.records()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("old records lost", err)
	}
}
