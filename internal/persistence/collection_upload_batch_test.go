package persistence

import (
	"errors"
	"reflect"
	"testing"
)

func TestCollectionUploadNextFenceMatchesCommittedPrefix(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "", 3)
	for n := 0; n < 3; n++ {
		command := reselectionUpload(t, s, head, "row-"+string(rune('a'+n)))
		want, err := CollectionUploadNextFence(head.ID, *command.UploadFence, *command.Item)
		if err != nil {
			t.Fatal(err)
		}
		head = validationApplyAllowed(t, collectionCommand(t, s, command, head.ActivityAt))
		got := CollectionUploadFence{Uploaded: head.Uploaded, EncodedBytes: head.EncodedBytes, ProgressDigest: head.ProgressDigest, Authority: want.Authority}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("predicted prefix differs from committed codec")
		}
	}
}
func TestCollectionUploadNextFenceRejectsInvalidInput(t *testing.T) {
	s := openCatalogMemory(t)
	head := reselectionCreate(t, s, "", 2)
	command := reselectionUpload(t, s, head, "row")
	for _, mode := range []string{"ordinal", "operation", "authority", "digest", "ciphertext", "quota"} {
		t.Run(mode, func(t *testing.T) {
			prior, item, id := *command.UploadFence, *command.Item, head.ID
			switch mode {
			case "ordinal":
				item.Ordinal++
			case "operation":
				id = "not-an-operation"
			case "authority":
				prior.Authority.Actor = ""
			case "digest":
				prior.ProgressDigest = "bad"
			case "ciphertext":
				item.Payload.Ciphertext = nil
			case "quota":
				prior.Uploaded = 1
				prior.EncodedBytes = maxCollectionLedgerBytes
				item.Ordinal = 2
			}
			if _, err := CollectionUploadNextFence(id, prior, item); err == nil {
				t.Fatal("invalid prediction accepted")
			}
		})
	}
	prior := *command.UploadFence
	prior.Uploaded = maxCollectionItems - 1
	prior.EncodedBytes = 100
	item := *command.Item
	item.Ordinal = maxCollectionItems
	last, err := CollectionUploadNextFence(head.ID, prior, item)
	if err != nil || last.Uploaded != maxCollectionItems {
		t.Fatal("final prefix prediction rejected", err)
	}
	item.Ordinal++
	if _, err = CollectionUploadNextFence(head.ID, last, item); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("completed prefix permitted another append", err)
	}
}
