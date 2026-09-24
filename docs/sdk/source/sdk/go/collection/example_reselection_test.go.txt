package collection_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func ExampleFreezeProfile() {
	frozen, err := collection.FreezeProfile(context.Background(), []collection.Source{
		collection.Reader("services.yaml", strings.NewReader(`apiVersion: cpra.io/v2
kind: Monitor
metadata:
  id: payments-http
spec:
  check:
    driver:
      type: http
      config:
        url: https://payments.example.test/health
    interval: 60s
    timeout: 5s
`)),
		collection.Reader("empty.yaml", strings.NewReader("")),
	}, collection.FileNormalizationProfile, collection.Options{})
	if err != nil {
		panic(err)
	}
	defer frozen.Close()
	fmt.Printf("%s: %d resource\n", frozen.NormalizationProfile(), frozen.Len())
	// Output: cpra.file.base.v1: 1 resource
}

// This example requires an existing profiled operation and its original inputs.
func ExampleReselect() {
	client, err := cpra.New(cpra.Config{
		BaseURL:   os.Getenv("CPRA_URL"),
		AuthToken: os.Getenv("CPRA_TOKEN"),
	})
	if err != nil {
		fmt.Println("Invalid connection settings")
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := collection.Reselect(ctx, client.Operations,
		os.Getenv("CPRA_OPERATION_ID"), []collection.Source{
			collection.File("services.yaml"),
			collection.File("empty.yaml"),
		}, collection.ReselectionOptions{AttemptID: os.Getenv("CPRA_ATTEMPT_ID")})
	fmt.Println("Original operation:", result.OperationID)
	if result.Attempt != nil {
		fmt.Println("Upload attempt:", result.Attempt.ID)
	}
	if err != nil {
		fmt.Println("Recovery stopped; retain these handles before reconciling progress")
		return
	}
	fmt.Println("Original upload complete:", result.Complete)
}
