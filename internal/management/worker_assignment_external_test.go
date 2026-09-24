//go:build externaljobs

package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerAssignmentFixture(t *testing.T) (*Catalog, *persistence.Store, persistence.WorkerExecutionIntent) {
	t.Helper()
	c, store := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	prepared := prepareReferenceResource(t, c, externalReferenceResource("check", false))
	if _, err := c.CommitAs(t.Context(), prepared, "team/operator"); err != nil {
		t.Fatal(err)
	}
	r := prepared.mutation.Record
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: r.Key, UID: r.UID, Revision: r.Revision}}}
	at := time.Now().UTC()
	m := persistence.Monitor{ID: r.Key.ID, Revision: "execution-config-v1", CatalogUID: r.UID, CatalogRevision: r.Revision,
		Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Healthy: 1, Unhealthy: 1}}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: &guard, At: at}})
	if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
		t.Fatalf("configure: %v %+v", err, results)
	}
	m, ok := store.Get(m.ID)
	if !ok {
		t.Fatal("missing configured monitor")
	}
	intent := persistence.WorkerExecutionIntent{ID: "execution-1", Revision: "execution-revision-1", MonitorID: m.ID, MonitorUID: m.CatalogUID, MonitorRevision: m.Revision,
		ControlRevision: m.ControlRevision, Category: "check", Generation: 1, Source: r.Key, SourceUID: r.UID, SourceRevision: r.Revision, JobType: r.JobTypeReferences[0], Guard: guard, Scheduled: at, Deadline: at.Add(5 * time.Second)}
	return c, store, intent
}

func TestWorkerAssignmentPreparationCommitAndOpen(t *testing.T) {
	c, store, intent := workerAssignmentFixture(t)
	prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if len(intent.Payload.Ciphertext) != 0 {
		t.Fatal("preparation modified caller intent")
	}
	committed, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil || !reflect.DeepEqual(committed, repeated) {
		t.Fatal("lost admission response changed execution", err)
	}
	page, err := store.WorkerExecutionsReady(t.Context(), intent.JobType, "", 100)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("committed intent missing from ready inventory", err)
	}
	descriptor, err := c.OpenWorkerExecution(t.Context(), page.Items[0])
	if err != nil || descriptor.Identity().Revision != intent.JobType.Revision || descriptor.CredentialProfile() != "local-profile" || !bytes.Contains(descriptor.Parameters(), []byte("private-parameter-canary")) {
		t.Fatal("prepared parameters changed", err)
	}
	raw, err := json.Marshal(committed)
	if err != nil || bytes.Contains(raw, []byte("private-parameter-canary")) || bytes.Contains(raw, []byte("local-profile")) {
		t.Fatal("plaintext entered execution record", err)
	}
	// Existing admission is not permission to invoke a provider or change health.
	m, _ := store.Get(intent.MonitorID)
	if m.Generation != 0 || m.TotalChecks != 0 || m.Incident || len(m.Actions) != 0 {
		t.Fatal("queued admission executed a lifecycle transition")
	}
	if _, err := c.PrepareWorkerExecution(t.Context(), prepared); !errors.Is(err, persistence.ErrWorkerExecutionInvalid) {
		t.Fatal("prepared ciphertext was silently replaced", err)
	}
}

