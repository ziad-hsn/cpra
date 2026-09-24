package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestActivateRequiresAdmissionDisposition(t *testing.T) {
	for _, state := range []string{"validated", "future-state", "canceled"} {
		t.Run(state, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(api.Operation{ID: "original", State: state})
			}, nil)
			_, err := c.Operations.Activate(context.Background(), "original")
			var ambiguous *AmbiguousError
			if !errors.Is(err, ErrAmbiguous) || !errors.As(err, &ambiguous) || ambiguous.OperationID != "original" {
				t.Fatal("successful HTTP reply fabricated activation admission", err)
			}
		})
	}
}

func TestActivateAcceptsRetainedSummaryWithoutLoadingItemPage(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		op := executionOperationFixture()
		op.Items = nil
		if invalid {
			op.ExecutionResult.Summary.Accepted = 0
		}
		c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(op)
		}, nil)
		r, err := c.Operations.Activate(context.Background(), op.ID)
		if invalid {
			if !errors.Is(err, ErrAmbiguous) {
				t.Fatal("invalid summary admitted", err)
			}
		} else if err != nil || r.Data.ExecutionResult.Summary.ResultID != op.ExecutionResult.Summary.ResultID || len(r.Data.Items) != 0 {
			t.Fatal("metadata-only retained admission required an item page", err)
		}
	}
}
