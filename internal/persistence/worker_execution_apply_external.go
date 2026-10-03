//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/btree"
)

const workerExecutionImageOverhead int64 = 4096

type workerExecutionImage struct {
	Records        map[string]WorkerExecutionRecord `json:"records"`
	EncodedBytes   int64                            `json:"encoded_bytes"`
	EvidenceBytes  int64                            `json:"evidence_bytes,omitempty"`
	ready          map[string]*btree.BTreeG[string]
	checks         map[string]string
	actions        map[string]string
	types          map[string]int
	activeTypes    map[string]int
	retainedChecks map[string]int
	schedules      map[WorkerScheduleTicket]string
}

func (i *workerExecutionImage) clone() *workerExecutionImage {
	if i == nil {
		return nil
	}
	out := &workerExecutionImage{EncodedBytes: i.EncodedBytes, EvidenceBytes: i.EvidenceBytes, Records: make(map[string]WorkerExecutionRecord, len(i.Records))}
	for id, r := range i.Records {
		out.Records[id] = r.Clone()
	}
	return out
}
func workerExecutionTypeKey(ref JobTypeReference) string {
	return ref.JobTypeID + "\x00" + ref.JobTypeUID
}
func workerExecutionCheckKey(intent WorkerExecutionIntent) string {
	return intent.MonitorID + "\x00" + intent.MonitorUID
}

