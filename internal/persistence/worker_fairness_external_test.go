//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/google/btree"
	"github.com/google/uuid"
	"github.com/hashicorp/raft"
)

func workerQueueTypes(t *testing.T, s *Store) (WorkerPrincipal, []WorkerSessionCapability, []JobTypeState) {
	t.Helper()
	p := workerPrincipal("queue-worker")
	var types []JobTypeState
	var capabilities []WorkerSessionCapability
	for _, category := range []string{"check", "recovery", "notification"} {
		cmd := jobTypeFixture(t, s, "queue-"+category)
		cmd.Value.Category = category
		job := commitJobType(t, s, cmd)
		types = append(types, job)
		kind := "Monitor"
		if category == "notification" {
			kind = "NotificationEndpoint"
		}
		p.Grants = append(p.Grants, WorkerGrant{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version, Category: category, ResourceKind: kind, ResourceIDs: []string{"*"}})
		capabilities = append(capabilities, WorkerSessionCapability{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: category})
	}
	return p, capabilities, types
}

func BenchmarkWorkerOfferOrder(b *testing.B) {
	for _, count := range []int{100, MaxWorkerExecutions} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			state := &workerExecutionImage{Records: make(map[string]WorkerExecutionRecord), ready: make(map[string]*btree.BTreeG[string])}
			f := &machine{image: image{imageExtensions: imageExtensions{WorkerExecutions: state, JobTypes: &jobTypeImage{Records: make(map[string]JobTypeState)}}}}
			c := WorkerSessionCommand{SelectionVersion: 1}
			for n := range count {
				id, typeID := fmt.Sprintf("execution-%04d", n), fmt.Sprintf("type-%02d", n/MaxWorkerExecutionsPerType)
				version := JobTypeVersion{Record: CatalogRecord{Key: CatalogKey{Kind: "JobType", ID: typeID}, UID: typeID, Revision: "revision"}, Version: "v1", Category: "check"}
				key := jobTypeReferenceKey(catalogJobTypeRef(version))
				if state.ready[key] == nil {
					state.ready[key] = btree.NewG(16, func(a, b string) bool { return a < b })
					f.image.JobTypes.Records[typeID] = JobTypeState{Current: version, Versions: map[string]JobTypeVersion{"v1": version}}
					c.Request.Capabilities = append(c.Request.Capabilities, WorkerSessionCapability{JobTypeID: typeID, Version: "v1", Category: "check"})
				}
				state.ready[key].ReplaceOrInsert(id)
				state.Records[id] = WorkerExecutionRecord{CommittedIndex: uint64((n*1009)%count + 1)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				ordered := f.workerOfferOrder(c)
				if len(ordered) != count || ordered[0].index != 1 || ordered[count-1].index != uint64(count) {
					b.Fatal("queue order lost entries")
				}
			}
		})
	}
}

func workerFairnessFixture(t *testing.T, s *Store) (WorkerAuthority, WorkerPollRequest, []JobTypeState) {
	t.Helper()
	p, capabilities, types := workerQueueTypes(t, s)
	s.administrative = true
	policy := commitWorker(t, s, workerCommand(t, s, "bootstrap", p))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	a, err := s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return a, WorkerPollRequest{ServerID: server.ServerID, WorkerID: p.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: capabilities}, types
}

