package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRuntimeAll33DriverFieldMappings(t *testing.T) {
	tests := []struct {
		category, kind, input string
		want                  any
	}{
		{"check", "http", `{"url":"https://example.test/health","method":"POST","headers":{"X-Custom_Header":"untouched"},"body":"body","expectedStatus":[200,204],"insecureSkipVerify":true,"retries":2}`, &manifest.PulseHTTPConfig{Url: "https://example.test/health", Method: "POST", Headers: map[string]string{"X-Custom_Header": "untouched"}, Body: "body", ExpectedStatus: []int{200, 204}, InsecureSkipVerify: true, Retries: 2}},
		{"check", "tcp", `{"host":"host","port":80,"retries":2}`, &manifest.PulseTCPConfig{Host: "host", Port: 80, Retries: 2}},
		{"check", "icmp", `{"host":"host","ignorePrivilege":true,"count":3,"retries":2}`, &manifest.PulseICMPConfig{Host: "host", Privilege: true, Count: 3, Retries: 2}},
		{"check", "dns", `{"host":"host","server":"[::1]:5353","retries":2}`, &manifest.PulseDNSConfig{Host: "host", Server: "[::1]:5353", Retries: 2}},
		{"check", "udp", `{"host":"host","port":53,"payload":"request","retries":2}`, &manifest.PulseUDPConfig{Host: "host", Port: 53, Payload: "request", Retries: 2}},
		{"check", "grpc", `{"host":"host","port":50051,"service":"ready","retries":2}`, &manifest.PulseGRPCConfig{Host: "host", Port: 50051, Service: "ready", Retries: 2}},
		{"check", "docker", `{"container":"container","retries":2}`, &manifest.PulseDockerConfig{Container: "container", Retries: 2}},
		{"check", "redis", `{"addr":"host:6379","password":"secret","username":"user","db":4,"retries":2}`, &manifest.PulseRedisConfig{Addr: "host:6379", Password: "secret", Username: "user", DB: 4, Retries: 2}},
		{"check", "postgres", `{"dsn":"host=host","host":"host","port":5432,"user":"user","password":"secret","database":"db","sslmode":"require","retries":2}`, &manifest.PulsePostgresConfig{DSN: "host=host", Host: "host", Port: 5432, User: "user", Password: "secret", Database: "db", SSLMode: "require", Retries: 2}},
		{"check", "mysql", `{"dsn":"user:secret@tcp(host:3306)/db","host":"host","port":3306,"user":"user","password":"secret","database":"db","retries":2}`, &manifest.PulseMySQLConfig{DSN: "user:secret@tcp(host:3306)/db", Host: "host", Port: 3306, User: "user", Password: "secret", Database: "db", Retries: 2}},
		{"check", "mongo", `{"uri":"mongodb://one:27017,two:27017/db","retries":2}`, &manifest.PulseMongoConfig{URI: "mongodb://one:27017,two:27017/db", Retries: 2}},
		{"check", "rabbitmq", `{"url":"amqps://user:secret@host/vhost","retries":2}`, &manifest.PulseRabbitMQConfig{URL: "amqps://user:secret@host/vhost", Retries: 2}},
		{"check", "kafka", `{"brokers":["one:9092","two:9092"],"retries":2}`, &manifest.PulseKafkaConfig{Brokers: []string{"one:9092", "two:9092"}, Retries: 2}},
		{"check", "tls", `{"host":"host","port":443,"serverName":"sni","warnDays":30,"criticalDays":7,"insecureSkipVerify":true,"retries":2}`, &manifest.PulseTLSConfig{Host: "host", Port: 443, ServerName: "sni", WarnDays: 30, CriticalDays: 7, InsecureSkipVerify: true, Retries: 2}},
		{"recovery", "docker", `{"type":"docker","container":"container","timeout":"9s"}`, &manifest.InterventionTargetDocker{Type: "docker", Container: "container", Timeout: 9 * time.Second}},
		{"recovery", "kubernetes", `{"type":"kubernetes","kubeconfigPath":"/not/read","namespace":"test","kind":"deployment","name":"worker","replicas":0,"timeout":"12s"}`, &manifest.InterventionTargetKubernetes{Type: "kubernetes", KubeconfigPath: "/not/read", Namespace: "test", Kind: "deployment", Name: "worker", Replicas: api.Pointer(0), Timeout: 12 * time.Second}},
		{"recovery", "webhook", `{"type":"webhook","url":"https://example.test/recover","method":"PATCH","headers":{"X-Custom_Header":"untouched"},"body":"body","timeout":"7s"}`, &manifest.InterventionTargetWebhook{Type: "webhook", URL: "https://example.test/recover", Method: "PATCH", Headers: map[string]string{"X-Custom_Header": "untouched"}, Body: "body", Timeout: 7 * time.Second}},
		{"recovery", "systemd", `{"type":"systemd","unit":"cpra-test.service","mode":"fail","timeout":"20s"}`, &manifest.InterventionTargetSystemd{Type: "systemd", Unit: "cpra-test.service", Mode: "fail", Timeout: 20 * time.Second}},
		{"recovery", "aws", `{"type":"aws","region":"eu-west-1","operation":"reboot-instance","instanceId":"i-test","timeout":"30s"}`, &manifest.InterventionTargetAWS{Type: "aws", Region: "eu-west-1", Operation: "reboot-instance", InstanceID: "i-test", Timeout: 30 * time.Second}},
		{"notification", "log", `{"file":"/not/written"}`, &manifest.CodeNotificationLog{File: "/not/written"}},
		{"notification", "email", `{"allowInsecure":true,"to":"ops@example.test","from":"cpra@example.test","server":"smtp.example.test:25","subject":"Alert"}`, &manifest.CodeNotificationEmail{AllowInsecure: true, To: "ops@example.test", From: "cpra@example.test", Server: "smtp.example.test:25", Subject: "Alert"}},
		{"notification", "webhook", `{"url":"https://example.test/notify","method":"PUT","headers":{"X-Custom_Header":"untouched"}}`, &manifest.CodeNotificationWebhook{URL: "https://example.test/notify", Method: "PUT", Headers: map[string]string{"X-Custom_Header": "untouched"}}},
		{"notification", "slack", `{"hook":"https://example.test/slack"}`, &manifest.CodeNotificationSlack{WebHook: "https://example.test/slack"}},
		{"notification", "pagerduty", `{"routingKey":"secret","url":"https://example.test/pagerduty"}`, &manifest.CodeNotificationPagerDuty{RoutingKey: "secret", URL: "https://example.test/pagerduty"}},
		{"notification", "telegram", `{"botToken":"secret","chatId":"room","testMode":true}`, &manifest.CodeNotificationTelegram{BotToken: "secret", ChatID: "room", TestMode: true}},
		{"notification", "discord", `{"webhookUrl":"https://example.test/discord"}`, &manifest.CodeNotificationDiscord{WebhookURL: "https://example.test/discord"}},
		{"notification", "opsgenie", `{"apiKey":"secret","url":"https://example.test/opsgenie"}`, &manifest.CodeNotificationOpsgenie{APIKey: "secret", URL: "https://example.test/opsgenie"}},
		{"notification", "teams", `{"webhookUrl":"https://example.test/teams"}`, &manifest.CodeNotificationTeams{WebhookURL: "https://example.test/teams"}},
		{"notification", "mattermost", `{"webhookUrl":"https://example.test/mattermost","channel":"alerts","username":"cpra"}`, &manifest.CodeNotificationMattermost{WebhookURL: "https://example.test/mattermost", Channel: "alerts", Username: "cpra"}},
		{"notification", "pushover", `{"appToken":"secret","userKey":"user","url":"https://example.test/pushover","title":"Alert","priority":2,"retry":30,"expire":60,"sound":"sound"}`, &manifest.CodeNotificationPushover{AppToken: "secret", UserKey: "user", URL: "https://example.test/pushover", Title: "Alert", Priority: 2, Retry: 30, Expire: 60, Sound: "sound"}},
		{"notification", "twilio", `{"accountSid":"account","authToken":"secret","from":"+15550001","to":"+15550002","url":"https://example.test/twilio"}`, &manifest.CodeNotificationTwilio{AccountSID: "account", AuthToken: "secret", From: "+15550001", To: "+15550002", URL: "https://example.test/twilio"}},
		{"notification", "datadog", `{"apiKey":"secret","appKey":"app","site":"datadoghq.eu","tags":["service:api"],"url":"https://example.test/datadog"}`, &manifest.CodeNotificationDatadog{APIKey: "secret", AppKey: "app", Site: "datadoghq.eu", Tags: []string{"service:api"}, URL: "https://example.test/datadog"}},
		{"notification", "victorops", `{"restEndpointKey":"secret","routingKey":"team","messageType":"RECOVERY","entityId":"monitor","url":"https://example.test/victorops"}`, &manifest.CodeNotificationVictorOps{RestEndpointKey: "secret", RoutingKey: "team", MessageType: "RECOVERY", EntityID: "monitor", URL: "https://example.test/victorops"}},
	}
	if len(tests) != 33 || len(runtimeDriverFactories) != len(tests) {
		t.Fatal("driver inventory is incomplete")
	}
	for _, test := range tests {
		t.Run(test.category+"/"+test.kind, func(t *testing.T) {
			driver := api.DriverConfig{Type: test.kind, Config: json.RawMessage(test.input)}
			if err := ValidateResolvedDriver(test.category, driver); err != nil {
				t.Fatal(err)
			}
			got, err := decodeRuntimeDriver(test.category, driver)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("runtime field mapping differs: got %#v; want %#v", got, test.want)
			}
		})
	}
}

