//go:build externaljobs

package persistence

import "time"

// Reserved inside each result slot, including JSON framing. Validated receipt
// IDs, timestamps and indices cannot exceed this fixed metadata allowance.
const maxWorkerObservationBytes = 1024

// WorkerResultObservation records confirmation of the original result commit.
// It contains no provider payload or worker-supplied clock. The result receipt
// remains authoritative even when a crash prevents saving this measurement.
type WorkerResultObservation struct {
	ReceiptID      string    `json:"receipt_id"`
	ConfirmedAt    time.Time `json:"confirmed_at"`
	CommittedIndex uint64    `json:"committed_index"`
}

type WorkerObservationCommand struct {
	ExecutionID       string `json:"execution_id"`
	ExecutionRevision string `json:"execution_revision"`
	ReceiptID         string `json:"receipt_id"`
	OwnerEpoch        string `json:"owner_epoch"`
}

func (c WorkerObservationCommand) validate(at time.Time) error {
	for _, id := range []string{c.ExecutionID, c.ExecutionRevision, c.ReceiptID, c.OwnerEpoch} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func (f *machine) applyWorkerObservation(c WorkerObservationCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	if f.image.WorkerExecutions == nil {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	r, ok := f.image.WorkerExecutions.Records[c.ExecutionID]
	if !ok || r.OwnerEpoch != c.OwnerEpoch || r.Intent.Revision != c.ExecutionRevision || r.ResultTarget == 0 ||
		r.Result == nil || !r.Result.Applied || r.Result.NoData || r.Result.Receipt.ReceiptID != c.ReceiptID {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if r.Observation != nil {
		return Result{Allowed: true}
	}
	if at.Before(r.Result.RecordedAt) || index <= r.Result.CommittedIndex {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	r.Observation = &WorkerResultObservation{ReceiptID: c.ReceiptID, ConfirmedAt: at, CommittedIndex: index}
	f.image.WorkerExecutions.Records[c.ExecutionID] = r
	if b := f.image.WorkerSLO.bucket(r); b != nil {
		elapsed := at.Sub(r.Intent.Scheduled)
		b.Result.Add(elapsed)
		if elapsed <= r.ResultTarget {
			b.ResultMet++
		}
		f.image.WorkerSLO.updated(at)
	}
	f.image.Version = max(f.image.Version, WorkerSLOFormatVersion)
	return Result{Allowed: true}
}

func validateWorkerObservation(i image, r WorkerExecutionRecord) error {
	if r.ResultTarget == 0 {
		if r.Observation != nil || r.SLOEpoch != 0 {
			return ErrWorkerExecutionInvalid
		}
		return nil
	}
	if i.Version < WorkerSLOFormatVersion || r.Intent.Category != "check" || r.ResultTarget < 0 || r.SLOEpoch == 0 ||
		i.WorkerSLO == nil || i.WorkerSLO.Epoch < r.SLOEpoch ||
		i.WorkerSLO.Epoch == r.SLOEpoch && i.WorkerSLO.ResultTarget != r.ResultTarget {
		return ErrWorkerExecutionInvalid
	}
	if r.Result != nil {
		cost, err := workerResultRecordCost(r.Result)
		if err != nil || cost > MaxWorkerResultRecordBytes-maxWorkerObservationBytes {
			return ErrWorkerExecutionQuota
		}
	}
	v := r.Observation
	if v == nil {
		return nil
	}
	if r.Result == nil || !r.Result.Applied || r.Result.NoData || v.ReceiptID != r.Result.Receipt.ReceiptID ||
		v.ConfirmedAt.Before(r.Result.RecordedAt) || v.ConfirmedAt.Year() > 9998 ||
		v.CommittedIndex <= r.Result.CommittedIndex || v.CommittedIndex > i.Index {
		return ErrWorkerExecutionInvalid
	}
	return nil
}
