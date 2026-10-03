package management

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestA2AObservationsPreserveReceiptAndUnknownOutcome(t *testing.T) {
	at := time.Now().UTC()
	r := &persistence.AgentReceipt{Sequence: 1<<63 + 17, TaskID: "original-task", ContextID: "original-context", State: "TASK_STATE_FUTURE", ObservedAt: at}
	a := persistence.ActionRecord{Action: persistence.Action{ID: "action", State: persistence.Unknown, AgentReceipt: r}, MonitorID: "monitor", Held: true, ExecutorFenced: false}
	out := actionView(a)
	if out.State != "unknown" || !out.Held || out.ExecutorFenced || out.UpdatedAt == nil || !out.UpdatedAt.Equal(at) || out.AgentReceipt.Sequence != "9223372036854775825" {
		t.Fatal("receipt changed outcome, ordering or hold", out)
	}
	*out.AgentReceipt.TaskID = "caller-mutated"
	if r.TaskID != "original-task" {
		t.Fatal("API receipt aliases committed state")
	}
}

func TestA2AConfigurationRejectsUnsafeResolvedValues(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		for _, input := range []string{
			`{}`, `{"url":"http://private-agent.test"}`, `{"url":"ftp://private-agent.test"}`,
			`{"url":"https://private-secret@agent.test"}`, `{"url":"https://agent.test?private-secret"}`,
			`{"url":"https://agent.test/#private-secret"}`, `{"url":"https://agent.test:65536"}`,
			`{"url":"https://agent.test","token":"private-secret\r\nInjected:value"}`,
			`{"url":"https://agent.test","tenant":"private-secret\n"}`,
			`{"url":"https://agent.test","requestTimeout":"6m"}`,
			`{"url":"https://agent.test","requestTimeout":"0s"}`,
			`{"url":"https://agent.test","requestTimeout":1000000}`,
		} {
			err := ValidateResolvedDriver(category, api.DriverConfig{Type: "a2a", Config: json.RawMessage(input)})
			if !errors.Is(err, ErrValidation) || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("unsafe %s rejection: %v", category, err)
			}
		}
	}
	for _, input := range []string{
		`{"url":"https://agent.test","taskTimeout":"500ms"}`, `{"url":"https://agent.test","taskTimeout":"6m"}`,
		`{"url":"https://agent.test","pollInterval":"500ms"}`, `{"url":"https://agent.test","pollInterval":"31s"}`,
		`{"url":"https://agent.test","taskTimeout":"1s","pollInterval":"2s"}`,
	} {
		if ValidateResolvedDriver("recovery", api.DriverConfig{Type: "a2a", Config: json.RawMessage(input)}) == nil {
			t.Fatal("invalid task bounds accepted")
		}
	}
}

func TestA2ACatalogRequiresProtectedAgentReferences(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	for _, category := range []string{"recovery", "notification"} {
		for _, field := range []string{"url", "token"} {
			raw, _ := json.Marshal(map[string]string{field: "private-secret"})
			driver := api.DriverConfig{Type: "a2a", Config: raw}
			r := resource("NotificationEndpoint", "agent", driver)
			if category == "recovery" {
				r = resource("Monitor", "agent", api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://health.test"}`)}}, Recovery: &api.RecoverySpec{Driver: driver}})
			}
			if _, err := c.Prepare(ctx, r, "", true); err == nil || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("inline agent credential admitted or echoed", err)
			}
		}
	}
}

func TestRecoveryNotificationObservationRetainsOriginalFailure(t *testing.T) {
	at := time.Now().UTC()
	failure := &persistence.RecoveryFailure{ActionID: "original-restart", Driver: "docker", Attempt: 2, MaxAttempts: 2, Trigger: "recovery_exhausted", FailedAt: at}
	action := persistence.ActionRecord{MonitorID: "monitor", Action: persistence.Action{ID: "notification", State: persistence.Unknown, RecoveryFailure: failure}, Held: true}
	out := actionView(action)
	if out.RecoveryFailure == nil || out.RecoveryFailure.Outcome != "failure" || out.RecoveryFailure.ActionID != "original-restart" || out.State != "unknown" || !out.Held {
		t.Fatal("recovery context changed notification disposition")
	}
	out.RecoveryFailure.ActionID = "caller-mutation"
	if failure.ActionID != "original-restart" {
		t.Fatal("API observation aliases committed state")
	}
}
