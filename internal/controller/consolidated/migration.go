package consolidated

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"
	"fmt"
	"log"

	"github.com/mlange-42/ark/ecs"
)

// MigrationManager handles the transition from fragmented to consolidated components
type MigrationManager struct {
	world               *ecs.World
	oldManager          *entities.EntityManager
	consolidatedManager *EntityManager
	migrationLog        []string
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(world *ecs.World) *MigrationManager {
	return &MigrationManager{
		world:               world,
		oldManager:          entities.InitializeMappers(world),
		consolidatedManager: NewConsolidatedEntityManager(world),
		migrationLog:        make([]string, 0),
	}
}

// GetOldManager returns the legacy entity manager for compatibility
func (m *MigrationManager) GetOldManager() *entities.EntityManager {
	return m.oldManager
}

// GetConsolidatedManager returns the new consolidated manager
func (m *MigrationManager) GetConsolidatedManager() *EntityManager {
	return m.consolidatedManager
}

// MigrateEntity migrates a single entity from old to new design
func (m *MigrationManager) MigrateEntity(entity ecs.Entity) error {
	if !m.world.Alive(entity) {
		return nil // Entity already dead
	}

	// Get entity name
	var entityName string
	if name := m.oldManager.Name.Get(entity); name != nil {
		entityName = string(*name)
	}

	// Create consolidated monitor state
	monitorState := &MonitorState{
		Name: entityName,
	}

	// Migrate state flags from individual components
	if m.oldManager.Disabled.HasAll(entity) {
		monitorState.SetDisabled(true)
	}
	if m.oldManager.PulseNeeded.HasAll(entity) {
		monitorState.SetPulseNeeded(true)
	}
	if m.oldManager.PulsePending.HasAll(entity) {
		monitorState.SetPulsePending(true)
	}
	if m.oldManager.PulseFirstCheck.HasAll(entity) {
		monitorState.SetPulseFirstCheck(true)
	}
	if m.oldManager.InterventionNeeded.HasAll(entity) {
		monitorState.SetInterventionNeeded(true)
	}
	if m.oldManager.InterventionPending.HasAll(entity) {
		monitorState.SetInterventionPending(true)
	}
	if m.oldManager.CodeNeeded.HasAll(entity) {
		monitorState.SetCodeNeeded(true)
	}
	if m.oldManager.CodePending.HasAll(entity) {
		monitorState.SetCodePending(true)
	}

	// Copy timing information
	if pulseStatus := m.oldManager.PulseStatus.Get(entity); pulseStatus != nil {
		monitorState.LastCheckTime = pulseStatus.LastCheckTime
		monitorState.LastSuccessTime = pulseStatus.LastSuccessTime
		monitorState.ConsecutiveFailures = pulseStatus.ConsecutiveFailures
		monitorState.LastError = pulseStatus.LastError
	}

	// Add the consolidated state
	m.consolidatedManager.MonitorState.Add(entity, monitorState)

	// Migrate pulse config
	if pulseConfig := m.oldManager.PulseConfig.Get(entity); pulseConfig != nil {
		newPulseConfig := &PulseConfig{
			Type:        pulseConfig.Type,
			Timeout:     pulseConfig.Timeout,
			Interval:    pulseConfig.Interval,
			Retries:     pulseConfig.Retries,
			MaxFailures: pulseConfig.MaxFailures,
			Config:      pulseConfig.Config,
		}
		m.consolidatedManager.PulseConfig.Add(entity, newPulseConfig)
	}

	// Migrate intervention config
	if interventionConfig := m.oldManager.InterventionConfig.Get(entity); interventionConfig != nil {
		newInterventionConfig := &InterventionConfig{
			Action:      interventionConfig.Action,
			MaxFailures: interventionConfig.MaxFailures,
			Target:      interventionConfig.Target,
		}
		m.consolidatedManager.InterventionConfig.Add(entity, newInterventionConfig)
	}

	// Migrate consolidated code configs
	codeConfig := &CodeConfig{
		Configs: make(map[string]*ColorCodeConfig),
	}
	codeStatus := &CodeStatus{
		Status: make(map[string]*ColorCodeStatus),
	}
	jobStorage := &JobStorage{
		CodeJobs: make(map[string]jobs.Job),
	}

	// Migrate each color separately
	colors := []string{"red", "green", "cyan", "yellow", "gray"}
	for _, color := range colors {
		switch color {
		case "red":
			if config := m.oldManager.RedCodeConfig.Get(entity); config != nil {
				codeConfig.Configs[color] = &ColorCodeConfig{
					Dispatch: config.Dispatch,
					Notify:   config.Notify,
					Config:   config.Config,
				}
			}
			if status := m.oldManager.RedCodeStatus.Get(entity); status != nil {
				codeStatus.Status[color] = &ColorCodeStatus{
					LastStatus:          status.LastStatus,
					ConsecutiveFailures: status.ConsecutiveFailures,
					LastAlertTime:       status.LastAlertTime,
					LastSuccessTime:     status.LastSuccessTime,
					LastError:           status.LastError,
				}
			}
		case "green":
			if config := m.oldManager.GreenCodeConfig.Get(entity); config != nil {
				codeConfig.Configs[color] = &ColorCodeConfig{
					Dispatch: config.Dispatch,
					Notify:   config.Notify,
					Config:   config.Config,
				}
			}
			if status := m.oldManager.GreenCodeStatus.Get(entity); status != nil {
				codeStatus.Status[color] = &ColorCodeStatus{
					LastStatus:          status.LastStatus,
					ConsecutiveFailures: status.ConsecutiveFailures,
					LastAlertTime:       status.LastAlertTime,
					LastSuccessTime:     status.LastSuccessTime,
					LastError:           status.LastError,
				}
			}
			// Add other colors as needed...
		}
	}

	if len(codeConfig.Configs) > 0 {
		m.consolidatedManager.CodeConfig.Add(entity, codeConfig)
		m.consolidatedManager.CodeStatus.Add(entity, codeStatus)
	}

	// Migrate job storage
	if pulseJob := m.oldManager.PulseJob.Get(entity); pulseJob != nil {
		jobStorage.PulseJob = pulseJob.Job
	}
	if interventionJob := m.oldManager.InterventionJob.Get(entity); interventionJob != nil {
		jobStorage.InterventionJob = interventionJob.Job
	}
	m.consolidatedManager.JobStorage.Add(entity, jobStorage)

	m.migrationLog = append(m.migrationLog, "Migrated entity: "+entityName)
	return nil
}

// MigrateAllEntities migrates all entities in the world
func (m *MigrationManager) MigrateAllEntities() error {
	// Find all entities with Name component
	nameFilter := ecs.NewFilter1[components.Name](m.world)
	query := nameFilter.Query()

	migrated := 0
	for query.Next() {
		entity := query.Entity()
		if err := m.MigrateEntity(entity); err != nil {
			log.Printf("Failed to migrate entity %v: %v", entity, err)
			continue
		}
		migrated++
	}
	query.Close()

	log.Printf("Migration completed: %d entities migrated", migrated)
	return nil
}

// GetMigrationLog returns the migration log
func (m *MigrationManager) GetMigrationLog() []string {
	return m.migrationLog
}

// ValidateMigration validates that migration was successful
func (m *MigrationManager) ValidateMigration() error {
	// Compare old vs new entity counts
	oldFilter := ecs.NewFilter1[components.Name](m.world)
	oldQuery := oldFilter.Query()
	oldCount := 0
	for oldQuery.Next() {
		oldCount++
	}
	oldQuery.Close()

	newFilter := ecs.NewFilter1[MonitorState](m.world)
	newQuery := newFilter.Query()
	newCount := 0
	for newQuery.Next() {
		newCount++
	}
	newQuery.Close()

	if oldCount != newCount {
		return fmt.Errorf("migration validation failed: old count %d != new count %d", oldCount, newCount)
	}

	log.Printf("Migration validation successful: %d entities in both old and new systems", newCount)
	return nil
}
