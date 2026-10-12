package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNotificationProviderWaitSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	store, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	monitor := testMonitor()
	monitor.Policy.Endpoints = map[string]int{"red": 1}
	at := time.Now().UTC()
	submit(t, store, Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: at})
	result := submit(t, store, Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, At: at, Generation: 1, Outcome: "failure"})[0]
	a := notificationByColor(t, *result.Monitor, "red", Queued)
	result = submit(t, store, Command{Kind: "start", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: a.ID, At: at})[0]
	a = result.Monitor.Actions[a.ID]
	result = submit(t, store, Command{Kind: "result", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "failure", Retryable: true, RetryDelay: 12 * time.Second})[0]
	retry := notificationByColor(t, *result.Monitor, "red", Queued)
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored, found := store.Get(monitor.ID)
	if !found || restored.Actions[retry.ID].NotBefore != at.Add(13*time.Second) || restored.Actions[retry.ID].Deadline != a.Deadline || restored.Actions[retry.ID].DeliveryID != a.DeliveryID {
		t.Fatal("restart lost provider wait, logical delivery or overall deadline")
	}
}

func TestNotificationProviderWaitPreservesIdentityAndDeadline(t *testing.T) {
	for _, delay := range []time.Duration{12 * time.Second, 2 * time.Minute} {
		policy := testMonitor().Policy
		policy.Endpoints = map[string]int{"red": 1}
		m := configuredPolicyMonitor(t, policy)
		at := policyTime()
		m = policyPulse(t, m, at, "failure")
		a := notificationByColor(t, m, "red", Queued)
		m = policyStart(t, m, a, at)
		a = m.Actions[a.ID]
		command := Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "failure", Retryable: true, RetryDelay: delay}
		if err := validateCommand(command); err != nil || commandMinimumFormat(command) != DeliveryFormatVersion {
			t.Fatal("provider delay did not require delivery format", err)
		}
		result := transition(m, command)
		if delay > NotificationTimeout {
			if result.Monitor.Actions[a.ID].State != Failed {
				t.Fatal("provider wait beyond overall budget was scheduled")
			}
			continue
		}
		retry := notificationByColor(t, *result.Monitor, "red", Queued)
		if retry.NotBefore != command.At.Add(delay) || retry.DeliveryID != a.DeliveryID || retry.Deadline != a.Deadline || retry.Attempt != 2 {
			t.Fatal("retry lost provider wait, identity or original budget")
		}
	}
}

func TestNotificationProviderDelayValidation(t *testing.T) {
	base := Command{Kind: "result", MonitorID: "monitor", Revision: "revision", ActionID: "action", At: policyTime(), Outcome: "failure", Retryable: true, RetryDelay: time.Second}
	for _, mutate := range []func(*Command){
		func(c *Command) { c.Kind = "start" }, func(c *Command) { c.Retryable = false }, func(c *Command) { c.Outcome = "success" },
		func(c *Command) { c.RetryDelay = -time.Second }, func(c *Command) { c.RetryDelay = 25 * time.Hour },
	} {
		command := base
		mutate(&command)
		if !errors.Is(validateCommand(command), ErrNotificationIdentity) {
			t.Fatal("invalid provider delay accepted")
		}
	}
}