// Held unknown actions still occupy capacity; a confirmed terminal check does not.
func workerExecutionActive(r WorkerExecutionRecord) bool {
	receipt, terminal := r.TerminalReceipt()
	return !terminal || receipt.Disposition == "unknown"
}
func (i *workerExecutionImage) addIndex(record WorkerExecutionRecord) {
	in := record.Intent
	key := jobTypeReferenceKey(in.JobType)
	if _, terminal := record.TerminalReceipt(); !terminal && record.Lifecycle == nil {
		if i.ready[key] == nil {
			i.ready[key] = btree.NewG[string](32, func(a, b string) bool { return a < b })
		}
		i.ready[key].ReplaceOrInsert(in.ID)
	}
	i.types[workerExecutionTypeKey(in.JobType)]++
	if workerExecutionActive(record) {
		i.activeTypes[workerExecutionTypeKey(in.JobType)]++
	}
	if in.Category == "check" {
		if in.Schedule != nil {
			i.schedules[*in.Schedule] = in.ID
		}
		i.retainedChecks[workerExecutionCheckKey(in)]++
		if workerExecutionActive(record) {
			i.checks[workerExecutionCheckKey(in)] = in.ID
		}
	} else {
		i.actions[in.ActionID] = in.ID
	}
}
func (i *workerExecutionImage) removeIndex(record WorkerExecutionRecord) {
	in := record.Intent
	key := jobTypeReferenceKey(in.JobType)
	if tree := i.ready[key]; tree != nil {
		tree.Delete(in.ID)
		if tree.Len() == 0 {
			delete(i.ready, key)
		}
	}
	typeKey := workerExecutionTypeKey(in.JobType)
	i.types[typeKey]--
	if i.types[typeKey] == 0 {
		delete(i.types, typeKey)
	}
	if workerExecutionActive(record) {
		i.activeTypes[typeKey]--
		if i.activeTypes[typeKey] == 0 {
			delete(i.activeTypes, typeKey)
		}
	}
	if in.Category == "check" {
		if in.Schedule != nil && i.schedules[*in.Schedule] == in.ID {
			delete(i.schedules, *in.Schedule)
		}
		key := workerExecutionCheckKey(in)
		i.retainedChecks[key]--
		if i.retainedChecks[key] == 0 {
			delete(i.retainedChecks, key)
		}
		if i.checks[key] == in.ID {
			delete(i.checks, key)
		}
	} else if i.actions[in.ActionID] == in.ID {
		delete(i.actions, in.ActionID)
	}
}
func (i *workerExecutionImage) replaceRecord(record WorkerExecutionRecord) {
	if i.ready != nil {
		i.removeIndex(i.Records[record.Intent.ID])
		i.addIndex(record)
	}
	i.Records[record.Intent.ID] = record
}
func (i *workerExecutionImage) ensureIndexes(ctx context.Context) error {
	if i.ready != nil {
		return ctx.Err()
	}
	if len(i.Records) > MaxWorkerExecutions {
		return ErrWorkerExecutionQuota
	}
	i.ready = make(map[string]*btree.BTreeG[string])
	i.checks = make(map[string]string)
	i.actions = make(map[string]string)
	i.types = make(map[string]int)
	i.activeTypes = make(map[string]int)
	i.retainedChecks = make(map[string]int)
	i.schedules = make(map[WorkerScheduleTicket]string)
	for _, r := range i.Records {
		if err := ctx.Err(); err != nil {
			i.ready, i.checks, i.actions, i.types, i.activeTypes, i.retainedChecks = nil, nil, nil, nil, nil, nil
			i.schedules = nil
			return err
		}
		i.addIndex(r)
	}
	return nil
}
func workerExecutionRecordCost(id string, record WorkerExecutionRecord) (int64, error) {
	// Outcomes have one independently bounded slot per admitted execution. Keep
	// the existing intent/lifecycle counter stable across outcome publication.
	record.Observation = nil // Confirmation metadata uses the reserved result slot.
	record.Result = nil
	record.Evidence = nil
	record.Finalization = nil // Uses the mutually exclusive reserved result slot.
	if _, err := encodedBound(record); err != nil {
		return 0, ErrWorkerExecutionQuota
	}
	var reserved int64
	if record.Lifecycle != nil || len(record.RetiredOffers) != 0 {
		lifecycle, err := json.Marshal(struct {
			Current *WorkerExecutionLifecycle
			Retired []WorkerExecutionLifecycle
		}{record.Lifecycle, record.RetiredOffers})
		if err != nil || len(lifecycle) > 32<<10 {
			return 0, ErrWorkerExecutionQuota
		}
		// The original offer reservation covers later leases and their fences.
		// Its exact historical charge includes the lifecycle field's JSON key.
		reserved = (32 << 10) + int64(len(`,"lifecycle":`))
		record.Lifecycle, record.RetiredOffers = nil, nil
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return 0, ErrWorkerExecutionInvalid
	}
	key, _ := json.Marshal(id)
	cost := int64(len(key)+len(raw)+2) + reserved
	if cost > MaxWorkerExecutionPageBytes {
		return 0, ErrWorkerExecutionQuota
	}
	return cost, nil
}

// checkWorkerExecution reuses the committed local admission rules, but it does
// not perform their transition. The same guards must be checked again at Start.
func (f *machine) checkWorkerExecutionPins(in WorkerExecutionIntent, at time.Time) error {
	if !at.Before(in.Deadline) {
		return ErrWorkerExecutionExpired
	}
	if at.Before(in.Scheduled) {
		return ErrWorkerExecutionConflict
	}
	return f.checkWorkerExecutionAuthority(in, at)
}

