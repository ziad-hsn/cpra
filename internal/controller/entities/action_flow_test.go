package entities

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/queue"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestActionDestinationUsesEffectiveProviderURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
	}{
		{"pagerduty", &manifest.CodeNotificationPagerDuty{RoutingKey: "key"}, &manifest.CodeNotificationPagerDuty{RoutingKey: "key", URL: "https://events.pagerduty.com/v2/enqueue"}},
		{"opsgenie", &manifest.CodeNotificationOpsgenie{APIKey: "key"}, &manifest.CodeNotificationOpsgenie{APIKey: "key", URL: "https://api.opsgenie.com/v2/alerts"}},
		{"pushover", &manifest.CodeNotificationPushover{UserKey: "user"}, &manifest.CodeNotificationPushover{UserKey: "user", URL: "https://api.pushover.net/1/messages.json"}},
		{"telegram", &manifest.CodeNotificationTelegram{BotToken: "123:fixture", ChatID: "chat"}, &manifest.CodeNotificationTelegram{ChatID: "chat", URL: "https://api.telegram.org/bot123:fixture/sendMessage"}},
		{"telegram", &manifest.CodeNotificationTelegram{BotToken: "123:fixture", ChatID: "chat", TestMode: true}, &manifest.CodeNotificationTelegram{ChatID: "chat", URL: "https://api.telegram.org/bot123:fixture/test/sendMessage"}},
		{"twilio", &manifest.CodeNotificationTwilio{AccountSID: "account", To: "recipient"}, &manifest.CodeNotificationTwilio{AccountSID: "account", To: "recipient", URL: "https://api.twilio.com/2010-04-01/Accounts/account/Messages.json"}},
		{"datadog", &manifest.CodeNotificationDatadog{APIKey: "key"}, &manifest.CodeNotificationDatadog{APIKey: "key", Site: "datadoghq.com"}},
		{"datadog", &manifest.CodeNotificationDatadog{APIKey: "key", Site: "datadoghq.eu"}, &manifest.CodeNotificationDatadog{APIKey: "key", Site: "ignored", URL: "https://api.datadoghq.eu/api/v1/events"}},
		{"victorops", &manifest.CodeNotificationVictorOps{RestEndpointKey: "key", RoutingKey: "route"}, &manifest.CodeNotificationVictorOps{URL: "https://alert.victorops.com/integrations/generic/20131114/alert/key/route"}},
	} {
		a, err := actionDestination(tc.name, tc.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := actionDestination(tc.name, tc.b)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("%s omitted and explicit operation destinations differ", tc.name)
		}
	}
}

type heldNotificationJob struct {
	jobs.Job
	started chan string
	release <-chan struct{}
	color   string
}

func (j *heldNotificationJob) Copy() jobs.Job { c := *j; return &c }
func (j *heldNotificationJob) Execute() jobs.Result {
	j.started <- j.color
	if j.color == "red" {
		<-j.release
	}
	return jobs.Result{Type: "code"}
}

