//go:build externaljobs

package management

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func workerInlineAssignmentFixture(t *testing.T) (*Catalog, *persistence.Store, persistence.WorkerExecutionIntent) {
	t.Helper()
	c, store := jobTypeCatalog(t)
	createReferenceJobType(t, c, "notification")
	input := externalReferenceResource("notification", true)
	var spec api.MonitorSpec
	if err := api.StrictDecode(input.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	for color, rule := range *spec.Notifications {
		driver := *rule.Driver
		driver.Config = bytes.ReplaceAll(driver.Config, []byte("private-parameter-canary"), []byte("private-"+color+"-target"))
		rule.Driver = &driver
		(*spec.Notifications)[color] = rule
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	input.Spec = raw
	committed := commitRuntimeReference(t, c, input)
	p := prepareRuntimeReference(t, c, input.Metadata.ID)
	source := persistence.WorkerNotificationSource{Kind: "Monitor", ID: input.Metadata.ID, UID: committed.Metadata.UID, Revision: committed.Metadata.ResourceVersion}
	monitor := persistence.Monitor{ID: input.Metadata.ID, Revision: p.ExecutionRevision, CatalogUID: p.MonitorUID,
		Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Healthy: 1, Unhealthy: 1, Endpoints: map[string]int{"red": 1, "yellow": 1}}}
	monitor.Policy.WorkerNotificationSources = map[string][]persistence.WorkerNotificationSource{"red": {source}, "yellow": {source}}
	guard := persistence.CatalogGuard{Conditions: p.Conditions}
	at := time.Now().UTC()
	commands := []persistence.Command{
		{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, Guard: &guard, At: at},
		{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Generation: 1, Guard: &guard, At: at, Outcome: "failure"},
	}
	results, err := store.Submit(t.Context(), commands)
	if err != nil || len(results) != len(commands) {
		t.Fatal("notification fixture admission", err)
	}
	for n, result := range results {
		if result.Err != nil || n == 0 && !result.Allowed {
			t.Fatal("notification fixture transition", result.Err)
		}
	}
	monitor, _ = store.Get(monitor.ID)
	view, err := store.CatalogSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := view.Get(persistence.CatalogKey{Kind: "Monitor", ID: monitor.ID})
	for _, action := range monitor.Actions {
		if action.Kind == "code" && action.Color == "red" {
			return c, store, persistence.WorkerExecutionIntent{ID: "inline-notification", Revision: "execution-revision", MonitorID: monitor.ID, MonitorUID: monitor.CatalogUID,
				MonitorRevision: monitor.Revision, ControlRevision: monitor.ControlRevision, Category: "notification", ActionID: action.ID, Source: record.Key, SourceUID: record.UID,
				SourceRevision: record.Revision, JobType: record.JobTypeReferences[0], Guard: guard, Scheduled: action.NotBefore, Deadline: action.NotBefore.Add(5 * time.Second)}
		}
	}
	t.Fatal("missing red notification action")
	return nil, nil, persistence.WorkerExecutionIntent{}
}

func TestWorkerAssignmentInlineSelectsOriginalAction(t *testing.T) {
	c, store, intent := workerInlineAssignmentFixture(t)
	prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil || !reflect.DeepEqual(record, replay) {
		t.Fatal("notification replay changed identity", err)
	}
	descriptor, err := c.OpenWorkerExecution(t.Context(), record)
	if err != nil || !bytes.Contains(descriptor.Parameters(), []byte("private-red-target")) || bytes.Contains(descriptor.Parameters(), []byte("private-yellow-target")) {
		t.Fatal("notification prepared parameters from another color", err)
	}
	raw, err := json.Marshal(record)
	if err != nil || bytes.Contains(raw, []byte("private-red-target")) || bytes.Contains(raw, []byte("local-profile")) {
		t.Fatal("notification parameters entered durable metadata", err)
	}
}

func TestWorkerAssignmentInlineRejectsChangedActionAndTarget(t *testing.T) {
	c, store, intent := workerInlineAssignmentFixture(t)
	for name, change := range map[string]func(*persistence.WorkerExecutionIntent){
		"action":             func(i *persistence.WorkerExecutionIntent) { i.ActionID = "missing-action" },
		"schedule":           func(i *persistence.WorkerExecutionIntent) { i.Scheduled = i.Scheduled.Add(time.Millisecond) },
		"source-incarnation": func(i *persistence.WorkerExecutionIntent) { i.SourceUID = "other-incarnation" },
		"source-revision":    func(i *persistence.WorkerExecutionIntent) { i.SourceRevision = "other-revision" },
		"monitor-revision":   func(i *persistence.WorkerExecutionIntent) { i.MonitorRevision = "other-revision" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := intent.Clone()
			change(&bad)
			if _, err := c.PrepareWorkerExecution(t.Context(), bad); err == nil {
				t.Fatal("modified original action prepared")
			}
			if !c.Ready() {
				t.Fatal("invalid preparation poisoned the catalog")
			}
		})
	}
	prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	commitRuntimeReference(t, c, externalReferenceResource("notification", true))
	if _, err = store.CommitWorkerExecution(t.Context(), prepared); err == nil {
		t.Fatal("old action admitted against changed notification configuration")
	}
	descriptor, err := c.OpenWorkerExecution(t.Context(), persistence.WorkerExecutionRecord{Intent: prepared})
	if err != nil || !bytes.Contains(descriptor.Parameters(), []byte("private-red-target")) {
		t.Fatal("configuration update changed the sealed assignment", err)
	}
}
