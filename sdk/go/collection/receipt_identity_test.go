package collection

import (
	"context"
	"reflect"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type alteredReceipts struct {
	*fakeOperations
	phase           string
	operationChange func(*api.Operation)
	preflightChange func(*api.Preflight)
}

func (a *alteredReceipts) Create(ctx context.Context, req api.OperationCreateRequest) (*cpra.Response[api.Operation], error) {
	r, err := a.fakeOperations.Create(ctx, req)
	if a.phase == "create" {
		a.operationChange(&r.Data)
	}
	return r, err
}
func (a *alteredReceipts) Upload(ctx context.Context, id string, req api.UploadRequest) (*cpra.Response[api.Operation], error) {
	r, err := a.fakeOperations.Upload(ctx, id, req)
	if a.phase == "upload" {
		a.operationChange(&r.Data)
	}
	return r, err
}
func (a *alteredReceipts) Activate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	r, err := a.fakeOperations.Activate(ctx, id)
	if a.phase == "activate" {
		a.operationChange(&r.Data)
	}
	return r, err
}
func (a *alteredReceipts) Validate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	r, err := a.fakeOperations.Validate(ctx, id)
	if a.phase == "validate" {
		a.operationChange(&r.Data)
	}
	return r, err
}
func (a *alteredReceipts) Preflight(ctx context.Context, req api.PreflightRequest) (*cpra.Response[api.Preflight], error) {
	r, err := a.fakeOperations.Preflight(ctx, req)
	a.preflightChange(&r.Data)
	return r, err
}

func TestApplyRejectsUnboundOperationReceipts(t *testing.T) {
	changes := map[string]func(*api.Operation){
		"missing-count":                 func(o *api.Operation) { o.ItemCount = nil },
		"changed-count":                 func(o *api.Operation) { o.ItemCount = api.Pointer(int64(2)) },
		"unknown-format":                func(o *api.Operation) { o.IdentityFormat = "other" },
		"changed-normalization-profile": func(o *api.Operation) { o.NormalizationProfile = FileNormalizationProfile },
		"changed-digest":                func(o *api.Operation) { o.ContentDigest = "other" },
		"negative-upload":               func(o *api.Operation) { o.Uploaded = api.Pointer(int64(-1)) },
		"excessive-upload":              func(o *api.Operation) { o.Uploaded = api.Pointer(int64(2)) },
	}
	for _, phase := range []string{"create", "upload", "validate", "activate"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				f := freezeTest(t, monitor("one"))
				op := &alteredReceipts{fakeOperations: &fakeOperations{valid: true}, phase: phase, operationChange: change}
				result, err := Apply(context.Background(), op, f)
				if err == nil || result.OperationID != "epoch.1" {
					t.Fatal("unbound operation receipt accepted or handle lost", err)
				}
				want := []string{"prepare", "create"}
				if phase != "create" {
					want = append(want, "upload")
				}
				if phase == "validate" {
					want = append(want, "validate")
				}
				if phase == "activate" {
					want = append(want, "validate", "waitValidation", "activate")
				}
				if !reflect.DeepEqual(op.calls, want) {
					t.Fatal("mutation followed unbound receipt", op.calls)
				}
			})
		}
	}
	for _, phase := range []string{"create", "upload"} {
		t.Run(phase+"/terminal", func(t *testing.T) {
			f := freezeTest(t, monitor("one"))
			op := &alteredReceipts{fakeOperations: &fakeOperations{valid: true}, phase: phase, operationChange: func(o *api.Operation) { o.State = "completed" }}
			if _, err := Apply(context.Background(), op, f); err == nil {
				t.Fatal("terminal receipt triggered further mutation")
			}
			if op.calls[len(op.calls)-1] != phase {
				t.Fatal(op.calls)
			}
		})
	}
}

func TestResumeDoesNotRelabelFrozenInputWithFileNormalization(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	op := &fakeOperations{valid: true, operation: api.Operation{ID: "original", State: "staging", IdentityFormat: commitment.Format,
		NormalizationProfile: FileNormalizationProfile, ItemCount: api.Pointer(int64(f.Len())), ContentDigest: f.Digest(), Uploaded: api.Pointer(int64(0))}}
	result, err := Resume(context.Background(), op, "original", f)
	if err == nil || result.OperationID != "original" || !reflect.DeepEqual(op.calls, []string{"get"}) {
		t.Fatal("resume accepted a substituted normalization profile", err, op.calls)
	}
}

func TestCollectionPreflightReceiptsRequireExactDeclaredCount(t *testing.T) {
	for _, phase := range []string{"preflight", "validate"} {
		for _, count := range []*int64{nil, api.Pointer(int64(-1)), api.Pointer(int64(0)), api.Pointer(int64(2))} {
			f := freezeTest(t, monitor("one"))
			op := &alteredReceipts{fakeOperations: &fakeOperations{valid: true}, phase: phase, preflightChange: func(p *api.Preflight) { p.ItemCount = count }, operationChange: func(o *api.Operation) { o.ItemCount = count }}
			var err error
			if phase == "preflight" {
				_, err = Preflight(context.Background(), op, f)
			} else {
				_, err = Apply(context.Background(), op, f)
			}
			if err == nil {
				t.Fatal("preflight accepted omitted/changed declared count")
			}
			for _, call := range op.calls {
				if call == "activate" {
					t.Fatal("activated despite invalid validation receipt")
				}
			}
		}
	}
}

func TestResumeRequiresOriginalCountBeforeMutation(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	for _, state := range []string{"staging", "validated", "applying", "completed"} {
		for _, count := range []*int64{nil, api.Pointer(int64(2))} {
			op := &fakeOperations{valid: true, operation: api.Operation{ID: "original", State: state, IdentityFormat: commitment.Format, ItemCount: count, ContentDigest: f.Digest(), Uploaded: api.Pointer(int64(1))}}
			result, err := Resume(context.Background(), op, "original", f)
			if err == nil || result.OperationID != "original" || !reflect.DeepEqual(op.calls, []string{"get"}) {
				t.Fatal("resume ignored original collection count", err)
			}
		}
	}
}
