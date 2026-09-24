//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/google/btree"
)

const workerExecutionImageOverhead int64 = 4096

type workerExecutionImage struct {
	Records      map[string]WorkerExecutionRecord `json:"records"`
	EncodedBytes int64                            `json:"encoded_bytes"`
	ready        map[string]*btree.BTreeG[string]
	checks       map[string]string
	actions      map[string]string
	types        map[string]int
}

func (i *workerExecutionImage) clone() *workerExecutionImage {
	if i == nil {
		return nil
	}
	out := &workerExecutionImage{EncodedBytes: i.EncodedBytes, Records: make(map[string]WorkerExecutionRecord, len(i.Records))}
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
func (i *workerExecutionImage) addIndex(record WorkerExecutionRecord) {
	in := record.Intent
	key := jobTypeReferenceKey(in.JobType)
	if i.ready[key] == nil {
		i.ready[key] = btree.NewG[string](32, func(a, b string) bool { return a < b })
	}
	i.ready[key].ReplaceOrInsert(in.ID)
	i.types[workerExecutionTypeKey(in.JobType)]++
	if in.Category == "check" {
		i.checks[workerExecutionCheckKey(in)] = in.ID
	} else {
		i.actions[in.ActionID] = in.ID
	}
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
	for _, r := range i.Records {
		if err := ctx.Err(); err != nil {
			i.ready, i.checks, i.actions, i.types = nil, nil, nil, nil
			return err
		}
		i.addIndex(r)
	}
	return nil
}
func workerExecutionRecordCost(id string, record WorkerExecutionRecord) (int64, error) {
	if _, err := encodedBound(record); err != nil {
		return 0, ErrWorkerExecutionQuota
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return 0, ErrWorkerExecutionInvalid
	}
	key, _ := json.Marshal(id)
	cost := int64(len(key) + len(raw) + 2)
	if record.Lifecycle != nil {
		lifecycle, err := json.Marshal(record.Lifecycle)
		if err != nil || len(lifecycle) > 32<<10 {
			return 0, ErrWorkerExecutionQuota
		}
		// Admission of an offer reserves every later start/reconciliation shape.
		// A full namespace cannot prevent persisting its conservative unknown state.
		cost += int64((32 << 10) - len(lifecycle))
	}
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
	action, ok := m.Actions[in.ActionID]
	if !ok || action.Revision != in.MonitorRevision || action.CatalogUID != in.MonitorUID {
		return ErrWorkerExecutionConflict
	}
	kind := "intervention"
	if in.Category == "notification" {
		kind = "code"
		sources := m.Policy.WorkerNotificationSources[action.Color]
		if action.Endpoint < 0 || action.Endpoint >= len(sources) || sources[action.Endpoint] != (WorkerNotificationSource{ID: in.Source.ID, UID: in.SourceUID, Revision: in.SourceRevision}) {
			return ErrWorkerExecutionConflict
		}
	}
	if action.Kind != kind || !action.NotBefore.Equal(in.Scheduled) {
		return ErrWorkerExecutionConflict
	}
	result := transition(m, Command{Kind: "start", MonitorID: in.MonitorID, Revision: in.MonitorRevision, ActionID: in.ActionID, At: at})
	if !result.Allowed || result.Err != nil {
		return ErrWorkerExecutionConflict
	}
	return nil
}
func (f *machine) applyWorkerExecution(c WorkerExecutionCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if err := f.checkWorkerExecution(c.Intent, at); err != nil {
		return Result{Err: err}
	}
	state := f.image.WorkerExecutions
	digest := workerExecutionDigest(c.Intent)
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
	if err := state.ensureIndexes(context.Background()); err != nil {
		return Result{Err: err}
	}
	if len(state.Records) >= MaxWorkerExecutions || state.types[workerExecutionTypeKey(c.Intent.JobType)] >= MaxWorkerExecutionsPerType {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	if c.Intent.Category == "check" && state.checks[workerExecutionCheckKey(c.Intent)] != "" || c.Intent.Category != "check" && state.actions[c.Intent.ActionID] != "" {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	record := WorkerExecutionRecord{Intent: c.Intent.Clone(), OwnerEpoch: c.OwnerEpoch, CreatedAt: at, CommittedIndex: index, Digest: digest}
	cost, err := workerExecutionRecordCost(c.Intent.ID, record)
	if err != nil {
		return Result{Err: err}
	}
	if cost > MaxWorkerExecutionBytes-state.EncodedBytes {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	state.Records[c.Intent.ID] = record
	state.EncodedBytes += cost
	state.addIndex(record)
	f.image.WorkerExecutions = state
	f.image.Version = max(f.image.Version, WorkerExecutionFormatVersion)
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
	if (i.Version != WorkerExecutionFormatVersion && i.Version != WorkerOfferFormatVersion) || state.Records == nil || len(state.Records) > MaxWorkerExecutions {
		return ErrWorkerExecutionInvalid
	}
	used := workerExecutionImageOverhead
	checks, actions := map[string]bool{}, map[string]bool{}
	leases, grants := map[string]bool{}, map[string]bool{}
	types := map[string]int{}
	for id, r := range state.Records {
		in := r.Intent
		if err := validateWorkerExecutionLifecycle(i, r); err != nil {
			return err
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

		if id != in.ID || in.validate() != nil || !catalogIdentifier(r.OwnerEpoch, 256) || r.CreatedAt.IsZero() || r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9998 || r.CreatedAt.Before(in.Scheduled) || !r.CreatedAt.Before(in.Deadline) || r.CommittedIndex == 0 || r.CommittedIndex > i.Index || r.Digest != workerExecutionDigest(in) {
			return ErrWorkerExecutionInvalid
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
		types[typeKey]++
		if types[typeKey] > MaxWorkerExecutionsPerType {
			return ErrWorkerExecutionQuota
		}
		if in.Category == "check" {
			key := workerExecutionCheckKey(in)
			if checks[key] {
				return ErrWorkerExecutionConflict
			}
			checks[key] = true
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
	if used != state.EncodedBytes {
		return ErrWorkerExecutionInvalid
	}
	return nil
}
