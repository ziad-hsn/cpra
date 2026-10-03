//go:build externaljobs

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestJournalAccountingSurvivesRestart(t *testing.T) {
	config := testConfig(t)
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	j := runner.journal
	for _, kind := range []string{"check", "recovery", "notification"} {
		if err := j.reserve(assignment(kind, kind)); err != nil {
			t.Fatal(err)
		}
	}
	reservation := int64(runner.config.Limits.OutcomeBytes + journalMetadataReserve)
	assertJournalTotals(t, j, Status{Records: 3, ReservedBytes: 3 * reservation})
	for _, kind := range []string{"check", "recovery", "notification"} {
		if err := j.update(kind, func(rec *journalRecord) error {
			rec.State, rec.GrantID = "started", "grant-"+kind
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertJournalTotals(t, j, Status{Records: 3, ReservedBytes: 3 * reservation})
	for kind, outcome := range map[string]string{"check": "success", "recovery": "completed", "notification": "unknown"} {
		runner.saveOutcome(kind, api.Outcome{Kind: kind, Status: outcome}, func(err error) { t.Fatal(err) })
	}
	assertJournalTotals(t, j, Status{Records: 3, PendingOutcomes: 3})
	if err := j.update("notification", func(rec *journalRecord) error {
		rec.State, rec.ReceiptID = "unknown", "notification-receipt"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertJournalTotals(t, j, Status{Records: 3, PendingOutcomes: 2, UnknownActions: 1})
	if err := runner.QueueLateEvidence(context.Background(), api.LateEvidenceRequest{
		ExecutionID: "notification", OriginalReceiptID: "notification-receipt", EvidenceID: "evidence-1", Diagnostic: "observed delivery",
	}); err != nil {
		t.Fatal(err)
	}
	assertJournalTotals(t, j, Status{Records: 3, PendingOutcomes: 3, UnknownActions: 1})
	if err := j.remove("check"); err != nil {
		t.Fatal(err)
	}
	if err := j.remove("absent"); err != nil {
		t.Fatal(err)
	}
	if err := j.remove("notification"); err != nil {
		t.Fatal(err)
	}
	assertJournalTotals(t, j, Status{Records: 2, PendingOutcomes: 2, UnknownActions: 1})
	before, err := runner.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, err := restarted.Status()
	if err != nil || after.Records != before.Records || after.PendingOutcomes != before.PendingOutcomes ||
		after.UnknownActions != before.UnknownActions || after.LiveBytes != before.LiveBytes || after.ReservedBytes != before.ReservedBytes {
		t.Fatal("restart changed retained accounting", before, after, err)
	}
	assertJournalTotals(t, restarted.journal, Status{Records: 2, PendingOutcomes: 2, UnknownActions: 1})
}

func TestJournalAccountingRejectsUncommittedChanges(t *testing.T) {
	config := testConfig(t)
	config.Limits.Records = 1
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	j := runner.journal
	a := assignment("original", "recovery")
	if err := j.reserve(a); err != nil {
		t.Fatal(err)
	}
	if err := j.reserve(a); !errors.Is(err, ErrDuplicate) {
		t.Fatal("duplicate reservation changed admission", err)
	}
	if err := j.reserve(assignment("excess", "check")); !errors.Is(err, ErrCapacity) {
		t.Fatal("record capacity was not enforced", err)
	}
	abort := errors.New("abort transaction")
	if err := j.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.State, rec.GrantID = "started", "grant"
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal("transaction abort was lost", err)
	}
	assertJournalTotals(t, j, Status{Records: 1, ReservedBytes: int64(runner.config.Limits.OutcomeBytes + journalMetadataReserve)})
	if err := j.update(a.ExecutionID, func(rec *journalRecord) error { rec.State, rec.GrantID = "started", "grant"; return nil }); err != nil {
		t.Fatal(err)
	}
	runner.saveOutcome(a.ExecutionID, api.Outcome{Kind: "recovery", Status: "completed"}, func(err error) { t.Fatal(err) })
	before, err := runner.Status()
	if err != nil {
		t.Fatal(err)
	}
	j.limits.LiveBytes = before.LiveBytes
	if err := j.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.Outcome.Diagnostic = strings.Repeat("x", 1024)
		return nil
	}); !errors.Is(err, ErrCapacity) {
		t.Fatal("additional bytes exceeded the journal quota", err)
	}
	assertJournalTotals(t, j, Status{Records: 1, PendingOutcomes: 1})
	j.limits.LiveBytes = runner.config.Limits.LiveBytes
	if err := j.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.Outcome.Diagnostic = "aborted outcome mutation"
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	after, err := runner.Status()
	if err != nil || after.LiveBytes != before.LiveBytes || after.PendingOutcomes != before.PendingOutcomes {
		t.Fatal("rejected transaction altered accounting", before, after, err)
	}
	// A real bbolt write failure must not advance counters for a transaction
	// which never commits, including deletion and an additional reservation.
	if err := j.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.update(a.ExecutionID, func(rec *journalRecord) error { rec.State = "unknown"; return nil }); !errors.Is(err, ErrStorage) {
		t.Fatal("failed write did not surface storage failure", err)
	}
	if err := j.remove(a.ExecutionID); !errors.Is(err, ErrStorage) {
		t.Fatal("failed deletion did not surface storage failure", err)
	}
	if err := j.reserve(assignment("failed", "check")); err == nil {
		t.Fatal("closed database accepted a reservation")
	}
	after, err = runner.Status()
	if err != nil || after.Records != before.Records || after.LiveBytes != before.LiveBytes || after.PendingOutcomes != before.PendingOutcomes {
		t.Fatal("failed database writes changed accounting", before, after, err)
	}
}

func assertJournalTotals(t *testing.T, j *journal, want Status) {
	t.Helper()
	records, err := j.records()
	if err != nil {
		t.Fatal(err)
	}
	// The byte oracle reads and serializes the committed encrypted records,
	// independently of the incremental counters used for admission/status.
	for _, rec := range records {
		if rec.State != "reserved" && rec.State != "started" {
			encoded, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			want.LiveBytes += int64(len(encoded))
		}
	}
	got, err := j.stats()
	if err != nil || len(records) != want.Records || got.Records != want.Records || got.PendingOutcomes != want.PendingOutcomes ||
		got.UnknownActions != want.UnknownActions || got.LiveBytes != want.LiveBytes || got.ReservedBytes != want.ReservedBytes {
		t.Fatal("status disagrees with committed records", got, want, len(records), err)
	}
}

func BenchmarkJournalStatus(b *testing.B) {
	for _, count := range []int{16, 128, 512} {
		b.Run(fmt.Sprintf("records=%d", count), func(b *testing.B) {
			config, err := testConfig(b).validate()
			if err != nil {
				b.Fatal(err)
			}
			j, err := openJournal(config)
			if err != nil {
				b.Fatal(err)
			}
			defer j.close()
			for n := range count {
				if err := j.reserve(assignment(fmt.Sprintf("execution-%d", n), "check")); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				status, err := j.stats()
				if err != nil || status.Records != count {
					b.Fatal("journal status lost records", status.Records, err)
				}
			}
			b.StopTimer()
		})
	}
}
