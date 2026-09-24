package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestCollectionValidationSnapshotMandatoryNamespacesAndFrozenV5(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	item, exists, err := s.fsm.collections.Item(head.ID, 1)
	if err != nil || !exists {
		t.Fatal(err)
	}
	s.fsm.image.Version = CollectionPlanFormatVersion
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	old := &collectionTestSink{}
	if err := frozen.Persist(old); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(old.Bytes(), []byte(collectionPlanSnapshotMagic)) || bytes.Contains(old.Bytes(), collectionValidationLedgerMagic) {
		t.Fatal("historical namespace framing changed")
	}
	state, err := s.CommitAuthentication(context.Background(), authenticationBootstrap())
	if err != nil || state.Version != AuthenticationLifecycleFormatVersion {
		t.Fatal(err)
	}
	unchanged := &collectionTestSink{}
	if err := frozen.Persist(unchanged); err != nil || !bytes.Equal(old.Bytes(), unchanged.Bytes()) {
		t.Fatal("current upgrade mutated old frozen snapshot", err)
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(collectionValidationSnapshotMagic)) {
		t.Fatal("new snapshot outer format absent")
	}
	inputOffset, planOffset, resultOffset := bytes.Index(raw, collectionLedgerMagic), bytes.Index(raw, collectionPlanLedgerMagic), bytes.Index(raw, collectionValidationLedgerMagic)
	if inputOffset < 0 || planOffset <= inputOffset || resultOffset <= planOffset {
		t.Fatal("mandatory namespace order changed")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != CollectionValidationFormatVersion || i.CatalogMutationSequence != 0 {
		t.Fatal("authentication format invented catalog mutation")
	}
	got, found, err := ledger.Item(head.ID, 1)
	if err != nil || !found || !reflect.DeepEqual(got, item) {
		t.Fatal("input namespace changed", err)
	}
	if count, size, err := ledger.ValidationStats(head.ID); err != nil || count != 0 || size != 0 {
		t.Fatal("empty validation namespace changed", err)
	}
	footer := bytes.Clone(raw)
	footer[len(footer)-1] ^= 1
	for name, invalid := range map[string][]byte{
		"missing validation":        raw[:resultOffset],
		"missing plan":              append(bytes.Clone(raw[:planOffset]), raw[resultOffset:]...),
		"missing input":             append(bytes.Clone(raw[:inputOffset]), raw[planOffset:]...),
		"truncated validation":      raw[:len(raw)-1],
		"corrupt validation footer": footer,
		"duplicate validation":      append(bytes.Clone(raw), raw[resultOffset:]...),
		"trailing byte":             append(bytes.Clone(raw), 0),
	} {
		t.Run(name, func(t *testing.T) {
			_, materialized, err := decodeSnapshot(bytes.NewReader(invalid), "")
			if materialized != nil {
				_ = materialized.Close()
			}
			if err == nil {
				t.Fatal("incomplete/extra namespace accepted")
			}
		})
	}
	plain, _ := json.Marshal(i)
	if _, materialized, err := decodeSnapshot(bytes.NewReader(plain), ""); err == nil {
		if materialized != nil {
			_ = materialized.Close()
		}
		t.Fatal("format6 plain image omitted mandatory namespaces")
	}
}

func TestCollectionValidationSnapshotAuthenticationOnlyDetachedEmptyLedger(t *testing.T) {
	s := openCatalogMemory(t)
	if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if s.fsm.collections != nil {
		t.Fatal("authentication opened materialized collection storage")
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if s.fsm.collections != nil {
		t.Fatal("snapshot created live materialization")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != CollectionValidationFormatVersion || i.Authentication.Version != AuthenticationLifecycleFormatVersion {
		t.Fatal("current authority lost format")
	}
	if size, err := ledger.Bytes(); err != nil || size != 0 {
		t.Fatal("empty namespaces consume logical quota", err)
	}
	for _, old := range []int{CatalogFormatVersion, CollectionFormatVersion} {
		mutation := formatCatalogCommand(CatalogMutation{Create: true, Record: catalogRecord(t, s, "Credential", "one", "uid", "rev", "private")})
		if result := applyCatalogFormat(t, s, old, mutation)[0]; !errors.Is(result.Err, ErrCatalogFormatDowngrade) {
			t.Fatal("new authority enabled old catalog token semantics", result.Err)
		}
	}
	before := s.fsm.image.Index
	mutation := formatCatalogCommand(CatalogMutation{Create: true, Record: catalogRecord(t, s, "Credential", "one", "uid", "rev", "private")})
	result := applyCatalogFormat(t, s, CollectionValidationFormatVersion, mutation)[0]
	if result.Err != nil || result.CatalogMutationSequence != before+1 || s.fsm.image.Version != CollectionValidationFormatVersion {
		t.Fatal("format6 catalog sequence mismatch", result.Err)
	}
}

func TestCollectionValidationFormatAcceptsOriginalPlanCommands(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, _ := planApplyArtifact(t, s, head)
	command := Command{Kind: "collection", At: head.ActivityAt, Collection: &CollectionCommand{
		Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID, PlanBegin: &begin}}
	for _, format := range []int{CollectionFormatVersion, CatalogMutationFormatVersion, CollectionPlanFormatVersion, CollectionValidationFormatVersion} {
		raw, _ := json.Marshal(envelope{Version: format, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (format == CollectionPlanFormatVersion || format == CollectionValidationFormatVersion) {
			t.Fatal("plan format recognition changed", format, err)
		}
	}
}

func TestCollectionValidationSnapshotStoppedAuthenticationOnlyRecovery(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "logs"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			config := testConfig(t)
			s := openAuthenticationAdmin(t, config)
			state, err := s.CommitAuthentication(context.Background(), authenticationBootstrap())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal("stopped format6 validation", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openAuthenticationAdmin(t, config)
			got, err := reopened.Authentication()
			if err != nil || !reflect.DeepEqual(got, state) || reopened.fsm.image.Version != CollectionValidationFormatVersion {
				t.Fatal("recovery lost original authority", err)
			}
			if _, err := reopened.ObserveOperatorAuthority(context.Background(), "oncall", state.UpdatedAt); err != nil {
				t.Fatal(err)
			}
		})
	}
}