func TestRuntimeDriverSemanticRejectionsAndOmittedDefaults(t *testing.T) {
	invalid := []struct{ category, kind, config string }{
		{"check", "http", `{}`}, {"check", "http", `{"url":"file:///private-secret"}`},
		{"check", "http", `{"url":"https://example.test","headers":{"Authorization":"private-secret\r\nInjected:value"}}`},
		{"check", "http", `{"url":"https://example.test","expectedStatus":[999]}`},
		{"check", "http", `{"url":"https://example.test","unknown":"private-secret"}`},
		{"check", "tcp", `{"host":"host","port":65536}`}, {"check", "grpc", `{"host":"host","port":-1}`},
		{"check", "icmp", `{"host":"host","count":-1}`}, {"check", "dns", `{"host":"host","server":"host:0"}`},
		{"check", "mongo", `{"uri":"mongodb+srv://private-secret@host/db"}`},
		{"check", "tls", `{"host":"host","port":443,"warnDays":3,"criticalDays":10}`},
		{"check", "redis", `{"db":-1}`}, {"check", "postgres", `{"port":-1}`}, {"check", "mysql", `{"dsn":"private-secret"}`}, {"check", "kafka", `{}`},
		{"recovery", "docker", `{"container":"c","timeout":5000}`}, {"recovery", "docker", `{"container":"c","timeout":"0s"}`},
		{"recovery", "kubernetes", `{"namespace":"ns","kind":"statefulset","name":"workload"}`},
		{"recovery", "kubernetes", `{"namespace":"ns","kind":"deployment","name":"workload","replicas":2147483648}`},
		{"recovery", "systemd", `{"unit":"cpra-test.service","mode":"reload-or-restart"}`},
		{"recovery", "aws", `{"operation":"terminate-instance","instanceId":"i-test"}`},
		{"notification", "log", `{}`}, {"notification", "slack", `{"hook":"private-secret"}`},
		{"notification", "email", `{"from":"not an address","to":"ops@example.test","server":"host:25"}`},
		{"notification", "telegram", `{"botToken":"private-secret","chatId":"room","testMode":true,"url":"https://example.test"}`},
		{"notification", "pushover", `{"appToken":"private-secret","userKey":"user","priority":2,"retry":1}`},
	}
	for _, test := range invalid {
		err := ValidateResolvedDriver(test.category, api.DriverConfig{Type: test.kind, Config: json.RawMessage(test.config)})
		if !errors.Is(err, ErrValidation) || strings.Contains(err.Error(), "private-secret") {
			t.Errorf("unsafe semantic rejection %s/%s: %v", test.category, test.kind, err)
		}
	}
	for _, test := range []struct{ category, kind, config string }{
		{"check", "http", `{"url":"https://example.test"}`}, {"check", "postgres", `{}`}, {"check", "mysql", `{}`}, {"check", "redis", `{}`}, {"check", "kafka", `{"brokers":["host"]}`}, {"check", "rabbitmq", `{"url":"amqp://"}`},
		{"recovery", "docker", `{"container":"c"}`}, {"recovery", "systemd", `{"unit":"test.service"}`},
		{"notification", "pagerduty", `{"routingKey":"key"}`}, {"notification", "opsgenie", `{"apiKey":"key"}`},
		{"notification", "datadog", `{"apiKey":"key"}`}, {"notification", "pushover", `{"appToken":"key","userKey":"user","priority":2}`},
	} {
		if err := ValidateResolvedDriver(test.category, api.DriverConfig{Type: test.kind, Config: json.RawMessage(test.config)}); err != nil {
			t.Errorf("lost production default %s/%s: %v", test.category, test.kind, err)
		}
	}
}

