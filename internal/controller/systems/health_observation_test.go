package systems

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
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

func TestRepeatedHealthReadTimeoutDoesNotReportOwnerProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &DurableSystem{}
		s.lastAdmissionProgress.Store(time.Now().UnixNano())
		for range 31 {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			s.healthObservationError(context.DeadlineExceeded, ctx)
			cancel()
			time.Sleep(time.Second)
		}
		if s.recovering.Load() || !s.AdmissionReady() || s.Progressing() {
			t.Fatal("health observation changed write status or renewed progress")
		}
		s.lastAdmissionProgress.Store(time.Now().UnixNano())
		if !s.Progressing() {
			t.Fatal("successful owner work did not restore availability")
		}
	})
}
