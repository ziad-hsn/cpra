package collection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type uncertainActivationOperations struct {
	*fakeOperations
	activateErr error
	readErr     error
	change      func(*api.Operation)
}

type unconfirmedAdmissionOperations struct {
	*fakeOperations
	state string
}

func (o *unconfirmedAdmissionOperations) Activate(context.Context, string) (*cpra.Response[api.Operation], error) {
	o.calls = append(o.calls, "activate")
	o.operation.State = o.state
	return &cpra.Response[api.Operation]{Data: o.operation}, nil
}

func TestApplySuccessfulHTTPReplyStillRequiresActivationAdmission(t *testing.T) {
	for _, state := range []string{"validated", "future-state", "canceled"} {
		t.Run(state, func(t *testing.T) {
			f := freezeTest(t, monitor("a"))
			o := &unconfirmedAdmissionOperations{fakeOperations: &fakeOperations{valid: true}, state: state}
			r, err := Apply(context.Background(), o, f)
			if !errors.Is(err, cpra.ErrAmbiguous) || r.OperationID != "epoch.1" || r.Operation.State != state || !reflect.DeepEqual(o.calls, []string{"prepare", "create", "upload", "validate", "waitValidation", "activate", "get"}) {
				t.Fatal("unconfirmed admission became success or was retried", r, err, o.calls)
			}
		})
	}
}

func (o *uncertainActivationOperations) Activate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	o.calls = append(o.calls, "activate")
	o.operation.State = "applying"
	return nil, o.activateErr
}
func (o *uncertainActivationOperations) Get(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	r, err := o.fakeOperations.Get(ctx, id)
	if o.change != nil {
		o.change(&r.Data)
	}
	if o.readErr != nil {
		return nil, o.readErr
	}
	return r, err
}

func TestApplyReconcilesOnlyOriginalUncertainActivation(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*api.Operation)
		readErr   error
		confirmed bool
	}{
		{name: "admitted", confirmed: true},
		{name: "read-unavailable", readErr: cpra.ErrUnavailable},
		{name: "still-validated", change: func(o *api.Operation) { o.State = "validated" }},
		{name: "canceled-before-activation", change: func(o *api.Operation) { o.State = "canceled" }},
		{name: "canceled-after-activation", confirmed: true, change: func(o *api.Operation) {
			o.State = "canceled"
			o.ExecutionResult = &api.ExecutionResultAvailability{State: "pending"}
		}},
		{name: "different-handle", change: func(o *api.Operation) { o.ID = "other" }},
		{name: "different-content", change: func(o *api.Operation) { o.ContentDigest = strings.Repeat("0", 64) }},
		{name: "different-count", change: func(o *api.Operation) { o.ItemCount = api.Pointer(int64(2)) }},
		{name: "different-format", change: func(o *api.Operation) { o.IdentityFormat = "future" }},
		{name: "different-normalization-profile", change: func(o *api.Operation) { o.NormalizationProfile = FileNormalizationProfile }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := freezeTest(t, monitor("a"))
			o := &uncertainActivationOperations{fakeOperations: &fakeOperations{valid: true}, activateErr: &cpra.AmbiguousError{Cause: errors.New("lost response")}, readErr: test.readErr, change: test.change}
			r, err := Apply(context.Background(), o, f)
			if test.confirmed != (err == nil) || !test.confirmed && !errors.Is(err, cpra.ErrAmbiguous) || r.OperationID != "epoch.1" || r.Operation.ID != "epoch.1" || r.Operation.ContentDigest != f.Digest() || *r.Operation.ItemCount != 1 {
				t.Fatal("uncertain activation lost original identity or fabricated admission", r, err)
			}
			if !reflect.DeepEqual(o.calls, []string{"prepare", "create", "upload", "validate", "waitValidation", "activate", "get"}) {
				t.Fatal("activation was retried or a new handle was allocated", o.calls)
			}
		})
	}
}

func TestApplyKnownActivationRejectionNeverReconcilesOrRetries(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	o := &uncertainActivationOperations{fakeOperations: &fakeOperations{valid: true}, activateErr: cpra.ErrConflict}
	r, err := Apply(context.Background(), o, f)
	if !errors.Is(err, cpra.ErrConflict) || r.OperationID != "epoch.1" || o.calls[len(o.calls)-1] != "activate" {
		t.Fatal(r, err, o.calls)
	}
}

type executionReaderFunc func(context.Context, string, cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error)

