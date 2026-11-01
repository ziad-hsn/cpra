//go:build externaljobs

package worker

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func validText(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text) && !strings.ContainsAny(text, "\x00\r\n")
}

func cloneAssignment(a api.Assignment) api.Assignment {
	a.Parameters = append([]byte(nil), a.Parameters...)
	a.FailureCodes = append([]string(nil), a.FailureCodes...)
	if a.Notification != nil {
		n := *a.Notification
		if n.Resolution != nil {
			v := *n.Resolution
			n.Resolution = &v
		}
		if n.Review != nil {
			review := *n.Review
			n.Review = &review
		}
		a.Notification = &n
	}
	return a
}
func validID(id string) bool          { return id != "" && validText(id, 256) }
func validDeadline(at time.Time) bool { return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 }
func validEvidence(diagnostic string, evidence []string) bool {
	if !utf8.ValidString(diagnostic) || len(evidence) > 64 {
		return false
	}
	for _, value := range evidence {
		if !validText(value, 4096) {
			return false
		}
	}
	return true
}

func validateAssignment(a api.Assignment, server, worker, session string) error {
	if api.ValidateJobTypeFailures(api.JobTypeSpec{Kind: a.Kind, ProtocolVersion: workerProtocol(a.ProtocolVersion), FailureCodes: a.FailureCodes}) != nil {
		return ErrIdentity
	}
	if a.ServerID != server || a.WorkerUID != worker || a.SessionID != session || !validKind(a.Kind) || !validDeadline(a.Deadline) {
		return ErrIdentity
	}
	for _, id := range []string{a.ServerID, a.WorkerUID, a.SessionID, a.ExecutionID, a.ExecutionRevision, a.LeaseID, a.MonitorID, a.IncarnationUID, a.JobTypeID, a.JobTypeUID, a.JobTypeVersion} {
		if !validID(id) {
			return ErrIdentity
		}
	}
	if a.CredentialProfile != "" && !validID(a.CredentialProfile) {
		return ErrIdentity
	}
	if len(a.Parameters) > 128<<10 || !json.Valid(a.Parameters) {
		return ErrIdentity
	}
	if n := a.Notification; n != nil {
		if a.Kind != "notification" || n.Format != 1 && n.Format != 2 || !validID(n.DeliveryID) || !validID(n.EndpointID) || !validID(n.CorrelationID) || n.IncidentID != "" && !validID(n.IncidentID) || !validDeadline(n.At) {
			return ErrIdentity
		}
		if r := n.Resolution; r != nil {
			if n.Format != 2 || n.Color != "green" || n.Review != nil || !validID(r.ConditionID) || !validDeadline(r.RecoveredAt) || (r.Health != "healthy" && r.Health != "unhealthy" && r.Health != "unknown") {
				return ErrIdentity
			}
		}
		switch n.Color {
		case "red", "yellow", "green", "cyan", "gray", "blue":
		default:
			return ErrIdentity
		}
		if n.Review != nil {
			r := n.Review
			if n.Color != "blue" || !validID(r.AlertID) || !validID(r.ActionID) || r.AlertID != n.CorrelationID ||
				(r.Phase != "open" && r.Phase != "close") || (r.Health != "healthy" && r.Health != "unhealthy" && r.Health != "unknown") ||
				(r.Phase == "open" && r.Reason != "" || r.Phase == "close" && r.Reason != "health_recovered" && r.Reason != "action_reviewed") {
				return ErrIdentity
			}
		} else if n.Color == "blue" {
			return ErrIdentity
		}
	}
	return nil
}

