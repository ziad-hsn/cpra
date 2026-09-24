package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func planSnapshotParts(t *testing.T, count uint64) (CollectionPlanHeader, CollectionPlanDescriptor, []CollectionPlanLedgerFragment) {
	t.Helper()
	header := planCodecHeader(count)
	descriptor, parts := planSnapshotPartsForHeader(t, header)
	return header, descriptor, parts
}

func planSnapshotPartsForHeader(t *testing.T, header CollectionPlanHeader) (CollectionPlanDescriptor, []CollectionPlanLedgerFragment) {
	t.Helper()
	var raw bytes.Buffer
	descriptor, err := EncodeCollectionPlan(context.Background(), &raw, header, func(e *CollectionPlanEncoder) error {
		for ordinal := uint64(1); ordinal <= header.ItemCount; ordinal++ {
			row := planCodecRow(ordinal, ordinal, CatalogKey{Kind: "Credential", ID: fmt.Sprintf("credential-%06d", ordinal)}, "create")
			if err := e.BeginRow(row); err != nil {
				return err
			}
			if err := e.EndRow(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var parts []CollectionPlanLedgerFragment
	got, err := DecodeCollectionPlan(context.Background(), &raw, func(f CollectionPlanFragment) error {
		parts = append(parts, CollectionPlanLedgerFragment{Ordinal: uint64(len(parts)) + 1, Fragment: f})
		return nil
	})
	if err != nil || got != descriptor {
		t.Fatal("plan fixture did not decode", err)
	}
	return descriptor, parts
}

func appendSnapshotParts(t *testing.T, ledger *collectionLedger, operation string, parts []CollectionPlanLedgerFragment) {
	t.Helper()
	for len(parts) > 0 {
		n := min(len(parts), collectionLedgerBatchLimit)
		if err := ledger.AppendPlanFragments(operation, parts[:n]); err != nil {
			t.Fatal(err)
		}
		parts = parts[n:]
	}
}

func TestCollectionPlanSnapshotNamespaceRoundTripAndFrozenView(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk-%v", disk), func(t *testing.T) {
			var ledger *collectionLedger
			var err error
			if disk {
				ledger, err = openCollectionLedger(filepath.Join(t.TempDir(), "ledger"), maxCollectionLedgerBytes)
			} else {
				ledger, err = newMemoryCollectionLedger(maxCollectionLedgerBytes)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			header, _, parts := planSnapshotParts(t, 170)
			appendSnapshotParts(t, ledger, header.OperationID, parts[:256])
			view, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			appendSnapshotParts(t, ledger, header.OperationID, parts[256:])
			var frozen bytes.Buffer
			count, err := writeCollectionPlanLedger(&frozen, view)
			if err != nil || count != int64(frozen.Len()) {
				t.Fatal("frozen stream byte count", count, frozen.Len(), err)
			}
			copy, _ := newMemoryCollectionLedger(maxCollectionLedgerBytes)
			defer copy.Close()
			if err := importCollectionPlanLedger(bytes.NewReader(frozen.Bytes()), copy); err != nil {
				t.Fatal(err)
			}
			if count, _, err := copy.PlanStats(header.OperationID); err != nil || count != 256 {
				t.Fatal("frozen namespace observed later fragments", count, err)
			}
			full, err := ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer full.Close()
			var stream bytes.Buffer
			if _, err := writeCollectionPlanLedger(&stream, full); err != nil {
				t.Fatal(err)
			}
			restored, _ := newMemoryCollectionLedger(maxCollectionLedgerBytes)
			defer restored.Close()
			if err := importCollectionPlanLedger(bytes.NewReader(stream.Bytes()), restored); err != nil {
				t.Fatal(err)
			}
			read, err := restored.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			var actual []CollectionPlanLedgerFragment
			if err := read.WalkPlans(context.Background(), func(op string, part CollectionPlanLedgerFragment) error {
				if op != header.OperationID {
					t.Fatal("operation identity changed")
				}
				actual = append(actual, part)
				return nil
			}); err != nil || !reflect.DeepEqual(actual, parts) {
				t.Fatal("fragment bytes/identity changed", err)
			}
		})
	}
}

func TestCollectionPlanSnapshotStreamFailureKeepsOnlyUnpublishedPrefix(t *testing.T) {
	header, _, parts := planSnapshotParts(t, 170)
	ledger, _ := newMemoryCollectionLedger(maxCollectionLedgerBytes)
	defer ledger.Close()
	appendSnapshotParts(t, ledger, header.OperationID, parts)
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	var stream bytes.Buffer
	if _, err := writeCollectionPlanLedger(&stream, view); err != nil {
		t.Fatal(err)
	}
	raw := stream.Bytes()
	badFooter := bytes.Clone(raw)
	badFooter[len(badFooter)-1] ^= 1
	oversize := append(bytes.Clone(collectionPlanLedgerMagic), []byte{0, 0, 0, 0}...)
	binary.BigEndian.PutUint32(oversize[len(collectionPlanLedgerMagic):], collectionPlanLedgerMaxFrame+1)
	for name, corrupt := range map[string][]byte{"footer": badFooter, "truncated": raw[:len(raw)-1], "magic": raw[1:], "oversize": oversize} {
		t.Run(name, func(t *testing.T) {
			destination, _ := newMemoryCollectionLedger(maxCollectionLedgerBytes)
			defer destination.Close()
			if err := importCollectionPlanLedger(bytes.NewReader(corrupt), destination); err == nil {
				t.Fatal("corrupt namespace accepted")
			}
			count, _, err := destination.PlanStats(header.OperationID)
			if err != nil || count > 256 {
				t.Fatal("failed final batch became visible", count, err)
			}
		})
	}
	var firstBatchBytes int64
	for _, part := range parts[:256] {
		cost, err := collectionPlanLedgerCost(header.OperationID, part)
		if err != nil {
			t.Fatal(err)
		}
		firstBatchBytes += cost
	}
	destination, _ := newMemoryCollectionLedger(firstBatchBytes)
	defer destination.Close()
	if err := importCollectionPlanLedger(bytes.NewReader(raw), destination); !errors.Is(err, errCollectionLedgerQuota) {
		t.Fatal("combined quota not enforced", err)
	}
	if _, err := writeCollectionPlanLedger(planCodecShortWriter{}, view); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short snapshot write accepted", err)
	}
}

func TestCollectionPlanSnapshotImportBatchesLargeArtifactByBytes(t *testing.T) {
	header := planCodecHeader(1)
	var artifact bytes.Buffer
	if d, err := EncodeCollectionPlan(context.Background(), &artifact, header, planCodecManyGuards(7000)); err != nil || d.Bytes <= collectionLedgerBatchBytes {
		t.Fatal("fixture did not exceed one encoded batch", err)
	}
	source, _ := newMemoryCollectionLedger(maxCollectionLedgerBytes)
	defer source.Close()
	var count uint64
	if _, err := DecodeCollectionPlan(context.Background(), &artifact, func(f CollectionPlanFragment) error {
		count++
		return source.AppendPlanFragments(header.OperationID, []CollectionPlanLedgerFragment{{Ordinal: count, Fragment: f}})
	}); err != nil {
		t.Fatal(err)
	}
	if count >= collectionLedgerBatchLimit {
		t.Fatal("fixture must isolate the byte bound from the count bound")
	}
	view, err := source.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	var raw bytes.Buffer
	if _, err := writeCollectionPlanLedger(&raw, view); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(raw.Bytes())
	bad[len(bad)-1] ^= 1
	failed, err := openCollectionLedger(filepath.Join(t.TempDir(), "failed"), maxCollectionLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer failed.Close()
	if err := importCollectionPlanLedger(bytes.NewReader(bad), failed); err == nil {
		t.Fatal("corrupt complete artifact accepted")
	}
	retained, used, err := failed.PlanStats(header.OperationID)
	if err != nil || retained == 0 || retained >= count || used > collectionLedgerBatchBytes {
		t.Fatal("failed import did not preserve only the prior bounded transaction", retained, used, err)
	}
	good, err := openCollectionLedger(filepath.Join(t.TempDir(), "good"), maxCollectionLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	if err := importCollectionPlanLedger(bytes.NewReader(raw.Bytes()), good); err != nil {
		t.Fatal(err)
	}
	if restored, _, err := good.PlanStats(header.OperationID); err != nil || restored != count {
		t.Fatal("bounded transactions lost artifact fragments", restored, err)
	}
}

func TestCollectionPlanSnapshotFormatFiveRequiresBothNamespaces(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "original")
	if r := uploadCollectionFixture(t, s, head, item, head.ActivityAt); r.Err != nil {
		t.Fatal(r.Err)
	}
	// A format-5 image can remain after its last plan was retired, without any
	// successful catalog mutation. Both empty/full namespaces remain explicit.
	s.fsm.image.Version = CollectionPlanFormatVersion
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(collectionPlanSnapshotMagic)) {
		t.Fatal("format-5 outer framing absent")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != CollectionPlanFormatVersion || i.CatalogMutationSequence != 0 {
		t.Fatal("plan format invented a catalog mutation")
	}
	got, exists, err := ledger.Item(head.ID, 1)
	if err != nil || !exists || !reflect.DeepEqual(got, item) {
		t.Fatal("input namespace changed", err)
	}
	planOffset := bytes.Index(raw, collectionPlanLedgerMagic)
	inputOffset := bytes.Index(raw, collectionLedgerMagic)
	if planOffset < 0 || inputOffset < 0 || planOffset <= inputOffset {
		t.Fatal("namespace order changed")
	}
	for name, corrupt := range map[string][]byte{
		"missing-plan":   raw[:planOffset],
		"missing-input":  append(bytes.Clone(raw[:inputOffset]), raw[planOffset:]...),
		"truncated-plan": raw[:len(raw)-1],
		"extra-stream":   append(bytes.Clone(raw), raw[planOffset:]...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, copy, err := decodeSnapshot(bytes.NewReader(corrupt), ""); err == nil {
				if copy != nil {
					copy.Close()
				}
				t.Fatal("incomplete or extra namespace accepted")
			}
		})
	}
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion} {
		command := formatCatalogCommand(CatalogMutation{Create: true, Record: catalogRecord(t, s, "Credential", "guard", "uid", "v1", "private")})
		if result := applyCatalogFormat(t, s, format, command)[0]; !errors.Is(result.Err, ErrCatalogFormatDowngrade) || s.fsm.image.CatalogMutationSequence != 0 {
			t.Fatal("zero-sequence format-5 accepted legacy catalog semantics", result.Err)
		}
	}
	before := s.fsm.image.Index
	command := formatCatalogCommand(CatalogMutation{Create: true, Record: catalogRecord(t, s, "Credential", "guard", "uid", "v1", "private")})
	result := applyCatalogFormat(t, s, CollectionPlanFormatVersion, command)[0]
	if result.Err != nil || !result.Allowed || result.CatalogMutationSequence != before+1 || s.fsm.image.Version != CollectionPlanFormatVersion {
		t.Fatal("format-5 catalog mutation lost unique-token semantics", result.Err)
	}
}

