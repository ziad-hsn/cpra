//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func enabledReferenceCatalog(t *testing.T, original *Catalog) *Catalog {
	t.Helper()
	options := CatalogOptions{}
	options.ExternalJobsEnabled = true
	c, err := NewCatalog(original.store, original.sealer, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExternalConfigurationConditionalEdits(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			c, store := jobTypeCatalog(t)
			job := createReferenceJobType(t, c, category)
			c = enabledReferenceCatalog(t, c)
			input := externalReferenceResource(category, category == "notification")
			prepared, err := c.Prepare(t.Context(), input, "", true)
			if err != nil {
				t.Fatal(err)
			}
			created, err := c.CommitAs(t.Context(), prepared, "team/operator")
			if err != nil {
				t.Fatal(err)
			}
			patch := []byte(`{"metadata":{"name":"Operator edit"}}`)
			change, err := c.PreparePatch(t.Context(), input.Kind, input.Metadata.ID, created.Resource.Metadata.ResourceVersion, patch)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := c.CommitAs(t.Context(), change, "team/operator")
			if err != nil || updated.Resource.Metadata.UID != created.Resource.Metadata.UID {
				t.Fatal("patch changed incarnation", err)
			}
			if _, err := c.PreparePatch(t.Context(), input.Kind, input.Metadata.ID, created.Resource.Metadata.ResourceVersion, patch); !errors.Is(err, persistence.ErrCatalogConflict) {
				t.Fatal("stale patch accepted", err)
			}
			row, ok, err := store.CatalogGet(persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID})
			if err != nil || !ok || len(row.JobTypeReferences) != 1 || row.JobTypeReferences[0].JobTypeUID != job.Metadata.UID {
				t.Fatal("edit lost registered contract", err)
			}
			restored, err := NewCatalog(store, c.sealer)
			if err != nil || restored.Verify(t.Context()) != nil {
				t.Fatal("disabled runtime could not authenticate retained configuration", err)
			}
			retained, err := restored.Get(t.Context(), input.Kind, input.Metadata.ID)
			if err != nil || retained.Metadata.ResourceVersion != updated.Resource.Metadata.ResourceVersion {
				t.Fatal("retained read lost original version", err)
			}
			if _, err := restored.Prepare(t.Context(), retained, retained.Metadata.ResourceVersion, false); !errors.Is(err, ErrValidation) {
				t.Fatal("disabled runtime admitted replacement", err)
			}
		})
	}
}

func TestExternalConfigurationRejectsChangedJobTypeBeforeCommit(t *testing.T) {
	c, store := jobTypeCatalog(t)
	job := createReferenceJobType(t, c, "check")
	c = enabledReferenceCatalog(t, c)
	input := externalReferenceResource("check", false)
	p, err := c.Prepare(t.Context(), input, "", true)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := c.PrepareDeleteJobType(t.Context(), job.Metadata.ID, job.Metadata.ResourceVersion, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitJobType(t.Context(), remove); err != nil {
		t.Fatal(err)
	}
	job.Metadata = api.Metadata{ID: job.Metadata.ID}
	job.Spec.Version = "2"
	commitTestJobType(t, c, job, "", true)
	if _, err := c.CommitAs(t.Context(), p, "team/operator"); !errors.Is(err, persistence.ErrCatalogDependency) {
		t.Fatal("stale contract activated", err)
	}
	if _, exists, err := store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: input.Metadata.ID}); exists || err != nil {
		t.Fatal("rejected commit created resource", err)
	}
}

func TestExternalConfigurationValidationProfileFencesRuntime(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	enabled := enabledReferenceCatalog(t, c)
	base, disabledDigest, err := c.collectionValidationProfile()
	if err != nil {
		t.Fatal(err)
	}
	profile, enabledDigest, err := enabled.collectionValidationProfile()
	if err != nil || enabledDigest == disabledDigest || len(profile.Drivers) != len(base.Drivers)+3 {
		t.Fatal("runtime setting absent from validation identity", err)
	}
	createReferenceJobType(t, enabled, "check")
	input := externalReferenceResource("check", false)
	captured, err := enabled.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := enabled.ValidateCollection(t.Context(), captured, []CollectionInput{{Resource: input, SourceID: "source", ItemID: "item"}}, CollectionValidationOptions{CanRead: func(persistence.CatalogKey) bool { return true }})
	if err != nil || !result.Valid {
		t.Fatal("enabled collection rejected contract", err, result)
	}
}

func TestExternalStartupSourceReportsUnverifiedContracts(t *testing.T) {
	input := externalReferenceResource("check", false)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	opts := StartupOptions{OpenSource: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }}
	if _, err := ValidateStartupSource(t.Context(), opts); !errors.Is(err, ErrStartupDriver) {
		t.Fatal("offline validation bypassed runtime opt-in", err)
	}
	opts.Catalog.ExternalJobsEnabled = true
	result, err := ValidateStartupSource(t.Context(), opts)
	if err != nil || result.UnverifiedContractReferences != 1 || result.Monitors != 1 {
		t.Fatal("offline validation claimed stored contract verification", err, result)
	}
}

