package collection

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type admissionOperations struct {
	*fakeOperations
	prepare func(context.Context, api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error)
	create  func(context.Context, api.OperationCreateRequest) (*cpra.Response[api.Operation], error)
}

func (a *admissionOperations) Prepare(ctx context.Context, req api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
	if a.prepare != nil {
		return a.prepare(ctx, req)
	}
	return a.fakeOperations.Prepare(ctx, req)
}
func (a *admissionOperations) Create(ctx context.Context, req api.OperationCreateRequest) (*cpra.Response[api.Operation], error) {
	if a.create != nil {
		return a.create(ctx, req)
	}
	return a.fakeOperations.Create(ctx, req)
}

func TestApplyRetainsOriginalAdmissionAfterUncertainCreateAndExpiry(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	ticket := "private-original-ticket-canary"
	prepares, creates := 0, 0
	op := &admissionOperations{fakeOperations: &fakeOperations{}, prepare: func(_ context.Context, req api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
		prepares++
		identity, err := f.creationRequest()
		if err != nil || req.IdentityKey == nil || req.SourceFingerprint == nil ||
			*req.IdentityKey != *identity.IdentityKey || *req.SourceFingerprint != *identity.SourceFingerprint ||
			req.ContentDigest != identity.ContentDigest || req.ItemCount != identity.ItemCount || string(req.IdentityFormat) != string(identity.IdentityFormat) {
			t.Fatal("preparation did not bind original frozen identity")
		}
		// Local clock expiry cannot justify renewing the ticket. The server
		// decides expiry and the caller may only start a new attempt explicitly.
		return &cpra.Response[api.CollectionAdmission]{Data: api.CollectionAdmission{Ticket: ticket, ExpiresAt: time.Unix(1, 0)}}, nil
	}, create: func(_ context.Context, req api.OperationCreateRequest) (*cpra.Response[api.Operation], error) {
		creates++
		if req.AdmissionTicket != ticket || string(f.admissionTicket) != ticket {
			t.Fatal("original ticket was not retained before Create")
		}
		if creates == 1 {
			return nil, &cpra.AmbiguousError{}
		}
		return nil, cpra.ErrExpired
	}}
	for _, want := range []error{cpra.ErrAmbiguous, cpra.ErrExpired, cpra.ErrExpired} {
		if _, err := Apply(context.Background(), op, f); !errors.Is(err, want) {
			t.Fatal("unexpected creation outcome", err)
		}
	}
	if prepares != 1 || creates != 3 || len(op.calls) != 0 {
		t.Fatal("application renewed admission or continued after uncertain creation")
	}
	// Ticket lifetime is strictly memory-local and does not enter the spool.
	err := filepath.WalkDir(f.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if bytes.Contains(raw, []byte(ticket)) {
			t.Fatal("ticket persisted in client spool")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	retained := f.admissionTicket
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if f.admissionTicket != nil || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("Close did not clear owned ticket storage")
	}
}

func TestApplyAdmissionCancellationRetainsTicketWithoutCreate(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	ctx, cancel := context.WithCancel(context.Background())
	op := &admissionOperations{fakeOperations: &fakeOperations{valid: true}, prepare: func(_ context.Context, _ api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
		cancel()
		return &cpra.Response[api.CollectionAdmission]{Data: api.CollectionAdmission{Ticket: "original", ExpiresAt: time.Now().Add(time.Hour)}}, nil
	}}
	if _, err := Apply(ctx, op, f); !errors.Is(err, context.Canceled) || len(op.calls) != 0 {
		t.Fatal("canceled application attempted Create", err)
	}
	op.prepare = func(context.Context, api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
		t.Fatal("retained ticket was replaced")
		return nil, nil
	}
	if _, err := Apply(context.Background(), op, f); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op.calls, []string{"create", "upload", "validate", "waitValidation", "activate"}) {
		t.Fatal(op.calls)
	}
}

func TestApplyAdmissionConcurrentWorkAndClose(t *testing.T) {
	for _, closeDuringPrepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "busy", true: "close"}[closeDuringPrepare], func(t *testing.T) {
			f := freezeTest(t, monitor("one"))
			entered, unblock := make(chan struct{}), make(chan struct{})
			op := &admissionOperations{fakeOperations: &fakeOperations{valid: true}, prepare: func(context.Context, api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
				close(entered)
				<-unblock
				return &cpra.Response[api.CollectionAdmission]{Data: api.CollectionAdmission{Ticket: "original", ExpiresAt: time.Now().Add(time.Hour)}}, nil
			}}
			done := make(chan error, 1)
			go func() { _, err := Apply(context.Background(), op, f); done <- err }()
			<-entered
			if _, err := Apply(context.Background(), op, f); !errors.Is(err, ErrApplyInProgress) {
				t.Fatal(err)
			}
			if _, err := Resume(context.Background(), op, "epoch.1", f); !errors.Is(err, ErrApplyInProgress) {
				t.Fatal(err)
			}
			if closeDuringPrepare {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			close(unblock)
			err := <-done
			if closeDuringPrepare {
				if err == nil || len(op.calls) != 0 || f.admissionTicket != nil {
					t.Fatal("closed collection admitted creation", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyAdmissionInvalidResponseNeverCreates(t *testing.T) {
	for _, value := range []*cpra.Response[api.CollectionAdmission]{nil, {}, {Data: api.CollectionAdmission{Ticket: "value"}}, {Data: api.CollectionAdmission{ExpiresAt: time.Now()}}, {Data: api.CollectionAdmission{Ticket: strings.Repeat("s", (128<<10)+1), ExpiresAt: time.Now()}}} {
		f := freezeTest(t, monitor("one"))
		op := &admissionOperations{fakeOperations: &fakeOperations{}, prepare: func(context.Context, api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
			return value, nil
		}}
		if _, err := Apply(context.Background(), op, f); err == nil || len(op.calls) != 0 || len(f.admissionTicket) != 0 {
			t.Fatal("invalid ticket accepted")
		}
	}
}
