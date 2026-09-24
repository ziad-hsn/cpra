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
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
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
			in, _ := workerExecutionFixture(t, s, "notification")
			saved, err := s.CommitWorkerExecution(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.Get(in.MonitorID)
			if snapshot {
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
	if !bytes.HasPrefix(sink.Bytes(), []byte(workerExecutionSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("private-execution-parameters-canary")) {
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
	for version := 1; version <= WorkerExecutionFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
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
	if s.fsm.image.Version != 20 || s.fsm.image.WorkerExecutions != nil {
		t.Fatal("policy-only format advancement missing")
	}
	data := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != 20 {
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
	if live.NumField() != frozen.NumField() {
		t.Fatal("new intent field requires explicit identity review")
	}
	for n := 0; n < live.NumField(); n++ {
		a, b := live.Field(n), frozen.Field(n)
		if a.Name != b.Name || a.Type != b.Type || a.Tag != b.Tag {
			t.Fatal("intent projection not reviewed", a.Name)
		}
	}
}
