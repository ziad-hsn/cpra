package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sourceCacheVariableFixture(t *testing.T) executionRecoveryFixture {
	t.Helper()
	s := openCatalogMemory(t)
	if _, err := s.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	sizes := []int{700, 300, 900, 400, 800, 200, 850, 650, 900, 400}
	c := collectionCreateFixture(t, s, uint64(len(sizes)))
	c.Create.Actor = "oncall"
	at := time.Now().UTC().Add(10 * time.Minute)
	c.Create.CreatedAt, c.Create.ActivityAt, c.Create.ExpiresAt = at, at, at.Add(CollectionInactivityLifetime)
	owner, err := s.ObserveCollectionOwner(t.Context(), c.Create.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	c.Create.Owner = owner
	head := validationApplyAllowed(t, collectionCommand(t, s, c, at))
	for n, size := range sizes {
		item := collectionItemFixture(t, s, head, uint64(n+1), fmt.Sprintf("item-%05d", n+1))
		item.Payload, err = catalogSealer(t).Seal(t.Context(), item.Binding(s.nodeID, head.UploadID), []byte(`{"kind":"Credential","metadata":{"id":"`+item.Key.ID+`"},"spec":{"value":"`+strings.Repeat("x", size<<10)+`"}}`))
		if err != nil {
			t.Fatal(err)
		}
		head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Millisecond)))
	}
	authority, err := s.ObserveOperatorAuthority(t.Context(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head, begin, items := validationApplyIntent(t, s, head, authority, true)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	at = head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: authority, CapabilitiesDigest: head.Activation.CapabilitiesDigest}, at.Add(time.Second)))
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	return executionRetirementRecoveryCapture(t, s, head)
}

func sourceCacheBuild(t *testing.T, f executionRecoveryFixture) *collectionExecutionSourceCertificate {
	t.Helper()
	var c *collectionExecutionSourceCertificate
	err := f.ledger.read(func(view *collectionLedgerView) error {
		var err error
		c, err = buildCollectionExecutionSourceCertificate(t.Context(), f.image, f.head, view, f.ledger)
		return err
	})
	if err != nil || c == nil || c.bytes > collectionExecutionSourceCertificateBytes || !c.matches(f.ledger, f.head) {
		t.Fatal("source certificate", err)
	}
	return c
}

func TestCollectionExecutionSourceCacheMatchesColdPlanner(t *testing.T) {
	for _, variable := range []bool{false, true} {
		var original executionRecoveryFixture
		if variable {
			original = sourceCacheVariableFixture(t)
		} else {
			original = executionSourceAuditFixture(t, 260)
		}
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("variable=%t/disk=%t", variable, disk), func(t *testing.T) {
				f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
				certificate := sourceCacheBuild(t, f)
				probe, partialPlan := false, false
				var maxRows uint64
				var maxBytes int64
				for step := 0; ; step++ {
					if step > 32 {
						t.Fatal("cached cleanup failed to converge")
					}
					cold := executionSourceAuditBatch(t, f)
					var warm *collectionExecutionSourceRetirementBatch
					var cost collectionExecutionSourceReadCost
					err := f.ledger.read(func(view *collectionLedgerView) error {
						var err error
						warm, cost, err = certificate.planRetirement(t.Context(), f.image, f.head, view, f.ledger)
						return err
					})
					if err != nil || !reflect.DeepEqual(warm, cold) {
						t.Fatal("warm certificate changed deletion or surviving digest", err, warm, cold)
					}
					if cost.Rows > 2*collectionLedgerBatchLimit+1 || cost.Bytes > 2*collectionLedgerBatchBytes+collectionLedgerMaxFrame {
						t.Fatal("warm read work grew beyond selected tail and one length probe", cost)
					}
					maxRows, maxBytes = max(maxRows, cost.Rows), max(maxBytes, cost.Bytes)
					if warm.Namespace == "header" {
						if cost != (collectionExecutionSourceReadCost{}) {
							t.Fatal("empty namespaces read rows", cost)
						}
						break
					}
					if warm.Removed.More && warm.Removed.Rows < collectionLedgerBatchLimit {
						probe = true
						if cost.Rows != 2*warm.Removed.Rows+1 || cost.Bytes <= 2*warm.Removed.EncodedBytes {
							t.Fatal("byte-limit probe absent from measured budget", cost)
						}
					} else if cost.Rows != 2*warm.Removed.Rows || cost.Bytes != 2*warm.Removed.EncodedBytes {
						t.Fatal("measured warm read work differs from two selected-tail passes", cost)
					}
					if warm.Namespace == "plan" && warm.Removed.More {
						partialPlan = true
					}
					executionSourceAuditDelete(t, &f, warm)
					if certificate.matches(f.ledger, f.head) {
						t.Fatal("certificate advanced before committed header acknowledgement")
					}
					certificate.advance(f.head)
				}
				if variable && !probe || !variable && !partialPlan {
					t.Fatal("fixture omitted variable-byte or partial-plan boundary", probe, partialPlan)
				}
				t.Logf("warm read charges: max records=%d max bytes=%d certificate bytes=%d byte-limit probe=%t", maxRows, maxBytes, certificate.bytes, probe)
			})
		}
	}
}

