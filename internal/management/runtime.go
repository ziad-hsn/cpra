package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// RuntimePreparation is privately owned execution configuration, not an API
// response or durable record. It contains resolved credentials. The owner must
// check Conditions before installing it and again before starting external work.
// AbsoluteMaintenance and per-monitor recovery overrides require corresponding
// owner-loop support; their presence must never be silently discarded.
type RuntimePreparation struct {
	runtimePreparationExtensions
	// PreparationBytes bounds retained inputs, including detached worker bindings.
	PreparationBytes      int
	Monitor               manifest.Monitor
	CheckTargetSummary    string
	Endpoints             map[string]manifest.Endpoint
	NotificationGroups    manifest.NotificationGroups
	NotificationTargets   map[string][]NotificationTarget
	Conditions            []persistence.CatalogCondition
	CatalogIndex          uint64
	MonitorUID            string
	ResourceVersion       string
	Generation            uint64
	ExecutionRevision     string
	ProjectionVersion     string
	RecoveryCooldown      *time.Duration
	RecoveryMaxAttempts   *int
	RecoveryTimeout       time.Duration
	AbsoluteMaintenance   []AbsoluteMaintenanceWindow
	NotificationGroupRefs []string
}

type AbsoluteMaintenanceWindow struct{ Start, End time.Time }

// Refuse accidental response/log serialization of resolved provider secrets.
func (RuntimePreparation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("runtime execution configuration must not be serialized")
}
func (RuntimePreparation) String() string {
	return "runtime preparation (resolved configuration omitted)"
}
func (p RuntimePreparation) GoString() string { return p.String() }

// runtimeDriverFactories only selects existing inert schema structs. Optional
// provider libraries and executable job implementations remain in jobs' build
// matrix. No constructor, file, socket, resolver, or provider call is used here.
var runtimeDriverFactories = map[string]func() any{
	"recovery/a2a":            func() any { return &manifest.InterventionTargetA2A{} },
	"notification/a2a":        func() any { return &manifest.CodeNotificationA2A{} },
	"check/http":              func() any { return &manifest.PulseHTTPConfig{} },
	"check/tcp":               func() any { return &manifest.PulseTCPConfig{} },
	"check/icmp":              func() any { return &manifest.PulseICMPConfig{} },
	"check/dns":               func() any { return &manifest.PulseDNSConfig{} },
	"check/udp":               func() any { return &manifest.PulseUDPConfig{} },
	"check/grpc":              func() any { return &manifest.PulseGRPCConfig{} },
	"check/docker":            func() any { return &manifest.PulseDockerConfig{} },
	"check/redis":             func() any { return &manifest.PulseRedisConfig{} },
	"check/postgres":          func() any { return &manifest.PulsePostgresConfig{} },
	"check/mysql":             func() any { return &manifest.PulseMySQLConfig{} },
	"check/mongo":             func() any { return &manifest.PulseMongoConfig{} },
	"check/rabbitmq":          func() any { return &manifest.PulseRabbitMQConfig{} },
	"check/kafka":             func() any { return &manifest.PulseKafkaConfig{} },
	"check/tls":               func() any { return &manifest.PulseTLSConfig{} },
	"recovery/docker":         func() any { return &manifest.InterventionTargetDocker{} },
	"recovery/kubernetes":     func() any { return &manifest.InterventionTargetKubernetes{} },
	"recovery/webhook":        func() any { return &manifest.InterventionTargetWebhook{} },
	"recovery/systemd":        func() any { return &manifest.InterventionTargetSystemd{} },
	"recovery/aws":            func() any { return &manifest.InterventionTargetAWS{} },
	"notification/log":        func() any { return &manifest.CodeNotificationLog{} },
	"notification/email":      func() any { return &manifest.CodeNotificationEmail{} },
	"notification/webhook":    func() any { return &manifest.CodeNotificationWebhook{} },
	"notification/slack":      func() any { return &manifest.CodeNotificationSlack{} },
	"notification/pagerduty":  func() any { return &manifest.CodeNotificationPagerDuty{} },
	"notification/telegram":   func() any { return &manifest.CodeNotificationTelegram{} },
	"notification/discord":    func() any { return &manifest.CodeNotificationDiscord{} },
	"notification/opsgenie":   func() any { return &manifest.CodeNotificationOpsgenie{} },
	"notification/teams":      func() any { return &manifest.CodeNotificationTeams{} },
	"notification/mattermost": func() any { return &manifest.CodeNotificationMattermost{} },
	"notification/pushover":   func() any { return &manifest.CodeNotificationPushover{} },
	"notification/twilio":     func() any { return &manifest.CodeNotificationTwilio{} },
	"notification/datadog":    func() any { return &manifest.CodeNotificationDatadog{} },
	"notification/victorops":  func() any { return &manifest.CodeNotificationVictorOps{} },
}

