//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerExecutionFixture(t *testing.T, s *Store, category string) (WorkerExecutionIntent, JobTypeState) {
	t.Helper()
	cmd := jobTypeFixture(t, s, "execution-"+category+uuid.NewString())
	cmd.Value.Category = category
	job := commitJobType(t, s, cmd)
	return workerExecutionFixtureType(t, s, job), job
}
func workerExecutionFixtureType(t *testing.T, s *Store, job JobTypeState) WorkerExecutionIntent {
	t.Helper()
	category := job.Current.Category
	monitor := testMonitor()
	monitor.ID = uuid.NewString()
	monitor.Revision = uuid.NewString()
	monitor.Policy.Intervention = category == "recovery"
	monitor.Policy.Endpoints = nil
	source := catalogJobTypeRecord(t, s, monitor.ID, job.Current)
	keys := []CatalogKey{source.Key}
	if category == "notification" {
		source.Key = CatalogKey{Kind: "NotificationEndpoint", ID: uuid.NewString()}
		source = createCatalog(t, s, source)
		root := catalogRecord(t, s, "Monitor", monitor.ID, uuid.NewString(), uuid.NewString(), "private-monitor", source.Key)
		root = createCatalog(t, s, root)
		keys = []CatalogKey{root.Key, source.Key}
		monitor.Policy.Endpoints = map[string]int{"red": 2}
		monitor.Policy.WorkerNotificationSources = map[string][]WorkerNotificationSource{"red": {{}, {ID: source.Key.ID, UID: source.UID, Revision: source.Revision}}}
	} else {
		source = createCatalog(t, s, source)
	}
	guard := CatalogGuard{Conditions: catalogConditions(t, s, keys)}
	now := time.Now().UTC()
	result := submit(t, s, Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, Guard: &guard, At: now})[0]
	if result.Err != nil || !result.Allowed {
		t.Fatal("configure", result.Err)
	}
	if category != "check" {
		result = submit(t, s, Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Generation: 1, Guard: &guard, At: now, Outcome: "failure"})[0]
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	monitor, _ = s.Get(monitor.ID)
	in := WorkerExecutionIntent{ID: uuid.NewString(), Revision: uuid.NewString(), MonitorID: monitor.ID, MonitorUID: monitor.CatalogUID, MonitorRevision: monitor.Revision, ControlRevision: monitor.ControlRevision, Category: category, Source: source.Key, SourceUID: source.UID, SourceRevision: source.Revision, JobType: catalogJobTypeRef(job.Current), Guard: guard, Scheduled: now, Deadline: now.Add(time.Hour)}
	if category == "check" {
		in.Generation = monitor.Generation + 1
	} else {
		for _, a := range monitor.Actions {
			if category == "recovery" && a.Kind == "intervention" || category == "notification" && a.Kind == "code" && a.Endpoint == 1 {
				in.ActionID = a.ID
				in.Scheduled = a.NotBefore
				break
			}
		}
		if in.ActionID == "" {
			t.Fatalf("no %s action", category)
		}
	}
	var err error
	in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-execution-parameters-canary"))
	if err != nil || in.Validate() != nil {
		t.Fatal("fixture intent", err, in.Validate())
	}
	return in
}
func workerExecutionSubmitAt(t *testing.T, s *Store, in WorkerExecutionIntent, owner string, at time.Time) Result {
	t.Helper()
	c := WorkerExecutionCommand{Intent: in, OwnerEpoch: owner}
	return submit(t, s, Command{Kind: "worker_execution", At: at, commandExtensions: commandExtensions{WorkerExecution: &c}})[0]
}
func TestWorkerExecutionAdmissionAllCategories(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s := openCatalogMemory(t)
			in, _ := workerExecutionFixture(t, s, category)
			before, _ := s.Get(in.MonitorID)
			first, err := s.CommitWorkerExecution(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := s.CommitWorkerExecution(t.Context(), in)
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatal("exact retry", err)
			}
			after, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("admission mutated incident, generation, cooldown or action state")
			}
			if first.OwnerEpoch != s.executorSession || first.Intent.ID != in.ID || first.CommittedIndex == 0 {
				t.Fatal("identity not preserved")
			}
			page, err := s.WorkerExecutionsReady(t.Context(), in.JobType, "", 100)
			if err != nil || len(page.Items) != 1 || page.Items[0].Intent.ID != in.ID {
				t.Fatal("ready", err, len(page.Items))
			}
			first.Intent.Payload.Ciphertext[0] ^= 1
			first.Intent.Guard.Conditions[0].Revision = "changed"
			got, ok, err := s.WorkerExecution(t.Context(), in.ID)
			if err != nil || !ok || !reflect.DeepEqual(got.Intent, in) {
				t.Fatal("record alias", err)
			}
			page.Items[0].Intent.Payload.Ciphertext[0] ^= 1
			got, _, _ = s.WorkerExecution(t.Context(), in.ID)
			if !reflect.DeepEqual(got.Intent, in) {
				t.Fatal("page alias")
			}
			raw, _ := json.Marshal(s.fsm.image)
			if bytes.Contains(raw, []byte("private-execution-parameters-canary")) {
				t.Fatal("plaintext in persisted state")
			}
			if err = validateWorkerExecutionImage(s.fsm.image); err != nil {
				t.Fatal("invalid committed image", err)
			}
			other := in.Clone()
			other.ID = uuid.NewString()
			other.Revision = uuid.NewString()
			if _, err = s.CommitWorkerExecution(t.Context(), other); !errors.Is(err, ErrWorkerExecutionConflict) {
				t.Fatal("duplicate work admitted", err)
			}
			changed := in.Clone()
			changed.Payload.Ciphertext[0] ^= 1
			if _, err = s.CommitWorkerExecution(t.Context(), changed); !errors.Is(err, ErrWorkerExecutionConflict) {
				t.Fatal("different ciphertext replay accepted", err)
			}
			if got := workerExecutionSubmitAt(t, s, in, s.executorSession, in.Deadline); !errors.Is(got.Err, ErrWorkerExecutionExpired) {
				t.Fatal("elapsed replay admitted", got.Err)
			}
		})
	}
}
func TestWorkerExecutionNotificationOriginalMixedOrdinal(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "notification")
	m, _ := s.Get(in.MonitorID)
	builtinSlots := 0
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Endpoint == 0 {
			builtinSlots++
			bad := in.Clone()
			bad.ActionID = a.ID
			bad.Scheduled = a.NotBefore
			if _, err := s.CommitWorkerExecution(t.Context(), bad); !errors.Is(err, ErrWorkerExecutionConflict) {
				t.Fatal("external endpoint redirected builtin slot", err)
			}
		}
	}
	if builtinSlots != 1 {
		t.Fatal("fixture lacks exact builtin notification slot", builtinSlots)
	}
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if s.fsm.image.Version != WorkerExecutionFormatVersion {
		t.Fatal("notification policy failed to advance format")
	}
	p := m.Policy.Clone()
	p.WorkerNotificationSources["red"][1].ID = "changed"
	if m.Policy.WorkerNotificationSources["red"][1].ID == "changed" {
		t.Fatal("policy clone shares slots")
	}
}
func TestWorkerExecutionAdmissionCurrentFences(t *testing.T) {
	for name, change := range map[string]func(*Store, *WorkerExecutionIntent){
		"control":            func(_ *Store, i *WorkerExecutionIntent) { i.ControlRevision = "old" },
		"execution-revision": func(_ *Store, i *WorkerExecutionIntent) { i.MonitorRevision = "old" },
		"generation":         func(_ *Store, i *WorkerExecutionIntent) { i.Generation++ },
		"source-revision": func(_ *Store, i *WorkerExecutionIntent) {
			i.SourceRevision = "old"
			for n := range i.Guard.Conditions {
				i.Guard.Conditions[n].Revision = "old"
			}
		},
		"job-version": func(_ *Store, i *WorkerExecutionIntent) { i.JobType.Version = "unknown" },
		"disabled": func(s *Store, i *WorkerExecutionIntent) {
			m := s.fsm.image.Monitors[i.MonitorID]
			m.Policy.Enabled = false
			s.fsm.image.Monitors[i.MonitorID] = m
		},
		"snoozed": func(s *Store, i *WorkerExecutionIntent) {
			m := s.fsm.image.Monitors[i.MonitorID]
			m.SnoozedUntil = time.Now().Add(time.Hour)
			s.fsm.image.Monitors[i.MonitorID] = m
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := openCatalogMemory(t)
			in, _ := workerExecutionFixture(t, s, "check")
			change(s, &in)
			if _, err := s.CommitWorkerExecution(t.Context(), in); err == nil {
				t.Fatal("stale/paused execution admitted")
			}
			if s.fsm.image.WorkerExecutions != nil {
				t.Fatal("rejected intent stored")
			}
		})
	}
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "check")
	if r := workerExecutionSubmitAt(t, s, in, "previous-owner", time.Now().UTC()); !errors.Is(r.Err, ErrWorkerExecutionConflict) {
		t.Fatal("old owner", r.Err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.CommitWorkerExecution(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestWorkerExecutionReadyPagesAndCurrentOwner(t *testing.T) {
	s := openCatalogMemory(t)
	first, job := workerExecutionFixture(t, s, "check")
	for n := 0; n < 3; n++ {
		in := first
		if n > 0 {
			in = workerExecutionFixtureType(t, s, job)
		}
		in.ID = fmt.Sprintf("execution-%02d", n)
		var err error
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-execution-parameters-canary"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.WorkerExecutionsReady(t.Context(), first.JobType, "", 2)
	if err != nil || len(page.Items) != 2 || !page.More || page.Next != "execution-01" {
		t.Fatal(page, err)
	}
	for _, record := range page.Items {
		plain, err := catalogSealer(t).Open(t.Context(), record.Intent.Binding(s.nodeID), record.Intent.Payload)
		if err != nil || string(plain) != "private-execution-parameters-canary" {
			t.Fatal("page lost encrypted binding", err)
		}
		clear(plain)
	}
	page, err = s.WorkerExecutionsReady(t.Context(), first.JobType, page.Next, 2)
	if err != nil || len(page.Items) != 1 || page.More || page.Items[0].Intent.ID != "execution-02" {
		t.Fatal(page, err)
	}
	s.executorSession = uuid.NewString()
	submit(t, s, Command{Kind: "local_session", At: time.Now().UTC(), ExecutorSession: s.executorSession})
	page, err = s.WorkerExecutionsReady(t.Context(), first.JobType, "", 2)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("old owner ready", err)
	}
}
func TestWorkerExecutionRetentionProtectsJobType(t *testing.T) {
	s := openCatalogMemory(t)
	in, job := workerExecutionFixture(t, s, "check")
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	source := requireCatalog(t, s, in.Source)
	update := updateCatalogMutation(t, s, source, uuid.NewString(), "builtin")
	update.Record.JobTypeReferences = nil
	if result := catalogSubmit(t, s, update); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := s.CommitJobType(t.Context(), catalogJobTypeDelete(t, s, job), job.Current.Record.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrCatalogReferenced) {
		t.Fatal("queued record lost immutable type protection", err)
	}
	s.fsm.image.WorkerExecutions = s.fsm.image.WorkerExecutions.clone()
	if !s.fsm.jobTypeExecutionReferenced(in.JobType.JobTypeID, in.JobType.JobTypeUID) {
		t.Fatal("recovered index lost incoming reference")
	}
	page, err := s.WorkerExecutionsReady(t.Context(), in.JobType, "", 1)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("stale source still ready", err)
	}
}
func TestWorkerExecutionStructuralBounds(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "check")
	for name, change := range map[string]func(*WorkerExecutionIntent){
		"payload":           func(i *WorkerExecutionIntent) { i.Payload.Ciphertext = make([]byte, MaxWorkerExecutionPayload+17) },
		"source-kind":       func(i *WorkerExecutionIntent) { i.Source.Kind = "Credential" },
		"empty-guard":       func(i *WorkerExecutionIntent) { i.Guard.Conditions = nil },
		"wrong-kind":        func(i *WorkerExecutionIntent) { i.Category = "other" },
		"zero-generation":   func(i *WorkerExecutionIntent) { i.Generation = 0 },
		"check-action":      func(i *WorkerExecutionIntent) { i.ActionID = "action" },
		"deadline":          func(i *WorkerExecutionIntent) { i.Deadline = i.Scheduled },
		"plaintext-payload": func(i *WorkerExecutionIntent) { i.Payload = secureconfig.Envelope{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in.Clone()
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
