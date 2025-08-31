package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"
	
	"github.com/mlange-42/ark/ecs"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
)

// StreamingEntityCreator handles batch entity creation for Ark ECS
// NOTE: All ECS operations are single-threaded due to Ark's thread safety constraints
type StreamingEntityCreator struct {
	world       *ecs.World
	batchSize   int
	
	// Component mappers (cached for performance)
	nameMapper        *ecs.Map1[components.Name]
	pulseConfigMapper *ecs.Map1[components.PulseConfig]
	pulseStatusMapper *ecs.Map1[components.PulseStatus]
	pulseFirstCheckMapper *ecs.Map1[components.PulseFirstCheck]
	pulseJobMapper    *ecs.Map1[components.PulseJob]
	interventionConfigMapper *ecs.Map1[components.InterventionConfig]
	interventionStatusMapper *ecs.Map1[components.InterventionStatus]
	interventionJobMapper    *ecs.Map1[components.InterventionJob]
	redCodeConfigMapper      *ecs.Map1[components.RedCodeConfig]
	redCodeStatusMapper      *ecs.Map1[components.RedCodeStatus]
	redCodeJobMapper         *ecs.Map1[components.RedCodeJob]
	greenCodeConfigMapper    *ecs.Map1[components.GreenCodeConfig]
	greenCodeStatusMapper    *ecs.Map1[components.GreenCodeStatus]
	greenCodeJobMapper       *ecs.Map1[components.GreenCodeJob]
	cyanCodeConfigMapper     *ecs.Map1[components.CyanCodeConfig]
	cyanCodeStatusMapper     *ecs.Map1[components.CyanCodeStatus]
	cyanCodeJobMapper        *ecs.Map1[components.CyanCodeJob]
	yellowCodeConfigMapper   *ecs.Map1[components.YellowCodeConfig]
	yellowCodeStatusMapper   *ecs.Map1[components.YellowCodeStatus]
	yellowCodeJobMapper      *ecs.Map1[components.YellowCodeJob]
	grayCodeConfigMapper     *ecs.Map1[components.GrayCodeConfig]
	grayCodeStatusMapper     *ecs.Map1[components.GrayCodeStatus]
	grayCodeJobMapper        *ecs.Map1[components.GrayCodeJob]
	
	// Color marker component mappers
	redCodeMapper            *ecs.Map1[components.RedCode]
	greenCodeMapper          *ecs.Map1[components.GreenCode]
	cyanCodeMapper           *ecs.Map1[components.CyanCode]
	yellowCodeMapper         *ecs.Map1[components.YellowCode]
	grayCodeMapper           *ecs.Map1[components.GrayCode]
	
	// Monitor status mapper
	monitorStatusMapper      *ecs.Map1[components.MonitorStatus]
	
	// Statistics
	entitiesCreated int64
	batchesProcessed int64
	startTime       time.Time
	
	mu sync.RWMutex // Protects statistics only
}

// EntityCreationConfig holds entity creation configuration
type EntityCreationConfig struct {
	BatchSize     int
	PreAllocate   int  // Pre-allocate entity capacity
	ProgressChan  chan<- EntityProgress
}

// EntityProgress represents entity creation progress
type EntityProgress struct {
	EntitiesCreated  int64
	BatchesProcessed int64
	Rate            float64
	MemoryUsage     int64
}

// NewStreamingEntityCreator creates a new entity creator
func NewStreamingEntityCreator(world *ecs.World, config EntityCreationConfig) *StreamingEntityCreator {
	creator := &StreamingEntityCreator{
		world:     world,
		batchSize: config.BatchSize,
		startTime: time.Now(),
	}
	
	// Initialize component mappers for performance
	creator.initializeMappers()
	
	// Pre-allocate entities if requested
	if config.PreAllocate > 0 {
		creator.preAllocateEntities(config.PreAllocate)
	}
	
	return creator
}