func runtimeInvalid(reason string) error { return fmt.Errorf("%w: %s", ErrValidation, reason) }

// decodeRuntimeDriver strictly maps API keys onto reviewed existing schema
// fields. Normalize only field names, never nested map keys, headers or values.
// API shape validation precedes reflection, so case folding is not permission
// to accept an unknown wire field. Missing/zero values retain constructor defaults.
func decodeRuntimeDriver(category string, driver api.DriverConfig) (any, error) {
	factory := runtimeDriverFactories[category+"/"+driver.Type]
	if factory == nil || api.ValidateDriver(category, driver) != nil ||
		(driver.CredentialRefs != nil && len(*driver.CredentialRefs) != 0) {
		return nil, runtimeInvalid("unsupported or unresolved driver configuration")
	}
	var fields map[string]json.RawMessage
	if api.StrictDecode(driver.Config, &fields) != nil || fields == nil {
		return nil, runtimeInvalid("driver configuration must be an object")
	}
	target := factory()
	value := reflect.ValueOf(target).Elem()
	normalize := func(name string) string { return strings.ToLower(strings.ReplaceAll(name, "_", "")) }
	mapping := make(map[string]int, value.NumField())
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name, _, _ = strings.Cut(field.Tag.Get("yaml"), ",")
		}
		if name == "" || name == "-" || !field.IsExported() {
			return nil, ErrUnavailable
		}
		normalized := normalize(name)
		if _, duplicate := mapping[normalized]; duplicate {
			return nil, ErrUnavailable
		}
		mapping[normalized] = i
	}
	for name, raw := range fields {
		i, ok := mapping[normalize(name)]
		if !ok {
			return nil, runtimeInvalid("driver field has no runtime representation")
		}
		field := value.Field(i)
		if field.Type() == reflect.TypeFor[time.Duration]() {
			if string(raw) == "null" {
				continue
			}
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return nil, runtimeInvalid("driver timeout must be a Go duration string")
			}
			duration, err := time.ParseDuration(text)
			if err != nil || duration <= 0 {
				return nil, runtimeInvalid("driver timeout must be positive")
			}
			field.SetInt(int64(duration))
		} else if json.Unmarshal(raw, field.Addr().Interface()) != nil {
			return nil, runtimeInvalid("driver field cannot be represented by the runtime")
		}
	}
	return target, nil
}

// ValidateResolvedDriver checks inert configuration only. It cannot establish
// connectivity, file readability, credential validity, or delivery. Those remain
// runtime/preflight evidence and never become side effects of API validation.
func ValidateResolvedDriver(category string, driver api.DriverConfig) error {
	if err := api.ValidateDriverSettings(category, driver); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	config, err := decodeRuntimeDriver(category, driver)
	if err != nil {
		return err
	}
	// Platform capabilities belong to the executing server, not the SDK caller.
	if cfg, ok := config.(*manifest.PulseICMPConfig); ok && cfg.Privilege && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("%w: %w", ErrValidation, api.InvalidField("config.ignorePrivilege", "unsupportedDriver"))
	}
	return nil
}

// ValidateMonitorSettings checks runtime control values independently of driver
// secrets. Schema validation of the resource envelope remains the caller's job.
func ValidateMonitorSettings(spec api.MonitorSpec) error {
	if err := api.ValidateMonitorSettings(spec); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	_, _, err := runtimeMaintenance(spec.Maintenance)
	return err
}