// Validate the complete batch before admitting any assignment. A malformed later
// item must not allow an earlier item from the same response to run.
func validateBatch(batch *api.Assignments, request api.PollRequest, uid string, expiry time.Time) error {
	if batch == nil || batch.ServerID != request.ServerID || batch.WorkerUID != uid || batch.ClientSessionID != request.ClientSessionID || batch.PollSequence != request.PollSequence || !validID(batch.SessionID) || (!validDeadline(batch.SessionExpiresAt) || !batch.SessionExpiresAt.After(time.Now())) || (!expiry.IsZero() && batch.SessionExpiresAt.Before(expiry)) || (request.SessionID != "" && batch.SessionID != request.SessionID) || int64(len(batch.Items)) > request.Capacity || int64(len(batch.Items)) > request.Limit {
		return ErrIdentity
	}
	seen := make(map[string]bool, len(batch.Items))
	leases := make(map[string]bool, len(batch.Items))
	parameterBytes := 0
	for _, a := range batch.Items {
		parameterBytes += len(a.Parameters)
		if parameterBytes > 4<<20 {
			return ErrIdentity
		}
		if err := validateAssignment(a, request.ServerID, uid, batch.SessionID); err != nil {
			return err
		}
		if seen[a.ExecutionID] || leases[a.LeaseID] {
			return ErrIdentity
		}
		seen[a.ExecutionID] = true
		leases[a.LeaseID] = true
		registered := false
		for _, c := range request.Capabilities {
			if c.JobTypeID == a.JobTypeID && c.Version == a.JobTypeVersion && c.Kind == a.Kind && workerProtocol(c.ProtocolVersion) == workerProtocol(a.ProtocolVersion) {
				registered = true
				break
			}
		}
		if !registered {
			return ErrIdentity
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > 4<<20 {
		return ErrIdentity
	}
	return nil
}

func workerProtocol(version string) string {
	if version == "" {
		return "1"
	}
	return version
}

func (r *Registry) validateContracts(assignments []api.Assignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range assignments {
		codes, structured := r.failures[handlerKey{a.JobTypeID, a.JobTypeVersion, a.Kind}]
		if (workerProtocol(a.ProtocolVersion) == "2") != structured {
			return ErrIdentity
		}
		if structured {
			x, y := slices.Clone(codes), slices.Clone(a.FailureCodes)
			slices.Sort(x)
			slices.Sort(y)
			if !slices.Equal(x, y) {
				return ErrIdentity
			}
		}
	}
	return nil
}

func recordFailureValid(rec journalRecord, out api.Outcome) bool {
	if out.Failure == nil {
		return true
	}
	if rec.ProtocolVersion != "2" || rec.Kind != "check" || out.Status != "failure" && out.Status != "noData" {
		return false
	}
	failure, err := api.CanonicalWorkerFailure(api.ExternalFailureContract{JobTypeID: rec.JobTypeID, JobTypeUID: rec.JobTypeUID, Version: rec.JobTypeVersion, ProtocolVersion: rec.ProtocolVersion, Codes: rec.FailureCodes}, *out.Failure)
	if err != nil {
		return false
	}
	definition, known := api.FailureCodeDefinition(api.FailureTaxonomyVersion, failure.Code)
	unavailable := known && !definition.Available
	if (failure.Code == "grpc_health_failed" || failure.Code == "grpc.rpc_failed") && failure.GRPCCode != nil && *failure.GRPCCode == "Canceled" {
		unavailable = true
	}
	return (out.Status == "noData") == unavailable
}

func validateStart(reply *api.StartResponse, request api.StartRequest, workerUID string) error {
	if reply == nil || reply.ServerID != request.ServerID || reply.WorkerUID != workerUID || reply.SessionID != request.SessionID || reply.ExecutionID != request.ExecutionID || reply.ExecutionRevision != request.ExecutionRevision || reply.LeaseID != request.LeaseID {
		return ErrIdentity
	}
	switch reply.Disposition {
	case api.StartDispositionGranted, api.StartDispositionStarted:
		if !validID(reply.GrantID) || !validDeadline(reply.Deadline) || reply.ReceiptID != "" {
			return ErrIdentity
		}
		if reply.Disposition == api.StartDispositionGranted && request.Mode != api.StartModeBegin {
			return ErrIdentity
		}
	case api.StartDispositionUnknown:
		if !validID(reply.GrantID) || !validDeadline(reply.Deadline) || reply.ReceiptID != "" {
			return ErrIdentity
		}
	case api.StartDispositionPending, api.StartDispositionRejected:
		if reply.GrantID != "" || reply.ReceiptID != "" || !reply.Deadline.IsZero() {
			return ErrIdentity
		}
	case api.StartDispositionTerminal:
		if !validID(reply.ReceiptID) || reply.GrantID != "" || !reply.Deadline.IsZero() {
			return ErrIdentity
		}
	default:
		return ErrIdentity
	}
	return nil
}
