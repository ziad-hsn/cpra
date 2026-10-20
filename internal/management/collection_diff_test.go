package management

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionDiffUsesCapturedVersionWithoutValues(t *testing.T) {
	c, store := testCatalog(t)
	original := collectionMonitor("service", "https://original.invalid/private-token")
	original.Metadata.Name = api.Pointer("service")
	saved := createResource(t, c, original)
	index := store.Status().CommittedIndex
	updated := collectionMonitor("service", "https://updated.invalid/another-token")
	updated.Metadata.Name = api.Pointer("service")
	result, err := validateCollection(t, c, collectionInputs(updated))
	if err != nil || !result.Valid || len(result.Items) != 1 {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.ResourceVersion != saved.Metadata.ResourceVersion || !reflect.DeepEqual(item.ChangedFields, []string{"spec.check.driver.config.url"}) || store.Status().CommittedIndex != index {
		t.Fatal("diff lost captured identity or made a mutation", item)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "invalid") || strings.Contains(string(raw), "token") {
		t.Fatal("diff exposed values")
	}
}

func TestCollectionDiffContainsPrivateMaps(t *testing.T) {
	a := api.Resource{Spec: json.RawMessage(`{"check":{"driver":{"config":{"headers":{"private-header":"old"}}}}}`)}
	b := api.Resource{Spec: json.RawMessage(`{"check":{"driver":{"config":{"headers":{"private-header":"new"}}}}}`)}
	if fields := collectionChangedFields(a, b); !reflect.DeepEqual(fields, []string{"spec.check.driver.config.headers"}) {
		t.Fatal(fields)
	}
	a.Spec, b.Spec = json.RawMessage(`{"value":"private-old"}`), json.RawMessage(`{"value":"private-new"}`)
	if fields := collectionChangedFields(a, b); !reflect.DeepEqual(fields, []string{"spec.value"}) {
		t.Fatal(fields)
	}
}
