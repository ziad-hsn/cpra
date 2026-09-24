package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strconv"
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
	// PreparationBytes bounds the encrypted inputs whose private copies are held.
	PreparationBytes      int
	Monitor               manifest.Monitor
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
	config, err := decodeRuntimeDriver(category, driver)
	if err != nil {
		return err
	}
	value := reflect.ValueOf(config).Elem()
	for _, field := range []string{"Retries", "Count", "DB", "WarnDays", "CriticalDays"} {
		if v := value.FieldByName(field); v.IsValid() && (v.Int() < 0 || v.Int() > math.MaxInt32) {
			return runtimeInvalid("driver counter must be between zero and 2147483647")
		}
	}
	if typ := value.FieldByName("Type"); typ.IsValid() && typ.String() != "" && typ.String() != driver.Type {
		return runtimeInvalid("recovery target type conflicts with its selected driver")
	}
	valid := true
	switch cfg := config.(type) {
	case *manifest.PulseHTTPConfig:
		valid = httpTarget(cfg.Url, cfg.Method, cfg.Headers)
		for _, status := range cfg.ExpectedStatus {
			valid = valid && status >= 100 && status <= 599
		}
	case *manifest.PulseTCPConfig:
		valid = hostPort(cfg.Host, cfg.Port)
	case *manifest.PulseICMPConfig:
		valid = networkHost(cfg.Host)
	case *manifest.PulseDNSConfig:
		valid = networkHost(cfg.Host) && (cfg.Server == "" || dnsAddress(cfg.Server))
	case *manifest.PulseUDPConfig:
		valid = hostPort(cfg.Host, cfg.Port)
	case *manifest.PulseGRPCConfig:
		valid = hostPort(cfg.Host, cfg.Port)
	case *manifest.PulseDockerConfig:
		valid = namedTarget(cfg.Container)
	case *manifest.PulseRedisConfig:
		// The production Redis client defaults an empty address to localhost.
		valid = cfg.Addr == "" || numericAddress(cfg.Addr)
	case *manifest.PulsePostgresConfig:
		valid = cfg.Port >= 0 && cfg.Port <= 65535 && (cfg.DSN == "" || connectionURL(cfg.DSN, "postgres", "postgresql") || (strings.Contains(cfg.DSN, "=") && !strings.ContainsRune(cfg.DSN, '\x00'))) &&
			(cfg.SSLMode == "" || slices.Contains([]string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}, cfg.SSLMode))
	case *manifest.PulseMySQLConfig:
		valid = cfg.Port >= 0 && cfg.Port <= 65535 && (cfg.DSN == "" || (strings.Contains(cfg.DSN, "/") && !strings.ContainsRune(cfg.DSN, '\x00')))
	case *manifest.PulseMongoConfig:
		valid = mongoURI(cfg.URI)
	case *manifest.PulseRabbitMQConfig:
		u, err := url.Parse(cfg.URL)
		valid = err == nil && (u.Scheme == "amqp" || u.Scheme == "amqps") && !strings.ContainsAny(cfg.URL, " \t\r\n\x00")
		if valid && u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			valid = err == nil && port >= 1 && port <= 65535
		}
	case *manifest.PulseKafkaConfig:
		// CPRa always calls SeedBrokers, so an empty list overrides the client's
		// default and is rejected. A supplied seed may omit the port (9092).
		valid = len(cfg.Brokers) != 0
		for _, broker := range cfg.Brokers {
			valid = valid && dnsAddress(broker)
		}
	case *manifest.PulseTLSConfig:
		valid = hostPort(cfg.Host, cfg.Port) && (cfg.WarnDays == 0 || cfg.CriticalDays == 0 || cfg.WarnDays >= cfg.CriticalDays)
	case *manifest.InterventionTargetDocker:
		valid = namedTarget(cfg.Container)
	case *manifest.InterventionTargetKubernetes:
		valid = cfg.Kind == "deployment" && namedTarget(cfg.Name) && namedTarget(cfg.Namespace) &&
			(cfg.Replicas == nil || (*cfg.Replicas >= 0 && *cfg.Replicas <= math.MaxInt32))
	case *manifest.InterventionTargetWebhook:
		valid = httpTarget(cfg.URL, cfg.Method, cfg.Headers)
	case *manifest.InterventionTargetSystemd:
		valid = namedTarget(cfg.Unit) && !strings.ContainsAny(cfg.Unit, "/\\") &&
			(cfg.Mode == "" || slices.Contains([]string{"replace", "fail", "isolate", "flush", "ignore-dependencies", "ignore-requirements", "replace-irreversibly"}, cfg.Mode))
	case *manifest.InterventionTargetAWS:
		valid = cfg.Operation == "reboot-instance" && namedTarget(cfg.InstanceID)
	case *manifest.CodeNotificationLog:
		valid = namedTarget(cfg.File)
	case *manifest.CodeNotificationEmail:
		_, fromErr := mail.ParseAddress(cfg.From)
		to, toErr := mail.ParseAddressList(cfg.To)
		valid = fromErr == nil && toErr == nil && len(to) != 0 && numericAddress(cfg.Server) && !strings.ContainsAny(cfg.Subject, "\r\n")
	case *manifest.CodeNotificationWebhook:
		valid = httpTarget(cfg.URL, cfg.Method, cfg.Headers)
	case *manifest.CodeNotificationSlack:
		valid = httpTarget(cfg.WebHook, "", nil)
	case *manifest.CodeNotificationPagerDuty:
		valid = cfg.RoutingKey != "" && optionalHTTP(cfg.URL)
	case *manifest.CodeNotificationTelegram:
		valid = cfg.BotToken != "" && cfg.ChatID != "" && optionalHTTP(cfg.URL) && !(cfg.TestMode && cfg.URL != "")
	case *manifest.CodeNotificationDiscord:
		valid = httpTarget(cfg.WebhookURL, "", nil)
	case *manifest.CodeNotificationOpsgenie:
		valid = cfg.APIKey != "" && optionalHTTP(cfg.URL)
	case *manifest.CodeNotificationTeams:
		valid = httpTarget(cfg.WebhookURL, "", nil)
	case *manifest.CodeNotificationMattermost:
		valid = httpTarget(cfg.WebhookURL, "", nil)
	case *manifest.CodeNotificationPushover:
		valid = cfg.AppToken != "" && cfg.UserKey != "" && optionalHTTP(cfg.URL) && cfg.Priority >= -2 && cfg.Priority <= 2 && cfg.Retry >= 0 && cfg.Expire >= 0
		if cfg.Priority == 2 {
			valid = valid && (cfg.Retry == 0 || cfg.Retry >= 30) && (cfg.Expire == 0 || cfg.Expire <= 10800)
		}
	case *manifest.CodeNotificationTwilio:
		valid = cfg.AccountSID != "" && cfg.AuthToken != "" && cfg.From != "" && cfg.To != "" && optionalHTTP(cfg.URL)
	case *manifest.CodeNotificationDatadog:
		valid = cfg.APIKey != "" && optionalHTTP(cfg.URL) && (cfg.Site == "" || networkHost(cfg.Site))
	case *manifest.CodeNotificationVictorOps:
		valid = optionalHTTP(cfg.URL) && (cfg.URL != "" || (cfg.RestEndpointKey != "" && cfg.RoutingKey != "")) &&
			(cfg.MessageType == "" || slices.Contains([]string{"CRITICAL", "WARNING", "ACKNOWLEDGEMENT", "INFO", "RECOVERY"}, cfg.MessageType))
	default:
		return ErrUnavailable
	}
	if !valid {
		return runtimeInvalid("driver target, required field, or range is invalid")
	}
	return nil
}

