//go:build externaljobs

package persistence

import (
	"encoding/json"
	"math"
	"time"
)

func workerSessionRecordCost(key string, r workerSessionRecord) (int64, error) {
	if len(r.Request.Capabilities) > MaxWorkerSessionCapabilities || len(r.Response.Offers) > 100 {
		return 0, ErrWorkerSessionQuota
	}
	k, _ := json.Marshal(key)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 1<<20 || len(r.Response.Offers) == 0 && len(raw) > 64<<10 {
		return 0, ErrWorkerSessionQuota
	}
	return int64(len(k) + len(raw) + 2), nil
}

// Accounting reserves 4KiB for escaped namespace metadata and a comma per map entry;
// it bounds canonical encoded bytes without re-encoding the complete namespace.
const workerSessionImageOverhead int64 = 4 << 10

// inspectWorkerSession is shared by waiting and committed polling. It borrows
// retained records under the FSM lock and never changes a session or its expiry.
func (f *machine) inspectWorkerSession(c WorkerSessionCommand, at time.Time) (workerSessionRecord, bool, bool, error) {
	if err := f.checkWorkerPollAuthority(c.Authority, c.Request, at); err != nil {
		return workerSessionRecord{}, false, false, err
	}
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return workerSessionRecord{}, false, false, ErrWorkerSessionExpired
	}
	request := c.Request
	old := f.image.WorkerSessions
	if old != nil && (old.NodeID != c.NodeID || old.PolicyEpoch != c.Authority.Epoch) {
		return workerSessionRecord{}, false, false, ErrWorkerSessionIdentity
	}
	var previous workerSessionRecord
	exists := false
	if old != nil {
		previous, exists = old.Sessions[c.Authority.WorkerUID]
	}
	digest := workerPollDigest(request)
	opening := request.SessionID == ""
	if !opening && (!exists || request.SessionID != previous.Response.SessionID) {
		return workerSessionRecord{}, false, false, ErrWorkerSessionExpired
	}
	if exists {
		live := previous.ReplyExpiredAt == nil && previous.OwnerEpoch == c.OwnerEpoch && at.Before(previous.Response.SessionExpiresAt)
		sameNonce := previous.Response.ClientSessionID == request.ClientSessionID
		if sameNonce && !live {
			return workerSessionRecord{}, false, false, ErrWorkerSessionExpired
		}
		if opening && !sameNonce {
			if live {
				return workerSessionRecord{}, false, false, ErrWorkerSessionConflict
			}
		} else {
			if !sameNonce {
				return workerSessionRecord{}, false, false, ErrWorkerSessionConflict
			}
			if !opening && request.SessionID != previous.Response.SessionID {
				return workerSessionRecord{}, false, false, ErrWorkerSessionExpired
			}
			if request.PollSequence == previous.Response.PollSequence {
				if digest != previous.RequestDigest {
					return workerSessionRecord{}, false, false, ErrWorkerSessionConflict
				}
				return previous, exists, true, nil
			}
			if opening || previous.Response.PollSequence == math.MaxInt64 || request.PollSequence != previous.Response.PollSequence+1 {
				return workerSessionRecord{}, false, false, ErrWorkerSessionSequence
			}
			if at.Before(previous.UpdatedAt) {
				return workerSessionRecord{}, false, false, ErrWorkerSessionConflict
			}
			if !sameWorkerCapabilities(request.Capabilities, previous.Request.Capabilities) {
				return workerSessionRecord{}, false, false, ErrWorkerSessionConflict
			}
		}
	} else if !opening {
		return workerSessionRecord{}, false, false, ErrWorkerSessionExpired
	}
	return previous, exists, false, nil
}

