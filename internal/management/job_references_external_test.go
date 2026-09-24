//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func externalReferenceResource(category string, inline bool) api.Resource {
	driver := api.DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"custom-` + category + `","version":"1","parameters":{"target":"private-parameter-canary"},"credentialProfile":"local-profile"}`)}
	if category == "notification" && !inline {
		return resource("NotificationEndpoint", "endpoint", driver)
	}
	spec := api.MonitorSpec{Check: api.CheckSpec{Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}, Interval: "60s", Timeout: "5s"}}
	switch category {
	case "check":
		spec.Check.Driver = driver
	case "recovery":
		spec.Recovery = &api.RecoverySpec{Driver: driver}
	case "notification":
		rules := map[string]api.AlertRule{"red": {Driver: &driver}, "yellow": {Driver: &driver}}
		spec.Notifications = &rules
	}
	return resource("Monitor", "monitor", spec)
}

func createReferenceJobType(t *testing.T, c *Catalog, category string) api.JobType {
	t.Helper()
	j := testJobType()
	j.Metadata.ID, j.Spec.Kind = "custom-"+category, category
	return commitTestJobType(t, c, j, "", true)
}

// The execution adapter is still absent. This fixture exercises the encrypted
// catalog contract directly without enabling external public admission.
func prepareReferenceResource(t *testing.T, c *Catalog, input api.Resource) *PreparedChange {
	t.Helper()
	at := time.Now().UTC()
	r := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at}
	input.Metadata.UID, input.Metadata.ResourceVersion, input.Metadata.Generation = r.UID, r.Revision, 1
	if err := c.prepareJobTypeReferences(t.Context(), input, &r); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	r.Payload, err = c.sealer.Seal(t.Context(), r.Binding(c.storeID), plain)
	if err != nil {
		t.Fatal(err)
	}
	return &PreparedChange{catalog: c, resource: input, mutation: persistence.CatalogMutation{Record: r, Create: true, OperationID: r.Revision, Actor: "team/operator"}}
}

func TestJobTypeReferencesPinContractsAndProtectDeletion(t *testing.T) {
	for _, tc := range []struct {
		category string
		inline   bool
	}{{"check", false}, {"recovery", false}, {"notification", false}, {"notification", true}} {
		t.Run(tc.category+map[bool]string{true: "-inline", false: ""}[tc.inline], func(t *testing.T) {
			c, store := jobTypeCatalog(t)
			job := createReferenceJobType(t, c, tc.category)
			input := externalReferenceResource(tc.category, tc.inline)
			if _, err := c.Prepare(t.Context(), input, "", true); !errors.Is(err, ErrValidation) {
				t.Fatal("external runtime admitted before execution support", err)
			}
			prepared := prepareReferenceResource(t, c, input)
			refs := prepared.mutation.Record.JobTypeReferences
			if len(refs) != 1 || refs[0].JobTypeUID != job.Metadata.UID || refs[0].Revision != job.Metadata.ResourceVersion || refs[0].Category != tc.category {
				t.Fatal("immutable version not pinned or duplicate references retained", refs)
			}
			raw, _ := json.Marshal(prepared.mutation)
			for _, secret := range []string{"private-parameter-canary", "local-profile"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("plaintext reached command")
				}
			}
			result, err := c.CommitAs(t.Context(), prepared, "team/operator")
			if err != nil {
				t.Fatal(err)
			}
			view, err := store.CatalogSnapshotContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			original, ok := view.Get(prepared.mutation.Record.Key)
			if !ok {
				t.Fatal("committed resource missing")
			}
			// A metadata replacement and a new current contract retain version 1.
			job = commitTestJobType(t, c, job, job.Metadata.ResourceVersion, false)
			job.Spec.Version = "2"
			job.Spec.ParameterSchema = json.RawMessage(`{"type":"object","required":["different"]}`)
			job = commitTestJobType(t, c, job, job.Metadata.ResourceVersion, false)
			if _, err := c.Get(t.Context(), input.Kind, input.Metadata.ID); err != nil {
				t.Fatal("new current contract invalidated pinned version", err)
			}
			deletion, err := c.PrepareDeleteJobType(t.Context(), job.Metadata.ID, job.Metadata.ResourceVersion, "team/operator")
			if err == nil {
				_, err = c.CommitJobType(t.Context(), deletion)
			}
			if !errors.Is(err, persistence.ErrCatalogReferenced) {
				t.Fatal("referenced JobType deletion permitted", err)
			}
			remove, err := c.PrepareDelete(t.Context(), input.Kind, input.Metadata.ID, result.Resource.Metadata.ResourceVersion)
			if err != nil {
				t.Fatal(err)
			}
			if len(remove.mutation.Record.JobTypeReferences) != 0 {
				t.Fatal("tombstone retained active references")
			}
			if _, err = c.CommitAs(t.Context(), remove, "team/operator"); err != nil {
				t.Fatal(err)
			}
			deletion, err = c.PrepareDeleteJobType(t.Context(), job.Metadata.ID, job.Metadata.ResourceVersion, "team/operator")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.CommitJobType(t.Context(), deletion); err != nil {
				t.Fatal("removed resource still holds reference", err)
			}
			job.Metadata = api.Metadata{ID: job.Metadata.ID}
			job.Spec.Version = "3"
			commitTestJobType(t, c, job, "", true)
			if _, err = c.open(t.Context(), original); err != nil || !c.Ready() {
				t.Fatal("detached catalog snapshot could not read retained contract", err)
			}
			page, cursor, err := (ReadView{view: view, catalog: c}).Page(t.Context(), input.Kind, "", 100)
			if err != nil || cursor != "" || len(page) != 1 || page[0].Metadata.ResourceVersion != original.Revision {
				t.Fatal("detached page lost its original resource generation", err)
			}
			var next persistence.CatalogRecord
			next.Key = original.Key
			if err = c.prepareJobTypeReferences(t.Context(), input, &next); !errors.Is(err, persistence.ErrCatalogDependency) {
				t.Fatal("new resource rebound deleted incarnation", err)
			}
		})
	}
}

func TestJobTypeReferencesRejectInvalidParametersAndCredentials(t *testing.T) {
	for name, config := range map[string]string{
		"missing parameters": `{"jobTypeID":"custom-check","version":"1"}`,
		"wrong schema":       `{"jobTypeID":"custom-check","version":"1","parameters":{"other":"private-error-canary"}}`,
		"duplicate key":      `{"jobTypeID":"custom-check","version":"1","parameters":{"target":"private-error-canary","target":"second"}}`,
		"unknown field":      `{"jobTypeID":"custom-check","version":"1","parameters":{"target":"valid"},"private-error-canary":true}`,
		"unknown version":    `{"jobTypeID":"custom-check","version":"missing","parameters":{"target":"valid"}}`,
		"profile path":       `{"jobTypeID":"custom-check","version":"1","parameters":{"target":"valid"},"credentialProfile":"../private-error-canary"}`,
		"oversized value":    `{"jobTypeID":"custom-check","version":"1","parameters":{"target":"` + strings.Repeat("x", jobValueBytes) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := jobTypeCatalog(t)
			createReferenceJobType(t, c, "check")
			input := externalReferenceResource("check", false)
			_ = visitDrivers(&input, func(_ string, driver *api.DriverConfig) error { driver.Config = json.RawMessage(config); return nil })
			record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}}
			err := c.prepareJobTypeReferences(t.Context(), input, &record)
			if err == nil || strings.Contains(err.Error(), "private-error-canary") || len(record.JobTypeReferences) != 0 {
				t.Fatal("invalid configuration accepted or exposed", err)
			}
			if !c.Ready() {
				t.Fatal("invalid input poisoned catalog")
			}
		})
	}
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	input := externalReferenceResource("check", false)
	_ = visitDrivers(&input, func(_ string, driver *api.DriverConfig) error {
		refs := map[string]string{"parameters": "cpra-held-secret"}
		driver.CredentialRefs = &refs
		return nil
	})
	record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}}
	if err := c.prepareJobTypeReferences(t.Context(), input, &record); !errors.Is(err, ErrValidation) {
		t.Fatal("CPRa credential admitted to worker configuration", err)
	}
	lookups := 0
	lookup := driverLookupFunc(func(context.Context, persistence.CatalogKey, func(*api.Resource) error) (bool, error) {
		lookups++
		return false, nil
	})
	_ = visitDrivers(&input, func(category string, driver *api.DriverConfig) error {
		original, _ := json.Marshal(driver)
		release, err := resolveDriverWithLookup(t.Context(), driver, category, lookup, nil)
		current, _ := json.Marshal(driver)
		if !errors.Is(err, ErrValidation) || release != nil || lookups != 0 || !bytes.Equal(original, current) {
			t.Fatal("external configuration reached CPRa credential lookup", err)
		}
		return nil
	})
}