func namedTarget(text string) bool { return text != "" && !strings.ContainsAny(text, "\x00\r\n") }
func networkHost(host string) bool {
	return namedTarget(host) && !strings.ContainsAny(host, " /\\\t?#@")
}
func hostPort(host string, port int) bool { return networkHost(host) && port >= 1 && port <= 65535 }
func numericAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	return err == nil && e == nil && hostPort(host, n)
}
func dnsAddress(address string) bool {
	if _, err := netip.ParseAddr(address); err == nil {
		return true
	}
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		ip, err := netip.ParseAddr(address[1 : len(address)-1])
		return err == nil && ip.Is6()
	}
	return numericAddress(address) || (networkHost(address) && !strings.ContainsAny(address, ":[]"))
}
func connectionURL(target string, schemes ...string) bool {
	u, err := url.Parse(target)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.Hostname() == "" || u.Fragment != "" {
		return false
	}
	return true
}

// Mongo's authority permits multiple seed hosts, unlike net/url's host:port
// grammar. Check each seed without SRV lookup or importing Mongo execution code.
func mongoURI(target string) bool {
	rest, ok := strings.CutPrefix(target, "mongodb://")
	if !ok || strings.ContainsAny(rest, "\x00\r\n#") {
		return false
	}
	authority, _, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	if authority == "" {
		return false
	}
	for _, host := range strings.Split(authority, ",") {
		if !dnsAddress(host) {
			return false
		}
	}
	return true
}
func optionalHTTP(target string) bool { return target == "" || httpTarget(target, "", nil) }
func httpTarget(target, method string, headers map[string]string) bool {
	if !connectionURL(target, "http", "https") {
		return false
	}
	u, _ := url.Parse(target)
	if u.Port() != "" {
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if _, err := http.NewRequest(method, target, nil); err != nil {
		return false
	}
	for key, value := range headers {
		if !httpToken(key) {
			return false
		}
		for i := range len(value) {
			if (value[i] < 32 && value[i] != '\t') || value[i] == 127 {
				return false
			}
		}
	}
	return true
}
func httpToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char <= 32 || char >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", char) {
			return false
		}
	}
	return true
}