func TestWorkerOfferSelectionReplay(t *testing.T) {
	for _, selection := range []int{0, 1} {
		t.Run(fmt.Sprint(selection), func(t *testing.T) {
			s := openCatalogMemory(t)
			a, request, types := workerFairnessFixture(t, s)
			oldest := commitWorkerQueueItem(t, s, types[1], "z-recovery")
			later := commitWorkerQueueItem(t, s, types[0], "a-check")
			before := captureSnapshotBytes(t, s.fsm)
			request, err := normalizeWorkerPoll(request)
			if err != nil {
				t.Fatal(err)
			}
			poll := WorkerSessionCommand{SelectionVersion: selection, NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request, ProposedSessionID: uuid.NewString(), ProposedLeaseIDs: []string{uuid.NewString()}}
			command := Command{Kind: "worker_poll", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerSession: &poll}}
			version, want := WorkerOfferFormatVersion, later.Intent.ID
			if selection == 1 {
				version, want = WorkerQueueFormatVersion, oldest.Intent.ID
			}
			raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
			if err != nil {
				t.Fatal(err)
			}
			// Exercise the historical envelope itself, not the current writer's
			// version upgrade and its required maintenance decision.
			result := s.fsm.Apply(&raft.Log{Index: s.fsm.image.Index + 1, Data: raw}).([]Result)[0]
			if result.Err != nil || result.WorkerPoll == nil || len(result.WorkerPoll.Offers) != 1 || result.WorkerPoll.Offers[0].ExecutionID != want {
				t.Fatalf("selection %d: %v, response %+v", selection, result.Err, result.WorkerPoll)
			}
			replayed := &machine{history: s.fsm.history}
			if err := replayed.Restore(io.NopCloser(bytes.NewReader(before))); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = replayed.collections.Close() })
			got := replayed.Apply(&raft.Log{Index: s.fsm.image.Index, Data: raw})
			results, ok := got.([]Result)
			if !ok || len(results) != 1 || !reflect.DeepEqual(results[0], result) {
				t.Fatalf("log replay changed selection %d: %+v", selection, got)
			}
			if !reflect.DeepEqual(replayed.image.WorkerExecutions.clone(), s.fsm.image.WorkerExecutions.clone()) || !reflect.DeepEqual(replayed.image.WorkerSessions, s.fsm.image.WorkerSessions) {
				t.Fatal("log replay changed retained assignments")
			}
			// A new binary replaying the original request must return its original
			// answer even when the command now proposes the newer selection rule.
			duplicate, err := s.CommitWorkerPoll(t.Context(), a, request)
			if err != nil || !reflect.DeepEqual(duplicate, *result.WorkerPoll) {
				t.Fatal("retry replaced a committed assignment", err)
			}
			snapshot := captureSnapshotBytes(t, s.fsm)
			if selection == 1 && !bytes.HasPrefix(snapshot, []byte(actionWorkerSnapshotMagic)) {
				t.Fatal("queue snapshot did not carry its format")
			}
			if err := replayed.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replayed.image.WorkerSessions, s.fsm.image.WorkerSessions) {
				t.Fatal("snapshot changed committed response")
			}
		})
	}
}

func TestWorkerOfferSelectionFormat(t *testing.T) {
	s, a, request, _, _ := workerOfferFixture(t, "check")
	poll := WorkerSessionCommand{SelectionVersion: 1, NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request, ProposedSessionID: uuid.NewString()}
	for range request.Limit {
		poll.ProposedLeaseIDs = append(poll.ProposedLeaseIDs, uuid.NewString())
	}
	command := Command{Kind: "worker_poll", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerSession: &poll}}
	for version := 1; version <= WorkerQueueFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion) {
			t.Fatalf("selection format %d: %v", version, err)
		}
	}
	for _, version := range []int{-1, 2} {
		poll.SelectionVersion = version
		raw, _ := json.Marshal(envelope{Version: WorkerQueueFormatVersion, Commands: []Command{command}})
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatalf("accepted unknown selection version %d", version)
		}
	}
	poll.SelectionVersion = 1
	poll.ProposedLeaseIDs = nil
	if err := validateCommand(command); err == nil {
		t.Fatal("selection without bounded lease reservation accepted")
	}
}