func TestExternalStartupPinsRegisteredContracts(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			store, config := bootstrapTestStore(t, "raft", 10)
			sealer := stageTestSealer(t, 42)
			if _, err := store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			c, err := NewCatalog(store, sealer)
			if err != nil || c.Verify(t.Context()) != nil {
				t.Fatal(err)
			}
			job := createReferenceJobType(t, c, category)
			input := externalReferenceResource(category, category == "notification")
			opts := startupTestOptions(config.Storage.Directory, input)
			opts.Catalog.ExternalJobsEnabled = true
			result, err := StartupCatalog(t.Context(), store, sealer, opts)
			if err != nil || !result.Catalog.Ready() {
				t.Fatal("registered startup contract rejected", err)
			}
			row, ok, err := store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: input.Metadata.ID})
			if err != nil || !ok || len(row.JobTypeReferences) != 1 || row.JobTypeReferences[0].Revision != job.Metadata.ResourceVersion {
				t.Fatal("bootstrap lost contract reference", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := persistence.Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			opts.OpenSource = noStartupSource(t)
			recovered, err := StartupCatalog(t.Context(), reopened, sealer, opts)
			if err != nil || !recovered.Catalog.options.ExternalJobsEnabled {
				t.Fatal("restart lost enabled catalog", err)
			}
		})
	}
}

func TestExternalStartupMissingContractLeavesCatalogInactive(t *testing.T) {
	store, config := bootstrapTestStore(t, "raft", 10)
	opts := startupTestOptions(config.Storage.Directory, externalReferenceResource("check", false))
	opts.Catalog.ExternalJobsEnabled = true
	_, err := StartupCatalog(t.Context(), store, stageTestSealer(t, 43), opts)
	if !errors.Is(err, persistence.ErrCatalogDependency) || strings.Contains(err.Error(), "private-parameter-canary") {
		t.Fatal("missing contract accepted or parameters leaked", err)
	}
	if active, err := store.HasCatalog(); err != nil || active {
		t.Fatal("missing final dependency changed active catalog", err)
	}
	if _, exists := store.Bootstrap(); exists {
		t.Fatal("failed dependency began bootstrap")
	}
}

func TestExternalStartupResumesOriginalSeeding(t *testing.T) {
	store, config := bootstrapTestStore(t, "raft", 1)
	sealer := stageTestSealer(t, 44)
	if _, err := store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog(store, sealer)
	if err != nil || c.Verify(t.Context()) != nil {
		t.Fatal(err)
	}
	job := createReferenceJobType(t, c, "check")
	input := externalReferenceResource("check", false)
	opts := startupTestOptions(config.Storage.Directory, input)
	opts.Catalog.ExternalJobsEnabled = true
	stage := makeStartupStage(t, store, sealer, opts, true)
	observer := &observingBootstrapStore{Store: store, loseAction: "seed"}
	if _, err := applyBootstrap(t.Context(), observer, stage); !errors.Is(err, persistence.ErrCommitUnconfirmed) {
		t.Fatal("fixture did not interrupt committed seeding", err)
	}
	if _, err := store.CatalogSnapshotContext(t.Context()); !errors.Is(err, persistence.ErrBootstrapPending) {
		t.Fatal("partial catalog became readable", err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	opts.OpenSource = noStartupSource(t)
	result, err := StartupCatalog(t.Context(), reopened, sealer, opts)
	if err != nil || !result.Catalog.Ready() || !result.Bootstrap.CatalogActivated {
		t.Fatal("original seeding did not resume", err)
	}
	row, ok, err := reopened.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: input.Metadata.ID})
	if err != nil || !ok || len(row.JobTypeReferences) != 1 || row.JobTypeReferences[0].Revision != job.Metadata.ResourceVersion {
		t.Fatal("resumed seeding changed pinned contract", err)
	}
}

func TestExternalStageRejectsChangedValidationMode(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	c = enabledReferenceCatalog(t, c)
	o := stageTestOptions(t)
	o.StoreID, o.Catalog, o.Validation = c.storeID, c, c.options
	stage, err := CreateStage(t.Context(), o, c.sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(t.Context(), externalReferenceResource("check", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Freeze(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	o.Catalog = nil
	if reopened, err := ResumeStage(t.Context(), o, c.sealer); err == nil {
		_ = reopened.Close()
		t.Fatal("authoritative stage resumed as offline validation")
	}
	o.Validation.ExternalJobsEnabled = false
	if reopened, err := ResumeStage(t.Context(), o, c.sealer); err == nil {
		_ = reopened.Close()
		t.Fatal("changed runtime resumed original stage")
	}
	if info, err := os.Stat(o.Directory); err != nil || !info.IsDir() {
		t.Fatal("rejection removed original stage", err)
	}
}