// initializeMappers initializes component mappers for efficient access
func (c *StreamingEntityCreator) initializeMappers() {
	c.nameMapper = ecs.NewMap1[components.Name](c.world)
	c.pulseConfigMapper = ecs.NewMap1[components.PulseConfig](c.world)
	c.pulseStatusMapper = ecs.NewMap1[components.PulseStatus](c.world)
	c.pulseFirstCheckMapper = ecs.NewMap1[components.PulseFirstCheck](c.world)
	c.pulseJobMapper = ecs.NewMap1[components.PulseJob](c.world)
	c.interventionConfigMapper = ecs.NewMap1[components.InterventionConfig](c.world)
	c.interventionStatusMapper = ecs.NewMap1[components.InterventionStatus](c.world)
	c.interventionJobMapper = ecs.NewMap1[components.InterventionJob](c.world)
	c.redCodeConfigMapper = ecs.NewMap1[components.RedCodeConfig](c.world)
	c.redCodeStatusMapper = ecs.NewMap1[components.RedCodeStatus](c.world)
	c.redCodeJobMapper = ecs.NewMap1[components.RedCodeJob](c.world)
	c.greenCodeConfigMapper = ecs.NewMap1[components.GreenCodeConfig](c.world)
	c.greenCodeStatusMapper = ecs.NewMap1[components.GreenCodeStatus](c.world)
	c.greenCodeJobMapper = ecs.NewMap1[components.GreenCodeJob](c.world)
	c.cyanCodeConfigMapper = ecs.NewMap1[components.CyanCodeConfig](c.world)
	c.cyanCodeStatusMapper = ecs.NewMap1[components.CyanCodeStatus](c.world)
	c.cyanCodeJobMapper = ecs.NewMap1[components.CyanCodeJob](c.world)
	c.yellowCodeConfigMapper = ecs.NewMap1[components.YellowCodeConfig](c.world)
	c.yellowCodeStatusMapper = ecs.NewMap1[components.YellowCodeStatus](c.world)
	c.yellowCodeJobMapper = ecs.NewMap1[components.YellowCodeJob](c.world)
	c.grayCodeConfigMapper = ecs.NewMap1[components.GrayCodeConfig](c.world)
	c.grayCodeStatusMapper = ecs.NewMap1[components.GrayCodeStatus](c.world)
	c.grayCodeJobMapper = ecs.NewMap1[components.GrayCodeJob](c.world)
	
	// Initialize color marker mappers
	c.redCodeMapper = ecs.NewMap1[components.RedCode](c.world)
	c.greenCodeMapper = ecs.NewMap1[components.GreenCode](c.world)
	c.cyanCodeMapper = ecs.NewMap1[components.CyanCode](c.world)
	c.yellowCodeMapper = ecs.NewMap1[components.YellowCode](c.world)
	c.grayCodeMapper = ecs.NewMap1[components.GrayCode](c.world)
	
	// Initialize monitor status mapper
	c.monitorStatusMapper = ecs.NewMap1[components.MonitorStatus](c.world)
}

// preAllocateEntities pre-allocates entity storage to reduce allocations
func (c *StreamingEntityCreator) preAllocateEntities(count int) {
	// Create and immediately remove entities to pre-allocate storage
	entities := make([]ecs.Entity, count)
	for i := 0; i < count; i++ {
		entities[i] = c.world.NewEntity()
	}
	for _, entity := range entities {
		c.world.RemoveEntity(entity)
	}
}

// ProcessBatches processes monitor batches and creates entities
// NOTE: This must be called from a single goroutine due to Ark ECS constraints
func (c *StreamingEntityCreator) ProcessBatches(ctx context.Context, batchChan <-chan MonitorBatch, progressChan chan<- EntityProgress) error {
	progressTicker := time.NewTicker(2 * time.Second)
	defer progressTicker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
			
		case <-progressTicker.C:
			c.reportProgress(progressChan)
			
		case batch, ok := <-batchChan:
			if !ok {
				// Channel closed, final progress report
				c.reportProgress(progressChan)
				return nil
			}
			
			// Process multiple small batches efficiently
			if len(batch.Monitors) > 500 {
				fmt.Printf("DEBUG: Processing large batch %d with %d monitors (splitting)\n", batch.BatchID, len(batch.Monitors))
				if err := c.processLargeBatch(batch); err != nil {
					return fmt.Errorf("failed to process large batch %d: %w", batch.BatchID, err)
				}
			} else {
				fmt.Printf("DEBUG: Processing batch %d with %d monitors\n", batch.BatchID, len(batch.Monitors))
				if err := c.processBatch(batch); err != nil {
					return fmt.Errorf("failed to process batch %d: %w", batch.BatchID, err)
				}
			}
		}
	}
}

