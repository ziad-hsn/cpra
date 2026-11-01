//go:build externaljobs

package persistence

import "testing"

func TestWorkerDispatchUpgradeFromRecoveryRulesStore(t *testing.T) {
	testDispatchStoreUpgrade(t, WorkerRecoveryRulesFormatVersion)
}