func TestCollectionExecutionSourceCacheRejectsSelectedCorruption(t *testing.T) {
	original := executionSourceAuditFixture(t, 260)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
			certificate := sourceCacheBuild(t, f)
			for f.head.Plan.RemovedFragments != f.head.Plan.UploadedFragments {
				executionSourceAuditDelete(t, &f, executionSourceAuditBatch(t, f))
				certificate.advance(f.head)
			}
			item, found, err := f.ledger.Item(f.head.ID, f.head.Uploaded)
			if err != nil || !found {
				t.Fatal(err)
			}
			item.Payload.Ciphertext[0] ^= 1
			raw, err := collectionItemEncoding(f.head.ID, item)
			if err != nil {
				t.Fatal(err)
			}
			executionSourceAuditPut(t, f, "input", item.Ordinal, raw)
			before, _ := f.ledger.Bytes()
			err = f.ledger.read(func(view *collectionLedgerView) error {
				batch, _, err := certificate.planRetirement(t.Context(), f.image, f.head, view, f.ledger)
				if err == nil || batch != nil {
					t.Fatal("selected altered ciphertext accepted by warm certificate")
				}
				cold, err := buildCollectionExecutionSourceCertificate(t.Context(), f.image, f.head, view, f.ledger)
				if err == nil || cold != nil {
					t.Fatal("corrupt source accepted during cold certificate reconstruction")
				}
				return nil
			})
			if after, _ := f.ledger.Bytes(); err != nil || after != before {
				t.Fatal("failed certificate verification changed quota", err)
			}
		})
	}
}

func TestCollectionExecutionSourceCacheFenceQuotaAndCancellation(t *testing.T) {
	original := executionSourceAuditFixture(t, 3)
	f := executionRetirementRecoveryPrefix(t, original, false, 0, false)
	c := sourceCacheBuild(t, f)
	if c.matches(&collectionLedger{}, f.head) {
		t.Fatal("certificate survived ledger replacement")
	}
	changed := f.head.Clone()
	changed.Plan.ProgressDigest = strings.Repeat("f", 64)
	if c.matches(f.ledger, changed) {
		t.Fatal("certificate accepted changed original plan")
	}
	err := f.ledger.read(func(view *collectionLedgerView) error {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if batch, _, err := c.planRetirement(ctx, f.image, f.head, view, f.ledger); !errors.Is(err, context.Canceled) || batch != nil {
			t.Fatal("canceled warm planning returned deletion", err)
		}
		quota := &collectionExecutionSourceCertificate{bytes: collectionExecutionSourceCertificateBytes - collectionExecutionSourceCheckpointBytes + 1}
		if _, err := quota.boundaries(t.Context(), view, f.head.ID, "input", f.head.Uploaded, f.head.EncodedBytes, collectionInitialDigest()); !errors.Is(err, ErrCollectionQuota) {
			t.Fatal("certificate metadata cap not enforced", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every canonical plan frame has a nonempty JSON body after four framing
	// bytes. Byte-only boundaries consume >2MiB; all namespaces share 1GiB.
	maxPoints := (CollectionPlanMaxBytes/5+2*CollectionValidationMaxItems)/collectionLedgerBatchLimit + int(maxCollectionLedgerBytes/(collectionLedgerBatchBytes-collectionLedgerMaxFrame)) + 6
	if 8*maxCollectionReceiptEventBytes+maxPoints*collectionExecutionSourceCheckpointBytes >= collectionExecutionSourceCertificateBytes {
		t.Fatal("valid admitted bounds can exceed certificate quota")
	}
}

func TestCollectionExecutionSourceCacheCommittedLifecycle(t *testing.T) {
	s, head := sourceRetirementFixture(t, false, 3)
	at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	first := sourceRetirementCommand(head)
	head = validationApplyAllowed(t, executeStoreCommand(t, s, first, at))
	cache := s.fsm.collectionSourceCertificate
	if cache == nil || !cache.matches(s.fsm.collections, head) {
		t.Fatal("committed source cleanup did not retain matching certificate")
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at.Add(time.Second)))
	if s.fsm.collectionSourceCertificate != cache || !cache.matches(s.fsm.collections, head) {
		t.Fatal("warm source cleanup rebuilt or failed to advance certificate")
	}
	validationApplyAllowed(t, executeStoreCommand(t, s, first, at))
	if s.fsm.collectionSourceCertificate != cache {
		t.Fatal("stale no-op unnecessarily discarded current certificate")
	}
	data := captureSnapshotBytes(t, s.fsm)
	restored := &machine{history: s.fsm.history, collectionSourceCertificate: cache}
	if err := restored.Restore(io.NopCloser(bytes.NewReader(data))); err != nil {
		t.Fatal(err)
	}
	defer restored.collections.Close()
	if restored.collectionSourceCertificate != nil {
		t.Fatal("restore retained prior ledger certificate")
	}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at.Add(2*time.Second)))
	if s.fsm.collectionSourceCertificate != nil || sourceHeaderPresent(t, s, head.ID) {
		t.Fatal("final header removal retained source certificate")
	}
}
