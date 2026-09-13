package collection_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func ExampleFreeze() {
	ctx := context.Background()
	frozen, err := collection.Freeze(ctx, []collection.Source{
		collection.Reader("shared.yaml", strings.NewReader(`endpoints:
  log:
    type: log
    config: {file: alerts.jsonl}
notification_groups:
  oncall: [log]
`)),
	}, collection.Options{})
	if err != nil {
		panic(err)
	}
	defer frozen.Close()
	_, err = collection.ValidateReferences(ctx, frozen, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%d resources validated locally\n", frozen.Len())
	// Output: 2 resources validated locally
}

// FreezeResources accepts resources produced by application code. A frozen
// collection can be inspected locally before any operation is sent to CPRa.
func ExampleFreezeResources() {
	ctx := context.Background()
	resource, err := api.DecodeResource([]byte(`{
  "apiVersion":"cpra.io/v2",
  "kind":"Monitor",
  "metadata":{"id":"payments-http"},
  "spec":{"check":{"driver":{"type":"http","config":{"url":"https://payments.example.test/health"}},"interval":"60s","timeout":"5s"}}
}`))
	if err != nil {
		panic(err)
	}
	frozen, err := collection.FreezeResources(ctx, collection.Slice([]api.Resource{resource}), collection.Options{})
	if err != nil {
		panic(err)
	}
	defer frozen.Close()
	if _, err := collection.ValidateReferences(ctx, frozen, nil); err != nil {
		panic(err)
	}
	item, err := frozen.Item(ctx, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(item.Resource.Kind, item.Resource.Metadata.ID)
	// Output: Monitor payments-http
}
