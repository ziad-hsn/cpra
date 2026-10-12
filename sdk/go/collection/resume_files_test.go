package collection

import (
	"context"
	"errors"
	"reflect"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type fileContinuationFixture struct {
	*reselectionFixture
	*fakeOperations
}

func (f *fileContinuationFixture) Get(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	return f.reselectionFixture.Get(ctx, id)
}

func (f *fileContinuationFixture) Validate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.fakeOperations.operation = f.reselectionFixture.operation
	return f.fakeOperations.Validate(ctx, id)
}

func (f *fileContinuationFixture) WaitValidation(ctx context.Context, id string) (*cpra.Response[api.ValidationResultPage], error) {
	f.fakeOperations.operation = f.reselectionFixture.operation
	return f.fakeOperations.WaitValidation(ctx, id)
}

func (f *fileContinuationFixture) Activate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.fakeOperations.operation = f.reselectionFixture.operation
	return f.fakeOperations.Activate(ctx, id)
}

func TestResumeFilesVerifiesCompleteInputBeforeActivation(t *testing.T) {
	for _, phase := range []string{"uploading", "validating", "validated"} {
		for _, profile := range []string{"", FileNormalizationProfile} {
			t.Run(phase+"/"+profile, func(t *testing.T) {
				f := &fileContinuationFixture{reselectionFixture: newReselectionFixture([]byte("original source")), fakeOperations: &fakeOperations{valid: true}}
				f.reselectionFixture.operation.State = phase
				f.reselectionFixture.operation.NormalizationProfile = profile
				f.attempt.NormalizationProfile = profile
				f.reselectionFixture.operation.Uploaded = api.Pointer(int64(3))
				f.attempt.OperationUploaded = 3
				result, err := ResumeFiles(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), reselectionTestOptions(t))
				if err != nil || result.OperationID != reselectOperationID || result.VerificationAttemptID != reselectAttemptID || result.Operation.State != "applying" {
					t.Fatal("continuation lost original operation", err, result.Operation.State)
				}
				want := []string{"waitValidation", "activate"}
				if phase == "uploading" {
					want = append([]string{"validate"}, want...)
				}
				if !reflect.DeepEqual(f.fakeOperations.calls, want) || !reflect.DeepEqual(f.received, f.raw) {
					t.Fatal("verification bypassed or original phase replayed", f.fakeOperations.calls, f.reselectionFixture.calls)
				}
			})
		}
	}
}

func TestResumeFilesMismatchAndLostVerificationNeverActivate(t *testing.T) {
	for _, fault := range []string{"changed", "verify", "resume"} {
		t.Run(fault, func(t *testing.T) {
			f := &fileContinuationFixture{reselectionFixture: newReselectionFixture([]byte("original source")), fakeOperations: &fakeOperations{valid: true}}
			f.reselectionFixture.operation.Uploaded = api.Pointer(int64(3))
			f.attempt.OperationUploaded = 3
			sources := reselectionTestSources(f.raw)
			if fault == "changed" {
				sources = reselectionTestSources([][]byte{[]byte("changed source")})
			} else {
				f.fault = fault
			}
			result, err := ResumeFiles(t.Context(), f, reselectOperationID, sources, reselectionTestOptions(t))
			if err == nil || result.OperationID != reselectOperationID || len(f.fakeOperations.calls) != 0 {
				t.Fatal("unsafe continuation activated", err, f.fakeOperations.calls)
			}
			if fault != "changed" && !errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("lost verification reply was not ambiguous", err)
			}
		})
	}
}