// processBatch creates entities for a single batch of monitors
func (c *StreamingEntityCreator) processBatch(batch MonitorBatch) error {
	// Pre-allocate slice for entities to reduce allocations
	entities := make([]ecs.Entity, 0, len(batch.Monitors))
	
	// NOTE: Entity creation must be single-threaded for Ark ECS compatibility
	// But we can optimize the batch processing itself
	start := time.Now()
	
	// Batch entity creation for better performance
	for _, monitor := range batch.Monitors {
		entity := c.world.NewEntity()
		entities = append(entities, entity)
		
		// Add components efficiently
		c.addMonitorComponents(entity, monitor)
	}
	
	// Update statistics
	c.updateStats(len(batch.Monitors))
	
	// Log processing time for large batches
	if len(batch.Monitors) > 100 {
		duration := time.Since(start)
		fmt.Printf("DEBUG: Created %d entities in batch %d (took %v)\n", 
			len(batch.Monitors), batch.BatchID, duration)
	}
	
	return nil
}

// processLargeBatch splits large batches into smaller chunks for efficient processing
func (c *StreamingEntityCreator) processLargeBatch(batch MonitorBatch) error {
	const chunkSize = 200
	monitors := batch.Monitors
	
	// Process in chunks to maintain memory efficiency
	for i := 0; i < len(monitors); i += chunkSize {
		end := i + chunkSize
		if end > len(monitors) {
			end = len(monitors)
		}
		
		// Create sub-batch
		subBatch := MonitorBatch{
			Monitors: monitors[i:end],
			BatchID:  batch.BatchID,
			Offset:   batch.Offset + int64(i),
		}
		
		// Process the chunk
		if err := c.processBatch(subBatch); err != nil {
			return fmt.Errorf("failed to process chunk %d-%d of batch %d: %w", 
				i, end, batch.BatchID, err)
		}
		
		// Small yield to prevent blocking other operations
		if (i/chunkSize)%10 == 0 {
			time.Sleep(time.Microsecond)
		}
	}
	
	return nil
}

// addMonitorComponents adds all necessary components to an entity
func (c *StreamingEntityCreator) addMonitorComponents(entity ecs.Entity, monitor schema.Monitor) {
	// Add Name component
	nameComponent := components.Name(monitor.Name)
	c.nameMapper.Add(entity, &nameComponent)
	
	// Add MonitorStatus component with initial state
	monitorStatus := components.MonitorStatus{
		Status:          "pending",
		LastCheckTime:   time.Time{},
		LastSuccessTime: time.Time{},
		LastError:       nil,
	}
	c.monitorStatusMapper.Add(entity, &monitorStatus)
	
	// Add PulseConfig component
	pulseConfig := components.PulseConfig{
		Type:        monitor.Pulse.Type,
		Timeout:     monitor.Pulse.Timeout,
		Interval:    monitor.Pulse.Interval,
		Retries:     3, // Default retries
		MaxFailures: monitor.Pulse.MaxFailures,
		Config:      monitor.Pulse.Config,
	}
	c.pulseConfigMapper.Add(entity, &pulseConfig)
	
	// Add PulseStatus component with initial state
	pulseStatus := components.PulseStatus{
		LastStatus:          "pending",
		ConsecutiveFailures: 0,
		LastCheckTime:       time.Time{}, // Will be set on first check
		LastSuccessTime:     time.Time{},
		LastError:           nil,
	}
	c.pulseStatusMapper.Add(entity, &pulseStatus)
	
	// Create pulse job component
	pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		// Log error but continue - we'll handle missing jobs in systems
		fmt.Printf("WARNING: Failed to create pulse job for entity %d: %v\n", entity.ID(), err)
	} else {
		pulseJobComp := components.PulseJob{Job: pulseJob}
		c.pulseJobMapper.Add(entity, &pulseJobComp)
	}
	
	// Add intervention components if configured
	if monitor.Intervention.Action != "" {
		interventionConfig := components.InterventionConfig{
			Action:      monitor.Intervention.Action,
			MaxFailures: monitor.Intervention.MaxFailures,
			Target:      monitor.Intervention.Target,
		}
		c.interventionConfigMapper.Add(entity, &interventionConfig)
		
		interventionStatus := components.InterventionStatus{
			LastStatus:           "pending",
			ConsecutiveFailures:  0,
			LastInterventionTime: time.Time{},
			LastSuccessTime:      time.Time{},
			LastError:           nil,
		}
		c.interventionStatusMapper.Add(entity, &interventionStatus)
		
		// Create intervention job component
		interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			// Log error but continue - we'll handle missing jobs in systems
			fmt.Printf("WARNING: Failed to create intervention job for entity %d: %v\n", entity.ID(), err)
		} else {
			interventionJobComp := components.InterventionJob{Job: interventionJob}
			c.interventionJobMapper.Add(entity, &interventionJobComp)
		}
	}
	
	// Add code notification components based on configured codes
	for color, codeConfig := range monitor.Codes {
		c.addCodeComponents(entity, color, codeConfig)
	}
	
	// Add first check marker for pulse system
	firstCheck := components.PulseFirstCheck{}
	c.pulseFirstCheckMapper.Add(entity, &firstCheck)
}