func TestPreparedDefaultDestinationOrdersColorDispatch(t *testing.T) {
	m := testMonitor()
	m.Codes = manifest.Codes{"red": {Dispatch: true, Notify: "pagerduty", Config: &manifest.CodeNotificationPagerDuty{RoutingKey: "key"}}, "green": {Dispatch: true, Notify: "pagerduty", Config: &manifest.CodeNotificationPagerDuty{RoutingKey: "key", URL: "https://events.pagerduty.com/v2/enqueue"}}}
	prepared, err := PrepareMonitor(m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	q, err := queue.NewActionQueue(64, "notification")
	if err != nil {
		t.Fatal(err)
	}
	cfg := queue.DefaultWorkerPoolConfig()
	cfg.FairDispatch = true
	cfg.MinWorkers = 2
	cfg.MaxWorkers = 2
	cfg.NumShards = 1
	cfg.AdjustmentInterval = 0
	cfg.ResultBatchSize = 1
	pool, err := queue.NewDynamicWorkerPool(queue.NewHandle(q), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	defer pool.DrainAndStop()
	defer func() {
		if release != nil {
			close(release)
		}
	}()
	for _, color := range []string{"red", "green"} {
		j := &heldNotificationJob{Job: prepared.bundle.jobs.CodeJobs[color][0], started: started, release: release, color: color}
		d := jobs.NewDispatch(j, ecs.Entity{}, "code", color, 1, 0)
		flow := prepared.bundle.jobs.CodeFlows[color][0]
		d.Driver, d.FlowKey = flow.Driver, flow.Key
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	pool.Start()
	select {
	case color := <-started:
		if color != "red" {
			t.Fatal("closure overtook opening")
		}
	case <-time.After(time.Second):
		t.Fatal("opening did not run")
	}
	select {
	case <-started:
		t.Fatal("closure ran while opening held its destination")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	release = nil
	select {
	case color := <-started:
		if color != "green" {
			t.Fatal("wrong next delivery")
		}
	case <-time.After(time.Second):
		t.Fatal("closure did not run after opening completed")
	}
}

func TestActionDestinationIgnoresMessageAndBudgetSettings(t *testing.T) {
	for _, test := range []struct {
		driver      string
		a, b, other any
	}{
		{"email", &manifest.CodeNotificationEmail{Server: "smtp://example.test:25", To: "a@example.test", Subject: "down"}, &manifest.CodeNotificationEmail{Server: "smtp://example.test:25", To: "a@example.test", Subject: "up"}, &manifest.CodeNotificationEmail{Server: "smtp://example.test:25", To: "b@example.test"}},
		{"webhook", &manifest.InterventionTargetWebhook{URL: "https://EXAMPLE.test:443/", Body: "down"}, &manifest.InterventionTargetWebhook{URL: "https://example.test", Body: "up", Timeout: time.Minute}, &manifest.InterventionTargetWebhook{URL: "https://other.test"}},
		{"pushover", &manifest.CodeNotificationPushover{UserKey: "private-a", Priority: 2, Title: "down"}, &manifest.CodeNotificationPushover{UserKey: "private-a", Priority: 0, Title: "up"}, &manifest.CodeNotificationPushover{UserKey: "private-b"}},
		{"docker", &manifest.InterventionTargetDocker{Container: "one", Timeout: time.Second}, &manifest.InterventionTargetDocker{Container: "one", Timeout: time.Minute}, &manifest.InterventionTargetDocker{Container: "two"}},
		{"a2a", &manifest.CodeNotificationA2A{URL: "https://agent.test", Tenant: "one"}, &manifest.CodeNotificationA2A{URL: "https://agent.test", Tenant: "one", Token: "rotated", Timeout: time.Minute}, &manifest.CodeNotificationA2A{URL: "https://other.test", Tenant: "one"}},
	} {
		a, err := actionDestination(test.driver, test.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := actionDestination(test.driver, test.b)
		if err != nil {
			t.Fatal(err)
		}
		other, err := actionDestination(test.driver, test.other)
		if err != nil {
			t.Fatal(err)
		}
		if a != b || a == other {
			t.Fatalf("%s identity depends on payload or aliases distinct destinations", test.driver)
		}
	}
}

func TestPreparedActionFlowSharedAcrossColorsGroupsAndMonitors(t *testing.T) {
	m := testMonitor()
	m.Codes = manifest.Codes{"red": {NotifyGroup: "oncall", Dispatch: true}, "green": {Notify: "email", Dispatch: true, Config: &manifest.CodeNotificationEmail{Server: "smtp://example.test:25", To: "ops@example.test", From: "cpra@example.test", Subject: "up"}}}
	endpoints := map[string]manifest.Endpoint{"first-name": {Type: "email", Config: &manifest.CodeNotificationEmail{Server: "smtp://example.test:25", To: "ops@example.test", From: "cpra@example.test", Subject: "down"}}}
	groups := manifest.NotificationGroups{"oncall": {"first-name"}}
	a, err := PrepareMonitor(m, endpoints, groups)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m.ID, m.Name = "second", "second"
	b, err := PrepareMonitor(m, endpoints, groups)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first := a.bundle.jobs.CodeFlows["red"][0]
	if first.Driver != "email" || first != a.bundle.jobs.CodeFlows["green"][0] || first != b.bundle.jobs.CodeFlows["red"][0] {
		t.Fatal("same destination split by monitor/color/group alias")
	}
	copy := a.bundle.jobs.Copy()
	copy.CodeFlows["red"][0].Key++
	if a.bundle.jobs.CodeFlows["red"][0] != first {
		t.Fatal("copied flow metadata mutated live preparation")
	}
}
