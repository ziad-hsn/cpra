package persistence

import "time"

// ObservationConfig contains nonsecret, immutable deployment settings and the
// current application snapshot format. It excludes paths and authentication.
type ObservationConfig struct {
	StorageMode                          string
	SnapshotFormat                       int
	HistoryRetentionDays                 int
	SLOWindow, QueueTarget, ResultTarget time.Duration
}

func (s *Store) ObservationConfig() ObservationConfig {
	s.fsm.mu.RLock()
	format := s.fsm.image.Version
	s.fsm.mu.RUnlock()
	return ObservationConfig{StorageMode: s.config.Storage.Mode, SnapshotFormat: format, HistoryRetentionDays: s.config.History.RetentionDays,
		SLOWindow: s.config.SLO.Window, QueueTarget: s.config.SLO.QueueTarget, ResultTarget: s.config.SLO.ResultTarget}
}
