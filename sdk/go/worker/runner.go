//go:build externaljobs

package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Runner owns a journal and one execution slot per accepted assignment. Run is
// single-use. Close releases an unused runner; a running runner closes only once
// all handler goroutines and protocol calls have returned.
type Runner struct {
	config       Config
	journal      *journal
	mu           sync.Mutex
	run          bool
	finished     bool
	draining     bool
	drainExpired bool
	active       map[string]bool
	lastError    error
	finalStatus  Status
	fatal        chan error
}

type polledBatch struct {
	assignments *api.Assignments
	admitted    chan struct{}
}

// New validates configuration and opens the exclusively locked encrypted
// journal. It starts no poller and invokes no handler until Run is called.
func New(config Config) (*Runner, error) {
	c, err := config.validate()
	if err != nil {
		return nil, err
	}
	j, err := openJournal(c)
	if err != nil {
		return nil, err
	}
	return &Runner{config: c, journal: j, active: make(map[string]bool), fatal: make(chan error, 1)}, nil
}

// Close releases an unused or finished runner's journal. It returns ErrRunning
// while Run still owns handlers or protocol calls; cancel Run's context first.
func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run && !r.finished {
		return ErrRunning
	}
	return r.journal.close()
}

// Status returns current admission, handler, and journal accounting. After Run
// returns it reports the final retained status without reopening the journal.
func (r *Runner) Status() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return r.finalStatus, nil
	}
	s, err := r.journal.stats()
	s.Running = r.run
	s.Draining = r.draining
	s.DrainDeadlineExpired = r.drainExpired
	s.ActiveHandlers = len(r.active)
	s.LastError = r.lastError
	s.AdmissionAvailable = s.AdmissionAvailable && !r.draining && len(r.active) < r.config.Limits.Concurrency
	if r.draining {
		s.AdmissionCapacity = 0
	}
	return s, err
}

func (r *Runner) recordError(err error)   { r.mu.Lock(); r.lastError = err; r.mu.Unlock() }
func (r *Runner) isActive(id string) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.active[id] }

