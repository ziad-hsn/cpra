//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestWorkerExecutionNativeRestartEncryptedIdentity(t *testing.T) {
	for _, mode := range []struct{ snapshot, inline bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("snapshot=%t/inline=%t", mode.snapshot, mode.inline), func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if s != nil {
					_ = s.Close()
				}
			}()
			var in WorkerExecutionIntent
			if mode.inline {
				in, _ = inlineNotificationFixture(t, s)
			} else {
				in, _ = workerExecutionFixture(t, s, "notification")
			}
			saved, err := s.CommitWorkerExecution(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.Get(in.MonitorID)
			if mode.snapshot {
				if err = s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s = nil
			observer := openAuthenticationAdmin(t, config)
			got, ok, err := observer.WorkerExecution(t.Context(), in.ID)
			if err != nil || !ok || !reflect.DeepEqual(got, saved) {
				t.Fatal("stopped replay changed ciphertext identity", err)
			}
			if _, err = observer.CommitWorkerExecution(t.Context(), in); err == nil {
				t.Fatal("stopped administration admitted work")
			}
			if err = observer.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, ok, err = s.WorkerExecution(t.Context(), in.ID)
			if err != nil || !ok || !reflect.DeepEqual(got, saved) {
				t.Fatal("live replay changed ciphertext identity", err)
			}
			after, _ := s.Get(in.MonitorID)
			for id, a := range before.Actions {
				if !reflect.DeepEqual(a, after.Actions[id]) {
					t.Fatal("admission/restart changed queued action")
				}
			}
			if saved.OwnerEpoch == s.executorSession {
				t.Fatal("owner did not rotate")
			}
			page, err := s.WorkerExecutionsReady(t.Context(), in.JobType, "", 1)
			if err != nil || len(page.Items) != 0 {
				t.Fatal("old owner offered", err)
			}
			replay, err := s.CommitWorkerExecution(t.Context(), in)
			if err != nil || !reflect.DeepEqual(replay, saved) {
				t.Fatal("exact retry changed original owner", err)
			}
			page, err = s.WorkerExecutionsReady(t.Context(), in.JobType, "", 1)
			if err != nil || len(page.Items) != 0 {
				t.Fatal("retry rebound old owner", err)
			}
			plaintext, err := catalogSealer(t).Open(t.Context(), in.Binding(s.nodeID), got.Intent.Payload)
			if err != nil || string(plaintext) != "private-execution-parameters-canary" {
				t.Fatal("original binding lost", err)
			}
			clear(plaintext)
		})
	}
}
func TestWorkerExecutionSnapshotIsolationCorruptionAndFormat(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "check")
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	second, _ := workerExecutionFixture(t, s, "notification")
	if _, err = s.CommitWorkerExecution(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	sink := &collectionTestSink{}
	if err = snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(actionWorkerSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("private-execution-parameters-canary")) {
		t.Fatal("format/secrecy")
	}
	saved, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if len(saved.WorkerExecutions.Records) != 1 {
		t.Fatal("snapshot saw later admission")
	}
	for name, change := range map[string]func(*image){
		"lower-format":    func(i *image) { i.Version = WorkerSessionFormatVersion },
		"bytes":           func(i *image) { i.WorkerExecutions.EncodedBytes++ },
		"missing-records": func(i *image) { i.WorkerExecutions.Records = nil },
		"map-identity": func(i *image) {
			r := i.WorkerExecutions.Records[in.ID]
			delete(i.WorkerExecutions.Records, in.ID)
			i.WorkerExecutions.Records["other"] = r
		},
		"digest": func(i *image) {
			r := i.WorkerExecutions.Records[in.ID]
			r.Digest = "wrong"
			i.WorkerExecutions.Records[in.ID] = r
		},
		"ciphertext": func(i *image) {
			r := i.WorkerExecutions.Records[in.ID]
			r.Intent.Payload.Ciphertext[0] ^= 1
			i.WorkerExecutions.Records[in.ID] = r
		},
		"retained-type": func(i *image) {
			state := i.JobTypes.Records[in.JobType.JobTypeID]
			delete(state.Versions, in.JobType.Version)
			i.JobTypes.Records[in.JobType.JobTypeID] = state
		},
		"index": func(i *image) {
			r := i.WorkerExecutions.Records[in.ID]
			r.CommittedIndex = i.Index + 1
			i.WorkerExecutions.Records[in.ID] = r
		},
		"duplicate-generation": func(i *image) {
			r := i.WorkerExecutions.Records[in.ID].Clone()
			r.Intent.ID = "duplicate"
			r.Digest = workerExecutionDigest(r.Intent)
			cost, _ := workerExecutionRecordCost(r.Intent.ID, r)
			i.WorkerExecutions.EncodedBytes += cost
			i.WorkerExecutions.Records[r.Intent.ID] = r
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := saved
			bad.imageExtensions = cloneImageExtensions(saved)
			change(&bad)
			if validateWorkerExecutionImage(bad) == nil {
				t.Fatal("corrupt execution accepted")
			}
		})
	}
	c := Command{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession}}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion || version == WorkerResultFormatVersion || version == WorkerLifecycleFormatVersion || version == WorkerFinalizationFormatVersion || version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion || version == WorkerNotificationConditionFormatVersion || version == WorkerRecoveryRulesFormatVersion || version == WorkerDispatchFormatVersion) {
			t.Fatal("command format", version, err)
		}
	}
	raw, _ := json.Marshal(c)
	raw = bytes.Replace(raw, []byte(`"intent":{`), []byte(`"intent":{"future_field":"unexpected",`), 1)
	if _, err = decodeEnvelope(append(append([]byte(`{"version":20,"commands":[`), raw...), ']', '}')); err == nil {
		t.Fatal("unknown future intent interpreted")
	}
}
func TestWorkerExecutionPolicyStrictFormatAndBounds(t *testing.T) {
	// Explicit empty/null extension presence still requires the new outer format.
	for _, empty := range []string{"null", "{}"} {
		monitor := testMonitor()
		command := Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: time.Now().UTC()}
		raw, _ := json.Marshal(command)
		raw = bytes.Replace(raw, []byte(`"policy":{`), []byte(`"policy":{"worker_notification_sources":`+empty+`,`), 1)
		for _, version := range []int{1, 18, 19, 20} {
			body := fmt.Sprintf(`{"version":%d,"commands":[%s]}`, version, raw)
			_, err := decodeEnvelope([]byte(body))
			if (err == nil) != (version == 20) {
				t.Fatal("policy presence gate", version, empty, err)
			}
		}
	}
	p := testMonitor().Policy
	p.Endpoints = map[string]int{"gray": 10000}
	p.WorkerNotificationSources = map[string][]WorkerNotificationSource{"gray": make([]WorkerNotificationSource, 10000)}
	p.WorkerNotificationSources["gray"][9999] = WorkerNotificationSource{ID: "external", UID: "uid", Revision: "revision"}
	if err := p.Validate(); err != nil {
		t.Fatal("existing graph-scale mixed sources rejected", err)
	}
	p.WorkerNotificationSources["gray"] = append(p.WorkerNotificationSources["gray"], WorkerNotificationSource{})
	p.Endpoints["gray"]++
	if p.Validate() == nil {
		t.Fatal("source quota ignored")
	}
	p.Endpoints = map[string]int{"magenta": 1}
	p.WorkerNotificationSources = map[string][]WorkerNotificationSource{"magenta": {{}}}
	if p.Validate() == nil {
		t.Fatal("unsupported color")
	}
	s := openCatalogMemory(t)
	_, _ = workerExecutionFixture(t, s, "notification")
	if s.fsm.image.Version != ActionWorkerFormatVersion || s.fsm.image.WorkerExecutions != nil {
		t.Fatal("policy-only format advancement missing")
	}
	data := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != ActionWorkerFormatVersion {
		t.Fatal("policy snapshot downgraded")
	}
}
func TestWorkerExecutionPerTypeAndEncodedQuotas(t *testing.T) {
	s := openCatalogMemory(t)
	in, job := workerExecutionFixture(t, s, "check")
	for n := 0; n < MaxWorkerExecutionsPerType; n++ {
		if n > 0 {
			in = workerExecutionFixtureType(t, s, job)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(n, err)
		}
	}
	extra := workerExecutionFixtureType(t, s, job)
	if _, err := s.CommitWorkerExecution(t.Context(), extra); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("per-type quota not enforced", err)
	}
	if len(s.fsm.image.WorkerExecutions.Records) != MaxWorkerExecutionsPerType {
		t.Fatal("rejected record installed")
	}
	// Encoded accounting includes escaped map IDs and JSON/base64 payload overhead.
	record := s.fsm.image.WorkerExecutions.Records[in.ID].Clone()
	for _, id := range []string{"normal", `quote"and\\slash`, "unicode-🙂"} {
		record.Intent.ID = id
		record.Digest = workerExecutionDigest(record.Intent)
		cost, err := workerExecutionRecordCost(id, record)
		raw, _ := json.Marshal(record)
		key, _ := json.Marshal(id)
		if err != nil || cost != int64(len(raw)+len(key)+2) {
			t.Fatal("encoded charge", cost, err)
		}
	}
	state := s.fsm.image.WorkerExecutions
	raw, _ := json.Marshal(state)
	if int64(len(raw)) > state.EncodedBytes {
		t.Fatal("namespace undercharged")
	}
	// A full byte reservation stops admission before mutating the canonical map.
	another, _ := workerExecutionFixture(t, s, "check")
	state.EncodedBytes = MaxWorkerExecutionBytes
	if _, err := s.CommitWorkerExecution(t.Context(), another); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("encoded byte quota not enforced", err)
	}
	if _, ok := state.Records[another.ID]; ok {
		t.Fatal("quota failure wrote record")
	}
	// Exercise the independent record-count guard with a synthetic occupied map;
	// the earlier loop proves actual admitted records and the per-type bound.
	state.EncodedBytes = workerExecutionImageOverhead
	for n := len(state.Records); n < MaxWorkerExecutions; n++ {
		state.Records[fmt.Sprintf("occupied-%d", n)] = WorkerExecutionRecord{}
	}
	if _, err := s.CommitWorkerExecution(t.Context(), another); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("record-count quota not enforced", err)
	}
}
func TestWorkerExecutionFrozenDigestProjection(t *testing.T) {
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	in := WorkerExecutionIntent{ID: "execution", Revision: "intent-v1", MonitorID: "monitor", MonitorUID: "monitor-uid", MonitorRevision: "monitor-revision", ControlRevision: "control", Category: "check", Generation: 4, Source: CatalogKey{Kind: "Monitor", ID: "monitor"}, SourceUID: "monitor-uid", SourceRevision: "source-revision", JobType: JobTypeReference{JobTypeID: "job", JobTypeUID: "job-uid", Version: "v1", Revision: "job-revision", Category: "check"}, Guard: CatalogGuard{Conditions: []CatalogCondition{{Key: CatalogKey{Kind: "Monitor", ID: "monitor"}, UID: "monitor-uid", Revision: "source-revision"}}}, Scheduled: at, Deadline: at.Add(time.Minute), Payload: secureconfig.Envelope{}}
	const expected = "0676fd77feef1c221ad6cb786d679d65f5c86e10b86628d82e5f60707ce642d0"
	if got := workerExecutionDigest(in); got != expected {
		t.Fatalf("frozen v1 digest changed: %s", got)
	}
	live, frozen := reflect.TypeFor[WorkerExecutionIntent](), reflect.TypeFor[workerExecutionIntentDigestV1]()
	if live.NumField() != frozen.NumField()+3 ||
		live.Field(0).Name != "ExecutionBudget" || live.Field(0).Type != reflect.TypeFor[time.Duration]() || live.Field(0).Tag != `json:"execution_budget,omitempty"` ||
		live.Field(1).Name != "Notification" || live.Field(1).Type != reflect.TypeFor[*WorkerNotification]() || live.Field(1).Tag != `json:"notification,omitempty"` ||
		live.Field(2).Name != "Schedule" || live.Field(2).Type != reflect.TypeFor[*WorkerScheduleTicket]() || live.Field(2).Tag != `json:"schedule,omitempty"` {
		t.Fatal("new intent field requires explicit identity review")
	}
	for n := 0; n < frozen.NumField(); n++ {
		a, b := live.Field(n+3), frozen.Field(n)
		if a.Name != b.Name || a.Type != b.Type || a.Tag != b.Tag {
			t.Fatal("intent projection not reviewed", a.Name)
		}
	}
	live, frozen = reflect.TypeFor[WorkerScheduleTicket](), reflect.TypeFor[workerScheduleTicketDigestV2]()
	if live.NumField() != frozen.NumField() {
		t.Fatal("new schedule field requires explicit identity review")
	}
	for n := 0; n < live.NumField(); n++ {
		a, b := live.Field(n), frozen.Field(n)
		if a.Name != b.Name || a.Type != b.Type || a.Tag != b.Tag {
			t.Fatal("schedule projection not reviewed", a.Name)
		}
	}
	in.Schedule = &WorkerScheduleTicket{OwnerEpoch: "owner", Epoch: workerSLOEpoch(at), Sequence: 4}
	v2 := workerExecutionDigest(in)
	const expectedV2 = "e0608792a7b79c0458c2c2827d7a53fe4041f60dc6bc2c7207e768cf3da32643"
	if v2 != expectedV2 || v2 != workerExecutionDigest(in.Clone()) {
		t.Fatal("frozen v2 schedule digest changed", v2)
	}
	in.Schedule.Sequence++
	if workerExecutionDigest(in) == v2 {
		t.Fatal("schedule sequence was omitted from identity")
	}
	in.Schedule = nil
	if workerExecutionDigest(in) != expected {
		t.Fatal("v1 identity was not preserved")
	}
	in.Category = "notification"
	in.JobType.Category = "notification"
	in.Notification = &WorkerNotification{Format: 1, Color: "red", DeliveryID: "delivery", EndpointID: "endpoint", IncidentID: "incident", CorrelationID: "correlation", At: at}
	const expectedV3 = "61e28d06395777c8443aa08af7370b8c13afb9af6d480a54ecc1b59573a9a32a"
	if got := workerExecutionDigest(in); got != expectedV3 {
		t.Fatal("frozen v3 notification digest changed", got)
	}
	before := in.Clone()
	in.ExecutionBudget = 45 * time.Second
	in.Deadline = time.Time{}
	in.Notification.Format = 2
	in.Notification.Color = "green"
	in.Notification.Resolution = &ResolutionNotice{ConditionID: "condition", RecoveredAt: at, Health: "unhealthy"}
	const expectedV4 = "ee009a615b791cb6ee5a7597910e3ddec2ca0479a5f4790e40b17d6dbd31d627"
	if got := workerExecutionDigest(in); got != expectedV4 || got != workerExecutionDigest(in.Clone()) {
		t.Fatal("frozen v4 budget/resolution digest changed", got)
	}
	for _, change := range []func(*WorkerExecutionIntent){
		func(i *WorkerExecutionIntent) { i.ExecutionBudget++ },
		func(i *WorkerExecutionIntent) { i.Notification.Resolution.ConditionID = "other-condition" },
		func(i *WorkerExecutionIntent) { i.Notification.Resolution.RecoveredAt = at.Add(time.Second) },
		func(i *WorkerExecutionIntent) { i.Notification.Resolution.Health = "healthy" },
	} {
		changed := in.Clone()
		change(&changed)
		if workerExecutionDigest(changed) == expectedV4 {
			t.Fatal("new execution semantics omitted from v4 identity")
		}
	}
	if workerExecutionDigest(before) != expectedV3 {
		t.Fatal("v4 mutation changed the old notification identity")
	}
}
