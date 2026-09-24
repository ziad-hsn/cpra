package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func seedMainCollectionReselection(t *testing.T, client *cpra.Client) (string, [][]byte) {
	t.Helper()
	raw := [][]byte{
		[]byte("# original raw comment\napiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: first-file\nspec:\n  value: PRIVATE-RESELECTION-MAIN-FIRST\n"),
		[]byte("apiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: second-file\nspec:\n  value: PRIVATE-RESELECTION-MAIN-SECOND\n"),
		{},
	}
	key := bytes.Repeat([]byte{91}, commitment.KeyBytes)
	defer clear(key)
	sources, err := commitment.NewSourceAccumulator(key, uint64(len(raw)), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	var items []api.ApplyItem
	var positions []commitment.Position
	var macs [][32]byte
	for n, source := range raw {
		token, _ := commitment.SourceToken(uint64(n + 1))
		if err := sources.Begin(token); err != nil {
			t.Fatal(err)
		}
		if _, err := sources.Write(source); err != nil {
			t.Fatal(err)
		}
		if err := sources.End(); err != nil {
			t.Fatal(err)
		}
		err := collection.NormalizeFile(t.Context(), bytes.NewReader(source), collection.FileNormalizationProfile, collection.DecodeOptions{}, func(item collection.NormalizedItem) error {
			defer clear(item.JSON)
			resource, err := api.DecodeResource(item.JSON)
			if err != nil {
				return err
			}
			pos := commitment.Position{Ordinal: uint64(len(items) + 1), ID: item.ID, Source: commitment.SourcePosition{Token: token, Document: uint64(item.Location.Document), Item: uint64(item.Location.Item)}}
			mac, err := commitment.ItemMAC(key, pos, item.JSON)
			if err != nil {
				return err
			}
			items = append(items, api.ApplyItem{ID: item.ID, Ordinal: int64(pos.Ordinal), Source: token, SourceDocument: int64(pos.Source.Document), SourceItem: int64(pos.Source.Item), Resource: resource, ContentDigest: hex.EncodeToString(mac[:])})
			positions, macs = append(positions, pos), append(macs, mac)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := sources.Finish()
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, uint64(len(items)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	for i, pos := range positions {
		if err := acc.Add(pos, macs[i]); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	identity := api.CollectionPrepareRequest{IdentityFormat: commitment.Format, IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: int64(len(items)), NormalizationProfile: collection.FileNormalizationProfile}
	ticket, err := client.Operations.Prepare(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.Operations.Create(t.Context(), api.OperationCreateRequest{AdmissionTicket: ticket.Data.Ticket, IdentityFormat: commitment.Format, IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount, NormalizationProfile: identity.NormalizationProfile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Operations.Upload(t.Context(), created.Data.ID, api.UploadRequest{Items: items[:1]}); err != nil {
		t.Fatal(err)
	}
	return created.Data.ID, raw
}

func waitMainReselection(t *testing.T, client *cpra.Client, id, attempt, phase string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		observed, err := client.Operations.GetReselection(t.Context(), id, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if string(observed.Data.Phase) == phase {
			return
		}
		if observed.Data.Phase == "failed" {
			t.Fatal("original input attempt failed", observed.Data.ErrorCode)
		}
		select {
		case <-deadline:
			t.Fatal("reselection did not reach", phase)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestMainCollectionReselectionRecoversOriginalUpload(t *testing.T) {
	f := newMainManagementFixture(t)
	client, stop := startMainManagement(t, f)
	capabilities, err := client.Capabilities(t.Context())
	if err != nil || !slices.Contains(capabilities.Data.ResourceOperations["Operation"], "CreateCollectionReselection") {
		t.Fatal("normal startup did not advertise reselection", err)
	}
	id, raw := seedMainCollectionReselection(t, client)
	attempt, err := client.Operations.CreateReselection(t.Context(), id, api.CollectionReselectionCreateRequest{NormalizationProfile: collection.FileNormalizationProfile, SourceCount: int64(len(raw))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Operations.UploadReselectionSource(t.Context(), id, attempt.Data.ID, cpra.ReselectionSourcePart{Source: 1, End: true, Data: raw[0]}); err != nil {
		t.Fatal(err)
	}
	stop()
	store, err := persistence.Open(t.Context(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := store.CollectionPage(id, 0, 1)
	if err != nil || len(prefix) != 1 {
		t.Fatal("original prefix unavailable", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	f.options.webAddr = availableLocalAddress(t)
	client, stop = startMainManagement(t, f)
	_, err = client.Operations.GetReselection(t.Context(), id, attempt.Data.ID)
	var problem *cpra.Error
	if !errors.As(err, &problem) || problem.StatusCode != 404 {
		t.Fatal("restart reused disposable attempt", err)
	}
	attempt, err = client.Operations.CreateReselection(t.Context(), id, api.CollectionReselectionCreateRequest{NormalizationProfile: collection.FileNormalizationProfile, SourceCount: int64(len(raw))})
	if err != nil {
		t.Fatal(err)
	}
	for n, source := range raw {
		if _, err := client.Operations.UploadReselectionSource(t.Context(), id, attempt.Data.ID, cpra.ReselectionSourcePart{Source: int64(n + 1), End: true, Data: source}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Operations.VerifyReselection(t.Context(), id, attempt.Data.ID); err != nil {
		t.Fatal(err)
	}
	waitMainReselection(t, client, id, attempt.Data.ID, "verified")
	if _, err := client.Operations.ResumeReselection(t.Context(), id, attempt.Data.ID); err != nil {
		t.Fatal(err)
	}
	waitMainReselection(t, client, id, attempt.Data.ID, "completed")
	operation, err := client.Operations.Get(t.Context(), id)
	if err != nil || operation.Data.ID != id || operation.Data.State != "uploading" || operation.Data.Uploaded == nil || *operation.Data.Uploaded != 2 || operation.Data.Committed == nil || *operation.Data.Committed != 0 {
		t.Fatal("resume changed identity or activated configuration", err)
	}
	resources, err := client.Credentials.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(resources.Data.Items) != 0 {
		t.Fatal("resume activated credentials", err)
	}
	stop()
	store, err = persistence.Open(t.Context(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.CollectionPage(id, 0, 2)
	if err != nil || len(rows) != 2 || !reflect.DeepEqual(prefix[0], rows[0]) {
		t.Fatal("original ciphertext changed", err)
	}
	entries, err := os.ReadDir(filepath.Join(f.settings.Storage.Directory, "collection-reselection"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "ownership.db" {
		t.Fatal("normal stop retained disposable input", err)
	}
}
