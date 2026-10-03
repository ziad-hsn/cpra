package persistence

import (
	"context"
	"time"
)

// ObservationConfig contains nonsecret, immutable deployment settings and the
// current application snapshot format. It excludes paths and authentication.
type ObservationConfig struct {
	StorageMode                          string
	SnapshotFormat                       int
	HistoryRetentionDays                 int
	SLOWindow, QueueTarget, ResultTarget time.Duration
}

func (s *Store) ObservationConfig() ObservationConfig {
	config, _ := s.ObservationConfigContext(context.Background())
	return config
}

// ObservationConfigContext reads public settings with a cancellable state-lock
// wait. An interrupted observation does not change storage health.
func (s *Store) ObservationConfigContext(ctx context.Context) (ObservationConfig, error) {
	if ctx == nil {
		return ObservationConfig{}, context.Canceled
	}
	unlock, err := s.lockStateContext(ctx, false)
	if err != nil {
		return ObservationConfig{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ObservationConfig{}, err
	}
	return ObservationConfig{StorageMode: s.config.Storage.Mode, SnapshotFormat: s.fsm.image.Version, HistoryRetentionDays: s.config.History.RetentionDays,
		SLOWindow: s.config.SLO.Window, QueueTarget: s.config.SLO.QueueTarget, ResultTarget: s.config.SLO.ResultTarget}, nil
}
