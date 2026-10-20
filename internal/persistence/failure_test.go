package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFailureDiagnosticRejectsArbitraryTextAndOlderFormats(t *testing.T) {
	d := NewFailureDiagnostic("dns_failed")
	if len(d.Message) > 256 || d.validate(DeliveryFormatVersion) != nil {
		t.Fatal("canonical diagnostic rejected")
	}
	for _, value := range []FailureDiagnostic{{Code: "dns_failed", Message: "password=private"}, {Code: "unsupported", Message: "private"}, {Message: "private"}} {
		if !errors.Is(value.validate(DeliveryFormatVersion), ErrFailureDiagnostic) {
			t.Fatal("unreviewed diagnostic accepted")
		}
	}
	if !errors.Is(d.validate(DeliveryFormatVersion-1), ErrFailureDiagnostic) {
		t.Fatal("old format accepted new diagnostic")
	}
	base := Command{Kind: "pulse", At: policyTime(), MonitorID: "monitor", Revision: "revision", Generation: 1, Outcome: "failure", Failure: d}
	if err := validateCommand(base); err != nil || commandMinimumFormat(base) != DeliveryFormatVersion {
		t.Fatal("diagnostic command rejected", err)
	}
	base.Outcome = "success"
	if !errors.Is(validateCommand(base), ErrFailureDiagnostic) {
		t.Fatal("success accepted failure detail")
	}
	m := testMonitor()
	m.LastFailure = d
	if !requiresDeliveryFormat(m) || validateNotificationIdentity(m, DeliveryFormatVersion-1) == nil {
		t.Fatal("monitor bypassed format gate")
	}
	m.LastFailure = FailureDiagnostic{}
	m.Actions = map[string]Action{"action": {Failure: d}}
	if !requiresDeliveryFormat(m) || validateNotificationIdentity(m, DeliveryFormatVersion-1) == nil {
		t.Fatal("action bypassed format gate")
	}
	m.Actions["action"] = Action{Cause: d}
	if !requiresDeliveryFormat(m) || validateNotificationIdentity(m, DeliveryFormatVersion-1) == nil {
		t.Fatal("notification cause bypassed format gate")
	}
}

func TestFailureDiagnosticsSurviveSnapshotAndHistory(t *testing.T) {
	config := testConfig(t)
	store, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	configure(t, store)
	now := time.Now().UTC()
	d := (FailureDiagnostic{Code: "certificate_expiring", CertificateNotAfter: now.Add(time.Hour), CertificateDaysRemaining: 0}).WithDetails()
	result := submit(t, store, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", At: now, Generation: 1, Outcome: "failure", Failure: d})[0]
	if result.Err != nil || result.Monitor == nil || result.Monitor.LastFailure != d {
		t.Fatal("diagnostic observation not committed", result.Err)
	}
	m := *result.Monitor
	id := sortedActions(m.Actions)[0]
	if m.Actions[id].Cause != d {
		t.Fatal("notification lost its committed cause")
	}
	if !submit(t, store, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now})[0].Allowed {
		t.Fatal("start rejected")
	}
	actionFailure := NewFailureDiagnostic("deadline_exceeded")
	result = submit(t, store, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: now, Outcome: "timeout", Ambiguous: true, Failure: actionFailure})[0]
	if result.Err != nil || result.Monitor.Actions[id].State != Unknown || result.Monitor.Actions[id].Failure != actionFailure {
		t.Fatal("ambiguous action lost diagnostic or hold", result.Err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := monitorWithActionHistory(t, store, m.ID)
	if !ok || m.LastFailure != d || m.Actions[id].Failure != actionFailure || m.Actions[id].Cause != d || m.Actions[id].State != Unknown {
		t.Fatal("snapshot lost diagnostics or unknown hold")
	}
	page, err := store.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Events {
		if event.Type == "action_unknown" && event.ActionID == id {
			found = event.Failure == actionFailure && event.Cause == d
		}
	}
	if !found {
		t.Fatal("retained history lost safe diagnostic")
	}
	result = submit(t, store, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: now.Add(time.Second), Generation: 2, Outcome: "success"})[0]
	if result.Monitor.LastFailure != (FailureDiagnostic{}) {
		t.Fatal("successful observation kept stale failure")
	}
	raw, err := json.Marshal(result.Monitor)
	if err != nil || strings.Contains(string(raw), "private-provider-response") {
		t.Fatal("unsafe serialized diagnostic", err)
	}
}

func TestFailureProtocolDetailsAreBoundedAndFormatGated(t *testing.T) {
	for _, d := range []FailureDiagnostic{
		{Code: "unexpected_http_status", HTTPStatus: 503},
		{Code: "grpc_health_failed", GRPCStatus: "NOT_SERVING"},
		{Code: "grpc_health_failed", GRPCCode: "PermissionDenied"},
		{Code: "certificate_expiring", CertificateNotAfter: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), CertificateDaysRemaining: -1},
	} {
		d = d.WithDetails()
		if err := d.validate(LifecycleFormatVersion); err != nil {
			t.Fatal(err, d)
		}
		if d.validate(DeliveryFormatVersion) == nil {
			t.Fatal("old format accepted protocol detail")
		}
		c := Command{Kind: "pulse", MonitorID: "monitor", Revision: "r1", At: policyTime(), Generation: 1, Outcome: "failure", Failure: d}
		if commandMinimumFormat(c) != LifecycleFormatVersion || validateCommand(c) != nil {
			t.Fatal("detail format not selected")
		}
		raw, err := json.Marshal(d)
		var restored FailureDiagnostic
		if err != nil || json.Unmarshal(raw, &restored) != nil || restored != d {
			t.Fatal("detail round trip failed")
		}
	}
	for _, d := range []FailureDiagnostic{
		{Code: "unexpected_http_status", HTTPStatus: 600},
		{Code: "dns_failed", HTTPStatus: 404},
		{Code: "grpc_health_failed", GRPCStatus: "private-provider-body"},
		{Code: "grpc_health_failed", GRPCCode: "OK"},
		{Code: "certificate_expiring", CertificateDaysRemaining: 2},
	} {
		if d.WithDetails().validate(LifecycleFormatVersion) == nil {
			t.Fatal("unbounded or contradictory detail accepted", d)
		}
	}
}
