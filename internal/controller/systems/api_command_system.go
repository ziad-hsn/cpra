package systems

import (
	"cpra/internal/controller/commands"
	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark/ecs"
	"go.uber.org/zap"
)

// DefaultAPICommandLimit is the maximum commands processed per tick.
// Prevents the API command processing from starving other systems.
const DefaultAPICommandLimit = 100

// APICommandSystem processes commands from the API layer during the ECS tick.
//
// This system is the bridge between HTTP handlers (which run in separate
// goroutines) and the ECS world (which is not thread-safe). Commands are
// queued via a channel and processed here within the ECS tick loop.
//
// The system processes up to a configurable limit of commands per tick
// to prevent starvation of other systems during high API traffic.
type APICommandSystem struct {
	cmdChan <-chan commands.Command
	mapper  *entities.EntityManager
	limit   int
	logger  *zap.SugaredLogger
}

// NewAPICommandSystem creates a new API command processor.
func NewAPICommandSystem(
	cmdChan <-chan commands.Command,
	mapper *entities.EntityManager,
	limit int,
	logger *zap.SugaredLogger,
) *APICommandSystem {
	if limit <= 0 {
		limit = DefaultAPICommandLimit
	}
	return &APICommandSystem{
		cmdChan: cmdChan,
		mapper:  mapper,
		limit:   limit,
		logger:  logger,
	}
}

// Initialize is called once when the system is added to the app.
func (s *APICommandSystem) Initialize(world *ecs.World) {}

// Update processes queued commands from the API layer.
// Called once per tick by the ark-tools app.
func (s *APICommandSystem) Update(world *ecs.World) {
	processed := 0
	for i := 0; i < s.limit; i++ {
		select {
		case cmd, ok := <-s.cmdChan:
			if !ok {
				// Channel closed, no more commands
				return
			}
			if err := cmd.Execute(world, s.mapper); err != nil {
				s.logger.Warnw("API command failed",
					"error", err,
					"command_type", commandTypeName(cmd),
				)
			} else {
				processed++
			}
		default:
			// No more commands in queue
			if processed > 0 {
				s.logger.Debugw("Processed API commands", "count", processed)
			}
			return
		}
	}
	// Hit the limit - log and continue next tick
	if processed > 0 {
		s.logger.Debugw("API command limit reached",
			"processed", processed,
			"limit", s.limit,
		)
	}
}

// Finalize is called when the system is removed or app shuts down.
func (s *APICommandSystem) Finalize(world *ecs.World) {}

// commandTypeName returns a string representation of the command type for logging.
func commandTypeName(cmd commands.Command) string {
	switch cmd.(type) {
	case *commands.CreateMonitorCmd:
		return "CreateMonitor"
	case *commands.DeleteMonitorCmd:
		return "DeleteMonitor"
	case *commands.UpdateMonitorCmd:
		return "UpdateMonitor"
	case *commands.EnableMonitorCmd:
		return "EnableMonitor"
	case *commands.DisableMonitorCmd:
		return "DisableMonitor"
	default:
		return "Unknown"
	}
}
