package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestExecutionPublicationReviewMissingAnchorPrimary(t *testing.T) {
	s, head := executionPublicationFixture(t, true, 1)
	at := head.ExecutionResult.Summary.FinalizedAt
	head = validationApplyAllowed(t, executionPublishStep(t, s, head, at))
	h := s.History()
	day := at.UTC().Format("2006-01-02")
	if err := h.databases[day].Update(func(tx *bolt.Tx) error {
		anchor, err := decodeCollectionExecutionResultEvent(tx.Bucket(collectionExecutionAnchorBucket).Get([]byte(head.ID)))
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("events")).Delete([]byte(anchor.MonitorID + "\x00" + anchor.ID))
	}); err != nil {
		t.Fatal(err)
	}
	_, err := h.collectionExecutionPage(context.Background(), collectionExecutionReceiptFor(*head.ExecutionResult), s.fsm.image.Index, 0, 100, at)
	if !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("missing authoritative anchor primary returned readable result: %v", err)
	}
}

func TestExecutionPublicationReviewSnapshotPrefixCommitment(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 2)
	head = validationApplyAllowed(t, executionPublishStep(t, s, head, head.ExecutionResult.Summary.FinalizedAt))
	snapshot := captureSnapshotBytes(t, s.fsm)
	parts := bytes.SplitN(snapshot, []byte("\n"), 3)
	if len(parts) != 3 {
		t.Fatal("missing streamed snapshot header")
	}
	for name, change := range map[string]func(*CollectionExecutionResultState){
		"bytes":          func(r *CollectionExecutionResultState) { r.PublishedBytes++ },
		"digest":         func(r *CollectionExecutionResultState) { r.ProgressDigest = strings.Repeat("b", 64) },
		"partial-prefix": func(r *CollectionExecutionResultState) { r.Published = 1; r.HistorySealed = false },
	} {
		t.Run(name, func(t *testing.T) {
			var image image
			if err := json.Unmarshal(parts[1], &image); err != nil {
				t.Fatal(err)
			}
			changed := image.Collections[head.ID].Clone()
			change(changed.ExecutionResult)
			image.Collections[head.ID] = changed
			encoded, err := json.Marshal(image)
			if err != nil {
				t.Fatal(err)
			}
			broken := append(append(append(append([]byte(nil), parts[0]...), '\n'), encoded...), '\n')
			broken = append(broken, parts[2]...)
			f := &machine{history: s.History()}
			err = f.Restore(io.NopCloser(bytes.NewReader(broken)))
			if f.collections != nil {
				defer f.collections.Close()
			}
			if err == nil {
				t.Fatal("snapshot accepted a publication prefix commitment inconsistent with its retained original rows")
			}
		})
	}
}

func TestExecutionPublicationReviewStaleAtAfterCutoff(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			at := head.ExecutionResult.Summary.FinalizedAt
			if err := s.History().Expire(at.AddDate(0, 0, 31)); err != nil {
				t.Fatal(err)
			}
			r := executionPublishStep(t, s, head, at.Add(time.Second))
			if r.Err != nil {
				t.Fatalf("stale publication should not fault the store: %v", r.Err)
			}
			updated := validationApplyAllowed(t, r)
			// Replay retains the original descriptor; the local monotonic cutoff
			// independently forbids availability after cohort reclamation.
			// Maintenance may commit explicit expiry before this stale command.
			// The protected reader handles both a replayed seal and an expired
			// never-materialized result without inventing an invalid receipt.
			if _, _, err := s.CollectionExecutionResultView(context.Background(), updated.ID, updated.Actor, at); !errors.Is(err, ErrOperationExpired) {
				t.Fatalf("stale publication resurrected result availability: %v", err)
			}
			if s.History().catalog.Cutoff.Before(at.AddDate(0, 0, 1)) {
				t.Fatal("stale publication moved history cutoff backward")
			}
		})
	}
}

func TestExecutionPublicationReviewMaintenanceCutoffFloor(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			at := head.ExecutionResult.Summary.FinalizedAt
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, at))
			observedExpiry := at.AddDate(0, 0, 31)
			if err := s.History().Expire(observedExpiry); err != nil {
				t.Fatal(err)
			}
			handled, err := s.maintainCollectionExecutionHistory(at.Add(time.Second))
			if err != nil || !handled {
				t.Fatalf("sealed expired result was skipped by backward-clock maintenance: handled=%v err=%v", handled, err)
			}
			current, ok, err := s.CollectionGet(head.ID)
			if err != nil || !ok || current.ExecutionResult.HistoryExpiredAt.Before(observedExpiry) {
				t.Fatalf("maintenance omitted retained cutoff expiry fence: %v", err)
			}
		})
	}
}