func runtimeMaintenance(windows *[]api.MaintenanceWindow) ([]manifest.MaintenanceWindow, []AbsoluteMaintenanceWindow, error) {
	var periodic []manifest.MaintenanceWindow
	var absolute []AbsoluteMaintenanceWindow
	if windows == nil {
		return nil, nil, nil
	}
	for _, window := range *windows {
		if window.Start != nil || window.End != nil {
			if window.Start == nil || window.End == nil || window.Start.IsZero() || window.End.IsZero() || !window.End.After(*window.Start) || window.Cron != nil || window.Duration != nil || window.Timezone != nil {
				return nil, nil, runtimeInvalid("absolute maintenance requires only an ordered start and end")
			}
			absolute = append(absolute, AbsoluteMaintenanceWindow{Start: *window.Start, End: *window.End})
			continue
		}
		if window.Cron == nil || window.Duration == nil {
			return nil, nil, runtimeInvalid("periodic maintenance requires cron and duration")
		}
		entry := manifest.MaintenanceWindow{Cron: *window.Cron, Duration: *window.Duration}
		if window.Timezone != nil {
			entry.Timezone = *window.Timezone
		}
		periodic = append(periodic, entry)
	}
	if _, err := manifest.CompileMaintenance(periodic); err != nil {
		return nil, nil, runtimeInvalid("periodic maintenance is invalid")
	}
	return periodic, absolute, nil
}