// Run stops admission on cancellation, then requests cooperative cancellation.
// Drain expiry is immediately visible through Status. It does not release the
// lock or return while a handler remains alive; process supervision is required
// for a hard execution deadline.
func (r *Runner) Run(ctx context.Context) (runErr error) {
	if ctx == nil {
		return errors.New("worker context is required")
	}
	r.mu.Lock()
	if r.run || r.finished {
		r.mu.Unlock()
		return ErrRunning
	}
	r.run = true
	r.mu.Unlock()
	handlers, capabilities := r.config.Registry.freeze()
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].JobTypeID != capabilities[j].JobTypeID {
			return capabilities[i].JobTypeID < capabilities[j].JobTypeID
		}
		if capabilities[i].Version != capabilities[j].Version {
			return capabilities[i].Version < capabilities[j].Version
		}
		return capabilities[i].Kind < capabilities[j].Kind
	})
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	deliveryCtx, cancelDelivery := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelDelivery()
	batches := make(chan polledBatch, 1)
	done := make(chan string, r.config.Limits.Concurrency)
	fail := func(err error) {
		select {
		case r.fatal <- err:
		default:
		}
	}
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); r.poll(workCtx, capabilities, batches, fail) }()
	go func() { defer background.Done(); r.flush(deliveryCtx, fail) }()
	defer func() {
		cancelWork()
		cancelDelivery()
		background.Wait()
		s, err := r.journal.stats()
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
		r.mu.Lock()
		s.Draining = r.draining
		s.DrainDeadlineExpired = r.drainExpired
		s.LastError = r.lastError
		s.AdmissionAvailable = false
		r.finalStatus = s
		r.finished = true
		r.mu.Unlock()
		if err = r.journal.close(); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	var drain *time.Timer
	var drainC <-chan time.Time
	ctxDone := ctx.Done()
	draining := false
	beginDrain := func(err error) {
		if draining {
			return
		}
		draining = true
		runErr = err
		ctxDone = nil
		cancelWork()
		r.mu.Lock()
		r.draining = true
		r.mu.Unlock()
		drain = time.NewTimer(r.config.DrainTimeout)
		drainC = drain.C
	}
	defer func() {
		if drain != nil {
			drain.Stop()
		}
	}()
	for {
		if draining {
			r.mu.Lock()
			n := len(r.active)
			r.mu.Unlock()
			if n == 0 {
				// Known outcomes are durable already. A final bounded delivery pass
				// is optional; a restart will resend the same persisted envelopes.
				return runErr
			}
		}
		select {
		case <-ctxDone:
			beginDrain(ctx.Err())
		case err := <-r.fatal:
			r.recordError(err)
			beginDrain(err)
		case <-drainC:
			r.mu.Lock()
			r.drainExpired = true
			r.lastError = ErrDrainDeadline
			r.mu.Unlock()
			runErr = errors.Join(runErr, ErrDrainDeadline)
			drainC = nil
			cancelDelivery()
		case id := <-done:
			r.mu.Lock()
			delete(r.active, id)
			r.mu.Unlock()
		case batch := <-batches:
			if draining || workCtx.Err() != nil {
				close(batch.admitted)
				continue
			}
			for _, assignment := range batch.assignments.Items {
				if err := validateAssignment(assignment, r.config.ServerID, r.config.WorkerUID, batch.assignments.SessionID); err != nil {
					fail(err)
					break
				}
				h := handlers[handlerKey{assignment.JobTypeID, assignment.JobTypeVersion, assignment.Kind}]
				if h == nil {
					r.recordError(errors.New("assignment has no registered exact handler"))
					continue
				}
				r.mu.Lock()
				if r.active[assignment.ExecutionID] || len(r.active) >= r.config.Limits.Concurrency {
					r.mu.Unlock()
					continue
				}
				r.active[assignment.ExecutionID] = true
				r.mu.Unlock()
				go func(a api.Assignment, handler Handler) {
					defer func() { done <- a.ExecutionID }()
					r.execute(workCtx, a, handler, fail)
				}(assignment, h)
			}
			close(batch.admitted)
		}
	}
}

func (r *Runner) poll(ctx context.Context, capabilities []api.WorkerCapability, out chan<- polledBatch, fail func(error)) {
	if len(capabilities) == 0 {
		return
	} // An outbox-only runner needs no polling session.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		fail(errors.New("worker session identity generation failed"))
		return
	}
	clientID := hex.EncodeToString(nonce[:])
	sessionID := ""
	sequence := int64(1)
	var expires time.Time
	var frozen *api.PollRequest
	for ctx.Err() == nil {
		if frozen == nil {
			s, err := r.Status()
			if err != nil {
				fail(err)
				return
			}
			if !s.AdmissionAvailable {
				if !pause(ctx, r.config.RetryInterval) {
					return
				}
				continue
			}
			capacity := r.config.Limits.Concurrency - s.ActiveHandlers
			if capacity > s.AdmissionCapacity {
				capacity = s.AdmissionCapacity
			}
			frozen = &api.PollRequest{ServerID: r.config.ServerID, WorkerID: r.config.WorkerID, ClientSessionID: clientID, SessionID: sessionID, PollSequence: sequence, Capabilities: append([]api.WorkerCapability(nil), capabilities...), Capacity: int64(capacity), Limit: int64(capacity), WaitSeconds: int64(r.config.PollWait / time.Second)}
		}
		request := *frozen
		request.Capabilities = append([]api.WorkerCapability(nil), frozen.Capabilities...)
		batch, err := r.config.Client.Poll(ctx, request)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, cpra.ErrWorkerSessionExpired) {
				if _, err := rand.Read(nonce[:]); err != nil {
					fail(errors.New("worker session identity generation failed"))
					return
				}
				clientID = hex.EncodeToString(nonce[:])
				sessionID, sequence, expires, frozen = "", 1, time.Time{}, nil
			}
			r.recordError(err)
			if !pause(ctx, r.config.RetryInterval) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err := validateBatch(batch, *frozen, r.config.WorkerUID, expires); err != nil {
			fail(err)
			return
		}
		sessionID, expires = batch.SessionID, batch.SessionExpiresAt
		if len(batch.Items) != 0 {
			// Detach assignment parameters before ownership passes to execution slots.
			detached := *batch
			detached.Items = append([]api.Assignment(nil), batch.Items...)
			for i := range detached.Items {
				detached.Items[i].Parameters = append([]byte(nil), batch.Items[i].Parameters...)
			}
			admitted := make(chan struct{})
			select {
			case out <- polledBatch{assignments: &detached, admitted: admitted}:
			case <-ctx.Done():
				return
			}
			select {
			case <-admitted:
			case <-ctx.Done():
				return
			}
		}
		if sequence == math.MaxInt64 {
			fail(errors.New("worker poll sequence exhausted"))
			return
		}
		sequence++
		frozen = nil
		if len(batch.Items) == 0 && !pause(ctx, r.config.RetryInterval) {
			return
		}
	}
}

