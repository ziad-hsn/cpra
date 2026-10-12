package persistence

import (
	"fmt"
	"testing"
	"time"
)

func TestCollectionUploadGroupUsesOneSynchronousTransaction(t *testing.T) {
	for _, count := range []int{3, 50, 64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := collectionReadStore(t, true)
			head := createCollectionFixture(t, s, uint64(count))
			commands := make([]Command, count)
			for n := range commands {
				item := collectionItemFixture(t, s, head, uint64(n+1), fmt.Sprintf("row-%03d", n))
				commands[n] = Command{Kind: "collection", At: head.ActivityAt.Add(time.Second), Collection: &CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}}
			}
			before := ledgerTestTransactionID(t, s.fsm.collections)
			results := submit(t, s, commands...)
			for _, r := range results {
				if r.Err != nil || !r.Allowed {
					t.Fatal("valid upload failed", r.Err)
				}
			}
			wantTransactions := (count + 63) / 64
			if actual := ledgerTestTransactionID(t, s.fsm.collections) - before; actual != wantTransactions {
				t.Fatalf("%d uploaded rows used %d write transactions, want %d", count, actual, wantTransactions)
			}
			if s.fsm.collections.db.NoSync {
				t.Fatal("upload batching disabled durable synchronization")
			}
		})
	}
}

func TestCollectionUploadGroupCommitsAcceptedPrefixOnQuotaRejection(t *testing.T) {
	s := collectionReadStore(t, true)
	head := createCollectionFixture(t, s, 3)
	commands := make([]Command, 3)
	for n := range commands {
		item := collectionItemFixture(t, s, head, uint64(n+1), fmt.Sprintf("row-%d", n))
		commands[n] = Command{Kind: "collection", At: head.ActivityAt.Add(time.Second), Collection: &CollectionCommand{Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item}}
	}
	firstCost, err := collectionItemCost(head.ID, *commands[0].Collection.Item)
	if err != nil {
		t.Fatal(err)
	}
	s.fsm.collections.maxBytes = firstCost + 1
	before := ledgerTestTransactionID(t, s.fsm.collections)
	results := submit(t, s, commands...)
	if !results[0].Allowed || results[1].Err != ErrCollectionQuota || results[2].Err != ErrCollectionConflict {
		t.Fatal("quota rejection changed per-command accepted-prefix outcomes")
	}
	current, found, err := s.CollectionGet(head.ID)
	if err != nil || !found || current.Uploaded != 1 || current.EncodedBytes != firstCost {
		t.Fatal("accepted prefix was rolled back or rejected suffix was retained", err)
	}
	if actual := ledgerTestTransactionID(t, s.fsm.collections) - before; actual != 1 {
		t.Fatal("accepted prefix was not committed in one transaction", actual)
	}
}