func TestRuntimePreparationTypedRoutingAndOwnedCredentials(t *testing.T) {
	c, _ := testCatalog(t)
	hook := "https://example.test/notify?token=private-token"
	headers := `{"Authorization":"Bearer private-header"}`
	createResource(t, c, resource("Credential", "hook", api.CredentialSpec{Value: &hook}))
	createResource(t, c, resource("Credential", "headers", api.CredentialSpec{Value: &headers}))
	createResource(t, c, resource("NotificationEndpoint", "slack", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
	logFile := filepath.Join(t.TempDir(), "must-not-exist.log")
	logConfig, _ := json.Marshal(api.CodeNotificationLog{File: &logFile})
	createResource(t, c, resource("NotificationEndpoint", "log", api.DriverConfig{Type: "log", Config: logConfig}))
	createResource(t, c, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"log", "slack"}}))
	createResource(t, c, resource("NotificationGroup", "ops", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}))
	spec := api.MonitorSpec{Enabled: api.Pointer(false), Tags: api.Pointer([]string{"service:api"}),
		Check:         api.CheckSpec{Interval: "60s", Timeout: "5s", Retries: api.Pointer(int64(3)), HealthyThreshold: api.Pointer(int64(2)), UnhealthyThreshold: api.Pointer(int64(4)), Groups: api.Pointer([]string{"api"}), Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health","retries":0}`), CredentialRefs: api.Pointer(map[string]string{"headers": "headers"})}},
		Recovery:      &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{"method":"PATCH","timeout":"7s"}`), CredentialRefs: api.Pointer(map[string]string{"url": "hook"})}, Retries: api.Pointer(int64(1)), MaxFailures: api.Pointer(int64(2))},
		Notifications: api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("ops")}, "green": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("ops"), Dispatch: api.Pointer(false)}})}
	monitor := createResource(t, c, resource("Monitor", "api", spec))
	view, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := c.PrepareRuntime(context.Background(), view, "api")
	if err != nil {
		t.Fatal(err)
	}
	check := prepared.Monitor.Pulse.Config.(*manifest.PulseHTTPConfig)
	if check.Retries != 0 || prepared.Monitor.Pulse.Retries != 3 || check.Headers["Authorization"] != "Bearer private-header" || prepared.Monitor.Enabled {
		t.Fatal("presence or credential resolution changed")
	}
	if prepared.Monitor.Pulse.Interval != time.Minute || prepared.Monitor.Pulse.Timeout != 5*time.Second || prepared.Monitor.Intervention.Target.(*manifest.InterventionTargetWebhook).Timeout != 7*time.Second {
		t.Fatal("duration conversion changed")
	}
	if len(prepared.Endpoints) != 1 || len(prepared.NotificationTargets["red"]) != 1 || prepared.NotificationTargets["red"][0].EndpointID != "slack" || prepared.Monitor.Codes["green"].Dispatch {
		t.Fatal("typed recipient filtering or Code dispatch changed")
	}
	if len(prepared.Conditions) != 7 || prepared.MonitorUID != monitor.Metadata.UID || prepared.ResourceVersion != monitor.Metadata.ResourceVersion || prepared.Generation != uint64(monitor.Metadata.Generation) {
		t.Fatalf("missing immutable dependency/monitor identity: %d", len(prepared.Conditions))
	}
	if _, err := os.Stat(logFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime preparation created a notification log")
	}
	if _, err := json.Marshal(prepared); err == nil {
		t.Fatal("plaintext runtime configuration could be serialized as an API response")
	}
	for _, text := range []string{fmt.Sprintf("%v", prepared), fmt.Sprintf("%+v", prepared), fmt.Sprintf("%#v", prepared)} {
		if strings.Contains(text, "private-token") || strings.Contains(text, "private-header") {
			t.Fatal("default formatting exposed resolved credentials")
		}
	}
	check.Headers["Authorization"] = "mutated"
	prepared.Endpoints["slack"].Config.(*manifest.CodeNotificationSlack).WebHook = "mutated"
	again, err := c.PrepareRuntime(context.Background(), view, "api")
	if err != nil {
		t.Fatal(err)
	}
	if again.Monitor.Pulse.Config.(*manifest.PulseHTTPConfig).Headers["Authorization"] != "Bearer private-header" || again.Endpoints["slack"].Config.(*manifest.CodeNotificationSlack).WebHook != hook || prepared.ExecutionRevision != again.ExecutionRevision {
		t.Fatal("returned preparation aliases retained catalog state")
	}
	patch, err := c.PreparePatch(context.Background(), "Monitor", "api", monitor.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"Renamed API"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), patch); err != nil {
		t.Fatal(err)
	}
	newView, _ := c.Snapshot()
	renamed, err := c.PrepareRuntime(context.Background(), newView, "api")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ExecutionRevision != again.ExecutionRevision || renamed.Monitor.Name != "Renamed API" || renamed.ResourceVersion == again.ResourceVersion ||
		renamed.ProjectionVersion == again.ProjectionVersion || again.ProjectionVersion == "" || prepared.ProjectionVersion != again.ProjectionVersion {
		t.Fatal("display rename changed execution content or lost CAS revision")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PrepareRuntime(ctx, view, "api"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled preparation continued", err)
	}
}

func TestRuntimeLegacyFingerprintAndSharedGroupOrder(t *testing.T) {
	c, _ := testCatalog(t)
	file := filepath.Join(t.TempDir(), "events.log")
	config, _ := json.Marshal(api.CodeNotificationLog{File: &file})
	createResource(t, c, resource("NotificationEndpoint", "log", api.DriverConfig{Type: "log", Config: config}))
	createResource(t, c, resource("NotificationGroup", "old-group", api.NotificationGroupSpec{EndpointRefs: []string{"log"}}))
	group := "old-group"
	spec := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}, Notifications: api.Pointer(map[string]api.AlertRule{"red": {GroupRef: &group}, "green": {GroupRef: &group}})}
	createResource(t, c, resource("Monitor", "legacy", spec))
	view, _ := c.Snapshot()
	got, err := c.PrepareRuntime(context.Background(), view, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.NotificationGroups[group], []string{"log"}) {
		t.Fatal("same group used by two Codes duplicated delivery targets")
	}
	legacy := manifest.Monitor{ID: "legacy", Name: "legacy", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Minute, Timeout: 5 * time.Second, Config: &manifest.PulseHTTPConfig{Url: "https://example.test/health"}}, Codes: manifest.Codes{"red": {Dispatch: true, NotifyGroup: group}, "green": {Dispatch: true, NotifyGroup: group}}}
	expected, err := manifest.ConfigurationRevision(legacy, map[string]manifest.Endpoint{"log": {Type: "log", Config: &manifest.CodeNotificationLog{File: file}}}, manifest.NotificationGroups{group: {"log"}})
	if err != nil || expected != got.ExecutionRevision {
		t.Fatal("same legacy execution content changed its fingerprint", err)
	}
}

func TestRuntimeValidationAndPreparationPerformNoProviderIO(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	urlConfig, _ := json.Marshal(api.PulseHTTPConfig{URL: api.Pointer(server.URL)})
	for range 5 {
		if err := ValidateResolvedDriver("check", api.DriverConfig{Type: "http", Config: urlConfig}); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := testCatalog(t)
	createResource(t, c, resource("Monitor", "local", api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: urlConfig}}}))
	view, _ := c.Snapshot()
	if _, err := c.PrepareRuntime(context.Background(), view, "local"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("validation or runtime preparation invoked a provider")
	}
	missing := filepath.Join(t.TempDir(), "absent", "kubeconfig")
	kube, _ := json.Marshal(api.InterventionTargetKubernetes{KubeconfigPath: &missing, Namespace: api.Pointer("test"), Kind: api.Pointer("deployment"), Name: api.Pointer("workload")})
	if err := ValidateResolvedDriver("recovery", api.DriverConfig{Type: "kubernetes", Config: kube}); err != nil {
		t.Fatal("pure validation tried to access a kubeconfig", err)
	}
}

func TestRuntimeSettingsPreserveNewControlsAndRejectInvalidValues(t *testing.T) {
	c, _ := testCatalog(t)
	start := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	spec := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}}, Recovery: &api.RecoverySpec{Driver: api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "target"})}, Cooldown: api.Pointer("30s"), MaxAttempts: api.Pointer(int64(2))}, Maintenance: &[]api.MaintenanceWindow{{Start: &start, End: &end}}}
	secret := "https://example.test/recover"
	createResource(t, c, resource("Credential", "target", api.CredentialSpec{Value: &secret}))
	createResource(t, c, resource("Monitor", "settings", spec))
	view, _ := c.Snapshot()
	got, err := c.PrepareRuntime(context.Background(), view, "settings")
	if err != nil {
		t.Fatal(err)
	}
	if got.RecoveryCooldown == nil || *got.RecoveryCooldown != 30*time.Second || got.RecoveryMaxAttempts == nil || *got.RecoveryMaxAttempts != 2 || len(got.AbsoluteMaintenance) != 1 || !got.AbsoluteMaintenance[0].Start.Equal(start) {
		t.Fatal("new controls were silently dropped")
	}
	for _, mutate := range []func(*api.MonitorSpec){func(s *api.MonitorSpec) { s.Check.Retries = api.Pointer(int64(-1)) }, func(s *api.MonitorSpec) { s.Check.Timeout = "0s" }, func(s *api.MonitorSpec) { s.Maintenance = &[]api.MaintenanceWindow{{Start: &end, End: &start}} }, func(s *api.MonitorSpec) { s.Notifications = api.Pointer(map[string]api.AlertRule{"unknown": {}}) }} {
		candidate := spec
		mutate(&candidate)
		if !errors.Is(ValidateMonitorSettings(candidate), ErrValidation) {
			t.Fatal("invalid monitor settings accepted")
		}
	}
}

func TestRuntimeAbsoluteMaintenanceRejectsZeroEndpoints(t *testing.T) {
	zero := time.Time{}
	beforeZero := zero.Add(-time.Hour)
	afterZero := zero.Add(time.Hour)
	for _, window := range []api.MaintenanceWindow{
		{Start: &zero, End: &afterZero},
		{Start: &beforeZero, End: &zero},
	} {
		spec := api.MonitorSpec{
			Check:       api.CheckSpec{Interval: "60s", Timeout: "5s"},
			Maintenance: &[]api.MaintenanceWindow{window},
		}
		if !errors.Is(ValidateMonitorSettings(spec), ErrValidation) {
			t.Fatal("zero absolute maintenance endpoint passed admission but cannot become durable policy")
		}
	}
}