func TestCollectionPlanSnapshotPreservesFrozenFormatFourBytes(t *testing.T) {
	s := openCatalogMemory(t)
	createCatalog(t, s, catalogRecord(t, s, "Credential", "guard", "uid", "v1", "private"))
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	first := &collectionTestSink{}
	if err := frozen.Persist(first); err != nil || !bytes.HasPrefix(first.Bytes(), []byte(catalogMutationSnapshotMagic)) {
		t.Fatal("format-4 framing", err)
	}
	s.fsm.image.Version = CollectionPlanFormatVersion
	second := &collectionTestSink{}
	if err := frozen.Persist(second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("format-5 state changed frozen format-4 bytes", err)
	}
	current := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(current, []byte(collectionPlanSnapshotMagic)) || !bytes.Contains(current, collectionPlanLedgerMagic) {
		t.Fatal("new format-5 snapshot did not include an empty plan namespace")
	}
}

func TestCollectionPlanSnapshotMixedLogRecoveryAndStoppedBackup(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "credential-000001")
	result := uploadCollectionFixture(t, s, head, item, head.ActivityAt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	head = *result.Collection
	if err := s.Snapshot(); err != nil || s.fsm.image.Version != CollectionFormatVersion {
		t.Fatal("old input-only snapshot", err)
	}
	planHeader := planCodecHeader(head.ItemCount)
	planHeader.OperationID, planHeader.UploadID, planHeader.Actor = head.ID, head.UploadID, head.Actor
	planHeader.ContentDigest, planHeader.InputProgressDigest = head.ContentDigest, head.ProgressDigest
	planHeader.ObservedIndex = s.Status().CommittedIndex
	descriptor, parts := planSnapshotPartsForHeader(t, planHeader)
	at := head.ActivityAt.Add(time.Second)
	begin := CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID,
		PlanBegin: &CollectionPlanBegin{Header: planHeader, Descriptor: descriptor}}
	if r := collectionCommand(t, s, begin, at); r.Err != nil || !r.Allowed {
		t.Fatal("begin", r.Err)
	}
	appendPart := func(part CollectionPlanLedgerFragment) {
		at = at.Add(time.Millisecond)
		command := CollectionCommand{Action: "plan_append", OperationID: head.ID, UploadID: head.UploadID, PlanID: planHeader.PlanID, PlanFragment: &part}
		if r := collectionCommand(t, s, command, at); r.Err != nil || !r.Allowed {
			t.Fatal("append", part.Ordinal, r.Err)
		}
	}
	appendPart(parts[0])
	appendPart(parts[1])
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	old := &collectionTestSink{}
	if err := frozen.Persist(old); err != nil {
		frozen.Release()
		t.Fatal(err)
	}
	frozen.Release()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("mixed old snapshot and plan logs fail stopped validation", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal("mixed replay", err)
	}
	if s.fsm.image.Version != CollectionPlanFormatVersion || s.fsm.image.CatalogMutationSequence != 0 || len(s.fsm.image.Catalog) != 0 {
		t.Fatal("restart invented catalog mutations")
	}
	laterFrozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer laterFrozen.Release()
	for _, part := range parts[2:] {
		appendPart(part)
	}
	verified, err := s.VerifyCollectionPlan(context.Background(), head.ID, at)
	if err != nil {
		t.Fatal("complete original artifact failed verification", err)
	}
	finish := CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &verified}
	if r := collectionCommand(t, s, finish, at); r.Err != nil || !r.Allowed || r.Collection.Phase != "validated" {
		t.Fatal("finalize", r.Err)
	}
	old = &collectionTestSink{}
	if err := laterFrozen.Persist(old); err != nil {
		t.Fatal("serialization overlapped later plan commits", err)
	}
	laterFrozen.Release()
	oldImage, oldLedger, err := decodeSnapshot(bytes.NewReader(old.Bytes()), "")
	if err != nil {
		t.Fatal("frozen incomplete artifact lost its valid prefix", err)
	}
	defer oldLedger.Close()
	if oldImage.Collections[head.ID].Plan.UploadedFragments != 2 || oldImage.Collections[head.ID].Phase != "validating" {
		t.Fatal("frozen image observed later plan completion")
	}
	restored := &machine{history: s.History(), planPrefixes: map[string]*collectionPlanPrefix{head.ID: newCollectionPlanPrefix(planHeader)}}
	if err := restored.Restore(io.NopCloser(bytes.NewReader(old.Bytes()))); err != nil {
		t.Fatal("restore frozen prefix", err)
	}
	defer restored.collections.Close()
	if len(restored.planPrefixes) != 0 {
		t.Fatal("snapshot installation retained a derived parser from a later image")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal("completed format5 snapshot", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("plan snapshot backup validation", err)
	}
	lock.Close()
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	state := s.fsm.image.Collections[head.ID]
	if state.Phase != "validated" || state.Plan.Descriptor != descriptor || state.Plan.UploadedFragments != uint64(len(parts)) || len(s.fsm.image.Catalog) != 0 {
		t.Fatal("completed inactive plan did not recover exactly")
	}
	before := s.fsm.collections
	corrupt := captureSnapshotBytes(t, s.fsm)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(corrupt[:len(corrupt)-1]))); err == nil || s.fsm.collections != before || s.fsm.image.Collections[head.ID].Plan.Descriptor != descriptor {
		t.Fatal("partial failed restore replaced authoritative materialization", err)
	}
}