func (f *machine) applyWorkerPoll(c WorkerSessionCommand, at time.Time, versions ...int) Result {
	version := commandExtensionMinimumFormat(Command{commandExtensions: commandExtensions{WorkerSession: &c}})
	if len(versions) > 0 {
		version = versions[0]
	}
	if lifecycleStorageFormat(version) && c.SelectionVersion != 2 {
		return Result{Err: ErrWorkerSessionInvalid}
	}
	previous, exists, replay, err := f.inspectWorkerSession(c, at)
	if err != nil {
		return Result{Err: err}
	}
	if replay {
		response := previous.Response.clone()
		return Result{Allowed: true, resultExtensions: resultExtensions{WorkerPoll: &response}}
	}
	request, old := c.Request, f.image.WorkerSessions
	opening := request.SessionID == ""
	digest := workerPollDigest(request)
	if opening && old != nil {
		for _, existing := range old.Sessions {
			if existing.Response.SessionID == c.ProposedSessionID {
				return Result{Err: ErrWorkerSessionConflict}
			}
		}
	}
	response := WorkerPollResponse{ServerID: request.ServerID, WorkerUID: c.Authority.WorkerUID, ClientSessionID: request.ClientSessionID, SessionID: c.ProposedSessionID, PollSequence: request.PollSequence, SessionExpiresAt: at.Add(WorkerSessionLifetime)}
	record := workerSessionRecord{WorkerID: request.WorkerID, WorkerUID: c.Authority.WorkerUID, OwnerEpoch: c.OwnerEpoch, OpenedAt: at, UpdatedAt: at, Request: request.clone(), RequestDigest: digest, Response: response}
	if !opening {
		record.OpenedAt = previous.OpenedAt
		record.Response.SessionID = previous.Response.SessionID
	}
	updates, offers, delta, err := f.prepareWorkerOffers(c, record.Response, at)
	if err != nil {
		return Result{Err: err}
	}
	record.Response.Offers = offers
	cost, err := workerSessionRecordCost(record.WorkerUID, record)
	if err != nil {
		return Result{Err: err}
	}
	used := workerSessionImageOverhead
	if old != nil {
		used = old.EncodedBytes
		if !exists && len(old.Sessions) >= MaxWorkerSessions {
			return Result{Err: ErrWorkerSessionQuota}
		}
		if exists {
			oldCost, err := workerSessionRecordCost(previous.WorkerUID, previous)
			if err != nil {
				return Result{Err: ErrWorkerSessionUnavailable}
			}
			used -= oldCost
		}
	}
	if cost > MaxWorkerSessionBytes-used {
		return Result{Err: ErrWorkerSessionQuota}
	}
	if old == nil {
		old = &workerSessionImage{Version: 1, NodeID: c.NodeID, PolicyEpoch: c.Authority.Epoch, Sessions: make(map[string]workerSessionRecord)}
		f.image.WorkerSessions = old
	}
	for _, r := range updates {
		f.image.WorkerExecutions.replaceRecord(r)
	}
	if len(updates) > 0 {
		f.image.WorkerExecutions.EncodedBytes += delta
	}
	old.Sessions[record.WorkerUID] = record
	old.EncodedBytes = used + cost
	var events []Event
	if lifecycleStorageFormat(version) && f.image.WorkerExecutions != nil {
		for _, d := range c.Maintenance {
			r := f.image.WorkerExecutions.Records[d.ExecutionID]
			if monitor, cancelled := f.workerMaintenanceCancellation(&d, r.Intent, at, version); monitor != nil {
				previous := f.image.Monitors[monitor.ID]
				events = append(events, f.installMonitor(previous, *monitor, at)...)
				f.publishWorkerMonitor(previous, *monitor)
				events = append(events, cancelled...)
			}
		}
	}
	f.image.Version = max(f.image.Version, WorkerSessionFormatVersion)
	if len(c.ProposedLeaseIDs) > 0 {
		f.image.Version = max(f.image.Version, WorkerOfferFormatVersion)
	}
	if c.SelectionVersion == 2 {
		f.image.Version = joinStorageFormat(f.image.Version, LifecycleWorkerFormatVersion)
	}
	if c.SelectionVersion == 1 {
		f.image.Version = max(f.image.Version, WorkerQueueFormatVersion)
	}
	response = record.Response.clone()
	return Result{Allowed: true, Events: events, resultExtensions: resultExtensions{WorkerPoll: &response}}
}