// addCodeComponents adds code-specific components based on color
func (c *StreamingEntityCreator) addCodeComponents(entity ecs.Entity, color string, codeConfig schema.CodeConfig) {
	switch color {
	case "red":
		config := components.RedCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5, // Default max failures
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.redCodeConfigMapper.Add(entity, &config)
		
		status := components.RedCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:          nil,
		}
		c.redCodeStatusMapper.Add(entity, &status)
		
		// Create red code job component
		// Need to get entity name for job creation
		entityName := "unknown" // default fallback
		if nameComp := c.nameMapper.Get(entity); nameComp != nil {
			entityName = string(*nameComp)
		}
		redCodeJob, err := jobs.CreateCodeJob(entityName, codeConfig, entity, "red")
		if err != nil {
			fmt.Printf("WARNING: Failed to create red code job for entity %d: %v\n", entity.ID(), err)
		} else {
			redCodeJobComp := components.RedCodeJob{Job: redCodeJob}
			c.redCodeJobMapper.Add(entity, &redCodeJobComp)
		}
		
		// Add red code marker
		redCode := components.RedCode{}
		c.redCodeMapper.Add(entity, &redCode)
		
	case "green":
		config := components.GreenCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.greenCodeConfigMapper.Add(entity, &config)
		
		status := components.GreenCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:          nil,
		}
		c.greenCodeStatusMapper.Add(entity, &status)
		
		// Create green code job component
		entityName := "unknown" // default fallback
		if nameComp := c.nameMapper.Get(entity); nameComp != nil {
			entityName = string(*nameComp)
		}
		greenCodeJob, err := jobs.CreateCodeJob(entityName, codeConfig, entity, "green")
		if err != nil {
			fmt.Printf("WARNING: Failed to create green code job for entity %d: %v\n", entity.ID(), err)
		} else {
			greenCodeJobComp := components.GreenCodeJob{Job: greenCodeJob}
			c.greenCodeJobMapper.Add(entity, &greenCodeJobComp)
		}
		
		greenCode := components.GreenCode{}
		c.greenCodeMapper.Add(entity, &greenCode)
		
	case "cyan":
		config := components.CyanCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.cyanCodeConfigMapper.Add(entity, &config)
		
		status := components.CyanCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:          nil,
		}
		c.cyanCodeStatusMapper.Add(entity, &status)
		
		// Create cyan code job component
		entityName := "unknown" // default fallback
		if nameComp := c.nameMapper.Get(entity); nameComp != nil {
			entityName = string(*nameComp)
		}
		cyanCodeJob, err := jobs.CreateCodeJob(entityName, codeConfig, entity, "cyan")
		if err != nil {
			fmt.Printf("WARNING: Failed to create cyan code job for entity %d: %v\n", entity.ID(), err)
		} else {
			cyanCodeJobComp := components.CyanCodeJob{Job: cyanCodeJob}
			c.cyanCodeJobMapper.Add(entity, &cyanCodeJobComp)
		}
		
		cyanCode := components.CyanCode{}
		c.cyanCodeMapper.Add(entity, &cyanCode)
		
	case "yellow":
		config := components.YellowCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.yellowCodeConfigMapper.Add(entity, &config)
		
		status := components.YellowCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:          nil,
		}
		c.yellowCodeStatusMapper.Add(entity, &status)
		
		// Create yellow code job component
		entityName := "unknown" // default fallback
		if nameComp := c.nameMapper.Get(entity); nameComp != nil {
			entityName = string(*nameComp)
		}
		yellowCodeJob, err := jobs.CreateCodeJob(entityName, codeConfig, entity, "yellow")
		if err != nil {
			fmt.Printf("WARNING: Failed to create yellow code job for entity %d: %v\n", entity.ID(), err)
		} else {
			yellowCodeJobComp := components.YellowCodeJob{Job: yellowCodeJob}
			c.yellowCodeJobMapper.Add(entity, &yellowCodeJobComp)
		}
		
		yellowCode := components.YellowCode{}
		c.yellowCodeMapper.Add(entity, &yellowCode)
		
	case "gray":
		config := components.GrayCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.grayCodeConfigMapper.Add(entity, &config)
		
		status := components.GrayCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:          nil,
		}
		c.grayCodeStatusMapper.Add(entity, &status)
		
		// Create gray code job component
		entityName := "unknown" // default fallback
		if nameComp := c.nameMapper.Get(entity); nameComp != nil {
			entityName = string(*nameComp)
		}
		grayCodeJob, err := jobs.CreateCodeJob(entityName, codeConfig, entity, "gray")
		if err != nil {
			fmt.Printf("WARNING: Failed to create gray code job for entity %d: %v\n", entity.ID(), err)
		} else {
			grayCodeJobComp := components.GrayCodeJob{Job: grayCodeJob}
			c.grayCodeJobMapper.Add(entity, &grayCodeJobComp)
		}
		
		grayCode := components.GrayCode{}
		c.grayCodeMapper.Add(entity, &grayCode)
	}
}

