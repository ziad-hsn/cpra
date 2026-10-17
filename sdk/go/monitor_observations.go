package cpra

import (
	"context"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

// MonitorObservationsService reads controller observations in both management
// and file-configured modes. These values are not writable monitor resources.
type MonitorObservationsService struct{ c *Client }

// List reads one bounded stable-ID ordered page. Membership and incarnation are
// fixed by the cursor; health can advance between reads. Selectors are unsupported.
func (s *MonitorObservationsService) List(ctx context.Context, opts ListOptions) (*Response[api.MonitorObservationList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListMonitorObservations(ctx, &transport.ListMonitorObservationsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.MonitorObservationList](s.c, resp, err, false)
}

// Get reads one monitor by stable ID without retrieving its provider configuration.
func (s *MonitorObservationsService) Get(ctx context.Context, id string) (*Response[api.MonitorObservation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetMonitorObservation(ctx, id)
	return response[api.MonitorObservation](s.c, resp, err, false)
}

// Iterate retains one page and fetches the next only when Next is called.
func (s *MonitorObservationsService) Iterate(opts ListOptions) *Iterator[api.MonitorObservation] {
	return &Iterator[api.MonitorObservation]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.MonitorObservation, string, error) {
		opts.Cursor = cursor
		result, err := s.List(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		return result.Data.Items, result.Data.NextCursor, nil
	}}
}
