package management

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionValidationCoordinatorRetainedRowsKeepOriginalAttribution(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "invalid-final-input"}[invalid], func(t *testing.T) {
			last := api.CredentialSpec{Value: api.Pointer("private")}
			if invalid {
				last.Value = nil
			}
			// Source order differs from identity order to detect accidental sorting.
			f := coordinatorFixture(t,
				resource("Credential", "z-first", api.CredentialSpec{Value: api.Pointer("private")}),
				resource("Credential", "a-last", last))
			coordinatorRequest(t, &f)
			state, err := f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
			if err != nil {
				t.Fatal(err)
			}
			if state.Validation.Descriptor.Count != uint64(len(f.items)) {
				t.Fatal("verdict omitted original input")
			}
			// Exercise the actual bounded publication command before reading the
			// retained interface. The private runner does not invent a history seal.
			state = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_publish", OperationID: state.ID,
				UploadID: state.UploadID, ValidationID: state.Validation.Header.ResultID, ValidationPublished: 0}, f.at.Add(3*time.Second))
			if !state.Validation.HistorySealed {
				t.Fatal("bounded result publication did not seal")
			}
			var retained []persistence.CollectionValidationItem
			var after uint64
			for {
				page, err := f.store.CollectionValidationPage(context.Background(), state.ID, after, 1, f.at.Add(4*time.Second))
				if err != nil || page.Receipt.Header != state.Validation.Header || page.Receipt.Descriptor != state.Validation.Descriptor || len(page.Items) != 1 {
					t.Fatal("retained receipt/page identity differs from committed verdict", err)
				}
				retained = append(retained, page.Items...)
				if page.NextAfter == 0 {
					break
				}
				if page.NextAfter <= after || len(retained) > len(f.items) {
					t.Fatal("pagination did not advance within original input")
				}
				after = page.NextAfter
			}
			if len(retained) != len(f.items) {
				t.Fatal("retained result count differs")
			}
			for i, got := range retained {
				original := f.items[i]
				if got.Key != original.Key || got.Ordinal != original.Ordinal || got.Source != original.Source || got.Document != original.SourceDocument || got.Item != original.SourceItem {
					t.Fatal("original result attribution changed", i)
				}
				if !invalid && (got.Change != "create" || got.Issue != "") || invalid && i == len(retained)-1 && got.Issue == "" {
					t.Fatal("final invalid item or creation classification lost", i, got.Issue)
				}
			}
		})
	}
}

func TestCollectionValidationCoordinatorCanceledUnwrapRetainsClaimWithoutVerdict(t *testing.T) {
	f := coordinatorFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	coordinatorRequest(t, &f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	f.catalog.sealer, err = secureconfig.NewSealer(sourceBeforeUnwrap{KeyWrapper: base, before: func(context.Context) {
		reads++
		cancel() // The durable claim has committed; encrypted input access started.
	}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.catalog.runCollectionValidation(ctx, f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if !errors.Is(err, context.Canceled) || reads != 1 || state.ValidationRequest == nil || state.ValidationRequest.Claim == nil || state.Plan != nil || state.Validation != nil {
		t.Fatal("transient unwrap failure became a durable verdict", err)
	}
	_, err = f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(3*time.Second)))
	stored, _, readErr := f.store.CollectionGet(f.head.ID)
	if !errors.Is(err, persistence.ErrCollectionConflict) || reads != 1 || readErr != nil || !reflect.DeepEqual(stored, state) {
		t.Fatal("new runner decrypted, rewrote or took over interrupted input", err, readErr)
	}
}

func TestCollectionValidationCoordinatorPipeJoinsWhenActiveVisitorCancels(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private")}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareCollectionPlanArtifact(context.Background(), plan, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	called := 0
	go func() {
		finished <- visitCollectionPlanArtifact(ctx, artifact, func(persistence.CollectionPlanFragment) error {
			called++
			cancel()
			return ctx.Err()
		})
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || called != 1 {
			t.Fatal("active visitor cancellation lost", err, called)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active cancellation failed to join encoder")
	}
}
