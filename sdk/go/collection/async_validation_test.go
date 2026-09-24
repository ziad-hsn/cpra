package collection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type validationOperations struct {
	*fakeOperations
	change    func(*api.ValidationResultPage)
	waitError error
}

func (o *validationOperations) WaitValidation(ctx context.Context, id string) (*cpra.Response[api.ValidationResultPage], error) {
	r, err := o.fakeOperations.WaitValidation(ctx, id)
	if o.change != nil {
		o.change(&r.Data)
	}
	if o.waitError != nil {
		return nil, o.waitError
	}
	return r, err
}
func TestApplyNeverActivatesUnsupportedOrUnboundValidation(t *testing.T) {
	cases := map[string]func(*api.ValidationResultPage){
		"identity":       func(p *api.ValidationResultPage) { p.ContentDigest = strings.Repeat("d", 64) },
		"format":         func(p *api.ValidationResultPage) { p.IdentityFormat = "future-format" },
		"count":          func(p *api.ValidationResultPage) { p.ItemCount = 2 },
		"unknown-change": func(p *api.ValidationResultPage) { p.Items[0].Change = "future-change" },
		"unknown-kind":   func(p *api.ValidationResultPage) { p.Items[0].Kind = "FutureKind" },
		"unknown-issue":  func(p *api.ValidationResultPage) { p.Items[0].Issue = "future-issue" },
		"wrong-id":       func(p *api.ValidationResultPage) { p.Items[0].ID = "another" },
		"source":         func(p *api.ValidationResultPage) { p.Items[0].SourceDocument = 2 },
		"summary":        func(p *api.ValidationResultPage) { p.Summary.PlanID = "" },
		"pending": func(p *api.ValidationResultPage) {
			p.Summary.FinalizedAt = p.Summary.FinalizedAt.AddDate(-2026, 0, 0)
			p.Summary.ResultID = ""
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := freezeTest(t, monitor("a"))
			op := &validationOperations{fakeOperations: &fakeOperations{valid: true}, change: change}
			r, err := Apply(context.Background(), op, f)
			if err == nil || r.OperationID != "epoch.1" {
				t.Fatal("unsafe validation accepted or handle lost", err)
			}
			if !reflect.DeepEqual(op.calls, []string{"prepare", "create", "upload", "validate", "waitValidation"}) {
				t.Fatal("mutation followed unknown verdict", op.calls)
			}
		})
	}
}
func TestResumeValidationOnlyReadsOriginalVerdict(t *testing.T) {
	for _, state := range []string{"validating", "validated", "rejected"} {
		t.Run(state, func(t *testing.T) {
			f := freezeTest(t, monitor("a"))
			item, err := f.Item(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			base := &fakeOperations{valid: state != "rejected", operation: api.Operation{ID: "original", State: state, IdentityFormat: "cpra.collection.hmac-sha256-json-bytes.v1", ContentDigest: f.Digest(), ItemCount: api.Pointer(int64(1)), Uploaded: api.Pointer(int64(1))}, uploads: [][]api.ApplyItem{{applyItem(item)}}}
			r, err := Resume(context.Background(), base, "original", f)
			want := []string{"get", "waitValidation"}
			if state != "rejected" {
				want = append(want, "activate")
			}
			if !reflect.DeepEqual(base.calls, want) || r.OperationID != "original" || r.Validation == nil || (state == "rejected") != errors.Is(err, ErrPreflightRejected) {
				t.Fatal("resume repeated validation or lost verdict", base.calls, err)
			}
		})
	}
}
func TestApplyValidationWaitCancellationKeepsOriginalReceipt(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	op := &validationOperations{fakeOperations: &fakeOperations{valid: true}, waitError: context.Canceled}
	r, err := Apply(context.Background(), op, f)
	if !errors.Is(err, context.Canceled) || r.OperationID != "epoch.1" || r.Operation.State != "validating" || r.Validation != nil || r.Preflight != nil {
		t.Fatal("wait fabricated outcome or lost original handle", err)
	}
	if !reflect.DeepEqual(op.calls, []string{"prepare", "create", "upload", "validate", "waitValidation"}) {
		t.Fatal("wait cancellation mutated", op.calls)
	}
}
func TestApplySuccessfulSummaryDoesNotFetchWholeCollection(t *testing.T) {
	var input strings.Builder
	input.WriteByte('[')
	for i := 0; i < 101; i++ {
		if i > 0 {
			input.WriteByte(',')
		}
		input.WriteString(monitor(strings.Repeat("a", i+1)))
	}
	input.WriteByte(']')
	f := freezeTest(t, input.String())
	op := &fakeOperations{valid: true}
	r, err := Apply(context.Background(), op, f)
	if err != nil || r.Validation == nil || r.Validation.Count != 101 || r.Preflight != nil {
		t.Fatal("bounded summary could not authorize explicit activation", err)
	}
	if !reflect.DeepEqual(op.calls, []string{"prepare", "create", "upload", "validate", "waitValidation", "activate"}) {
		t.Fatal("helper accumulated result pages or repeated validation", op.calls)
	}
}
