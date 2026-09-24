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

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func fileProfileExternalResources(t *testing.T) map[string]api.Resource {
	t.Helper()
	external := `{"type":"external","config":{"jobTypeID":"private-job","version":"v1"}}`
	check := `{"driver":{"type":"http","config":{"url":"https://example.test/health"}},"interval":"60s","timeout":"5s"}`
	inputs := map[string]string{
		"check":        `{"check":{"driver":` + external + `,"interval":"60s","timeout":"5s"}}`,
		"recovery":     `{"check":` + check + `,"recovery":{"driver":` + external + `}}`,
		"notification": `{"check":` + check + `,"notifications":{"red":{"driver":` + external + `}}}`,
		"notifyType":   `{"check":` + check + `,"notifications":{"red":{"notifyType":"external","groupRef":"private-group"}}}`,
	}
	resources := map[string]api.Resource{}
	for name, spec := range inputs {
		resources[name] = api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "service"}, Spec: json.RawMessage(spec)}
	}
	resources["endpoint"] = api.Resource{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "endpoint"}, Spec: json.RawMessage(external)}
	resources["jobType"] = api.Resource{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "private-job"}, Spec: json.RawMessage(`{"kind":"check","handler":"handler","protocolVersion":"v1","version":"v1"}`)}
	for name, r := range resources {
		raw, _ := json.Marshal(r)
		if _, err := api.DecodeResource(raw); err != nil {
			t.Fatal("tagged fixture rejected", name, err)
		}
	}
	return resources
}

func TestCollectionFileProfileRejectsExternalUploadAndStaged(t *testing.T) {
	for name, input := range fileProfileExternalResources(t) {
		if name == "jobType" {
			continue
		} // The ordinary catalog vocabulary already excludes JobType.
		for _, profile := range []string{"", collection.FileNormalizationProfile} {
			t.Run(name+"/"+profile, func(t *testing.T) {
				for _, uploaded := range []bool{false, true} {
					f := fileProfileFixture(t, input, profile, uploaded)
					p := f.openUpload(t, nil)
					wrapper, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
					observed := &uploadObservedWrapper{KeyWrapper: wrapper}
					f.catalog.sealer, _ = secureconfig.NewSealer(observed)
					row, retry, err := p.prepare(context.Background(), f.input[0])
					if profile == "" {
						if err != nil || retry != uploaded || row.Ordinal != 1 {
							t.Fatal("unprofiled tagged compatibility changed", err)
						}
					} else if !errors.Is(err, ErrValidation) || retry || row.Ordinal != 0 || observed.wraps != 0 || observed.unwraps != 0 {
						t.Fatal("external input reached sealing or prefix lookup", err)
					}
					if !uploaded {
						continue
					}
					source := f.open(t, nil)
					called := false
					err = source.walk(context.Background(), func(stagedItemRef, *api.Resource) error { called = true; return nil })
					if profile == "" {
						if err != nil || !called {
							t.Fatal("legacy staged input changed", err)
						}
					} else {
						if !errors.Is(err, ErrValidation) || called || strings.Contains(err.Error(), "private-") {
							t.Fatal("profiled replay reached compilation callback", err)
						}
						result, plan, compileErr := compilePlanFixture(t, &f.collectionSourceFixture)
						if result.Valid || plan != nil || !errors.Is(compileErr, ErrValidation) {
							t.Fatal("profiled external compiled", compileErr)
						}
					}
					snapshot, _ := f.store.CatalogSnapshot()
					latest, _, _ := f.store.CollectionGet(f.head.ID)
					if snapshot.Len() != 0 || !reflect.DeepEqual(latest, f.head) || source.liveBytes != 0 || !f.catalog.Ready() {
						t.Fatal("profile rejection changed state or poisoned catalog")
					}
				}
			})
		}
	}
	// JobType reaches the ordinary kind gate before any encryption, regardless of profile.
	for _, profile := range []string{"", collection.FileNormalizationProfile} {
		f := fileProfileFixture(t, collectionMonitor("service", "https://example.test"), profile, false)
		p := f.openUpload(t, nil)
		input := f.input[0]
		input.Resource, _ = json.Marshal(fileProfileExternalResources(t)["jobType"])
		input.Ref.Key = persistence.CatalogKey{Kind: "JobType", ID: "private-job"}
		resignUploadInput(t, &input)
		if _, _, err := p.prepare(context.Background(), input); !errors.Is(err, ErrValidation) {
			t.Fatal("JobType admitted", err)
		}
	}
}

func TestCollectionFileProfileRejectsHistoricalActivation(t *testing.T) {
	for name, input := range fileProfileExternalResources(t) {
		if input.Kind != "Monitor" {
			continue
		}
		for _, mode := range []string{"new", "retained", "unchanged"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				f := fileProfileFixture(t, input, collection.FileNormalizationProfile, true)
				head := fileProfileAdmitHistorical(t, f, mode == "unchanged")
				if mode == "retained" {
					head = fileProfileRetainCandidate(t, f, head)
				}
				before, _ := f.store.CatalogSnapshot()
				p := candidateOpen(t, f.collectionSourceFixture, head, nil)
				candidate, retry, err := p.prepare(context.Background(), 1)
				if !errors.Is(err, ErrValidation) || retry || candidate.Ordinal != 0 || strings.Contains(err.Error(), "private-") {
					t.Fatal("old declared base input passed candidate preparation", err)
				}
				_ = p.close()
				w := executionTestWorker(t, f.collectionSourceFixture, head)
				if mode != "retained" {
					// Beginning creates no active resource. The next original decision must
					// still check the staged profile, including an unchanged plan row.
					item := executionWork(t, w, head.ID)
					if err = w.prepareStep(context.Background(), item); err != nil {
						t.Fatal(err)
					}
					if err = w.submitStep(context.Background(), head.Actor); err != nil {
						t.Fatal(err)
					}
					head, _, _ = f.store.CollectionGet(head.ID)
					before, _ = f.store.CatalogSnapshot()
				}
				if err = w.prepareStep(context.Background(), executionWork(t, w, head.ID)); !errors.Is(err, ErrValidation) || w.pending != nil {
					t.Fatal("old profiled input reached activation decision", err)
				}
				after, _ := f.store.CatalogSnapshot()
				latest, _, _ := f.store.CollectionGet(head.ID)
				if after.Index != before.Index || after.Len() != before.Len() || !reflect.DeepEqual(latest, head) {
					t.Fatal("rejected profile changed durable outcome")
				}
			})
		}
	}
}