func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *Runner) execute(ctx context.Context, a api.Assignment, h Handler, fail func(error)) {
	if !a.Deadline.After(time.Now()) {
		return
	}
	if err := r.journal.reserve(a); err != nil {
		if errors.Is(err, ErrCapacity) || errors.Is(err, ErrDuplicate) {
			return
		}
		fail(err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	request := api.StartRequest{ServerID: a.ServerID, SessionID: a.SessionID, Mode: api.StartModeBegin, ExecutionID: a.ExecutionID, LeaseID: a.LeaseID, ExecutionRevision: a.ExecutionRevision}
	start, err := r.config.Client.Start(ctx, request)
	if err != nil {
		r.recordError(err)
		return
	} // reservation prevents a later invocation
	if err := validateStart(start, request, r.config.WorkerUID); err != nil {
		fail(err)
		return
	}
	switch start.Disposition {
	case api.StartDispositionTerminal, api.StartDispositionRejected:
		if err = r.journal.remove(a.ExecutionID); err != nil {
			fail(err)
		}
		return
	case api.StartDispositionPending:
		return
	case api.StartDispositionStarted, api.StartDispositionUnknown:
		if err = r.interruptedOutcome(a.ExecutionID, start); err != nil {
			fail(err)
		}
		return
	case api.StartDispositionGranted:
	}
	deadline := a.Deadline
	if !start.Deadline.IsZero() && start.Deadline.Before(deadline) {
		deadline = start.Deadline
	}
	if err = r.journal.update(a.ExecutionID, func(rec *journalRecord) error {
		rec.State = "started"
		rec.GrantID = start.GrantID
		rec.Deadline = deadline
		return nil
	}); err != nil {
		fail(err)
		return
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if execCtx.Err() != nil {
		r.saveOutcome(a.ExecutionID, unknownOutcome(a.Kind, "execution cancelled before handler invocation"), fail)
		return
	}
	var credentials any
	if a.CredentialProfile != "" {
		if r.config.Credentials == nil {
			r.saveOutcome(a.ExecutionID, unknownOutcome(a.Kind, "worker credential profile is unavailable"), fail)
			return
		}
		credentials, err = resolveCredentials(execCtx, r.config.Credentials, a.CredentialProfile)
		if err != nil {
			r.saveOutcome(a.ExecutionID, unknownOutcome(a.Kind, "worker credential resolution failed"), fail)
			return
		}
	}
	if execCtx.Err() != nil {
		r.saveOutcome(a.ExecutionID, unknownOutcome(a.Kind, "execution cancelled during worker credential resolution"), fail)
		return
	}
	// The heartbeat goroutine belongs to this invocation and is joined before
	// its journal record becomes eligible for delivery/reconciliation.
	heartbeatCtx, stopHeartbeat := context.WithCancel(execCtx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		r.heartbeat(heartbeatCtx, a, start.GrantID, cancel, fail)
	}()
	job := Job{Assignment: a, Credentials: credentials}
	job.Assignment.Parameters = append([]byte(nil), a.Parameters...)
	outcome, handlerErr := invoke(execCtx, h, job)
	stopHeartbeat()
	<-heartbeatDone
	if handlerErr != nil {
		outcome = unknownOutcome(a.Kind, "worker handler did not produce a confirmed outcome")
	}
	r.saveOutcome(a.ExecutionID, outcome, fail)
}

func invoke(ctx context.Context, h Handler, job Job) (out api.Outcome, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("worker handler panicked")
		}
	}()
	if err := ctx.Err(); err != nil {
		return api.Outcome{}, err
	}
	return h(ctx, job)
}

func resolveCredentials(ctx context.Context, resolver CredentialResolver, profile string) (credentials any, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("worker credential resolver panicked")
		}
	}()
	return resolver(ctx, profile)
}

