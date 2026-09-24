package collection

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestJSONDocumentStreamAttributionAndFailedTail(t *testing.T) {
	f := freezeTest(t, monitor("first")+"\n["+monitor("second")+"]")
	for i := 0; i < 2; i++ {
		item, err := f.Item(context.Background(), i)
		if err != nil || item.Location.Document != i+1 {
			t.Fatalf("document %d: item=%+v error=%v", i+1, item, err)
		}
	}
	dir := t.TempDir()
	_, err := Freeze(context.Background(), []Source{Reader("bad-tail.jsonl", strings.NewReader(monitor("first")+"\n{"))}, Options{TempDir: dir})
	if err == nil {
		t.Fatal("malformed final document accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed input retained staging: %v %v", entries, err)
	}
}

func TestRejectOverflowingQuota(t *testing.T) {
	_, err := Freeze(context.Background(), []Source{Reader("one", strings.NewReader(monitor("first")))}, Options{MaxStagingBytes: math.MaxInt64})
	if err == nil {
		t.Fatal("overflowing quota accepted")
	}
}

type missingValidationIdentity struct {
	Operations
	activated bool
}

func (o *missingValidationIdentity) Validate(context.Context, string) (*cpra.Response[api.Operation], error) {
	return &cpra.Response[api.Operation]{Data: api.Operation{State: "validating"}}, nil
}

func (o *missingValidationIdentity) Activate(context.Context, string) (*cpra.Response[api.Operation], error) {
	o.activated = true
	return nil, nil
}

func TestValidationMustIdentifyFrozenContent(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	op := &missingValidationIdentity{}
	_, err := validateAndActivate(context.Background(), op, f, Result{OperationID: "op"})
	if err == nil || op.activated {
		t.Fatalf("validation without identity reached activation: %v", err)
	}
}
