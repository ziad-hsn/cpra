package cpra

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

// ValidationPageOptions selects one original-order retained result page. A zero
// Limit requests 100 items; the maximum is 500. Cursors are opaque and scoped to
// the original result, authenticated reader, and page limit.
type ValidationPageOptions struct {
	Cursor string
	Limit  int
}

func (o ValidationPageOptions) validate() error {
	if o.Limit < 0 || o.Limit > 500 || len(o.Cursor) > 4096 {
		return errors.New("invalid validation page options")
	}
	return nil
}
func (o ValidationPageOptions) limit() int {
	if o.Limit == 0 {
		return 100
	}
	return o.Limit
}

// Validation reads a sealed result page. Pending compilation is a typed problem
// with code validationPending, not a negative validation verdict. The response
// has a separate 4 MiB ceiling, further reduced by Config.MaxResponseBytes.
func (s *OperationsService) Validation(ctx context.Context, id string, opts ValidationPageOptions) (*Response[api.ValidationResultPage], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	params := &transport.GetOperationValidationParams{Limit: ptr(opts.limit())}
	if opts.Cursor != "" {
		params.Cursor = ptr(opts.Cursor)
	}
	raw, err := s.c.generated.GetOperationValidation(ctx, id, params)
	var retryHeader string
	if raw != nil {
		retryHeader = raw.Header.Get("Retry-After")
	}
	r, err := responseBounded[api.ValidationResultPage](s.c, raw, err, false, 4<<20)
	var problem *Error
	serverID := ""
	if r != nil {
		serverID = r.OperationID
		if r.OperationID != "" && r.OperationID != id {
			return r, errors.New("validation operation identity mismatch")
		}
		r.OperationID = id // Caller handle, not proof that the server confirmed it.
	}
	if errors.As(err, &problem) {
		if problem.Problem.OperationID != "" && problem.Problem.OperationID != id {
			return r, errors.New("validation problem identity mismatch")
		}
		if problem.StatusCode == http.StatusConflict && problem.Problem.Code == "validationPending" {
			if serverID != id && problem.Problem.OperationID != id {
				return r, errors.New("pending validation omitted original operation identity")
			}
			delay, delayErr := validationRetryDelay(retryHeader, time.Now())
			if delayErr != nil {
				return r, delayErr
			}
			r.RetryAfter = delay
		}
	}
	if err != nil {
		return r, err
	}
	if r.StatusCode != http.StatusOK || r.Data.OperationID != id || len(r.Data.Items) > opts.limit() {
		return r, errors.New("invalid validation page response")
	}
	if err = api.ValidateValidationResultPage(r.Data); err != nil {
		return r, err
	}
	if opts.Cursor == "" && len(r.Data.Items) > 0 && r.Data.Items[0].Ordinal != 1 {
		return r, errors.New("validation first page is incomplete")
	}
	return r, nil
}

// WaitValidation polls only GET validation, retaining the original handle on
// cancellation. It returns one bounded first page and summary, never the entire
// collection. It does not submit, cancel, retry or replace a validation request.
func (s *OperationsService) WaitValidation(ctx context.Context, id string) (*Response[api.ValidationResultPage], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	last := &Response[api.ValidationResultPage]{OperationID: id}
	for {
		r, err := s.Validation(ctx, id, ValidationPageOptions{})
		if r != nil {
			last = r
		}
		if err == nil {
			return r, nil
		}
		var problem *Error
		if !errors.As(err, &problem) || problem.StatusCode != http.StatusConflict || problem.Problem.Code != "validationPending" {
			return last, err
		}
		if problem.Problem.OperationID != "" && problem.Problem.OperationID != id {
			return last, errors.New("validation problem identity mismatch")
		}
		delay := 5 * time.Second
		if last.RetryAfter > delay {
			delay = last.RetryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
}

// ValidationItems lazily reads result pages. It pins the complete original
// summary and collection identity across pages and retains at most one page.
func (s *OperationsService) ValidationItems(id string, opts ValidationPageOptions) *Iterator[api.ValidationResultItem] {
	var first *api.ValidationResultPage
	var lastOrdinal int64
	return &Iterator[api.ValidationResultItem]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.ValidationResultItem, string, error) {
		opts.Cursor = cursor
		r, err := s.Validation(ctx, id, opts)
		if err != nil {
			return nil, "", err
		}
		page := r.Data
		if first != nil && (page.OperationID != first.OperationID || page.IdentityFormat != first.IdentityFormat || page.ContentDigest != first.ContentDigest || page.ItemCount != first.ItemCount || !sameValidationSummary(page.Summary, first.Summary)) {
			return nil, "", errors.New("validation result changed between pages")
		}
		if first == nil {
			copy := page
			copy.Items = nil
			first = &copy
		} else if len(page.Items) > 0 && page.Items[0].Ordinal != lastOrdinal+1 {
			return nil, "", errors.New("validation page ordering changed")
		}
		if len(page.Items) > 0 {
			lastOrdinal = page.Items[len(page.Items)-1].Ordinal
		}
		return page.Items, page.NextCursor, nil
	}}
}

// The validation poller honors a longer server interval up to 24 hours. An
// unsupported/malformed interval fails explicitly; it never polls early.
func validationRetryDelay(value string, now time.Time) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	var d time.Duration
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > 86400 {
			return 0, errors.New("unsupported validation retry interval")
		}
		d = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		d = at.Sub(now)
		if d < 0 {
			d = 0
		}
		if d > 24*time.Hour {
			return 0, errors.New("unsupported validation retry interval")
		}
	} else {
		return 0, errors.New("invalid validation retry interval")
	}
	return d, nil
}
func sameValidationSummary(a, b api.ValidationResultSummary) bool {
	if !a.FinalizedAt.Equal(b.FinalizedAt) || !a.ExpiresAt.Equal(b.ExpiresAt) {
		return false
	}
	a.FinalizedAt = time.Time{}
	b.FinalizedAt = time.Time{}
	a.ExpiresAt = time.Time{}
	b.ExpiresAt = time.Time{}
	return a == b
}