func TestWorkerAssignmentPreparationRejectsChangedSourceAndDeadline(t *testing.T) {
	c, _, intent := workerAssignmentFixture(t)
	for name, change := range map[string]func(*persistence.WorkerExecutionIntent){
		"source-version":    func(i *persistence.WorkerExecutionIntent) { i.SourceRevision = "other" },
		"source-uid":        func(i *persistence.WorkerExecutionIntent) { i.SourceUID = "other" },
		"type-version":      func(i *persistence.WorkerExecutionIntent) { i.JobType.Revision = "other" },
		"category":          func(i *persistence.WorkerExecutionIntent) { i.Category = "notification" },
		"handler-timeout":   func(i *persistence.WorkerExecutionIntent) { i.Deadline = i.Scheduled.Add(6 * time.Second) },
		"unbounded-timeout": func(i *persistence.WorkerExecutionIntent) { i.Deadline = i.Scheduled.Add(25 * time.Hour) },
		"unsafe-identity":   func(i *persistence.WorkerExecutionIntent) { i.ID = "unsafe\n" },
		"missing-guard":     func(i *persistence.WorkerExecutionIntent) { i.Guard.Conditions = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := intent.Clone()
			change(&changed)
			if _, err := c.PrepareWorkerExecution(t.Context(), changed); err == nil {
				t.Fatal("invalid intent prepared")
			}
			if !c.Ready() {
				t.Fatal("invalid intent poisoned catalog")
			}
		})
	}
}

func TestWorkerAssignmentCiphertextAuthenticatesSchedulingAndDependencies(t *testing.T) {
	for name, change := range map[string]func(*persistence.WorkerExecutionIntent){
		"execution":  func(i *persistence.WorkerExecutionIntent) { i.ID = "other" },
		"revision":   func(i *persistence.WorkerExecutionIntent) { i.Revision = "other" },
		"generation": func(i *persistence.WorkerExecutionIntent) { i.Generation++ },
		"controls":   func(i *persistence.WorkerExecutionIntent) { i.ControlRevision = "other" },
		"deadline":   func(i *persistence.WorkerExecutionIntent) { i.Deadline = i.Deadline.Add(time.Second) },
		"dependency": func(i *persistence.WorkerExecutionIntent) {
			i.Guard.Conditions[0].Revision = "other"
			i.SourceRevision = "other"
		},
		"type":       func(i *persistence.WorkerExecutionIntent) { i.JobType.Revision = "other" },
		"ciphertext": func(i *persistence.WorkerExecutionIntent) { i.Payload.Ciphertext[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c, _, intent := workerAssignmentFixture(t)
			prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
			if err != nil {
				t.Fatal(err)
			}
			change(&prepared)
			if _, err := c.OpenWorkerExecution(t.Context(), persistence.WorkerExecutionRecord{Intent: prepared}); !errors.Is(err, ErrUnavailable) {
				t.Fatal("modified protected assignment accepted", err)
			}
			if c.Ready() {
				t.Fatal("unauthenticated protected assignment did not fail readiness")
			}
		})
	}
}

func TestWorkerAssignmentRetainsOriginalParametersAcrossCatalogUpdates(t *testing.T) {
	c, store, intent := workerAssignmentFixture(t)
	prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := c.GetJobType(t.Context(), intent.JobType.JobTypeID)
	if err != nil {
		t.Fatal(err)
	}
	resource.Spec.Version = "2"
	resource.Spec.ParameterSchema = json.RawMessage(`{"const":"different-contract"}`)
	commitTestJobType(t, c, resource, resource.Metadata.ResourceVersion, false)
	descriptor, err := c.OpenWorkerExecution(t.Context(), committed)
	if err != nil || descriptor.Identity().Version != "1" {
		t.Fatal("new contract replaced original assignment", err)
	}
	// Returned ciphertext and parameters belong to separate callers.
	copy := committed.Clone()
	clear(copy.Intent.Payload.Ciphertext)
	parameters := descriptor.Parameters()
	clear(parameters)
	if _, err := c.OpenWorkerExecution(t.Context(), committed); err != nil {
		t.Fatal("borrowed buffers changed retained assignment", err)
	}
	plain, err := c.sealer.Open(t.Context(), committed.Intent.Binding(c.storeID), committed.Intent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	clear(plain)
	wrongStore := c.storeID + "-other"
	if _, err := c.sealer.Open(t.Context(), committed.Intent.Binding(wrongStore), committed.Intent.Payload); !errors.Is(err, secureconfig.ErrAuthentication) {
		t.Fatal("assignment moved across store", err)
	}
}
