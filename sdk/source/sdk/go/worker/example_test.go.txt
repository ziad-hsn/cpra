//go:build externaljobs

package worker_test

import (
	"context"
	"fmt"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/worker"
)

var _ worker.Protocol = (*cpra.WorkerClient)(nil)

func ExampleRegistry_Register() {
	registry := worker.NewRegistry()
	for _, kind := range []string{"check", "recovery", "notification"} {
		category := kind
		err := registry.Register("example/"+category, "1", category, func(ctx context.Context, job worker.Job) (api.Outcome, error) {
			// Replace this body with a context-aware call to an operator-owned
			// provider using worker-local credentials. No effect is claimed here.
			status := "unknown"
			if category == "check" {
				status = "noData"
			}
			return api.Outcome{Status: status}, ctx.Err()
		})
		fmt.Println(category, err)
	}
	// Output:
	// check <nil>
	// recovery <nil>
	// notification <nil>
}
