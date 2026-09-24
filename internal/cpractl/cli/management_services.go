package cli

import (
	"context"
	"encoding/json"
	"errors"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type managementReply struct {
	data                            json.RawMessage
	operationID, requestID, version string
	incidents                       bool
	actions                         bool
	events                          bool
	operations                      bool
}
type managementService[T, L any] interface {
	List(context.Context, cpra.ListOptions) (*cpra.Response[L], error)
	Get(context.Context, string) (*cpra.Response[T], error)
	Create(context.Context, T) (*cpra.Response[T], error)
	Replace(context.Context, string, string, T) (*cpra.Response[T], error)
	Patch(context.Context, string, string, api.MergePatch) (*cpra.Response[T], error)
	Delete(context.Context, string, string) (*cpra.Response[api.Operation], error)
}
type managementResourceService struct {
	list    func(context.Context, cpra.ListOptions) (managementReply, error)
	get     func(context.Context, string) (managementReply, error)
	create  func(context.Context, api.Resource) (managementReply, error)
	replace func(context.Context, string, string, api.Resource) (managementReply, error)
	patch   func(context.Context, string, string, api.MergePatch) (managementReply, error)
	remove  func(context.Context, string, string) (managementReply, error)
}

func bindManagementService[T, L any](s managementService[T, L]) managementResourceService {
	decode := func(resource api.Resource) (T, error) {
		var value T
		raw, err := json.Marshal(resource)
		if err == nil {
			err = api.StrictDecode(raw, &value)
		}
		clear(raw)
		if err != nil {
			return value, &managementCommandError{"invalid typed resource", cpra.ErrInvalid}
		}
		return value, nil
	}
	return managementResourceService{
		list: func(ctx context.Context, o cpra.ListOptions) (managementReply, error) {
			return managementResponse(s.List(ctx, o))
		},
		get: func(ctx context.Context, id string) (managementReply, error) {
			return managementResponse(s.Get(ctx, id))
		},
		create: func(ctx context.Context, r api.Resource) (managementReply, error) {
			value, err := decode(r)
			if err != nil {
				return managementReply{}, err
			}
			return managementResponse(s.Create(ctx, value))
		},
		replace: func(ctx context.Context, id, version string, r api.Resource) (managementReply, error) {
			value, err := decode(r)
			if err != nil {
				return managementReply{}, err
			}
			return managementResponse(s.Replace(ctx, id, version, value))
		},
		patch: func(ctx context.Context, id, version string, r api.MergePatch) (managementReply, error) {
			return managementResponse(s.Patch(ctx, id, version, r))
		},
		remove: func(ctx context.Context, id, version string) (managementReply, error) {
			return managementResponse(s.Delete(ctx, id, version))
		},
	}
}
func managementServiceFor(c *cpra.Client, kind string) (managementResourceService, error) {
	switch kind {
	case "Monitor":
		return bindManagementService[api.Monitor, api.MonitorList](c.Monitors), nil
	case "NotificationEndpoint":
		return bindManagementService[api.NotificationEndpoint, api.NotificationEndpointList](c.NotificationEndpoints), nil
	case "Recipient":
		return bindManagementService[api.Recipient, api.RecipientList](c.Recipients), nil
	case "NotificationGroup":
		return bindManagementService[api.NotificationGroup, api.NotificationGroupList](c.NotificationGroups), nil
	case "Credential":
		return bindManagementService[api.Credential, api.CredentialList](c.Credentials), nil
	}
	return managementResourceService{}, errors.New("unsupported management resource type")
}
func managementResponse[T any](response *cpra.Response[T], err error) (managementReply, error) {
	if err != nil {
		return managementReply{}, managementFailure(err)
	}
	if response == nil {
		return managementReply{}, errors.New("management response was absent")
	}
	data, err := json.Marshal(response.Data)
	if err != nil {
		return managementReply{}, errors.New("management response could not be encoded")
	}
	credential := false
	incidents := false
	actions := false
	events := false
	operations := false
	switch any(response.Data).(type) {
	case api.Credential, api.CredentialList:
		credential = true
	case api.Incident, api.IncidentList:
		incidents = true
	case api.Action, api.ActionList:
		actions = true
	case api.EventList:
		events = true
	case api.OperationList:
		operations = true
	}
	clean, err := sanitizeManagementCredentials(data, credential)
	clear(data)
	if err != nil {
		return managementReply{}, err
	}
	return managementReply{data: clean, operationID: response.OperationID, requestID: response.RequestID, version: response.ResourceVersion, incidents: incidents, actions: actions, events: events, operations: operations}, nil
}

// The server's write-only contract is enforced again at the display boundary.
// A mistaken value echo must not reach table, JSON, YAML or diagnostic output.
func sanitizeManagementCredentials(raw []byte, credential bool) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, errors.New("invalid management response object")
	}
	var kind string
	_ = json.Unmarshal(fields["kind"], &kind)
	if credential || kind == "Credential" {
		for _, field := range []string{"spec", "status"} {
			if len(fields[field]) == 0 {
				continue
			}
			var value map[string]json.RawMessage
			if json.Unmarshal(fields[field], &value) != nil {
				return nil, errors.New("invalid credential response")
			}
			delete(value, "value")
			delete(value, "Value")
			fields[field], _ = json.Marshal(value)
		}
	}
	if items, ok := fields["items"]; ok {
		var rows []json.RawMessage
		if json.Unmarshal(items, &rows) != nil {
			return nil, errors.New("invalid resource page")
		}
		for i, row := range rows {
			clean, err := sanitizeManagementCredentials(row, credential)
			if err != nil {
				return nil, err
			}
			clear(row)
			rows[i] = clean
		}
		fields["items"], _ = json.Marshal(rows)
	}
	return json.Marshal(fields)
}