func workerQueueIntent(t *testing.T, s *Store, job JobTypeState, id string) WorkerExecutionIntent {
	t.Helper()
	in := workerExecutionFixtureType(t, s, job)
	in.ID = id
	var err error
	in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-queue-parameters"))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func commitWorkerQueueItem(t *testing.T, s *Store, job JobTypeState, id string) WorkerExecutionRecord {
	t.Helper()
	in := workerQueueIntent(t, s, job, id)
	record, err := s.CommitWorkerExecution(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestWorkerOfferSameCommitUsesStableIdentityOrder(t *testing.T) {
	s := openCatalogMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	var commands []Command
	for i, id := range []string{"z-check", "a-recovery", "m-notification"} {
		in := workerQueueIntent(t, s, types[i], id)
		commands = append(commands, Command{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession}}})
	}
	for _, result := range submit(t, s, commands...) {
		if result.Err != nil || !result.Allowed {
			t.Fatal("batch admission", result.Err)
		}
	}
	request.Capacity, request.Limit = 3, 3
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil || len(response.Offers) != 3 {
		t.Fatal("batch assignment", err, len(response.Offers))
	}
	var index uint64
	for i, id := range []string{"a-recovery", "m-notification", "z-check"} {
		if response.Offers[i].ExecutionID != id {
			t.Fatal("same-commit tie was not stable", response.Offers)
		}
		record, _, err := s.WorkerExecution(t.Context(), id)
		if err != nil || i != 0 && index != record.CommittedIndex {
			t.Fatal("fixture did not share a commit", err)
		}
		index = record.CommittedIndex
	}
}

func TestWorkerOfferByteBudgetPreservesQueueProgress(t *testing.T) {
	s := openCatalogMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	const count = 40
	var queue []string
	for n := range count {
		in := workerQueueIntent(t, s, types[n%len(types)], fmt.Sprintf("payload-%03d", count-n))
		var err error
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), bytes.Repeat([]byte("p"), 112<<10))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		queue = append(queue, in.ID)
	}
	request.Capacity, request.Limit = 100, 100
	for page := 0; len(queue) > 0; page++ {
		response, err := s.CommitWorkerPoll(t.Context(), a, request)
		if err != nil || len(response.Offers) == 0 || len(response.Offers) > len(queue) {
			t.Fatal("bounded assignment stopped making progress", err, len(response.Offers))
		}
		if page == 0 && len(response.Offers) == count {
			t.Fatal("oversized queue returned in one response")
		}
		for i, offer := range response.Offers {
			if offer.ExecutionID != queue[i] {
				t.Fatal("byte budget reordered committed work", offer.ExecutionID, queue[i])
			}
		}
		queue = queue[len(response.Offers):]
		request.SessionID = response.SessionID
		request.PollSequence++
	}
}

func TestWorkerOffersDoNotStarveOlderTypes(t *testing.T) {
	s := openCatalogMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	// Recovery sorts after both other types. It must still be assigned first
	// when its committed intent is oldest.
	queue := []WorkerExecutionRecord{
		commitWorkerQueueItem(t, s, types[1], "z-old-recovery"),
		commitWorkerQueueItem(t, s, types[2], "z-old-notification"),
		commitWorkerQueueItem(t, s, types[0], "z-old-check"),
	}
	for n := range 64 {
		// Keep the alphabetically first type continuously busy. Later low IDs
		// must not overtake already committed work, including that same type.
		queue = append(queue, commitWorkerQueueItem(t, s, types[0], fmt.Sprintf("a-check-%03d", 100-n)))
		if n%3 == 0 {
			queue = append(queue, commitWorkerQueueItem(t, s, types[1+n%2], fmt.Sprintf("a-action-%03d", 100-n)))
		}
		response, err := s.CommitWorkerPoll(t.Context(), a, request)
		if err != nil || len(response.Offers) != 1 {
			t.Fatalf("poll %d: %v, offers %d", n, err, len(response.Offers))
		}
		if got := response.Offers[0].ExecutionID; got != queue[0].Intent.ID {
			t.Fatalf("poll %d assigned %q ahead of older committed %q", n, got, queue[0].Intent.ID)
		}
		repeated, err := s.CommitWorkerPoll(t.Context(), a, request)
		if err != nil || !reflect.DeepEqual(repeated, response) {
			t.Fatalf("poll %d replay changed assignment: %v", n, err)
		}
		queue = queue[1:]
		request.SessionID = response.SessionID
		request.PollSequence++
	}
}