// PrepareRuntime converts one committed monitor and its outgoing references
// from the supplied immutable view. It does not change state, create jobs or
// access any provider. The returned configuration has one private owner.
func (c *Catalog) PrepareRuntime(ctx context.Context, view ReadView, monitorID string) (RuntimePreparation, error) {
	var out RuntimePreparation
	if ctx == nil {
		return out, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if view.catalog != c {
		return out, ErrUnavailable
	}
	if err := c.readyContext(ctx); err != nil {
		return out, err
	}
	if !validID(monitorID) {
		return out, ErrValidation
	}
	resources := make(map[persistence.CatalogKey]api.Resource)
	records := make(map[persistence.CatalogKey]persistence.CatalogRecord)
	queue := []persistence.CatalogKey{{Kind: "Monitor", ID: monitorID}}
	bytes := 0
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key := queue[0]
		queue = queue[1:]
		if _, exists := resources[key]; exists {
			continue
		}
		if len(resources) >= maxValidationGraph {
			return out, ErrGraphLimit
		}
		record, ok := view.view.Get(key)
		if !ok {
			if key.Kind == "Monitor" && key.ID == monitorID {
				return out, persistence.ErrCatalogNotFound
			}
			return out, persistence.ErrCatalogDependency
		}
		bytes += len(record.Payload.Ciphertext)
		if bytes > 32<<20 {
			return out, ErrGraphLimit
		}
		resource, err := c.open(ctx, record)
		if err != nil {
			return out, err
		}
		refs, err := directReferences(resource)
		if err != nil {
			return out, err
		}
		queue = append(queue, refs...)
		resources[key], records[key] = resource, record
	}
	key := persistence.CatalogKey{Kind: "Monitor", ID: monitorID}
	resource := resources[key]
	var spec api.MonitorSpec
	if api.StrictDecode(resource.Spec, &spec) != nil {
		return out, ErrUnavailable
	}
	if err := ValidateMonitorSettings(spec); err != nil {
		return out, err
	}
	out.CheckTargetSummary = api.CheckTargetSummary(spec.Check.Driver)
	out.PreparationBytes = bytes
	type runtimeSlot struct {
		source persistence.CatalogKey
		slot   string
	}
	var resolvedSlots map[runtimeSlot]any
	resolve := func(category string, source persistence.CatalogKey, slot string, driver api.DriverConfig) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected := runtimeSlot{source, slot}
		if value, ok := resolvedSlots[selected]; ok {
			return value, nil
		}
		value, handled, err := out.resolveRuntimeExtension(ctx, resources[source], records[source], slot, category, driver.Type)
		if err != nil || handled {
			if err == nil {
				if resolvedSlots == nil {
					resolvedSlots = make(map[runtimeSlot]any)
				}
				resolvedSlots[selected] = value
			}
			return value, err
		}
		if jobs.ValidateDriver(category, driver.Type) != nil {
			return nil, runtimeInvalid("selected driver is not compiled into this server")
		}
		if err := resolveDriver(&driver, category, resources); err != nil {
			return nil, err
		}
		if err := c.options.validateNotificationLog(category, driver); err != nil {
			return nil, err
		}
		if err := ValidateResolvedDriver(category, driver); err != nil {
			return nil, err
		}
		return decodeRuntimeDriver(category, driver)
	}
	config, err := resolve("check", key, "check", spec.Check.Driver)
	if err != nil {
		return out, err
	}
	monitor := manifest.Monitor{ID: monitorID, Name: monitorID, Enabled: true}
	if spec.AutoIgnore != nil {
		v := *spec.AutoIgnore
		monitor.AutoIgnore = &v
	}
	if resource.Metadata.Name != nil && *resource.Metadata.Name != "" {
		monitor.Name = *resource.Metadata.Name
	}
	if spec.Enabled != nil {
		monitor.Enabled = *spec.Enabled
	}
	if spec.Tags != nil {
		monitor.Tags = slices.Clone(*spec.Tags)
	}
	monitor.Pulse = manifest.Pulse{Type: spec.Check.Driver.Type, Config: config.(manifest.PulseConfig)}
	monitor.Pulse.Interval, _ = time.ParseDuration(spec.Check.Interval)
	monitor.Pulse.Timeout, _ = time.ParseDuration(spec.Check.Timeout)
	assign := func(target *int, source *int64) {
		if source != nil {
			*target = int(*source)
		}
	}
	assign(&monitor.Pulse.Retries, spec.Check.Retries)
	assign(&monitor.Pulse.MaxFailures, spec.Check.MaxFailures)
	assign(&monitor.Pulse.HealthyThreshold, spec.Check.HealthyThreshold)
	assign(&monitor.Pulse.UnhealthyThreshold, spec.Check.UnhealthyThreshold)
	if spec.Check.UnhealthyThreshold == nil && monitor.Pulse.MaxFailures > 0 {
		monitor.Pulse.UnhealthyThreshold = monitor.Pulse.MaxFailures
	}
	if spec.Check.Groups != nil {
		monitor.Pulse.Groups = slices.Clone(*spec.Check.Groups)
	}
	// Preserve explicit per-driver zero retries; inherit only an omitted value.
	var rawCheck map[string]json.RawMessage
	_ = json.Unmarshal(spec.Check.Driver.Config, &rawCheck)
	_, presentRetries := rawCheck["retries"]
	if spec.Check.Driver.CredentialRefs != nil {
		_, referenced := (*spec.Check.Driver.CredentialRefs)["retries"]
		presentRetries = presentRetries || referenced
	}
	if !presentRetries && spec.Check.Retries != nil {
		// Worker markers carry no local retry loop. Keep the monitor's retry
		// setting in Pulse without trying to mutate an executable driver config.
		if retries := reflect.ValueOf(config).Elem().FieldByName("Retries"); retries.IsValid() {
			retries.SetInt(*spec.Check.Retries)
		}
	}
	if spec.Recovery != nil {
		target, err := resolve("recovery", key, "recovery", spec.Recovery.Driver)
		if err != nil {
			return out, err
		}
		monitor.Intervention = manifest.Intervention{Action: spec.Recovery.Driver.Type, Target: target.(manifest.InterventionTarget)}
		out.RecoveryTimeout, err = c.interventionBudget(ctx, monitor.Intervention.Target)
		if err != nil {
			return out, err
		}
		assign(&monitor.Intervention.Retries, spec.Recovery.Retries)
		assign(&monitor.Intervention.MaxFailures, spec.Recovery.MaxFailures)
		if spec.Recovery.Cooldown != nil {
			d, _ := time.ParseDuration(*spec.Recovery.Cooldown)
			out.RecoveryCooldown = &d
		}
		if spec.Recovery.MaxAttempts != nil {
			n := int(*spec.Recovery.MaxAttempts)
			out.RecoveryMaxAttempts = &n
		}
	}
	monitor.Maintenance, out.AbsoluteMaintenance, err = runtimeMaintenance(spec.Maintenance)
	if err != nil {
		return out, err
	}
	if spec.NotificationGroupRefs != nil {
		out.NotificationGroupRefs = slices.Clone(*spec.NotificationGroupRefs)
	}
	notifications := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	for key, resource := range resources {
		raw, _ := json.Marshal(resource)
		switch key.Kind {
		case "NotificationEndpoint":
			var value api.NotificationEndpoint
			_ = json.Unmarshal(raw, &value)
			notifications.Endpoints[key.ID] = value
		case "Recipient":
			var value api.Recipient
			_ = json.Unmarshal(raw, &value)
			notifications.Recipients[key.ID] = value
		case "NotificationGroup":
			var value api.NotificationGroup
			_ = json.Unmarshal(raw, &value)
			notifications.Groups[key.ID] = value
		}
	}
	out.Endpoints, out.NotificationGroups, out.NotificationTargets = map[string]manifest.Endpoint{}, manifest.NotificationGroups{}, map[string][]NotificationTarget{}
	if spec.Notifications != nil {
		monitor.Codes = manifest.Codes{}
		for color, rule := range *spec.Notifications {
			if err := ctx.Err(); err != nil {
				return RuntimePreparation{}, err
			}
			resolved, err := ResolveNotificationRule(rule, notifications)
			if err != nil {
				return RuntimePreparation{}, errors.Join(ErrValidation, err)
			}
			code := manifest.CodeConfig{Dispatch: true}
			if rule.Trigger != nil {
				code.Trigger = string(*rule.Trigger)
			}
			if rule.Dispatch != nil {
				code.Dispatch = *rule.Dispatch
			}
			if resolved.InlineDriver != nil {
				config, err := resolve("notification", key, "notifications."+color, *resolved.InlineDriver)
				if err != nil {
					return RuntimePreparation{}, err
				}
				code.Notify, code.Config = resolved.InlineDriver.Type, config.(manifest.CodeNotification)
				out.NotificationTargets[color] = []NotificationTarget{{EndpointID: "$inline/" + monitorID + "/" + color, EndpointUID: resource.Metadata.UID, ResourceVersion: resource.Metadata.ResourceVersion, DriverType: resolved.InlineDriver.Type}}
			} else if len(resolved.Targets) != 0 {
				code.NotifyGroup = "cpra-runtime/" + monitorID + "/" + color
				if rule.NotifyType == nil && rule.GroupRef != nil && rule.EndpointRefs == nil && rule.RecipientRefs == nil {
					code.NotifyGroup = *rule.GroupRef
				}
				group := make([]string, 0, len(resolved.Targets))
				for _, endpoint := range resolved.Targets {
					driver := notifications.Endpoints[endpoint.EndpointID].Spec
					config, err := resolve("notification", persistence.CatalogKey{Kind: "NotificationEndpoint", ID: endpoint.EndpointID}, "endpoint", driver)
					if err != nil {
						return RuntimePreparation{}, err
					}
					out.Endpoints[endpoint.EndpointID] = manifest.Endpoint{Type: driver.Type, Config: config.(manifest.CodeNotification)}
					group = append(group, endpoint.EndpointID)
				}
				out.NotificationGroups[code.NotifyGroup] = group
				out.NotificationTargets[color] = slices.Clone(resolved.Targets)
			} else {
				code.Dispatch = false
			}
			monitor.Codes[color] = code
		}
	}
	out.Monitor, out.CatalogIndex = monitor, view.Index()
	out.MonitorUID, out.ResourceVersion, out.Generation = resource.Metadata.UID, resource.Metadata.ResourceVersion, uint64(resource.Metadata.Generation)
	for key, record := range records {
		out.Conditions = append(out.Conditions, persistence.CatalogCondition{Key: key, UID: record.UID, Revision: record.Revision})
	}
	slices.SortFunc(out.Conditions, func(a, b persistence.CatalogCondition) int {
		if n := strings.Compare(a.Key.Kind, b.Key.Kind); n != 0 {
			return n
		}
		return strings.Compare(a.Key.ID, b.Key.ID)
	})
	// Resource versions also change when credentials are rewrapped or metadata
	// changes without changing executable content. Result correlation must retain
	// this preparation identity separately from the legacy execution fingerprint.
	identity, err := json.Marshal(out.Conditions)
	if err != nil {
		return RuntimePreparation{}, ErrUnavailable
	}
	preparationHash := sha256.Sum256(append([]byte("cpra-runtime-projection-v1\x00"), identity...))
	out.ProjectionVersion = hex.EncodeToString(preparationHash[:])
	out.ExecutionRevision, err = out.configurationRevision(ctx, resources, records)
	if err != nil {
		if ctx.Err() != nil {
			return RuntimePreparation{}, ctx.Err()
		}
		return RuntimePreparation{}, ErrUnavailable
	}
	// Preserve the legacy content fingerprint when no new execution settings
	// are present; bind added settings so changing them also invalidates work.
	if out.RecoveryCooldown != nil || out.RecoveryMaxAttempts != nil || len(out.AbsoluteMaintenance) != 0 {
		data, err := json.Marshal(struct {
			Legacy      string
			Cooldown    *time.Duration
			MaxAttempts *int
			Maintenance []AbsoluteMaintenanceWindow
		}{out.ExecutionRevision, out.RecoveryCooldown, out.RecoveryMaxAttempts, out.AbsoluteMaintenance})
		if err != nil {
			return RuntimePreparation{}, ErrUnavailable
		}
		digest := sha256.Sum256(data)
		out.ExecutionRevision = hex.EncodeToString(digest[:])
	}
	if err := ctx.Err(); err != nil {
		return RuntimePreparation{}, err
	}
	return out, nil
}