func validateWorkerSessionImage(i image) error {
	state := i.WorkerSessions
	if state == nil {
		return nil
	}
	if (i.Version < WorkerSessionFormatVersion || !externalStorageFormat(i.Version)) || state.Version != 1 || !catalogIdentifier(state.NodeID, 256) || !validAuthenticationID(state.PolicyEpoch) || state.Sessions == nil || len(state.Sessions) > MaxWorkerSessions || i.WorkerPolicy == nil || state.PolicyEpoch != i.WorkerPolicy.Epoch || i.WorkerPolicy.ResetRequired {
		return ErrWorkerSessionInvalid
	}
	used := workerSessionImageOverhead
	identities := make(map[string]bool, len(state.Sessions))
	for key, r := range state.Sessions {
		if r.ReplyExpiredAt != nil && (i.Version < WorkerCheckRetentionFormatVersion || r.ReplyExpiredAt.Before(r.Response.SessionExpiresAt) || r.ReplyExpiredAt.Year() > 9998 || len(r.Response.Offers) != 0) {
			return ErrWorkerSessionInvalid
		}
		worker, ok := i.WorkerPolicy.Workers[r.WorkerID]
		request, response := r.Request, r.Response
		if !ok || worker.UID != key || r.WorkerUID != key || request.WorkerID != r.WorkerID || !catalogIdentifier(r.OwnerEpoch, 256) || request.validate() != nil || r.RequestDigest != workerPollDigest(request) || r.OpenedAt.IsZero() || r.OpenedAt.Year() < 1 || r.UpdatedAt.Before(r.OpenedAt) || r.UpdatedAt.Year() > 9998 || !response.SessionExpiresAt.Equal(r.UpdatedAt.Add(WorkerSessionLifetime)) || response.ServerID != workerProtocolServerID(state.NodeID, state.PolicyEpoch) || response.ServerID != request.ServerID || response.WorkerUID != key || response.ClientSessionID != request.ClientSessionID || response.PollSequence != request.PollSequence || !catalogIdentifier(response.SessionID, 256) || request.SessionID != "" && response.SessionID != request.SessionID || request.SessionID == "" && !r.OpenedAt.Equal(r.UpdatedAt) {
			return ErrWorkerSessionInvalid
		}
		if len(response.Offers) > 0 && i.Version < WorkerOfferFormatVersion {
			return ErrWorkerSessionInvalid
		}
		if len(response.Offers) > request.Limit || len(response.Offers) > request.Capacity {
			return ErrWorkerSessionInvalid
		}
		seenOffers := map[string]bool{}
		for _, offer := range response.Offers {
			if i.WorkerExecutions == nil || seenOffers[offer.ExecutionID] {
				return ErrWorkerSessionInvalid
			}
			seenOffers[offer.ExecutionID] = true
			execution, ok := i.WorkerExecutions.Records[offer.ExecutionID]
			lease, _ := workerLease(execution, offer.LeaseID)
			if !ok || lease == nil || execution.Intent.Revision != offer.ExecutionRevision || !execution.Intent.Deadline.Equal(offer.Deadline) || lease.WorkerUID != key || lease.SessionID != response.SessionID || lease.OwnerEpoch != r.OwnerEpoch || lease.ServerID != response.ServerID {
				return ErrWorkerSessionInvalid
			}
		}
		if identities[response.SessionID] {
			return ErrWorkerSessionInvalid
		}
		identities[response.SessionID] = true
		cost, err := workerSessionRecordCost(key, r)
		if err != nil || cost > MaxWorkerSessionBytes-used {
			return ErrWorkerSessionQuota
		}
		used += cost
	}
	if used != state.EncodedBytes {
		return ErrWorkerSessionInvalid
	}
	return nil
}