func (f executionReaderFunc) ExecutionResult(ctx context.Context, id string, o cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error) {
	return f(ctx, id, o)
}

func TestCollectionWaitRetainsOriginalPartialObservationOnCancellation(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	result, err := Apply(context.Background(), &fakeOperations{valid: true}, f)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	reader := executionReaderFunc(func(ctx context.Context, id string, opts cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error) {
		reads++
		if id != result.OperationID || opts.Cursor != "" || opts.Limit != 0 {
			t.Fatal("wait changed identity or requested another page")
		}
		op := result.Operation
		op.Committed = api.Pointer(int64(1))
		op.Applied = api.Pointer(int64(0))
		op.State = "canceled"
		op.ExecutionResult = &api.ExecutionResultAvailability{State: "pending", Counts: &api.ExecutionResultCounts{Processed: 1, Accepted: 1, ChildPending: 1}}
		cancel()
		return &cpra.Response[api.Operation]{Data: op}, nil
	})
	got, err := Wait(ctx, reader, result)
	if !errors.Is(err, context.Canceled) || reads != 1 || got.OperationID != result.OperationID || got.Operation.State != "canceled" || *got.Operation.Committed != 1 || got.Operation.ExecutionResult.Counts.ChildPending != 1 {
		t.Fatal(got, err)
	}
}

func TestCollectionWaitRejectsUnboundOrFailedReadWithoutLosingReceipt(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	result, err := Apply(context.Background(), &fakeOperations{valid: true}, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*api.Operation){func(o *api.Operation) { o.ID = "other" }, func(o *api.Operation) { o.ContentDigest = strings.Repeat("d", 64) }, func(o *api.Operation) { o.ItemCount = api.Pointer(int64(2)) }, func(o *api.Operation) { o.IdentityFormat = "future" }, func(o *api.Operation) { o.NormalizationProfile = FileNormalizationProfile }, func(o *api.Operation) { o.ExecutionResult = &api.ExecutionResultAvailability{State: "ready"} }} {
		reader := executionReaderFunc(func(context.Context, string, cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error) {
			op := result.Operation
			change(&op)
			return &cpra.Response[api.Operation]{Data: op}, nil
		})
		got, err := Wait(context.Background(), reader, result)
		if err == nil || !reflect.DeepEqual(got, result) {
			t.Fatal("invalid response replaced original receipt", got, err)
		}
	}
	reader := executionReaderFunc(func(context.Context, string, cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error) {
		return &cpra.Response[api.Operation]{Data: api.Operation{ID: "other"}}, cpra.ErrUnavailable
	})
	got, err := Wait(context.Background(), reader, result)
	if !errors.Is(err, cpra.ErrUnavailable) || !reflect.DeepEqual(got, result) {
		t.Fatal(got, err)
	}
}

func TestCollectionWaitReturnsBoundedReadyPartialResult(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	result, err := Apply(context.Background(), &fakeOperations{valid: true}, f)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	op := result.Operation
	op.State = "partial"
	op.Committed = api.Pointer(int64(0))
	op.Applied = api.Pointer(int64(0))
	op.ExecutionResult = &api.ExecutionResultAvailability{State: "ready", Summary: &api.ExecutionResultSummary{ResultID: "result", UploadID: "upload", PlanID: "plan", PlanDigest: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), Outcome: "partial", ItemCount: 1, Processed: 1, Conflicts: 1, FinalizedAt: at, ExpiresAt: at.Add(time.Hour)}}
	op.Items = []api.ApplyResult{{ID: "Monitor/a", Kind: "Monitor", Outcome: "conflict", CatalogDecision: "conflict", InputOrdinal: api.Pointer(int64(1)), PlanOrdinal: api.Pointer(int64(1)), Source: "source.00000000000000000001", SourceDocument: api.Pointer(int64(1)), SourceItem: api.Pointer(int64(1)), DecidedAt: &at, CommittedIndex: api.Pointer(int64(1)), Committed: api.Pointer(false)}}
	reads := 0
	reader := executionReaderFunc(func(context.Context, string, cpra.ExecutionResultPageOptions) (*cpra.Response[api.Operation], error) {
		reads++
		return &cpra.Response[api.Operation]{Data: op}, nil
	})
	got, err := Wait(context.Background(), reader, result)
	if err != nil || reads != 1 || got.Operation.ExecutionResult.Summary.Conflicts != 1 || got.Operation.State != "partial" || len(got.Operation.Items) != 1 {
		t.Fatal(got, err)
	}
}
