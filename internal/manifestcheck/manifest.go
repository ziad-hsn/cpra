// Package manifestcheck validates monitor input without constructing jobs, accessing
// provider accounts, or opening durable state. Temporary parser spools are private.
package manifestcheck

import (
	"context"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/loader"
	"strings"
)

func Manifest(ctx context.Context, path string, allowEmpty bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	config := loader.ParseConfig{BatchSize: 1000, MaxMemory: 1 << 30}
	var batches <-chan loader.MonitorBatch
	var errs <-chan error
	if strings.HasSuffix(strings.TrimSuffix(strings.ToLower(path), ".gz"), ".json") {
		p, err := loader.NewStreamingJsonParser(path, config)
		if err != nil {
			return err
		}
		batches, errs = p.ParseBatches(ctx, nil)
	} else {
		p, err := loader.NewStreamingYamlParser(path, config)
		if err != nil {
			return err
		}
		batches, errs = p.ParseBatches(ctx, nil)
	}
	// Always join the parser, including an early validation error.
	defer func() {
		cancel()
		for range batches {
		}
		for range errs {
		}
	}()
	ids := map[string]bool{}
	for batch := range batches {
		for i := range batch.Monitors {
			m := &batch.Monitors[i]
			id, err := m.EffectiveID()
			if err != nil {
				return err
			}
			if ids[id] {
				return fmt.Errorf("duplicate effective monitor id %q", id)
			}
			ids[id] = true
			if err := jobs.ValidateCapabilities(m); err != nil {
				return fmt.Errorf("monitor %q: %w", m.Name, err)
			}
			for _, code := range m.Codes {
				for _, name := range batch.NotificationGroups[code.NotifyGroup] {
					if err := jobs.ValidateDriver("notification", batch.Endpoints[name].Type); err != nil {
						return fmt.Errorf("monitor %q: %w", m.Name, err)
					}
				}
			}
		}
	}
	for err := range errs {
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowEmpty && len(ids) == 0 {
		return fmt.Errorf("monitor manifest contains no monitors; use -allow-empty for an intentional empty instance")
	}
	return nil
}
