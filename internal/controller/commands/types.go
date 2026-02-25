// Package commands defines the command types for API-to-ECS communication.
//
// The ECS World is not thread-safe for external mutation. API handlers run
// in HTTP goroutines and cannot directly modify entities. Commands are
// queued via a channel and processed by APICommandSystem during the ECS tick.
//
// # Pattern
//
// Commands use an async-accept + subscribe pattern:
//   - Server accepts request immediately (no backpressure)
//   - Command is queued to the channel
//   - Each command has a result channel
//   - CLI handlers wait on result channel for sync UX
//   - Fire-and-forget callers ignore the result
package commands

import (
	"context"
	"fmt"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/runtime/jobs"
	"cpra/internal/platform/loader/schema"

	"github.com/mlange-42/ark/ecs"
)

// Command represents an operation to execute within the ECS tick loop.
// All commands must be safe to execute from a single goroutine (the ECS loop).
type Command interface {
	// Execute performs the command within the ECS world.
	// Called by APICommandSystem during the tick loop.
	Execute(world *ecs.World, mapper *entities.EntityManager) error

	// ResultChan returns the channel where the result will be sent.
	// Callers can wait on this for sync UX or ignore for fire-and-forget.
	ResultChan() <-chan Result
}

// Result contains the outcome of a command execution.
type Result struct {
	// Monitor is the affected monitor (for create/get/update operations)
	Monitor *schema.Monitor

	// Monitors is a list of monitors (for list operations)
	Monitors []*schema.Monitor

	// Entity is the ECS entity ID (useful for subsequent operations)
	Entity ecs.Entity

	// TotalCount is the total number of items (for pagination)
	TotalCount int

	// Err is set if the command failed
	Err error

	// Deferred indicates the command was accepted but applied asynchronously.
	Deferred bool

	// DeferredReason provides context for deferred operations.
	DeferredReason string

	// Timestamp when the command completed
	CompletedAt time.Time
}

// baseCommand provides common functionality for all commands.
type baseCommand struct {
	resultCh chan Result
	ctx      context.Context
}

func newBaseCommand(ctx context.Context) baseCommand {
	return baseCommand{
		resultCh: make(chan Result, 1), // Buffered to prevent blocking
		ctx:      ctx,
	}
}

func (b *baseCommand) ResultChan() <-chan Result {
	return b.resultCh
}

func (b *baseCommand) sendResult(r Result) {
	r.CompletedAt = time.Now()
	select {
	case b.resultCh <- r:
	default:
		// Result already sent or channel full - this shouldn't happen
		// with buffered channel, but be defensive
	}
}

// CreateMonitorCmd creates a new monitor entity in the ECS world.
type CreateMonitorCmd struct {
	baseCommand
	Spec *schema.Monitor
}

// NewCreateMonitorCmd creates a command to add a new monitor.
func NewCreateMonitorCmd(ctx context.Context, spec *schema.Monitor) *CreateMonitorCmd {
	return &CreateMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		Spec:        spec,
	}
}

// Execute creates the monitor entity in the ECS world.
func (c *CreateMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	if err := mapper.CreateEntityFromMonitor(c.Spec, world); err != nil {
		c.sendResult(Result{Err: err})
		return err
	}

	// Return the created monitor spec
	c.sendResult(Result{
		Monitor: c.Spec,
		// Note: We don't have easy access to the entity ID here without
		// modifying CreateEntityFromMonitor. Can be added if needed.
	})
	return nil
}

// DeleteMonitorCmd removes a monitor entity from the ECS world.
type DeleteMonitorCmd struct {
	baseCommand
	MonitorID string
	Entity    ecs.Entity // If known, use directly; otherwise lookup by ID
}

// NewDeleteMonitorCmd creates a command to remove a monitor.
func NewDeleteMonitorCmd(ctx context.Context, monitorID string) *DeleteMonitorCmd {
	return &DeleteMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		MonitorID:   monitorID,
	}
}

// Execute removes the monitor entity from the ECS world.
func (c *DeleteMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	if mapper == nil {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}
	if world == nil {
		c.sendResult(Result{Err: ErrInvalidSpec})
		return ErrInvalidSpec
	}

	entity := c.Entity
	if entity.IsZero() {
		var ok bool
		entity, ok = mapper.ResolveEntity(c.MonitorID)
		if !ok {
			c.sendResult(Result{Err: ErrEntityNotFound})
			return ErrEntityNotFound
		}
	}

	state := mapper.GetMonitorState(entity)
	if state != nil {
		mapper.UnregisterEntity(entity)
	}

	if mapper.JobStorage.HasAll(entity) {
		jobStorage := mapper.JobStorage.Get(entity)
		if jobStorage.PulseJob != nil {
			jobs.ReleasePulseJob(jobStorage.PulseJob)
			jobStorage.PulseJob = nil
		}
		if jobStorage.InterventionJob != nil {
			jobs.ReleaseInterventionJob(jobStorage.InterventionJob)
			jobStorage.InterventionJob = nil
		}
	}
	if mapper.PendingUpdate.HasAll(entity) {
		mapper.PendingUpdate.Remove(entity)
	}

	world.RemoveEntity(entity)
	c.sendResult(Result{Entity: entity})
	return nil
}

// UpdateMonitorCmd updates an existing monitor entity.
type UpdateMonitorCmd struct {
	baseCommand
	MonitorID string
	Spec      *schema.Monitor
}

