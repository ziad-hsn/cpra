package controller

import (
	"context"
	"cpra/internal/config"
	"cpra/internal/controller/components"
	"cpra/internal/runtime/queue"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// calculateShardSlots determines shard slots based on TPS and desired sweep duration,
// unless an explicit override is provided.
func calculateShardSlots(tps float64, targetSweep time.Duration, override int) int {
	if override > 0 {
		return override
	}

	// Default sweep of the 10s if unset or non-positive
	if targetSweep <= 0 {
		targetSweep = 10 * time.Second
	}

	slots := int(math.Ceil(tps * targetSweep.Seconds()))
	if slots < 1 {
		slots = 1
	}

	// Clamp to a reasonable upper bound to prevent runaway slot counts.
	const maxSlots = 20000
	if slots > maxSlots {
		slots = maxSlots
	}
	return slots
}

// createWorkerPool creates a dynamic worker pool for the given queue.
func createWorkerPool(name string, q queue.Queue, poolConfig queue.WorkerPoolConfig, envCfg *config.EnvConfig) (*queue.DynamicWorkerPool, error) {
	poolLogger := log.New(log.Writer(), fmt.Sprintf("[%sPool] ", name), log.LstdFlags)
	pool, err := queue.NewDynamicWorkerPoolWithEnvConfig(context.Background(), q, poolConfig, poolLogger, envCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s worker pool: %w", name, err)
	}
	return pool, nil
}

// createQueue creates a named hybrid queue with the specified drop policy and capacity.
func createQueue(name string, dropPolicy queue.DropPolicy, capacity uint64) (queue.Queue, error) {
	cfg := queue.DefaultQueueConfig()
	cfg.Name = name
	cfg.HybridConfig.Name = name
	cfg.HybridConfig.DropPolicy = dropPolicy
	// Wire the capacity from controller config to queue config
	if capacity > 0 {
		cfg.Capacity = int(capacity)
		cfg.HybridConfig.RingCapacity = int(capacity)
	}
	q, err := queue.NewQueue(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s HybridQueue: %w", name, err)
	}
	return q, nil
}

// precomputeSizingFromConfig computes a recommended worker count from initial world contents
// and configured (or env) service time and latency SLO. It currently targets the Pulse pool only.
func (c *Controller) precomputeSizingFromConfig() {
	// Determine τ (service time) and W_slo from centralized config; fallback to sane defaults
	tau := c.config.SizingServiceTime
	if c.config.EnvConfig != nil && c.config.EnvConfig.SizingTauMS > 0 {
		tau = time.Duration(c.config.EnvConfig.SizingTauMS) * time.Millisecond
	}
	if tau <= 0 {
		tau = defaultServiceTime
	}
	wSLO := c.config.SizingSLO
	if c.config.EnvConfig != nil && c.config.EnvConfig.SizingSLOMS > 0 {
		wSLO = time.Duration(c.config.EnvConfig.SizingSLOMS) * time.Millisecond
	}
	if wSLO <= 0 {
		wSLO = defaultSLO
	}

	// Compute λ for Pulse from world: sum over active monitors of 1/Interval
	lambda := computePulseLambda(c.world)
	if lambda <= 0 {
		c.logger.Warnf("[Pre-Sizing] No active pulse workload detected; skipping sizing")
		return
	}

	cMin, w, err := queue.FindCForSLO(lambda, tau.Seconds(), wSLO.Seconds(), 0, 0, 0)
	if err != nil {
		c.logger.Warnf("[Pre-Sizing] Could not compute Pulse workers: %v", err)
		return
	}
	// Determine safe headroom from centralized config; default 0.15
	headroom := c.config.SizingHeadroomPct
	if c.config.EnvConfig != nil && c.config.EnvConfig.SizingHeadroomPct > 0 {
		headroom = c.config.EnvConfig.SizingHeadroomPct
	}
	if headroom <= 0 {
		headroom = defaultHeadroom
	} // default 15%%
	// Compute a safe recommended c with headroom
	cSafe := int(math.Ceil(float64(cMin) * (1.0 + headroom)))
	if cSafe <= cMin {
		cSafe = cMin + 1
	}
	// Predict W for cSafe (informational)
	mu := 1.0 / tau.Seconds()
	_, wSafe, errSafe := queue.MmcWait(lambda, mu, cSafe, 0, 0)
	if errSafe != nil {
		wSafe = w
	} // fallback
	c.logger.Infof("[Pre-Sizing] Pulse: λ=%.2f/s τ=%.3fs W_slo=%.3fs => c_min=%d (W≈%.3fs), recommended c_safe=%d (+%.0f%%) (predicted W≈%.3fs)",
		lambda, tau.Seconds(), wSLO.Seconds(), cMin, w, cSafe, headroom*100.0, wSafe)

	// APPLY the calculated sizing to worker pools (not just log!)
	// Only tune if calculated size exceeds current minimum
	if cSafe > c.config.WorkerConfig.MinWorkers {
		c.pools.Pulse().Tune(cSafe)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Pulse pool", cSafe)

		// Scale Intervention and Code pools proportionally (typically lower volume)
		// Use ratio of pulse pool as baseline - these handle triggered actions
		interventionSize := int(math.Ceil(float64(cSafe) * interventionPoolRatio))
		if interventionSize < c.config.WorkerConfig.MinWorkers {
			interventionSize = c.config.WorkerConfig.MinWorkers
		}
		c.pools.Intervention().Tune(interventionSize)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Intervention pool (25%% of pulse)", interventionSize)

		// Code evaluations are even less frequent
		codeSize := int(math.Ceil(float64(cSafe) * codePoolRatio))
		if codeSize < c.config.WorkerConfig.MinWorkers {
			codeSize = c.config.WorkerConfig.MinWorkers
		}
		c.pools.Code().Tune(codeSize)
		c.logger.Infof("[Pre-Sizing] Applied c_safe=%d to Code pool (12.5%% of pulse)", codeSize)
	}
}

// computePulseLambda estimates arrival rate (jobs/sec) from Pulse intervals of enabled monitors.
func computePulseLambda(world *ecs.World) float64 {
	f := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
		Without(ecs.C[components.Disabled]())
	q := f.Query()
	sum := 0.0
	for q.Next() {
		_, cfg := q.Get()
		if cfg == nil || cfg.Interval <= 0 {
			continue
		}
		sum += 1.0 / cfg.Interval.Seconds()
	}
	return sum
}