func (f *machine) checkWorkerExecutionAuthority(in WorkerExecutionIntent, at time.Time) error {
	if err := f.checkCatalogGuard(in.MonitorID, &in.Guard); err != nil {
		return err
	}
	m, ok := f.image.Monitors[in.MonitorID]
	if !ok || m.Removed || m.CatalogUID != in.MonitorUID || m.Revision != in.MonitorRevision || m.ControlRevision != in.ControlRevision || m.DependencyRevision != in.Guard.revision() || !m.Policy.Enabled || m.snoozed(at) {
		return ErrWorkerExecutionConflict
	}
	source, ok := f.image.Catalog[in.Source.indexKey()]
	if !ok || source.Removed || source.UID != in.SourceUID || source.Revision != in.SourceRevision {
		return ErrCatalogDependency
	}
	found := false
	for _, ref := range source.JobTypeReferences {
		if ref == in.JobType {
			found = true
			break
		}
	}
	if !found || validateCatalogJobTypeReferences(f.image, source, false) != nil {
		return ErrCatalogDependency
	}
	return nil
}
func (f *machine) checkWorkerExecution(in WorkerExecutionIntent, at time.Time) error {
	if err := f.checkWorkerExecutionPins(in, at); err != nil {
		return err
	}
	m := f.image.Monitors[in.MonitorID]
	if in.Category == "check" {
		if m.Generation == math.MaxUint64 || in.Generation != m.Generation+1 {
			return ErrWorkerExecutionConflict
		}
		return nil
	}
	if err := f.checkWorkerActionIdentity(in); err != nil {
		return err
	}
	result := transition(m, Command{Kind: "start", MonitorID: in.MonitorID, Revision: in.MonitorRevision, ActionID: in.ActionID, At: at})
	if !result.Allowed || result.Err != nil {
		return ErrWorkerExecutionConflict
	}
	return nil
}