func TestJobTypeReferencesDoNotEnableBootstrapOrRuntime(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			c, _ := jobTypeCatalog(t)
			createReferenceJobType(t, c, category)
			input := externalReferenceResource(category, false)
			stage := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 61))
			if err := stage.Add(t.Context(), input); !errors.Is(err, ErrValidation) {
				t.Fatal("bootstrap admitted unsupported execution", err)
			}
			_ = visitDrivers(&input, func(category string, driver *api.DriverConfig) error {
				if driver.Type == "external" && ValidateResolvedDriver(category, *driver) == nil {
					t.Fatal("runtime accepted external driver without dispatcher")
				}
				return nil
			})
		})
	}
}

func TestJobTypeReferenceReadAuthentication(t *testing.T) {
	for _, field := range []string{"uid", "revision", "category", "missing", "extra"} {
		t.Run(field, func(t *testing.T) {
			c, _ := jobTypeCatalog(t)
			createReferenceJobType(t, c, "check")
			prepared := prepareReferenceResource(t, c, externalReferenceResource("check", false))
			record := prepared.mutation.Record.Clone()
			switch field {
			case "uid":
				record.JobTypeReferences[0].JobTypeUID = "different"
			case "revision":
				record.JobTypeReferences[0].Revision = "different"
			case "category":
				record.JobTypeReferences[0].Category = "recovery"
			case "missing":
				record.JobTypeReferences = nil
			case "extra":
				record.JobTypeReferences = append(record.JobTypeReferences, record.JobTypeReferences[0])
			}
			if _, err := c.open(t.Context(), record); !errors.Is(err, ErrUnavailable) || c.Ready() {
				t.Fatal("unauthenticated index accepted", err)
			}
		})
	}
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	p := prepareReferenceResource(t, c, externalReferenceResource("check", false))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	original := p.mutation.Record.Clone()
	if err := c.prepareJobTypeReferences(ctx, p.resource, &p.mutation.Record); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(original, p.mutation.Record) {
		t.Fatal("cancelled preparation changed candidate", err)
	}
	if _, err := c.open(ctx, original); !errors.Is(err, context.Canceled) || !c.Ready() {
		t.Fatal("cancelled read poisoned catalog", err)
	}
}

func TestJobTypeReferenceCategoryAndStorageFailure(t *testing.T) {
	t.Run("category", func(t *testing.T) {
		c, _ := jobTypeCatalog(t)
		job := testJobType()
		job.Spec.Kind = "recovery"
		commitTestJobType(t, c, job, "", true)
		input := externalReferenceResource("check", false)
		record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}}
		if err := c.prepareJobTypeReferences(t.Context(), input, &record); !errors.Is(err, persistence.ErrCatalogDependency) || !c.Ready() {
			t.Fatal("wrong JobType category accepted or catalog poisoned", err)
		}
	})
	t.Run("storage read failure", func(t *testing.T) {
		c, store := jobTypeCatalog(t)
		createReferenceJobType(t, c, "check")
		p := prepareReferenceResource(t, c, externalReferenceResource("check", false))
		store.MarkUnavailable(errors.New("storage-read-private-path-canary"))
		if err := c.authenticateJobTypeReferences(t.Context(), p.resource, p.mutation.Record); !errors.Is(err, ErrUnavailable) || c.failed.Load() || strings.Contains(err.Error(), "private-path-canary") {
			t.Fatal("storage read failure marked configuration corrupt", err)
		}
	})
}