func unknownOutcome(kind, diagnostic string) api.Outcome {
	status := "unknown"
	if kind == "check" {
		status = "noData"
	}
	return api.Outcome{Kind: kind, Status: status, Diagnostic: diagnostic, Evidence: []string{}}
}

func (r *Runner) heartbeat(ctx context.Context, a api.Assignment, grantID string, cancelExecution context.CancelFunc, fail func(error)) {
	for pause(ctx, r.config.HeartbeatInterval) {
		reply, err := r.config.Client.Heartbeat(ctx, api.HeartbeatRequest{ServerID: a.ServerID, SessionID: a.SessionID, WorkerID: r.config.WorkerID, ExecutionID: a.ExecutionID, GrantID: grantID})
		if err != nil && ctx.Err() == nil {
			r.recordError(err)
			if errors.Is(err, cpra.ErrUnauthorized) || errors.Is(err, cpra.ErrFeatureUnavailable) {
				cancelExecution()
				fail(err)
				return
			}
			if errors.Is(err, cpra.ErrConflict) || errors.Is(err, cpra.ErrExpired) || errors.Is(err, cpra.ErrNotFound) {
				cancelExecution()
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if reply == nil || reply.ServerID != a.ServerID || reply.WorkerUID != a.WorkerUID || reply.SessionID != a.SessionID || reply.ExecutionID != a.ExecutionID || reply.GrantID != grantID {
			cancelExecution()
			fail(ErrIdentity)
			return
		}
		if !reply.Accepted {
			cancelExecution()
			return
		}
	}
}

func validOutcome(kind, status string) bool {
	if kind != "check" && (status == "unknown" || status == "rejected") {
		return true
	}
	switch kind {
	case "check":
		return status == "success" || status == "failure" || status == "noData"
	case "recovery":
		return status == "accepted" || status == "completed"
	case "notification":
		return status == "accepted" || status == "delivered"
	}
	return false
}

func truncateDiagnostic(d string, limit int) (string, bool) {
	if len(d) <= limit {
		return d, false
	}
	d = d[:limit]
	for len(d) > 0 && !utf8.ValidString(d) {
		d = d[:len(d)-1]
	}
	return d, true
}

func (r *Runner) saveOutcome(id string, out api.Outcome, fail func(error)) {
	err := r.journal.update(id, func(rec *journalRecord) error {
		out.ServerID = rec.Start.ServerID
		out.WorkerUID = rec.WorkerUID
		out.ExecutionID = id
		out.GrantID = rec.GrantID
		out.Kind = rec.Kind
		if rec.Kind == "check" && (out.Status == "unknown" || out.Status == "rejected") {
			out.Status = "noData"
		}
		if !validOutcome(rec.Kind, out.Status) {
			out = unknownOutcome(rec.Kind, "worker returned an invalid outcome status")
			out.ServerID = rec.Start.ServerID
			out.WorkerUID = rec.WorkerUID
			out.ExecutionID = id
			out.GrantID = rec.GrantID
		}
		if out.Evidence == nil {
			out.Evidence = []string{}
		}
		var truncated bool
		out.Diagnostic, truncated = truncateDiagnostic(out.Diagnostic, r.config.Limits.DiagnosticBytes)
		out.DiagnosticTruncated = out.DiagnosticTruncated || truncated
		encoded, err := json.Marshal(out)
		if err != nil || len(encoded) > r.config.Limits.OutcomeBytes || !validText(out.RejectionCode, 128) || !validEvidence(out.Diagnostic, out.Evidence) {
			out = unknownOutcome(rec.Kind, "worker outcome exceeded limits or could not be encoded")
			out.ServerID = rec.Start.ServerID
			out.WorkerUID = rec.WorkerUID
			out.ExecutionID = id
			out.GrantID = rec.GrantID
		}
		rec.Outcome = &out
		rec.State = "outcome"
		return nil
	})
	if err != nil {
		fail(err)
	}
}

func (r *Runner) flush(ctx context.Context, fail func(error)) {
	for ctx.Err() == nil {
		records, err := r.journal.records()
		if err != nil {
			fail(err)
			return
		}
		for _, rec := range records {
			if ctx.Err() != nil {
				return
			}
			if err = r.deliverInactive(ctx, rec.ExecutionID); err != nil {
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrIdentity) || errors.Is(err, ErrClosed) || errors.Is(err, ErrStorage) {
					fail(err)
					return
				}
				r.recordError(err)
			}
		}
		if !pause(ctx, r.config.RetryInterval) {
			return
		}
	}
}

// Reload after observing an inactive slot: the flush inventory may predate the
// handler's final durable outcome. Reconciliation must never replace it with an
// interrupted outcome from a stale reserved/started inventory entry.
func (r *Runner) deliverInactive(ctx context.Context, id string) error {
	if r.isActive(id) {
		return nil
	}
	rec, found, err := r.journal.record(id)
	if err != nil || !found {
		return err
	}
	return r.deliver(ctx, rec)
}

func (r *Runner) deliver(ctx context.Context, rec journalRecord) error {
	if rec.Evidence != nil {
		receipt, err := r.config.Client.LateEvidence(ctx, *rec.Evidence)
		if err != nil {
			return err
		}
		if err = r.checkReceipt(rec.ExecutionID, receipt); err != nil {
			return err
		}
		return r.journal.update(rec.ExecutionID, func(stored *journalRecord) error {
			if stored.Evidence != nil && stored.Evidence.EvidenceID == rec.Evidence.EvidenceID {
				stored.Evidence = nil
			}
			return nil
		})
	}
	switch rec.State {
	case "reserved", "unknown":
		// Persisted requests are always read-only reconciliations. Never repeat begin.
		start, err := r.config.Client.Start(ctx, rec.Start)
		if err != nil {
			return err
		}
		if err = validateStart(start, rec.Start, rec.WorkerUID); err != nil {
			return err
		}
		switch start.Disposition {
		case api.StartDispositionTerminal:
			return r.journal.remove(rec.ExecutionID)
		case api.StartDispositionRejected:
			if rec.State == "reserved" {
				return r.journal.remove(rec.ExecutionID)
			}
			return ErrIdentity // A confirmed unknown action cannot become an unstarted rejection.
		case api.StartDispositionPending:
			return nil
		case api.StartDispositionStarted, api.StartDispositionUnknown:
			if rec.GrantID != "" && rec.GrantID != start.GrantID {
				return ErrIdentity
			}
			if rec.State == "unknown" {
				return nil
			}
			return r.interruptedOutcome(rec.ExecutionID, start)
		default:
			return ErrIdentity
		}
	case "started":
		return r.journal.update(rec.ExecutionID, func(stored *journalRecord) error {
			out := unknownOutcome(rec.Kind, "worker restarted with an interrupted handler; it will not be repeated")
			out.ServerID = rec.Start.ServerID
			out.WorkerUID = rec.WorkerUID
			out.ExecutionID = rec.ExecutionID
			out.GrantID = rec.GrantID
			stored.Outcome = &out
			stored.State = "outcome"
			return nil
		})
	case "outcome":
		if rec.Outcome == nil {
			return ErrCorrupt
		}
		receipt, err := r.config.Client.Result(ctx, *rec.Outcome)
		if err != nil {
			return err
		}
		if err = r.checkReceipt(rec.ExecutionID, receipt); err != nil {
			return err
		}
		if receipt.Disposition == "unknown" || (rec.Kind != "check" && rec.Outcome.Status == "unknown" && receipt.Disposition != "finalized") {
			return r.journal.update(rec.ExecutionID, func(stored *journalRecord) error {
				stored.State = "unknown"
				stored.ReceiptID = receipt.ReceiptID
				return nil
			})
		}
		return r.journal.remove(rec.ExecutionID)
	default:
		return ErrCorrupt
	}
}

func (r *Runner) checkReceipt(id string, receipt *api.Receipt) error {
	if receipt == nil || receipt.ServerID != r.config.ServerID || receipt.WorkerUID != r.config.WorkerUID || receipt.ExecutionID != id {
		return ErrIdentity
	}
	if !validID(receipt.ReceiptID) {
		return errors.New("worker receipt identity is missing")
	}
	if receipt.Disposition != "accepted" && receipt.Disposition != "finalized" && receipt.Disposition != "unknown" {
		return errors.New("unsupported worker receipt disposition")
	}
	return nil
}

// QueueLateEvidence durably queues one append-only evidence submission for an
// unknown execution with a confirmed original receipt. The caller supplies a
// stable fresh EvidenceID and may safely retry identical evidence. It neither
// replaces the original result nor re-enables execution.
func (r *Runner) QueueLateEvidence(ctx context.Context, evidence api.LateEvidenceRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(evidence.ExecutionID) || !validID(evidence.EvidenceID) || !validID(evidence.OriginalReceiptID) {
		return errors.New("late evidence requires execution, original receipt and evidence identities")
	}
	if (evidence.ServerID != "" && evidence.ServerID != r.config.ServerID) || (evidence.WorkerUID != "" && evidence.WorkerUID != r.config.WorkerUID) {
		return ErrIdentity
	}
	evidence.ServerID = r.config.ServerID
	evidence.WorkerUID = r.config.WorkerUID
	if evidence.Evidence == nil {
		evidence.Evidence = []string{}
	}
	if !validEvidence(evidence.Diagnostic, evidence.Evidence) {
		return errors.New("invalid worker evidence")
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if len(encoded) > r.config.Limits.OutcomeBytes || len(evidence.Diagnostic) > r.config.Limits.DiagnosticBytes {
		return errors.New("late evidence exceeds worker limits")
	}
	err = r.journal.update(evidence.ExecutionID, func(rec *journalRecord) error {
		if rec.State != "unknown" || rec.ReceiptID != evidence.OriginalReceiptID {
			return errors.New("late evidence requires the original unknown-action receipt")
		}
		if rec.Evidence != nil {
			previous, _ := json.Marshal(rec.Evidence)
			if string(previous) == string(encoded) {
				return nil
			}
			return fmt.Errorf("%w: late evidence is pending", ErrDuplicate)
		}
		// Decode a private copy so caller mutations cannot change durable delivery.
		var copy api.LateEvidenceRequest
		if err := json.Unmarshal(encoded, &copy); err != nil {
			return err
		}
		rec.Evidence = &copy
		return nil
	})
	if errors.Is(err, ErrStorage) || errors.Is(err, ErrCorrupt) {
		select {
		case r.fatal <- err:
		default:
		}
	}
	return err
}

// interruptedOutcome reports a confirmed original start without invoking its
// handler. The original grant is retained for result delivery across sessions.
func (r *Runner) interruptedOutcome(id string, start *api.StartResponse) error {
	return r.journal.update(id, func(rec *journalRecord) error {
		out := unknownOutcome(rec.Kind, "start permission was interrupted; handler will not be repeated")
		out.ServerID, out.WorkerUID = rec.Start.ServerID, rec.WorkerUID
		out.ExecutionID, out.GrantID = id, start.GrantID
		rec.GrantID, rec.Outcome, rec.State = start.GrantID, &out, "outcome"
		if start.Deadline.Before(rec.Deadline) {
			rec.Deadline = start.Deadline
		}
		return nil
	})
}
