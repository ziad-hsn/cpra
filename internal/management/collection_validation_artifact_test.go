package management

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func validationArtifactCompile(t *testing.T, f *collectionSourceFixture) (CollectionValidation, *collectionPlan, error, *collectionValidationSource) {
	t.Helper()
	source := f.open(t, nil)
	captured, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, plan, err := f.catalog.compileStagedCollectionPlan(context.Background(), captured, source, collectionReadAll)
	return result, plan, err, source
}

func validationArtifactItems(t *testing.T, artifact *collectionValidationArtifact) []persistence.CollectionValidationItem {
	t.Helper()
	var items []persistence.CollectionValidationItem
	if err := artifact.emit(context.Background(), func(batch []persistence.CollectionValidationItem) error {
		if len(batch) == 0 || len(batch) > 256 {
			t.Fatal("invalid callback batch size", len(batch))
		}
		raw, err := json.Marshal(batch)
		if err != nil || len(raw) > 4<<20 {
			t.Fatal("oversized callback payload", err)
		}
		items = append(items, batch...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestCollectionValidationArtifactSuccessFreezesSafeOriginalMetadata(t *testing.T) {
	secret := "PRIVATE-VALIDATION-ARTIFACT-CANARY"
	f := stagedValidationFixture(t,
		resource("Credential", "existing", api.CredentialSpec{}),
		resource("Credential", "new", api.CredentialSpec{Value: &secret}),
		collectionMonitor("api", "https://example.test/private-provider-path"))
	saved := createResource(t, f.catalog, resource("Credential", "existing", api.CredentialSpec{Value: &secret}))
	result, plan, err, source := validationArtifactCompile(t, &f)
	if err != nil || !result.Valid || plan == nil {
		t.Fatal("real compiler did not produce success", err)
	}
	before, _, _ := f.store.CollectionGet(f.head.ID)
	index := f.store.Status().CommittedIndex
	watch := collectionBlockSealing(t, f.catalog)
	artifact, err := prepareCollectionValidationArtifact(context.Background(), source, result, nil)
	if err != nil || !artifact.valid || artifact.issue != "" || artifact.summaryOnly {
		t.Fatal("prepare success", err)
	}
	defer artifact.close()
	items := validationArtifactItems(t, artifact)
	if len(items) != len(f.items) || items[0].Change != "unchanged" || items[0].UID != saved.Metadata.UID ||
		items[0].ResourceVersion != saved.Metadata.ResourceVersion || items[1].Change != "create" || artifact.header != plan.Header {
		t.Fatal("classification or original version pair changed")
	}
	for i, item := range items {
		original := f.items[i]
		if item.Ordinal != original.Ordinal || item.Key != original.Key || item.Source != original.Source || item.Document != original.SourceDocument || item.Item != original.SourceItem {
			t.Fatal("source coordinates changed")
		}
	}
	for _, canary := range []string{secret, "private-provider-path", "/private/config/team.yaml"} {
		if bytes.Contains(artifact.encoded, []byte(canary)) {
			t.Fatal("private source/provider text entered result bytes")
		}
	}
	if watch.opens.Load() != 0 || watch.wraps.Load() != 0 {
		t.Fatal("metadata freezing used encryption/decryption")
	}
	after, _, _ := f.store.CollectionGet(f.head.ID)
	if f.store.Status().CommittedIndex != index || !reflect.DeepEqual(before, after) {
		t.Fatal("artifact changed or renewed durable input")
	}
	// The artifact owns no source, compiler slices, or callback result storage.
	result.Items[0].UID = "changed-after-freeze"
	source.close()
	if err := artifact.emit(context.Background(), func(batch []persistence.CollectionValidationItem) error {
		batch[0].UID = "callback-mutated"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if again := validationArtifactItems(t, artifact); !reflect.DeepEqual(again, items) {
		t.Fatal("retry was changed by caller mutation/source release")
	}
	commitment := persistence.CollectionValidationDescriptor{Digest: persistence.CollectionValidationInitialDigest()}
	for _, item := range items {
		digest, cost, err := persistence.CollectionValidationNextDigest(commitment.Digest, item)
		if err != nil {
			t.Fatal(err)
		}
		commitment.Count++
		commitment.Bytes += cost
		commitment.Digest = digest
	}
	if commitment != artifact.descriptor || commitment.Bytes != uint64(len(artifact.encoded)) {
		t.Fatal("descriptor did not commit exact original-order result")
	}
	owned := artifact.encoded
	artifact.close()
	if !bytes.Equal(owned, make([]byte, len(owned))) || artifact.emit(context.Background(), func([]persistence.CollectionValidationItem) error { return nil }) == nil {
		t.Fatal("closed artifact retained/reused its buffer")
	}
}

func TestCollectionValidationArtifactMalformedInputAndEarlyLimitRetainAllPositions(t *testing.T) {
	for _, scenario := range []string{"malformed-middle", "malformed-last", "scratch-limit"} {
		t.Run(scenario, func(t *testing.T) {
			input := sourceCredentials(10)
			bad := 1
			if scenario == "malformed-last" {
				bad = 4
			}
			f := stagedSourceFixture(t, 5, func(i int) (persistence.CatalogKey, []byte) {
				key, raw := input(i)
				if scenario != "scratch-limit" && i == bad {
					clear(raw)
					raw = []byte(`{"kind":"PRIVATE-MALFORMED-PROVIDER-CANARY",`)
				}
				return key, raw
			}, nil, nil)
			source := f.open(t, nil)
			captured, _ := f.catalog.Snapshot()
			var release func()
			if scenario == "scratch-limit" {
				var err error
				release, err = source.reserve(collectionSourcePlaintextBudget)
				if err != nil {
					t.Fatal(err)
				}
				bad = 0
			}
			result, plan, compileErr := f.catalog.compileStagedCollectionPlan(context.Background(), captured, source, collectionReadAll)
			if release != nil {
				release()
			}
			if compileErr == nil || result.Valid || plan != nil {
				t.Fatal("invalid fixture unexpectedly compiled")
			}
			watch := collectionBlockSealing(t, f.catalog)
			artifact, err := prepareCollectionValidationArtifact(context.Background(), source, result, compileErr)
			if err != nil || artifact.valid || artifact.summaryOnly {
				t.Fatal("deterministic failure was not frozen", err)
			}
			defer artifact.close()
			items := validationArtifactItems(t, artifact)
			if len(items) != 5 || items[bad].Issue == "" || artifact.descriptor.Count != 5 {
				t.Fatal("original invalid input not identified")
			}
			for i, item := range items {
				if item.Key != f.items[i].Key || item.Ordinal != uint64(i+1) || item.Source != f.items[i].Source ||
					item.Document != f.items[i].SourceDocument || item.Item != f.items[i].SourceItem {
					t.Fatal("unevaluated source identity lost")
				}
				if i > bad && item.Issue != "notEvaluated" {
					t.Fatal("unexamined suffix misclassified", i, item.Issue)
				}
			}
			if scenario == "scratch-limit" && (artifact.issue != "validationLimit" || items[0].Issue != "validationLimit") {
				t.Fatal("work limit lost deterministic classification")
			}
			if watch.opens.Load() != 0 || watch.wraps.Load() != 0 || bytes.Contains(artifact.encoded, []byte("CANARY")) {
				t.Fatal("result fill opened resource bytes or retained diagnostics")
			}
		})
	}
}

func TestCollectionValidationArtifactRejectsTransientFailuresAndFalseSuccess(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "one", api.CredentialSpec{Value: api.Pointer("secret")}))
	result, _, err, source := validationArtifactCompile(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errCollectionReadDenied, ErrUnavailable,
		persistence.ErrCollectionUnavailable, persistence.ErrOperationExpired, errors.New("PRIVATE-PROVIDER-DIAGNOSTIC")} {
		invalid := result
		invalid.Valid = false
		artifact, err := prepareCollectionValidationArtifact(context.Background(), source, invalid, errors.Join(failure, ErrValidation))
		if artifact != nil || err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("transient/unknown error received deterministic result", err)
		}
	}
	for _, field := range []string{"order", "missing", "source", "item", "unknown-issue", "readDenied", "unavailable", "incomplete-version"} {
		t.Run(field, func(t *testing.T) {
			bad := result
			bad.Items = append([]CollectionValidationItem(nil), result.Items...)
			switch field {
			case "order":
				bad.Order = nil
			case "missing":
				bad.Items = nil
			case "source":
				bad.Items[0].SourceID = "/private/config.yaml"
			case "item":
				bad.Items[0].ItemID = "item.00000000000000000002"
			case "unknown-issue":
				bad.Items[0].Issue = "PRIVATE-PROVIDER-DIAGNOSTIC"
			case "readDenied", "unavailable":
				bad.Items[0].Issue = field
			case "incomplete-version":
				bad.Items[0].Change, bad.Items[0].UID = "update", "original"
			}
			if artifact, err := prepareCollectionValidationArtifact(context.Background(), source, bad, nil); artifact != nil || err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("inconsistent compiler result accepted", err)
			}
		})
	}
}

func TestCollectionValidationArtifactBoundsBatchesAndCancellation(t *testing.T) {
	f := stagedSourceFixture(t, 260, sourceCredentials(1), nil, nil)
	result, _, err, source := validationArtifactCompile(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	if artifact, err := prepareCollectionValidationArtifactBounded(context.Background(), source, result, nil, 200); artifact != nil || !errors.Is(err, ErrGraphLimit) {
		t.Fatal("explicit result byte quota did not fail before emission", err)
	}
	artifact, err := prepareCollectionValidationArtifact(context.Background(), source, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	var sizes []int
	if err := artifact.emit(context.Background(), func(batch []persistence.CollectionValidationItem) error {
		sizes = append(sizes, len(batch))
		return nil
	}); err != nil || !reflect.DeepEqual(sizes, []int{256, 4}) {
		t.Fatal("bounded batches", sizes, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	if err := artifact.emit(ctx, func([]persistence.CollectionValidationItem) error { calls++; cancel(); return nil }); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled emission continued", err, calls)
	}
	want := errors.New("append unavailable")
	if err := artifact.emit(context.Background(), func([]persistence.CollectionValidationItem) error { return want }); !errors.Is(err, want) {
		t.Fatal("callback failure lost", err)
	}
	if len(validationArtifactItems(t, artifact)) != 260 {
		t.Fatal("interrupted callback changed exact retry")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if a, err := prepareCollectionValidationArtifact(ctx, source, result, nil); a != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled preparation yielded result", err)
	}
}

func TestCollectionValidationArtifactRechecksAuthorityAndCancellationWhileFilling(t *testing.T) {
	for _, scenario := range []string{"denied", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f := stagedValidationFixture(t,
				resource("Credential", "first", api.CredentialSpec{Value: api.Pointer("PRIVATE-CANARY")}),
				resource("Credential", "second", api.CredentialSpec{Value: api.Pointer("PRIVATE-CANARY")}))
			result, _, err, source := validationArtifactCompile(t, &f)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			source.canRead = func(persistence.CatalogKey) bool {
				calls++
				if calls == 2 {
					if scenario == "denied" {
						return false
					}
					cancel()
				}
				return true
			}
			watch := collectionBlockSealing(t, f.catalog)
			artifact, err := prepareCollectionValidationArtifact(ctx, source, result, nil)
			want := errCollectionReadDenied
			if scenario == "canceled" {
				want = context.Canceled
			}
			if artifact != nil || !errors.Is(err, want) || calls != 2 || watch.opens.Load() != 0 || watch.wraps.Load() != 0 {
				t.Fatal("in-flight authority/cancellation was converted into a frozen result", err, calls)
			}
		})
	}
}

// Build a real complete 10,001-item encrypted input with bounded command batches
// so the graph-limit test does not need 10,001 separate five-millisecond flushes.
func validationArtifactOversizedInput(t *testing.T) collectionSourceFixture {
	t.Helper()
	catalog, store := testCatalog(t)
	count := maxValidationGraph + 1
	at := time.Now().UTC()
	key := bytes.Repeat([]byte{41}, commitment.KeyBytes)
	defer clear(key)
	fingerprint := [commitment.MACBytes]byte{73, 11}
	acc, err := commitment.NewAccumulator(key, uint64(count), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	head := persistence.CollectionState{UploadID: uuid.NewString(), Actor: "test/operator", IdentityFormat: commitment.Format,
		ItemCount: uint64(count), MaxEncodedBytes: 512 << 20, ProgressDigest: persistence.CollectionInitialDigest(), Phase: "uploading",
		CreatedAt: at, ActivityAt: at, ExpiresAt: at.Add(persistence.CollectionInactivityLifetime)}
	items := make([]persistence.CollectionItem, count)
	for i := range items {
		identity, raw := sourceCredentials(1)(i)
		item := persistence.CollectionItem{Ordinal: uint64(i + 1), Key: identity, Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: uint64(i + 1)}
		mac, err := commitment.ItemMAC(key, itemPosition(item), raw)
		if err != nil || acc.Add(itemPosition(item), mac) != nil {
			t.Fatal("inventory fixture", err)
		}
		item.ContentDigest = hex.EncodeToString(mac[:])
		item.Payload, err = catalog.sealer.Seal(context.Background(), item.Binding(catalog.storeID, head.UploadID), raw)
		clear(raw)
		if err != nil {
			t.Fatal(err)
		}
		items[i] = item
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	head.ContentDigest = hex.EncodeToString(digest[:])
	head.Secret, err = sealCollectionIdentity(context.Background(), catalog.sealer, head, catalog.storeID, key, fingerprint[:])
	if err != nil {
		t.Fatal(err)
	}
	head = submitStagedSource(t, store, persistence.CollectionCommand{Action: "create", Epoch: uuid.NewString(), Create: &head}, at)
	for start := 0; start < count; start += 256 {
		end := min(start+256, count)
		commands := make([]persistence.Command, 0, end-start)
		for i := start; i < end; i++ {
			at = at.Add(time.Millisecond)
			commands = append(commands, persistence.Command{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{
				Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &items[i]}})
		}
		results, err := store.Submit(context.Background(), commands)
		if err != nil || len(results) != len(commands) {
			t.Fatal("input batch", err)
		}
		for _, result := range results {
			if result.Err != nil || result.Collection == nil {
				t.Fatal("input batch item", result.Err)
			}
		}
		head = results[len(results)-1].Collection.Clone()
	}
	return collectionSourceFixture{catalog: catalog, store: store, head: head, items: items, at: at}
}

func TestCollectionValidationArtifactRealInputCountLimitIsExplicitSummary(t *testing.T) {
	f := validationArtifactOversizedInput(t)
	source := f.open(t, nil)
	captured, _ := f.catalog.Snapshot()
	watch := collectionBlockSealing(t, f.catalog)
	result, plan, compileErr := f.catalog.compileStagedCollectionPlan(context.Background(), captured, source, collectionReadAll)
	if !errors.Is(compileErr, ErrGraphLimit) || result.Valid || plan != nil || len(result.Items) != 0 {
		t.Fatal("real original input count did not exceed graph limit", compileErr)
	}
	artifact, err := prepareCollectionValidationArtifact(context.Background(), source, result, compileErr)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	if !artifact.summaryOnly || artifact.valid || artifact.issue != "validationLimit" || artifact.header.ItemCount != uint64(maxValidationGraph+1) ||
		artifact.descriptor != (persistence.CollectionValidationDescriptor{Digest: persistence.CollectionValidationInitialDigest()}) || len(artifact.encoded) != 0 {
		t.Fatal("oversized declared input was silently truncated")
	}
	if err := artifact.emit(context.Background(), func([]persistence.CollectionValidationItem) error {
		t.Fatal("summary emitted partial item list")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if watch.opens.Load() != 0 || watch.wraps.Load() != 0 {
		t.Fatal("input-count summary unwrapped resources")
	}
	// A caller cannot manufacture a large-input summary from a mismatched
	// private source header: the original committed identity is rechecked.
	source.head.ItemCount++
	if a, err := prepareCollectionValidationArtifact(context.Background(), source, result, compileErr); a != nil || !errors.Is(err, persistence.ErrCollectionConflict) {
		t.Fatal("modified private input count bypassed committed inventory", err)
	}
}

func TestCollectionValidationArtifactSafeDeterministicClassifications(t *testing.T) {
	for _, test := range []struct {
		issue   string
		failure error
	}{
		{"invalidResource", ErrValidation}, {"invalidGraph", ErrValidation}, {"unsafePrefix", ErrValidation},
		{"conflict", persistence.ErrCatalogConflict}, {"missingReference", persistence.ErrCatalogDependency}, {"validationLimit", ErrGraphLimit},
	} {
		t.Run(test.issue, func(t *testing.T) {
			f := stagedValidationFixture(t, resource("Credential", "one", api.CredentialSpec{Value: api.Pointer("PRIVATE-CANARY")}))
			result, _, err, source := validationArtifactCompile(t, &f)
			if err != nil {
				t.Fatal(err)
			}
			result.Valid = false
			result.Items[0].Issue = test.issue
			result.Items[0].Dependencies = []persistence.CatalogKey{{Kind: "Credential", ID: "PRIVATE-OUTSIDE-DIAGNOSTIC"}}
			artifact, err := prepareCollectionValidationArtifact(context.Background(), source, result, fmt.Errorf("PRIVATE-DIAGNOSTIC: %w", test.failure))
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.close()
			if validationArtifactItems(t, artifact)[0].Issue != test.issue || bytes.Contains(artifact.encoded, []byte("PRIVATE")) {
				t.Fatal("issue changed or diagnostic/dependency data escaped")
			}
		})
	}
}