// updateStats updates creation statistics
func (c *StreamingEntityCreator) updateStats(entitiesInBatch int) {
	c.mu.Lock()
	c.entitiesCreated += int64(entitiesInBatch)
	c.batchesProcessed++
	c.mu.Unlock()
}

// reportProgress sends progress update
func (c *StreamingEntityCreator) reportProgress(progressChan chan<- EntityProgress) {
	if progressChan == nil {
		return
	}
	
	c.mu.RLock()
	entitiesCreated := c.entitiesCreated
	batchesProcessed := c.batchesProcessed
	c.mu.RUnlock()
	
	elapsed := time.Since(c.startTime)
	rate := float64(entitiesCreated) / elapsed.Seconds()
	
	// Get memory usage (rough estimate)
	memoryUsage := c.estimateMemoryUsage()
	
	select {
	case progressChan <- EntityProgress{
		EntitiesCreated:  entitiesCreated,
		BatchesProcessed: batchesProcessed,
		Rate:            rate,
		MemoryUsage:     memoryUsage,
	}:
	default:
		// Don't block if channel is full
	}
}

// estimateMemoryUsage provides a rough estimate of memory usage
func (c *StreamingEntityCreator) estimateMemoryUsage() int64 {
	// Rough estimation: each entity uses approximately 200 bytes
	// This includes components and ECS overhead
	c.mu.RLock()
	entitiesCreated := c.entitiesCreated
	c.mu.RUnlock()
	
	return entitiesCreated * 200
}

// GetStats returns current creation statistics
func (c *StreamingEntityCreator) GetStats() (entitiesCreated int64, batchesProcessed int64, rate float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	elapsed := time.Since(c.startTime)
	return c.entitiesCreated, c.batchesProcessed, float64(c.entitiesCreated) / elapsed.Seconds()
}