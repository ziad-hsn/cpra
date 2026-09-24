//go:build externaljobs

package cpra

import (
	"context"
	"errors"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

type JobTypesService struct{ c *Client }

// List calls GET /api/v2/job-types.
func (s *JobTypesService) List(ctx context.Context, opts ListOptions) (*Response[api.JobTypeList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListJobTypes(ctx, &transport.ListJobTypesParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.JobTypeList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *JobTypesService) Iterate(opts ListOptions) *Iterator[api.JobType] {
	return &Iterator[api.JobType]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.JobType, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/job-types.
func (s *JobTypesService) Create(ctx context.Context, req api.JobType) (*Response[api.JobType], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateJobType(ctx, &transport.CreateJobTypeParams{IfNoneMatch: "*"}, req)
	return response[api.JobType](s.c, resp, err, true)
}

// Get calls GET /api/v2/job-types/{id}.
func (s *JobTypesService) Get(ctx context.Context, id string) (*Response[api.JobType], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetJobType(ctx, id)
	return response[api.JobType](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/job-types/{id}.
func (s *JobTypesService) Replace(ctx context.Context, id string, version string, req api.JobType) (*Response[api.JobType], error) {
	if req.Metadata.ID != id {
		return nil, errors.New("resource identity does not match request path")
	}
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ReplaceJobType(ctx, id, &transport.ReplaceJobTypeParams{IfMatch: strongETag(version)}, req)
	return response[api.JobType](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/job-types/{id}.
func (s *JobTypesService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteJobType(ctx, id, &transport.DeleteJobTypeParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

// Workers calls GET /api/v2/external-workers.
func (c *Client) Workers(ctx context.Context, opts ListOptions) (*Response[api.WorkerObservationList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := c.generated.ListWorkers(ctx, &transport.ListWorkersParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.WorkerObservationList](c, resp, err, false)
}
