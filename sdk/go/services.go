package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
	"net/http"
	"time"
)

type MonitorsService struct{ c *Client }

// List calls GET /api/v2/monitors.
func (s *MonitorsService) List(ctx context.Context, opts ListOptions) (*Response[api.MonitorList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListMonitors(ctx, &transport.ListMonitorsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.MonitorList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *MonitorsService) Iterate(opts ListOptions) *Iterator[api.Monitor] {
	return &Iterator[api.Monitor]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Monitor, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/monitors.
func (s *MonitorsService) Create(ctx context.Context, req api.Monitor) (*Response[api.Monitor], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateMonitor(ctx, &transport.CreateMonitorParams{IfNoneMatch: "*"}, req)
	return response[api.Monitor](s.c, resp, err, true)
}

// Get calls GET /api/v2/monitors/{id}.
func (s *MonitorsService) Get(ctx context.Context, id string) (*Response[api.Monitor], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetMonitor(ctx, id)
	return response[api.Monitor](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/monitors/{id}.
func (s *MonitorsService) Replace(ctx context.Context, id string, version string, req api.Monitor) (*Response[api.Monitor], error) {
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
	resp, err := s.c.generated.ReplaceMonitor(ctx, id, &transport.ReplaceMonitorParams{IfMatch: strongETag(version)}, req)
	return response[api.Monitor](s.c, resp, err, true)
}

// Patch calls PATCH /api/v2/monitors/{id}.
func (s *MonitorsService) Patch(ctx context.Context, id string, version string, req api.MergePatch) (*Response[api.Monitor], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := validatePatch(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PatchMonitorWithApplicationMergePatchPlusJSONBody(ctx, id, &transport.PatchMonitorParams{IfMatch: strongETag(version)}, req)
	return response[api.Monitor](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/monitors/{id}.
func (s *MonitorsService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteMonitor(ctx, id, &transport.DeleteMonitorParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

type NotificationEndpointsService struct{ c *Client }

// List calls GET /api/v2/notification-endpoints.
func (s *NotificationEndpointsService) List(ctx context.Context, opts ListOptions) (*Response[api.NotificationEndpointList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListNotificationEndpoints(ctx, &transport.ListNotificationEndpointsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.NotificationEndpointList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *NotificationEndpointsService) Iterate(opts ListOptions) *Iterator[api.NotificationEndpoint] {
	return &Iterator[api.NotificationEndpoint]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.NotificationEndpoint, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/notification-endpoints.
func (s *NotificationEndpointsService) Create(ctx context.Context, req api.NotificationEndpoint) (*Response[api.NotificationEndpoint], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateNotificationEndpoint(ctx, &transport.CreateNotificationEndpointParams{IfNoneMatch: "*"}, req)
	return response[api.NotificationEndpoint](s.c, resp, err, true)
}

// Get calls GET /api/v2/notification-endpoints/{id}.
func (s *NotificationEndpointsService) Get(ctx context.Context, id string) (*Response[api.NotificationEndpoint], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetNotificationEndpoint(ctx, id)
	return response[api.NotificationEndpoint](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/notification-endpoints/{id}.
func (s *NotificationEndpointsService) Replace(ctx context.Context, id string, version string, req api.NotificationEndpoint) (*Response[api.NotificationEndpoint], error) {
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
	resp, err := s.c.generated.ReplaceNotificationEndpoint(ctx, id, &transport.ReplaceNotificationEndpointParams{IfMatch: strongETag(version)}, req)
	return response[api.NotificationEndpoint](s.c, resp, err, true)
}

// Patch calls PATCH /api/v2/notification-endpoints/{id}.
func (s *NotificationEndpointsService) Patch(ctx context.Context, id string, version string, req api.MergePatch) (*Response[api.NotificationEndpoint], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := validatePatch(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PatchNotificationEndpointWithApplicationMergePatchPlusJSONBody(ctx, id, &transport.PatchNotificationEndpointParams{IfMatch: strongETag(version)}, req)
	return response[api.NotificationEndpoint](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/notification-endpoints/{id}.
func (s *NotificationEndpointsService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteNotificationEndpoint(ctx, id, &transport.DeleteNotificationEndpointParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

type NotificationGroupsService struct{ c *Client }

// List calls GET /api/v2/notification-groups.
func (s *NotificationGroupsService) List(ctx context.Context, opts ListOptions) (*Response[api.NotificationGroupList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListNotificationGroups(ctx, &transport.ListNotificationGroupsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.NotificationGroupList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *NotificationGroupsService) Iterate(opts ListOptions) *Iterator[api.NotificationGroup] {
	return &Iterator[api.NotificationGroup]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.NotificationGroup, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/notification-groups.
func (s *NotificationGroupsService) Create(ctx context.Context, req api.NotificationGroup) (*Response[api.NotificationGroup], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateNotificationGroup(ctx, &transport.CreateNotificationGroupParams{IfNoneMatch: "*"}, req)
	return response[api.NotificationGroup](s.c, resp, err, true)
}

// Get calls GET /api/v2/notification-groups/{id}.
func (s *NotificationGroupsService) Get(ctx context.Context, id string) (*Response[api.NotificationGroup], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetNotificationGroup(ctx, id)
	return response[api.NotificationGroup](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/notification-groups/{id}.
func (s *NotificationGroupsService) Replace(ctx context.Context, id string, version string, req api.NotificationGroup) (*Response[api.NotificationGroup], error) {
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
	resp, err := s.c.generated.ReplaceNotificationGroup(ctx, id, &transport.ReplaceNotificationGroupParams{IfMatch: strongETag(version)}, req)
	return response[api.NotificationGroup](s.c, resp, err, true)
}

// Patch calls PATCH /api/v2/notification-groups/{id}.
func (s *NotificationGroupsService) Patch(ctx context.Context, id string, version string, req api.MergePatch) (*Response[api.NotificationGroup], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := validatePatch(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PatchNotificationGroupWithApplicationMergePatchPlusJSONBody(ctx, id, &transport.PatchNotificationGroupParams{IfMatch: strongETag(version)}, req)
	return response[api.NotificationGroup](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/notification-groups/{id}.
func (s *NotificationGroupsService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteNotificationGroup(ctx, id, &transport.DeleteNotificationGroupParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

type RecipientsService struct{ c *Client }

// List calls GET /api/v2/recipients.
func (s *RecipientsService) List(ctx context.Context, opts ListOptions) (*Response[api.RecipientList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListRecipients(ctx, &transport.ListRecipientsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.RecipientList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *RecipientsService) Iterate(opts ListOptions) *Iterator[api.Recipient] {
	return &Iterator[api.Recipient]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Recipient, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/recipients.
func (s *RecipientsService) Create(ctx context.Context, req api.Recipient) (*Response[api.Recipient], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateRecipient(ctx, &transport.CreateRecipientParams{IfNoneMatch: "*"}, req)
	return response[api.Recipient](s.c, resp, err, true)
}

// Get calls GET /api/v2/recipients/{id}.
func (s *RecipientsService) Get(ctx context.Context, id string) (*Response[api.Recipient], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetRecipient(ctx, id)
	return response[api.Recipient](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/recipients/{id}.
func (s *RecipientsService) Replace(ctx context.Context, id string, version string, req api.Recipient) (*Response[api.Recipient], error) {
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
	resp, err := s.c.generated.ReplaceRecipient(ctx, id, &transport.ReplaceRecipientParams{IfMatch: strongETag(version)}, req)
	return response[api.Recipient](s.c, resp, err, true)
}

// Patch calls PATCH /api/v2/recipients/{id}.
func (s *RecipientsService) Patch(ctx context.Context, id string, version string, req api.MergePatch) (*Response[api.Recipient], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := validatePatch(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PatchRecipientWithApplicationMergePatchPlusJSONBody(ctx, id, &transport.PatchRecipientParams{IfMatch: strongETag(version)}, req)
	return response[api.Recipient](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/recipients/{id}.
func (s *RecipientsService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteRecipient(ctx, id, &transport.DeleteRecipientParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

type CredentialsService struct{ c *Client }

// List calls GET /api/v2/credentials.
func (s *CredentialsService) List(ctx context.Context, opts ListOptions) (*Response[api.CredentialList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListCredentials(ctx, &transport.ListCredentialsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.CredentialList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *CredentialsService) Iterate(opts ListOptions) *Iterator[api.Credential] {
	return &Iterator[api.Credential]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Credential, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Create calls POST /api/v2/credentials.
func (s *CredentialsService) Create(ctx context.Context, req api.Credential) (*Response[api.Credential], error) {
	if err := api.ValidateResourceValue(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateCredential(ctx, &transport.CreateCredentialParams{IfNoneMatch: "*"}, req)
	return response[api.Credential](s.c, resp, err, true)
}

// Get calls GET /api/v2/credentials/{id}.
func (s *CredentialsService) Get(ctx context.Context, id string) (*Response[api.Credential], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetCredential(ctx, id)
	return response[api.Credential](s.c, resp, err, false)
}

// Replace calls PUT /api/v2/credentials/{id}.
func (s *CredentialsService) Replace(ctx context.Context, id string, version string, req api.Credential) (*Response[api.Credential], error) {
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
	resp, err := s.c.generated.ReplaceCredential(ctx, id, &transport.ReplaceCredentialParams{IfMatch: strongETag(version)}, req)
	return response[api.Credential](s.c, resp, err, true)
}

// Patch calls PATCH /api/v2/credentials/{id}.
func (s *CredentialsService) Patch(ctx context.Context, id string, version string, req api.MergePatch) (*Response[api.Credential], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	if err := validatePatch(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PatchCredentialWithApplicationMergePatchPlusJSONBody(ctx, id, &transport.PatchCredentialParams{IfMatch: strongETag(version)}, req)
	return response[api.Credential](s.c, resp, err, true)
}

// Delete calls DELETE /api/v2/credentials/{id}.
func (s *CredentialsService) Delete(ctx context.Context, id string, version string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := precondition(version); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DeleteCredential(ctx, id, &transport.DeleteCredentialParams{IfMatch: strongETag(version)})
	return response[api.Operation](s.c, resp, err, true)
}

type IncidentsService struct{ c *Client }

// List calls GET /api/v2/incidents.
func (s *IncidentsService) List(ctx context.Context, opts ListOptions) (*Response[api.IncidentList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListIncidents(ctx, &transport.ListIncidentsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.IncidentList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *IncidentsService) Iterate(opts ListOptions) *Iterator[api.Incident] {
	return &Iterator[api.Incident]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Incident, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Get calls GET /api/v2/incidents/{id}.
func (s *IncidentsService) Get(ctx context.Context, id string) (*Response[api.Incident], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetIncident(ctx, id)
	return response[api.Incident](s.c, resp, err, false)
}

type ActionsService struct{ c *Client }

// List calls GET /api/v2/actions.
func (s *ActionsService) List(ctx context.Context, opts ListOptions) (*Response[api.ActionList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListActions(ctx, &transport.ListActionsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.ActionList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *ActionsService) Iterate(opts ListOptions) *Iterator[api.Action] {
	return &Iterator[api.Action]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Action, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Get calls GET /api/v2/actions/{id}.
func (s *ActionsService) Get(ctx context.Context, id string) (*Response[api.Action], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetAction(ctx, id)
	return response[api.Action](s.c, resp, err, false)
}

// Acknowledge calls POST /api/v2/incidents/{id}/acknowledge.
func (s *IncidentsService) Acknowledge(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Incident], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.AcknowledgeIncident(ctx, id, &transport.AcknowledgeIncidentParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Incident](s.c, resp, err, true)
}

// Dismiss calls POST /api/v2/incidents/{id}/dismiss.
func (s *IncidentsService) Dismiss(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Incident], error) {
	if req.Reason == "" {
		return nil, errors.New("reason is required")
	}
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.DismissIncident(ctx, id, &transport.DismissIncidentParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Incident](s.c, resp, err, true)
}

// Reopen calls POST /api/v2/incidents/{id}/reopen.
func (s *IncidentsService) Reopen(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Incident], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ReopenIncident(ctx, id, &transport.ReopenIncidentParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Incident](s.c, resp, err, true)
}

// Snooze calls POST /api/v2/monitors/{id}/snooze.
func (s *MonitorsService) Snooze(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Operation], error) {
	if req.Reason == "" {
		return nil, errors.New("reason is required")
	}
	duration, err := time.ParseDuration(req.Duration)
	if err != nil || duration <= 0 {
		return nil, errors.New("snooze duration must be positive")
	}
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.SnoozeMonitor(ctx, id, &transport.SnoozeMonitorParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Operation](s.c, resp, err, true)
}

// Unsnooze calls POST /api/v2/monitors/{id}/unsnooze.
func (s *MonitorsService) Unsnooze(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.UnsnoozeMonitor(ctx, id, &transport.UnsnoozeMonitorParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Operation](s.c, resp, err, true)
}

// Recover calls POST /api/v2/monitors/{id}/recover.
func (s *MonitorsService) Recover(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Operation], error) {
	if req.Reason == "" {
		return nil, errors.New("reason is required")
	}
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.RecoverMonitor(ctx, id, &transport.RecoverMonitorParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Operation](s.c, resp, err, true)
}

// Review calls POST /api/v2/actions/{id}/review.
func (s *ActionsService) Review(ctx context.Context, id string, req api.ControlRequest) (*Response[api.Action], error) {
	if req.Reason == "" {
		return nil, errors.New("reason is required")
	}
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := control(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ReviewAction(ctx, id, &transport.ReviewActionParams{IfMatch: strongETag(req.Revision)}, req)
	return response[api.Action](s.c, resp, err, true)
}

type OperationsService struct{ c *Client }

// Preflight calls POST /api/v2/collections/preflight.
func (s *OperationsService) Preflight(ctx context.Context, req api.PreflightRequest) (*Response[api.Preflight], error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 4<<20 {
		return nil, errors.New("preflight exceeds 4 MiB; use staged operation validation for larger collections")
	}
	if err = validateCollectionPreflight(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PreflightCollection(ctx, req)
	return response[api.Preflight](s.c, resp, err, false)
}

// List returns one bounded page of collection operations.
func (s *OperationsService) List(ctx context.Context, opts ListOptions) (*Response[api.OperationList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ListOperations(ctx, &transport.ListOperationsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.OperationList](s.c, resp, err, false)
}

// Iterate walks operation pages lazily without collecting all operations.
func (s *OperationsService) Iterate(opts ListOptions) *Iterator[api.Operation] {
	return &Iterator[api.Operation]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Operation, string, error) {
		opts.Cursor = cursor
		r, err := s.List(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// Prepare obtains a private, expiring creation ticket without uploading or
// activating resources. Retain this ticket with the original frozen collection;
// never replace it automatically after attempting Create.
func (s *OperationsService) Prepare(ctx context.Context, req api.CollectionPrepareRequest) (*Response[api.CollectionAdmission], error) {
	if err := validateCollectionPreparation(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.PrepareCollection(ctx, req)
	r, err := response[api.CollectionAdmission](s.c, resp, err, false)
	if err == nil && (r == nil || r.Data.ExpiresAt.IsZero() || len(r.Data.Ticket) == 0 || len(r.Data.Ticket) > 128<<10) {
		return nil, errors.New("invalid collection admission response")
	}
	return r, err
}

// Create calls POST /api/v2/operations with the original preparation ticket.
func (s *OperationsService) Create(ctx context.Context, req api.OperationCreateRequest) (*Response[api.Operation], error) {
	if err := validateCollectionCreation(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CreateOperation(ctx, req)
	return response[api.Operation](s.c, resp, err, true)
}

// Upload calls PUT /api/v2/operations/{id}/items.
func (s *OperationsService) Upload(ctx context.Context, id string, req api.UploadRequest) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if len(req.Items) > 256 {
		return nil, errors.New("upload exceeds 256 resources")
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 4<<20 {
		return nil, errors.New("upload exceeds 4 MiB")
	}
	if err = validateCollectionUpload(req); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.UploadOperation(ctx, id, req)
	return response[api.Operation](s.c, resp, err, true)
}

// Activate calls POST /api/v2/operations/{id}/activate.
func (s *OperationsService) Activate(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ActivateOperation(ctx, id)
	r, err := response[api.Operation](s.c, resp, err, true)
	if err == nil && (r.StatusCode != http.StatusOK || r.Data.ID != id || r.OperationID != "" && r.OperationID != id || !activationAdmitted(r.Data)) {
		return r, &AmbiguousError{Cause: errors.New("invalid activation admission response"), OperationID: id}
	}
	if r != nil {
		r.OperationID = id
	}
	if errors.Is(err, ErrAmbiguous) {
		return r, &AmbiguousError{Cause: err, OperationID: id}
	}
	return r, err
}

func activationAdmitted(operation api.Operation) bool {
	if operation.State == "applying" {
		return true
	}
	if operation.ExecutionResult == nil || api.ValidateExecutionResultMetadata(operation) != nil {
		return false
	}
	switch operation.ExecutionResult.State {
	case "pending", "ready", "expired":
		return true
	}
	return false
}

// Cancel calls POST /api/v2/operations/{id}/cancel.
func (s *OperationsService) Cancel(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.CancelOperation(ctx, id)
	return response[api.Operation](s.c, resp, err, true)
}

// Get calls GET /api/v2/operations/{id}.
func (s *OperationsService) Get(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetOperation(ctx, id, nil)
	return response[api.Operation](s.c, resp, err, false)
}

// State calls GET /api/v2/state.
func (c *Client) State(ctx context.Context) (*Response[api.State], error) {
	resp, err := c.generated.GetState(ctx)
	return response[api.State](c, resp, err, false)
}

// History calls GET /api/v2/history.
func (c *Client) History(ctx context.Context, opts ListOptions) (*Response[api.EventList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := validID(opts.MonitorID); err != nil {
		return nil, err
	}
	resp, err := c.generated.GetHistory(ctx, &transport.GetHistoryParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: opts.MonitorID})
	return response[api.EventList](c, resp, err, false)
}

type SLOService struct{ c *Client }

// Get calls GET /api/v2/slo.
func (s *SLOService) Get(ctx context.Context) (*Response[api.SLOView], error) {
	resp, err := s.c.generated.GetSLO(ctx)
	return response[api.SLOView](s.c, resp, err, false)
}

type QueuesService struct{ c *Client }

// List calls GET /api/v2/queues.
func (s *QueuesService) List(ctx context.Context, opts ListOptions) (*Response[api.QueueList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetQueues(ctx, &transport.GetQueuesParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.QueueList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *QueuesService) Iterate(opts ListOptions) *Iterator[api.Queue] {
	return &Iterator[api.Queue]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Queue, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

type PoolsService struct{ c *Client }

// List calls GET /api/v2/pools.
func (s *PoolsService) List(ctx context.Context, opts ListOptions) (*Response[api.PoolList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetPools(ctx, &transport.GetPoolsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.PoolList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *PoolsService) Iterate(opts ListOptions) *Iterator[api.Pool] {
	return &Iterator[api.Pool]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.Pool, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

type SystemsService struct{ c *Client }

// List calls GET /api/v2/systems.
func (s *SystemsService) List(ctx context.Context, opts ListOptions) (*Response[api.SystemList], error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.GetSystems(ctx, &transport.GetSystemsParams{Cursor: ptr(opts.Cursor), Limit: ptr(opts.limit()), Selector: ptr(opts.Selector), MonitorID: ptr(opts.MonitorID)})
	return response[api.SystemList](s.c, resp, err, false)
}

// Iterate walks pages lazily.
func (s *SystemsService) Iterate(opts ListOptions) *Iterator[api.System] {
	return &Iterator[api.System]{next: opts.Cursor, fetch: func(ctx context.Context, cursor string) ([]api.System, string, error) {
		opts.Cursor = cursor
		r, e := s.List(ctx, opts)
		if e != nil {
			return nil, "", e
		}
		return r.Data.Items, r.Data.NextCursor, nil
	}}
}

// RuntimeConfig calls GET /api/v2/config.
func (c *Client) RuntimeConfig(ctx context.Context) (*Response[api.RuntimeConfig], error) {
	resp, err := c.generated.GetConfig(ctx)
	return response[api.RuntimeConfig](c, resp, err, false)
}

// Ready calls GET /api/v2/readyz.
func (c *Client) Ready(ctx context.Context) (*Response[api.Health], error) {
	resp, err := c.generated.GetReady(ctx)
	return response[api.Health](c, resp, err, false)
}

// Live calls GET /api/v2/healthz.
func (c *Client) Live(ctx context.Context) (*Response[api.Health], error) {
	resp, err := c.generated.GetLive(ctx)
	return response[api.Health](c, resp, err, false)
}

// Capabilities calls GET /api/v2/discovery.
func (c *Client) Capabilities(ctx context.Context) (*Response[api.Capabilities], error) {
	resp, err := c.generated.GetCapabilities(ctx)
	return response[api.Capabilities](c, resp, err, false)
}

// Version calls GET /api/v2/version.
func (c *Client) Version(ctx context.Context) (*Response[api.Version], error) {
	resp, err := c.generated.GetVersion(ctx)
	return response[api.Version](c, resp, err, false)
}

// Explain calls GET /api/v2/schemas/{id}.
func (c *Client) Explain(ctx context.Context, id string) (*Response[api.Explanation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := c.generated.ExplainResource(ctx, id)
	return response[api.Explanation](c, resp, err, false)
}

// Validate calls POST /api/v2/operations/{id}/validate.
func (s *OperationsService) Validate(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	resp, err := s.c.generated.ValidateOperation(ctx, id)
	r, err := response[api.Operation](s.c, resp, err, true)
	if err == nil && (r.StatusCode != 202 || r.Data.ID != id) {
		return r, &AmbiguousError{Cause: errors.New("invalid validation admission response"), OperationID: id}
	}
	return r, err
}

// Wait observes operation progress. Cancelling the context never cancels the server operation.
func (s *OperationsService) Wait(ctx context.Context, id string) (*Response[api.Operation], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	last := &Response[api.Operation]{OperationID: id}
	for {
		r, err := s.Get(ctx, id)
		if err != nil {
			return last, err
		}
		if r.Data.ID != id || r.OperationID != "" && r.OperationID != id {
			return last, errors.New("operation progress identity mismatch")
		}
		r.OperationID = id
		last = r
		switch r.Data.State {
		case "succeeded", "completed", "partial", "failed", "canceled", "cancelled", "interrupted", "expired", "rejected", "invalidated":
			return r, nil
		}
		delay := 5 * time.Second
		if r.RetryAfter > delay {
			delay = r.RetryAfter
		}
		seconds := r.Data.RetryAfterSeconds
		if seconds > 60 {
			seconds = 60
		}
		if seconds > 0 && time.Duration(seconds)*time.Second > delay {
			delay = time.Duration(seconds) * time.Second
		}
		if delay > time.Minute {
			delay = time.Minute
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return r, ctx.Err()
		case <-timer.C:
		}
	}
}

// Disable conditionally disables admission without altering other operator controls.
func (s *MonitorsService) Disable(ctx context.Context, id, version string) (*Response[api.Monitor], error) {
	return s.Patch(ctx, id, version, api.MergePatch(`{"spec":{"enabled":false}}`))
}

// Enable preserves snoozes, dismissals, and maintenance policy.
func (s *MonitorsService) Enable(ctx context.Context, id, version string) (*Response[api.Monitor], error) {
	return s.Patch(ctx, id, version, api.MergePatch(`{"spec":{"enabled":true}}`))
}
func validatePatch(raw api.MergePatch) error {
	if len(raw) > api.MaxResourceBytes {
		return errors.New("patch exceeds 1 MiB")
	}
	var obj map[string]json.RawMessage
	if err := api.StrictDecode(raw, &obj); err != nil {
		return err
	}
	if obj == nil {
		return errors.New("merge patch must be an object")
	}
	for k := range obj {
		if k != "spec" && k != "metadata" {
			return errors.New("merge patch may edit only spec and metadata")
		}
	}
	return nil
}

// Access returns the authenticated principal and explicit effective permissions.
// Resource discovery reports supported operations and does not grant permission.
func (c *Client) Access(ctx context.Context) (*Response[api.AccessInfo], error) {
	resp, err := c.generated.GetAccess(ctx)
	return response[api.AccessInfo](c, resp, err, false)
}
