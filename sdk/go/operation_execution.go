package cpra

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

var (
	// ErrExecutionResultUnsupported means availability is absent or uses an unknown contract.
	ErrExecutionResultUnsupported = errors.New("execution result availability is unsupported")
	// ErrExecutionResultExpired reports explicit retained-result expiry, not a missing operation.
	ErrExecutionResultExpired = errors.New("execution result has expired")
	// ErrExecutionResultPending means execution or immutable publication is still pending.
	ErrExecutionResultPending = errors.New("execution result is pending")
)

// ExecutionResultPageOptions selects a retained page on the existing operation
// detail endpoint. Limit defaults to 100 and cannot exceed 500.
type ExecutionResultPageOptions struct {
	Cursor string
	Limit  int
}

func (o ExecutionResultPageOptions) validate() error {
	if o.Limit < 0 || o.Limit > 500 || len(o.Cursor) > 4096 {
		return errors.New("invalid execution result page options")
	}
	return nil
}
func (o ExecutionResultPageOptions) limit() int {
	if o.Limit == 0 {
		return 100
	}
	return o.Limit
}

// ExecutionResult reads one bounded operation detail page. It preserves pending,
// expired and unknown availability as observations; it never activates work.
func (s *OperationsService) ExecutionResult(ctx context.Context, id string, opts ExecutionResultPageOptions) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	params := &transport.GetOperationParams{Limit: ptr(opts.limit())}
	if opts.Cursor != "" {
		params.Cursor = ptr(opts.Cursor)
	}
	raw, err := s.c.generated.GetOperation(ctx, id, params)
	retryHeader := ""
	if raw != nil {
		retryHeader = raw.Header.Get("Retry-After")
	}
	r, err := responseBounded[api.Operation](s.c, raw, err, false, 4<<20)
	if r != nil {
		if r.OperationID != "" && r.OperationID != id {
			return r, errors.New("execution result operation identity mismatch")
		}
		r.OperationID = id
	}
	if err != nil {
		return r, err
	}
	if r.StatusCode != http.StatusOK || r.Data.ID != id || len(r.Data.Items) > opts.limit() {
		return r, errors.New("invalid execution result response")
	}
	if err = api.ValidateExecutionResult(r.Data); err != nil {
		return r, err
	}
	if r.Data.ExecutionResult != nil && opts.Cursor == "" && len(r.Data.Items) > 0 && (r.Data.Items[0].InputOrdinal == nil || *r.Data.Items[0].InputOrdinal != 1) {
		return r, errors.New("execution first page is incomplete")
	}
	if opts.Cursor != "" && r.Data.NextCursor == opts.Cursor {
		return r, errors.New("execution result repeated cursor")
	}
	delay, err := validationRetryDelay(retryHeader, time.Now())
	if err != nil {
		return r, err
	}
	r.RetryAfter = delay
	if r.Data.RetryAfterSeconds < 0 || r.Data.RetryAfterSeconds > 86400 {
		return r, errors.New("unsupported execution retry interval")
	}
	return r, nil
}

// WaitExecutionResult waits for an explicitly ready retained result and returns
// one first page. Parent cancellation is not result readiness. Canceling ctx
// stops this reader only; it never cancels, resubmits or replaces the operation.
func (s *OperationsService) WaitExecutionResult(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	last := &Response[api.Operation]{OperationID: id}
	for {
		r, err := s.ExecutionResult(ctx, id, ExecutionResultPageOptions{})
		if err != nil {
			return last, err
		}
		if last.Data.ID != "" && (r.Data.IdentityFormat != last.Data.IdentityFormat || r.Data.NormalizationProfile != last.Data.NormalizationProfile || r.Data.ContentDigest != last.Data.ContentDigest || (r.Data.ItemCount == nil) != (last.Data.ItemCount == nil) || r.Data.ItemCount != nil && *r.Data.ItemCount != *last.Data.ItemCount) {
			return last, errors.New("execution result collection identity changed")
		}
		last = r
		if r.Data.ExecutionResult == nil {
			return last, ErrExecutionResultUnsupported
		}
		switch r.Data.ExecutionResult.State {
		case "ready":
			return last, nil
		case "expired":
			return last, ErrExecutionResultExpired
		case "pending":
		default:
			return last, ErrExecutionResultUnsupported
		}
		delay := max(5*time.Second, r.RetryAfter, time.Duration(r.Data.RetryAfterSeconds)*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
}

// ExecutionItems lazily reads immutable original-input-order results while
// pinning the complete summary and collection identity across every page.
func (s *OperationsService) ExecutionItems(id string, opts ExecutionResultPageOptions) *Iterator[api.ApplyResult] {
	var first *api.Operation
	var ordinal int64
	return &Iterator[api.ApplyResult]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.ApplyResult, string, error) {
		opts.Cursor = cursor
		r, err := s.ExecutionResult(ctx, id, opts)
		if err != nil {
			return nil, "", err
		}
		p := r.Data
		if p.ExecutionResult == nil {
			return nil, "", ErrExecutionResultUnsupported
		}
		switch p.ExecutionResult.State {
		case "ready":
		case "expired":
			return nil, "", ErrExecutionResultExpired
		case "pending":
			return nil, "", ErrExecutionResultPending
		default:
			return nil, "", ErrExecutionResultUnsupported
		}
		if first != nil && (p.ID != first.ID || p.IdentityFormat != first.IdentityFormat || p.NormalizationProfile != first.NormalizationProfile || p.ContentDigest != first.ContentDigest || *p.ItemCount != *first.ItemCount || !sameExecutionSummary(*p.ExecutionResult.Summary, *first.ExecutionResult.Summary)) {
			return nil, "", errors.New("execution result changed between pages")
		}
		if first == nil {
			copy := p
			copy.Items = nil
			first = &copy
		} else if len(p.Items) > 0 && *p.Items[0].InputOrdinal != ordinal+1 {
			return nil, "", errors.New("execution result ordering changed")
		}
		if len(p.Items) > 0 {
			ordinal = *p.Items[len(p.Items)-1].InputOrdinal
		}
		return p.Items, p.NextCursor, nil
	}}
}
func sameExecutionSummary(a, b api.ExecutionResultSummary) bool {
	if !a.FinalizedAt.Equal(b.FinalizedAt) || !a.ExpiresAt.Equal(b.ExpiresAt) {
		return false
	}
	a.FinalizedAt = time.Time{}
	a.ExpiresAt = time.Time{}
	b.FinalizedAt = time.Time{}
	b.ExpiresAt = time.Time{}
	return a == b
}