func (f *machine) checkWorkerActionIdentity(in WorkerExecutionIntent) error {
	m := f.image.Monitors[in.MonitorID]
	action, ok := m.Actions[in.ActionID]
	if !ok || action.Revision != in.MonitorRevision || action.CatalogUID != in.MonitorUID {
		return ErrWorkerExecutionConflict
	}
	kind := "intervention"
	if in.Category == "notification" {
		kind = "code"
		sources := m.Policy.WorkerNotificationSources[action.Color]
		if action.Endpoint < 0 || action.Endpoint >= len(sources) || !sources[action.Endpoint].matches(in) {
			return ErrWorkerExecutionConflict
		}
	}
	if action.Kind != kind || !action.NotBefore.Equal(in.Scheduled) {
		return ErrWorkerExecutionConflict
	}
	return nil
}
func (f *machine) applyWorkerExecution(c WorkerExecutionCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	state := f.image.WorkerExecutions
	digest := workerExecutionDigest(c.Intent)
	if !c.CheckReserved {
		if err := f.checkWorkerExecution(c.Intent, at); err != nil {
			return Result{Err: err}
		}
	}
	if state != nil {
		if old, exists := state.Records[c.Intent.ID]; exists {
			if old.Digest != digest {
				return Result{Err: ErrWorkerExecutionConflict}
			}
			out := old.Clone()
			return Result{Allowed: true, resultExtensions: resultExtensions{WorkerExecution: &out}}
		}
	} else {
		state = &workerExecutionImage{Records: make(map[string]WorkerExecutionRecord), EncodedBytes: workerExecutionImageOverhead}
	}
	var reservationCost int64
	if c.CheckReserved {
		if f.workerCheckExpired(c.Intent.ID) {
			return Result{Err: ErrWorkerCheckExpired}
		}
		if !f.image.WorkerChecks.issued(c.Intent.ID) {
			return Result{Err: ErrWorkerExecutionConflict}
		}
		r, ok := f.image.WorkerChecks.Reservations[c.Intent.ID]
		if !ok || r.OwnerEpoch != c.OwnerEpoch || at.Before(r.CreatedAt) || !at.Before(r.CreatedAt.Add(WorkerCheckReservationLifetime)) {
			return Result{Err: ErrWorkerExecutionConflict}
		}
		var err error
		reservationCost, err = workerCheckReservationCost(c.Intent.ID, r)
		if err != nil {
			return Result{Err: err}
		}
		if err := f.checkWorkerExecution(c.Intent, at); err != nil {
			return Result{Err: err}
		}
	}
	if err := state.ensureIndexes(context.Background()); err != nil {
		return Result{Err: err}
	}
	if c.Intent.Schedule != nil && state.schedules[*c.Intent.Schedule] != "" {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	typeCount := state.types[workerExecutionTypeKey(c.Intent.JobType)]
	if c.SchedulingVersion == 1 {
		typeCount = state.activeTypes[workerExecutionTypeKey(c.Intent.JobType)]
	}
	if len(state.Records) >= MaxWorkerExecutions || typeCount >= MaxWorkerExecutionsPerType {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	checkBusy := state.checks[workerExecutionCheckKey(c.Intent)] != ""
	if c.SchedulingVersion == 0 {
		checkBusy = state.retainedChecks[workerExecutionCheckKey(c.Intent)] != 0
	}
	if c.Intent.Category == "check" && checkBusy || c.Intent.Category != "check" && state.actions[c.Intent.ActionID] != "" {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	record := WorkerExecutionRecord{ResultTarget: c.ResultTarget, Intent: c.Intent.Clone(), OwnerEpoch: c.OwnerEpoch, CreatedAt: at, CommittedIndex: index, Digest: digest}
	var err error
	record.SLOEpoch, err = f.workerWindowEpoch(record.ResultTarget)
	if err != nil {
		return Result{Err: err}
	}
	cost, err := workerExecutionRecordCost(c.Intent.ID, record)
	if err != nil {
		return Result{Err: err}
	}
	if cost > MaxWorkerExecutionBytes-state.EncodedBytes {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	if err := f.admitWorkerSLO(&record, at); err != nil {
		return Result{Err: err}
	}
	state.Records[c.Intent.ID] = record
	state.EncodedBytes += cost
	state.addIndex(record)
	f.image.WorkerExecutions = state
	if c.CheckReserved {
		delete(f.image.WorkerChecks.Reservations, c.Intent.ID)
		f.image.WorkerChecks.EncodedBytes -= reservationCost
		f.image.Version = max(f.image.Version, WorkerCheckRetentionFormatVersion)
	}
	f.image.Version = max(f.image.Version, c.Intent.minimumFormat())
	if c.SchedulingVersion == 1 {
		f.image.Version = max(f.image.Version, WorkerSchedulingFormatVersion)
	}
	out := record.Clone()
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerExecution: &out}}
}
func (f *machine) jobTypeExecutionReferenced(id, uid string) bool {
	state := f.image.WorkerExecutions
	if state == nil {
		return false
	}
	if state.ensureIndexes(context.Background()) != nil {
		return true
	}
	return state.types[id+"\x00"+uid] != 0
}
func validateWorkerExecutionImage(i image) error {
	for _, m := range i.Monitors {
		if i.Version < policyMinimumFormat(m.Policy) || validatePolicyExtensions(m.Policy) != nil {
			return ErrWorkerExecutionInvalid
		}
	}
	state := i.WorkerExecutions
	if state == nil {
		return nil
	}
	if (i.Version < WorkerExecutionFormatVersion || !externalStorageFormat(i.Version)) || state.Records == nil || len(state.Records) > MaxWorkerExecutions {
		return ErrWorkerExecutionInvalid
	}
	used := workerExecutionImageOverhead
	var evidenceBytes int64
	checks, generations, actions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	leases, grants := map[string]bool{}, map[string]bool{}
	types := map[string]int{}
	schedules := map[WorkerScheduleTicket]bool{}
	for id, r := range state.Records {
		in := r.Intent
		if strings.HasPrefix(id, "wc.") && (i.Version < WorkerCheckRetentionFormatVersion || in.Category != "check" || !i.WorkerChecks.issued(id)) {
			return ErrWorkerExecutionInvalid
		}
		if err := validateWorkerRetiredOffers(i, r); err != nil {
			return err
		}
		for _, retired := range r.RetiredOffers {
			if leases[retired.LeaseID] {
				return ErrWorkerExecutionInvalid
			}
			leases[retired.LeaseID] = true
		}
		if err := validateWorkerObservation(i, r); err != nil {
			return err
		}
		if err := validateWorkerFinalization(i, r); err != nil {
			return err
		}
		if err := validateWorkerExecutionLifecycle(i, r); err != nil {
			return err
		}
		if err := validateWorkerResultRecord(i, r); err != nil {
			return err
		}
		evidenceCost, err := validateWorkerEvidence(i, r)
		if err != nil {
			return err
		}
		evidenceBytes += evidenceCost
		if evidenceBytes > MaxWorkerEvidenceBytes {
			return ErrWorkerExecutionQuota
		}
		if l := r.Lifecycle; l != nil {
			if leases[l.LeaseID] || l.GrantID != "" && grants[l.GrantID] {
				return ErrWorkerExecutionInvalid
			}
			leases[l.LeaseID] = true
			if l.GrantID != "" {
				grants[l.GrantID] = true
			}
		}

		if i.Version < in.minimumFormat() || id != in.ID || in.validate() != nil || !catalogIdentifier(r.OwnerEpoch, 256) || r.CreatedAt.IsZero() || r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9998 || r.CreatedAt.Before(in.Scheduled) || !r.CreatedAt.Before(in.Deadline) || r.CommittedIndex == 0 || r.CommittedIndex > i.Index || r.Digest != workerExecutionDigest(in) {
			return ErrWorkerExecutionInvalid
		}
		if ticket := in.Schedule; ticket != nil {
			if r.ResultTarget <= 0 || r.SLOEpoch == 0 || i.WorkerSLO == nil {
				return ErrWorkerExecutionInvalid
			}
			if window := i.WorkerSLO; window.Epoch == r.SLOEpoch {
				if window.ScheduleOwner == "" {
					return ErrWorkerExecutionInvalid
				}
				bucket := window.Buckets[workerSLOSlot(ticket.Epoch)]
				if window.ScheduleOwner == ticket.OwnerEpoch && bucket.Epoch == ticket.Epoch && (bucket.Scheduled < ticket.Sequence || bucket.Admitted == 0) {
					return ErrWorkerExecutionInvalid
				}
			}
		}
		if i.JobTypes == nil {
			return ErrCatalogDependency
		}
		typ, ok := i.JobTypes.Records[in.JobType.JobTypeID]
		if !ok {
			return ErrCatalogDependency
		}
		version, ok := typ.Versions[in.JobType.Version]
		if !ok || version.Record.UID != in.JobType.JobTypeUID || version.Record.Revision != in.JobType.Revision || version.Category != in.Category {
			return ErrCatalogDependency
		}
		typeKey := workerExecutionTypeKey(in.JobType)
		if i.Version < WorkerSchedulingFormatVersion || workerExecutionActive(r) {
			types[typeKey]++
		}
		if types[typeKey] > MaxWorkerExecutionsPerType {
			return ErrWorkerExecutionQuota
		}
		if in.Category == "check" {
			if in.Schedule != nil {
				if schedules[*in.Schedule] {
					return ErrWorkerExecutionConflict
				}
				schedules[*in.Schedule] = true
			}
			key := workerExecutionCheckKey(in)
			generation := fmt.Sprintf("%s\x00%s\x00%d", key, in.MonitorRevision, in.Generation)
			if generations[generation] {
				return ErrWorkerExecutionConflict
			}
			generations[generation] = true
			if i.Version < WorkerSchedulingFormatVersion || workerExecutionActive(r) {
				if checks[key] {
					return ErrWorkerExecutionConflict
				}
				checks[key] = true
			}
		} else {
			if actions[in.ActionID] {
				return ErrWorkerExecutionConflict
			}
			actions[in.ActionID] = true
		}
		cost, err := workerExecutionRecordCost(id, r)
		if err != nil || cost > MaxWorkerExecutionBytes-used {
			return ErrWorkerExecutionQuota
		}
		used += cost
	}
	if used != state.EncodedBytes || evidenceBytes != state.EvidenceBytes {
		return ErrWorkerExecutionInvalid
	}
	return nil
}
