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

func (f *machine) applyWorkerPoll(c WorkerSessionCommand, at time.Time) Result {
	if err := f.checkWorkerPollAuthority(c.Authority, c.Request, at); err != nil {
		return Result{Err: err}
	}
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	request := c.Request
	old := f.image.WorkerSessions
	if old != nil && (old.NodeID != c.NodeID || old.PolicyEpoch != c.Authority.Epoch) {
		return Result{Err: ErrWorkerSessionIdentity}
	}
	var previous workerSessionRecord
	exists := false
	if old != nil {
		previous, exists = old.Sessions[c.Authority.WorkerUID]
	}
	digest := workerPollDigest(request)
	opening := request.SessionID == ""
	if !opening && (!exists || request.SessionID != previous.Response.SessionID) {
		return Result{Err: ErrWorkerSessionExpired}
	}
	if exists {
		live := previous.OwnerEpoch == c.OwnerEpoch && at.Before(previous.Response.SessionExpiresAt)
		sameNonce := previous.Response.ClientSessionID == request.ClientSessionID
		if sameNonce && !live {
			return Result{Err: ErrWorkerSessionExpired}
		}
		if opening && !sameNonce {
			if live {
				return Result{Err: ErrWorkerSessionConflict}
			}
		} else {
			if !sameNonce {
				return Result{Err: ErrWorkerSessionConflict}
			}
			if !opening && request.SessionID != previous.Response.SessionID {
				return Result{Err: ErrWorkerSessionExpired}
			}
			if request.PollSequence == previous.Response.PollSequence {
				if digest != previous.RequestDigest {
					return Result{Err: ErrWorkerSessionConflict}
				}
				response := previous.Response.clone()
				return Result{Allowed: true, resultExtensions: resultExtensions{WorkerPoll: &response}}
			}
			if opening || previous.Response.PollSequence == math.MaxInt64 || request.PollSequence != previous.Response.PollSequence+1 {
				return Result{Err: ErrWorkerSessionSequence}
			}
			if at.Before(previous.UpdatedAt) {
				return Result{Err: ErrWorkerSessionConflict}
			}
			if !sameWorkerCapabilities(request.Capabilities, previous.Request.Capabilities) {
				return Result{Err: ErrWorkerSessionConflict}
			}
		}
	} else if !opening {
		return Result{Err: ErrWorkerSessionExpired}
	}
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
		f.image.WorkerExecutions.Records[r.Intent.ID] = r
	}
	if len(updates) > 0 {
		f.image.WorkerExecutions.EncodedBytes += delta
	}
	old.Sessions[record.WorkerUID] = record
	old.EncodedBytes = used + cost
	f.image.Version = max(f.image.Version, WorkerSessionFormatVersion)
	if len(c.ProposedLeaseIDs) > 0 {
		f.image.Version = max(f.image.Version, WorkerOfferFormatVersion)
	}
	response = record.Response.clone()
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerPoll: &response}}
}

func validateWorkerSessionImage(i image) error {
	state := i.WorkerSessions
	if state == nil {
		return nil
	}
	if (i.Version != WorkerSessionFormatVersion && i.Version != WorkerExecutionFormatVersion && i.Version != WorkerOfferFormatVersion) || state.Version != 1 || !catalogIdentifier(state.NodeID, 256) || !validAuthenticationID(state.PolicyEpoch) || state.Sessions == nil || len(state.Sessions) > MaxWorkerSessions || i.WorkerPolicy == nil || state.PolicyEpoch != i.WorkerPolicy.Epoch || i.WorkerPolicy.ResetRequired {
		return ErrWorkerSessionInvalid
	}
	used := workerSessionImageOverhead
	identities := make(map[string]bool, len(state.Sessions))
	for key, r := range state.Sessions {
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
			if !ok || execution.Lifecycle == nil || workerRecordOffer(execution) != offer || execution.Lifecycle.WorkerUID != key || execution.Lifecycle.SessionID != response.SessionID || execution.Lifecycle.OwnerEpoch != r.OwnerEpoch || execution.Lifecycle.ServerID != response.ServerID {
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
