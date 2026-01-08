// Package handlers implements Connect-Go service handlers for the CPRA API.
package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/gen/cpra/v1/cprav1connect"
	"cpra/internal/controller/commands"
	"cpra/internal/platform/loader/schema"
)

// Ensure MonitorHandler implements the service interface.
var _ cprav1connect.MonitorServiceHandler = (*MonitorHandler)(nil)

// MonitorHandler implements the MonitorService Connect-Go handler.
// It uses the async-accept + subscribe pattern for CLI sync UX without backpressure.
type MonitorHandler struct {
	commandCh   chan<- commands.Command
	syncTimeout time.Duration
}

// NewMonitorHandler creates a new handler with the given command channel.
func NewMonitorHandler(cmdCh chan<- commands.Command) *MonitorHandler {
	return &MonitorHandler{
		commandCh:   cmdCh,
		syncTimeout: 5 * time.Second,
	}
}

// CreateMonitor creates a new monitor.
// Returns the created monitor synchronously (waiting for ECS processing).
func (h *MonitorHandler) CreateMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.CreateMonitorRequest],
) (*connect.Response[cprav1.CreateMonitorResponse], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	// Convert proto to schema.Monitor
	spec, err := protoToMonitor(req.Msg.GetMonitor())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := schema.ValidateMonitor(spec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Create command with result channel
	cmd := commands.NewCreateMonitorCmd(ctx, spec)

	// Send command (async accept - no blocking)
	select {
	case h.commandCh <- cmd:
		// Command accepted
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	// Wait for result (sync UX for CLI)
	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		protoMonitor := monitorToProto(result.Monitor)
		return connect.NewResponse(&cprav1.CreateMonitorResponse{
			Monitor: protoMonitor,
		}), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// GetMonitor retrieves a single monitor by ID.
func (h *MonitorHandler) GetMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.GetMonitorRequest],
) (*connect.Response[cprav1.Monitor], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	cmd := commands.NewGetMonitorCmd(ctx, req.Msg.GetId())

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			return nil, connect.NewError(connect.CodeNotFound, result.Err)
		}
		return connect.NewResponse(monitorToProto(result.Monitor)), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// ListMonitors retrieves all monitors with optional pagination.
func (h *MonitorHandler) ListMonitors(
	ctx context.Context,
	req *connect.Request[cprav1.ListMonitorsRequest],
) (*connect.Response[cprav1.ListMonitorsResponse], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100 // Default page size
	}

	offset := 0
	pageToken := strings.TrimSpace(req.Msg.GetPageToken())
	if pageToken != "" {
		parsed, err := strconv.Atoi(pageToken)
		if err != nil || parsed < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page_token"))
		}
		offset = parsed
	}

	var enabledFilter *bool
	if req.Msg.Enabled != nil {
		enabled := req.Msg.GetEnabled()
		enabledFilter = &enabled
	}

	pulseType := ""
	if req.Msg.PulseType != nil {
		pulseType = req.Msg.GetPulseType()
	}

	cmd := commands.NewListMonitorsCmd(ctx, pageSize, offset, enabledFilter, pulseType)

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		monitors := make([]*cprav1.Monitor, len(result.Monitors))
		for i, m := range result.Monitors {
			monitors[i] = monitorToProto(m)
		}
		nextToken := ""
		if offset+len(result.Monitors) < result.TotalCount {
			nextToken = strconv.Itoa(offset + len(result.Monitors))
		}
		return connect.NewResponse(&cprav1.ListMonitorsResponse{
			Monitors:      monitors,
			NextPageToken: nextToken,
			TotalCount:    int32(result.TotalCount),
		}), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// UpdateMonitor modifies an existing monitor.
func (h *MonitorHandler) UpdateMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.UpdateMonitorRequest],
) (*connect.Response[cprav1.Monitor], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	spec, err := protoToMonitor(req.Msg.GetMonitor())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	identifier := spec.ID
	if identifier == "" {
		identifier = spec.Slug
	}
	if identifier == "" {
		identifier = spec.Name
	}
	if identifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("monitor id, slug, or name is required"))
	}

	cmd := commands.NewUpdateMonitorCmd(ctx, identifier, spec)

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			if errors.Is(result.Err, commands.ErrEntityNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, result.Err)
			}
			if errors.Is(result.Err, commands.ErrInvalidSpec) {
				return nil, connect.NewError(connect.CodeInvalidArgument, result.Err)
			}
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		resp := connect.NewResponse(monitorToProto(result.Monitor))
		if result.Deferred {
			resp.Header().Set("X-CPRA-Deferred", "true")
			if result.DeferredReason != "" {
				resp.Header().Set("X-CPRA-Deferred-Reason", result.DeferredReason)
			}
		}
		return resp, nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// DeleteMonitor removes a monitor from the system.
func (h *MonitorHandler) DeleteMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.DeleteMonitorRequest],
) (*connect.Response[cprav1.DeleteMonitorResponse], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	monitorID := req.Msg.GetId()
	if monitorID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("monitor id is required"))
	}

	cmd := commands.NewDeleteMonitorCmd(ctx, monitorID)

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			if errors.Is(result.Err, commands.ErrEntityNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, result.Err)
			}
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		return connect.NewResponse(&cprav1.DeleteMonitorResponse{Success: true}), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// EnableMonitor enables a disabled monitor.
func (h *MonitorHandler) EnableMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.EnableMonitorRequest],
) (*connect.Response[cprav1.Monitor], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	monitorID := req.Msg.GetId()
	if monitorID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("monitor id is required"))
	}

	cmd := commands.NewEnableMonitorCmd(ctx, monitorID)

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			if errors.Is(result.Err, commands.ErrEntityNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, result.Err)
			}
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		return connect.NewResponse(monitorToProto(result.Monitor)), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}

// DisableMonitor disables a monitor without removing it.
func (h *MonitorHandler) DisableMonitor(
	ctx context.Context,
	req *connect.Request[cprav1.DisableMonitorRequest],
) (*connect.Response[cprav1.Monitor], error) {
	if h.commandCh == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("command channel not configured"))
	}

	monitorID := req.Msg.GetId()
	if monitorID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("monitor id is required"))
	}

	cmd := commands.NewDisableMonitorCmd(ctx, monitorID)

	select {
	case h.commandCh <- cmd:
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, commands.ErrQueueFull)
	}

	select {
	case result := <-cmd.ResultChan():
		if result.Err != nil {
			if errors.Is(result.Err, commands.ErrEntityNotFound) {
				return nil, connect.NewError(connect.CodeNotFound, result.Err)
			}
			return nil, connect.NewError(connect.CodeInternal, result.Err)
		}
		return connect.NewResponse(monitorToProto(result.Monitor)), nil
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	case <-time.After(h.syncTimeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("command processing timed out"))
	}
}
