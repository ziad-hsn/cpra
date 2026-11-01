// Package manifestcheck validates monitor input without constructing jobs, accessing
// provider accounts, or opening durable state. Temporary parser spools are private.
package manifestcheck

import (
	"context"
	"fmt"
	"strings"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/loader"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func Manifest(ctx context.Context, path string, allowEmpty, encryptionAvailable, notificationLogEnabled bool, warnings ...func(api.FieldError)) error {
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
	checkLog := func(config manifest.CodeNotification) error {
		if log, ok := config.(*manifest.CodeNotificationLog); ok && log.File != "" && !notificationLogEnabled {
			return fmt.Errorf("file notifications require notifications.log_directory in the runtime configuration")
		}
		return nil
	}
	for batch := range batches {
		for i := range batch.Monitors {
			err := func() error {
				m := &batch.Monitors[i]
				id, err := m.EffectiveID()
				if err != nil {
					return err
				}
				if ids[id] {
					return api.InvalidField("id", "duplicateIdentity")
				}
				ids[id] = true
				if err := jobs.ValidateCapabilities(m); err != nil {
					return err
				}
				if m.Intervention.Action == "a2a" && !encryptionAvailable {
					return fmt.Errorf("A2A file configuration requires storage.encryption")
				}
				for _, code := range m.Codes {
					if err := checkLog(code.Config); err != nil {
						return err
					}
					if code.Notify == "a2a" && !encryptionAvailable {
						return fmt.Errorf("A2A file configuration requires storage.encryption")
					}
					for _, name := range batch.NotificationGroups[code.NotifyGroup] {
						if err := checkLog(batch.Endpoints[name].Config); err != nil {
							return err
						}
						if batch.Endpoints[name].Type == "a2a" && !encryptionAvailable {
							return fmt.Errorf("A2A file configuration requires storage.encryption")
						}
						if err := jobs.ValidateDriver("notification", batch.Endpoints[name].Type); err != nil {
							return err
						}
					}
				}
				hasAction := m.Intervention.Action != ""
				for _, rule := range m.Intervention.Rules {
					hasAction = hasAction || rule.Action != ""
					if rule.Action == "a2a" && !encryptionAvailable {
						return fmt.Errorf("A2A rule configuration requires storage.encryption")
					}
				}
				if red, ok := m.Codes["red"]; ok && red.Dispatch && len(m.Intervention.Rules) > 0 && (red.Trigger == "recovery_failed" || red.Trigger == "recovery_exhausted") {
					warning := api.FieldError{Field: fmt.Sprintf("monitors[%d].codes.red.trigger", len(ids)-1), Reason: "recoveryOnlyCoverage", Message: api.ValidationMessage("recoveryOnlyCoverage")}
					for _, report := range warnings {
						if report != nil {
							report(warning)
						}
					}
				}
				if hasAction {
					blue, exists := m.Codes["blue"]
					if !exists || !blue.Dispatch || blue.Notify == "" && len(batch.NotificationGroups[blue.NotifyGroup]) == 0 {
						warning := api.FieldError{Field: fmt.Sprintf("monitors[%d].codes.blue", len(ids)-1), Reason: "unknownRecoveryDestination", Message: api.ValidationMessage("unknownRecoveryDestination")}
						for _, report := range warnings {
							if report != nil {
								report(warning)
							}
						}
					}
				}
				return nil
			}()
			if err != nil {
				if i < len(batch.Sources) {
					return batch.Sources[i].Error(ctx, path, err)
				}
				return err
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