func TestCollectionPlanSnapshotCommandFormatsAndPayloadFreeCancel(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "credential-000001")
	r := uploadCollectionFixture(t, s, head, item, head.ActivityAt)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	head = *r.Collection
	h := planCodecHeader(1)
	h.OperationID, h.UploadID, h.Actor = head.ID, head.UploadID, head.Actor
	h.ContentDigest, h.InputProgressDigest, h.ObservedIndex = head.ContentDigest, head.ProgressDigest, s.Status().CommittedIndex
	descriptor, _ := planSnapshotPartsForHeader(t, h)
	at := head.ActivityAt.Add(time.Second)
	begin := CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID,
		PlanBegin: &CollectionPlanBegin{Header: h, Descriptor: descriptor}}
	r = collectionCommand(t, s, begin, at)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	head = *r.Collection
	progress := collectionCleanupFor(head)
	cleanup := CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &progress}
	for _, c := range []CollectionCommand{begin, cleanup} {
		command := Command{Kind: "collection", At: at, Collection: &c}
		if commandWriteFormat(command) != CollectionPlanFormatVersion {
			t.Fatal("plan payload was not assigned format5")
		}
		for _, version := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion} {
			raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodeEnvelope(raw)
			if (err == nil) != (version == CollectionPlanFormatVersion) {
				t.Fatal("plan payload envelope capability mismatch", c.Action, version, err)
			}
		}
	}
	// Cancellation carries no new plan payload. The committed image chooses the
	// correct inactive transition while the unchanged command retains format3.
	at = at.Add(time.Second)
	cancel := CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID,
		Cancel: &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}}
	if commandWriteFormat(Command{Kind: "collection", At: at, Collection: &cancel}) != CollectionFormatVersion {
		t.Fatal("unchanged cancellation payload requires a new wire shape")
	}
	r = collectionCommand(t, s, cancel, at)
	if r.Err != nil || r.Collection.Phase != "canceled" || r.Collection.Plan.Header != h || s.fsm.image.Version != CollectionPlanFormatVersion {
		t.Fatal("payload-free cancellation lost format5 inactive plan", r.Err)
	}
	if err := validateCollectionHeaders(s.fsm.image); err != nil {
		t.Fatal("canceled plan header invalid", err)
	}
}
