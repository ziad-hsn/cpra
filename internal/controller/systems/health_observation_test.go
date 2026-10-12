package systems

import (
	"context"
	"testing"
)

func TestObservationTimeoutDoesNotEnterUnresolvedWriteRecovery(t *testing.T) {
	system := &DurableSystem{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	system.healthObservationError(context.Canceled, ctx)
	if system.recovering.Load() || system.stopping.Load() || system.lastError != nil {
		t.Fatal("pure health observation cancellation poisoned admission")
	}
	system.recoveryError(context.Canceled, ctx)
	if !system.recovering.Load() || system.stopping.Load() || system.lastError != nil {
		t.Fatal("uncertain write cancellation failed to close unsafe admission")
	}
}