// ValidateMonitorSettings checks runtime control values independently of driver
// secrets. Schema validation of the resource envelope remains the caller's job.
func ValidateMonitorSettings(spec api.MonitorSpec) error {
	for _, text := range []string{spec.Check.Interval, spec.Check.Timeout} {
		duration, err := time.ParseDuration(text)
		if err != nil || duration <= 0 {
			return runtimeInvalid("check interval and timeout must be positive")
		}
	}
	for _, value := range []*int64{spec.Check.Retries, spec.Check.MaxFailures, spec.Check.UnhealthyThreshold, spec.Check.HealthyThreshold} {
		if value != nil && (*value < 0 || *value > math.MaxInt32) {
			return runtimeInvalid("check counters are out of range")
		}
	}
	if spec.Recovery != nil {
		for _, value := range []*int64{spec.Recovery.Retries, spec.Recovery.MaxFailures, spec.Recovery.MaxAttempts} {
			if value != nil && (*value < 0 || *value > math.MaxInt32) {
				return runtimeInvalid("recovery counters are out of range")
			}
		}
		if spec.Recovery.Cooldown != nil {
			d, err := time.ParseDuration(*spec.Recovery.Cooldown)
			if err != nil || d < 0 {
				return runtimeInvalid("recovery cooldown is invalid")
			}
		}
	}
	if spec.Notifications != nil {
		for color := range *spec.Notifications {
			if !slices.Contains([]string{"red", "yellow", "green", "cyan", "gray"}, color) {
				return runtimeInvalid("unsupported notification Code color")
			}
		}
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
	resolve := func(category string, driver api.DriverConfig) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if jobs.ValidateDriver(category, driver.Type) != nil {
			return nil, runtimeInvalid("selected driver is not compiled into this server")
		}
		if err := resolveDriver(&driver, category, resources); err != nil {
			return nil, err
		}
		if err := ValidateResolvedDriver(category, driver); err != nil {
			return nil, err
		}
		return decodeRuntimeDriver(category, driver)
	}
	config, err := resolve("check", spec.Check.Driver)
	if err != nil {
		return out, err
	}
	monitor := manifest.Monitor{ID: monitorID, Name: monitorID, Enabled: true}
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
		reflect.ValueOf(config).Elem().FieldByName("Retries").SetInt(*spec.Check.Retries)
	}
	if spec.Recovery != nil {
		target, err := resolve("recovery", spec.Recovery.Driver)
		if err != nil {
			return out, err
		}
		monitor.Intervention = manifest.Intervention{Action: spec.Recovery.Driver.Type, Target: target.(manifest.InterventionTarget)}
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
			if rule.Dispatch != nil {
				code.Dispatch = *rule.Dispatch
			}
			if resolved.InlineDriver != nil {
				config, err := resolve("notification", *resolved.InlineDriver)
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
					config, err := resolve("notification", driver)
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
	out.PreparationBytes = bytes
	// Resource versions also change when credentials are rewrapped or metadata
	// changes without changing executable content. Result correlation must retain
	// this preparation identity separately from the legacy execution fingerprint.
	identity, err := json.Marshal(out.Conditions)
	if err != nil {
		return RuntimePreparation{}, ErrUnavailable
	}
	preparationHash := sha256.Sum256(append([]byte("cpra-runtime-projection-v1\x00"), identity...))
	out.ProjectionVersion = hex.EncodeToString(preparationHash[:])
	out.ExecutionRevision, err = manifest.ConfigurationRevision(monitor, out.Endpoints, out.NotificationGroups)
	if err != nil {
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