// NewUpdateMonitorCmd creates a command to update a monitor.
func NewUpdateMonitorCmd(ctx context.Context, monitorID string, spec *schema.Monitor) *UpdateMonitorCmd {
	return &UpdateMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		MonitorID:   monitorID,
		Spec:        spec,
	}
}

// Execute updates the monitor entity in the ECS world.
func (c *UpdateMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	if mapper == nil {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}
	if c.Spec == nil {
		c.sendResult(Result{Err: ErrInvalidSpec})
		return ErrInvalidSpec
	}
	if err := schema.ValidateMonitor(c.Spec); err != nil {
		wrapped := fmt.Errorf("%w: %v", ErrInvalidSpec, err)
		c.sendResult(Result{Err: wrapped})
		return wrapped
	}

	entity, ok := mapper.ResolveEntity(c.MonitorID)
	if !ok {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}

	state := mapper.GetMonitorState(entity)
	if state == nil {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}

	pulseCfg := mapper.PulseConfig.Get(entity)
	interval := c.Spec.Pulse.Interval
	if interval <= 0 && pulseCfg != nil {
		interval = pulseCfg.Interval
	}

	pending := state.Flags&(components.StatePulsePending|components.StateInterventionPending|components.StateCodePending) != 0
	shortInterval := interval > 0 && interval <= time.Second
	if shortInterval || pending {
		mapper.SetPendingUpdate(entity, c.Spec)
		monitor, err := mapper.MonitorFromEntity(entity)
		if err != nil {
			c.sendResult(Result{Err: err})
			return err
		}
		reason := "pending"
		if shortInterval {
			reason = "interval"
		}
		c.sendResult(Result{Monitor: monitor, Deferred: true, DeferredReason: reason})
		return nil
	}

	monitor, err := mapper.ApplyMonitorSpec(entity, c.Spec)
	if err != nil {
		c.sendResult(Result{Err: err})
		return err
	}
	c.sendResult(Result{Monitor: monitor})
	return nil
}

// EnableMonitorCmd enables a disabled monitor.
type EnableMonitorCmd struct {
	baseCommand
	MonitorID string
}

// NewEnableMonitorCmd creates a command to enable a monitor.
func NewEnableMonitorCmd(ctx context.Context, monitorID string) *EnableMonitorCmd {
	return &EnableMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		MonitorID:   monitorID,
	}
}

// Execute enables the monitor.
func (c *EnableMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	entity, ok := mapper.ResolveEntity(c.MonitorID)
	if !ok {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}
	mapper.EnableMonitor(entity)
	monitor, err := mapper.MonitorFromEntity(entity)
	if err != nil {
		c.sendResult(Result{Err: err})
		return err
	}
	c.sendResult(Result{Entity: entity, Monitor: monitor})
	return nil
}

// DisableMonitorCmd disables a monitor without removing it.
type DisableMonitorCmd struct {
	baseCommand
	MonitorID string
}

// NewDisableMonitorCmd creates a command to disable a monitor.
func NewDisableMonitorCmd(ctx context.Context, monitorID string) *DisableMonitorCmd {
	return &DisableMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		MonitorID:   monitorID,
	}
}

// Execute disables the monitor.
func (c *DisableMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	entity, ok := mapper.ResolveEntity(c.MonitorID)
	if !ok {
		c.sendResult(Result{Err: ErrEntityNotFound})
		return ErrEntityNotFound
	}
	mapper.DisableMonitor(entity)
	monitor, err := mapper.MonitorFromEntity(entity)
	if err != nil {
		c.sendResult(Result{Err: err})
		return err
	}
	c.sendResult(Result{Entity: entity, Monitor: monitor})
	return nil
}

// GetMonitorCmd retrieves a monitor by ID.
type GetMonitorCmd struct {
	baseCommand
	MonitorID string
}

// NewGetMonitorCmd creates a command to get a monitor by ID.
func NewGetMonitorCmd(ctx context.Context, monitorID string) *GetMonitorCmd {
	return &GetMonitorCmd{
		baseCommand: newBaseCommand(ctx),
		MonitorID:   monitorID,
	}
}

// Execute retrieves the monitor from the ECS world.
func (c *GetMonitorCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	monitor, err := mapper.GetMonitorByID(c.MonitorID)
	if err != nil {
		c.sendResult(Result{Err: err})
		return err
	}
	c.sendResult(Result{Monitor: monitor})
	return nil
}

// ListMonitorsCmd retrieves all monitors with optional pagination.
type ListMonitorsCmd struct {
	baseCommand
	PageSize  int
	Offset    int
	Enabled   *bool
	PulseType string
}

// NewListMonitorsCmd creates a command to list monitors.
func NewListMonitorsCmd(ctx context.Context, pageSize, offset int, enabled *bool, pulseType string) *ListMonitorsCmd {
	return &ListMonitorsCmd{
		baseCommand: newBaseCommand(ctx),
		PageSize:    pageSize,
		Offset:      offset,
		Enabled:     enabled,
		PulseType:   pulseType,
	}
}

// Execute retrieves monitors from the ECS world.
func (c *ListMonitorsCmd) Execute(world *ecs.World, mapper *entities.EntityManager) error {
	monitors, total := mapper.ListMonitors(c.PageSize, c.Offset, c.Enabled, c.PulseType)
	c.sendResult(Result{
		Monitors:   monitors,
		TotalCount: total,
	})
	return nil
}
