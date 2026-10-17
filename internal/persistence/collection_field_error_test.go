package persistence

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCollectionValidationFieldErrorsPreserveOldEncoding(t *testing.T) {
	item := CollectionValidationItem{Ordinal: 1, Key: CatalogKey{Kind: "Monitor", ID: "sample"}, Source: "source.00000000000000000001", Document: 1, Item: 1, Issue: "invalidResource"}
	want := `{"ordinal":1,"key":{"kind":"Monitor","id":"sample"},"source":"source.00000000000000000001","document":1,"item":1,"issue":"invalidResource"}`
	raw, err := CollectionValidationItemEncoding(item)
	if err != nil || string(raw) != want {
		t.Fatalf("old result bytes changed: %s %v", raw, err)
	}
	for _, bad := range []CollectionValidationItem{
		func() CollectionValidationItem {
			v := item
			v.Field = "spec.PRIVATE_VALUE"
			v.Reason = "required"
			return v
		}(),
		func() CollectionValidationItem { v := item; v.Field = "spec"; v.Reason = "PRIVATE_VALUE"; return v }(),
		func() CollectionValidationItem { v := item; v.Field = "spec"; return v }(),
	} {
		if _, err := CollectionValidationItemEncoding(bad); err == nil {
			t.Fatal("unreviewed diagnostic persisted")
		}
	}
}

func TestCollectionValidationFieldErrorsRequireCurrentFormatAndRestore(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 1)
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	items[0].Field, items[0].Reason = "spec.check.driver.config.url", "required"
	digest, cost, err := CollectionValidationNextDigest(CollectionValidationInitialDigest(), items[0])
	if err != nil {
		t.Fatal(err)
	}
	begin.Descriptor.Digest, begin.Descriptor.Bytes = digest, cost
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	c := Command{Kind: "collection", At: head.ActivityAt, Collection: &CollectionCommand{Action: "validation_append", OperationID: head.ID, UploadID: head.UploadID, ValidationID: begin.Header.ResultID, ValidationItems: items}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != lifecycleStorageFormat(version) {
			t.Fatalf("diagnostic under format %d: %v", version, err)
		}
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
	if !lifecycleStorageFormat(s.fsm.image.Version) {
		t.Fatal("diagnostic did not promote image format")
	}
	validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	encoded := captureSnapshotBytes(t, s.fsm)
	image, ledger, err := decodeSnapshot(bytes.NewReader(encoded), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	page, err := ledger.ValidationPage(head.ID, 0, 1)
	if err != nil || len(page) != 1 || page[0] != items[0] {
		t.Fatal("restore lost field result", err)
	}
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	image.Version = DeliveryFormatVersion
	if validateCollectionValidationRows(t.Context(), image, view) == nil {
		t.Fatal("old snapshot format accepted new diagnostics")
	}
}
