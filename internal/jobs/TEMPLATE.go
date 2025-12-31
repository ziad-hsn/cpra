package jobs

// TEMPLATE - Copy this file when creating a new job type.
//
// See docs/how-to/adding-new-jobs.md for the complete guide.
//
// SAFETY CHECKLIST (required for network jobs):
//
//  1. Call GetDialLimiter().Acquire(ctx) before network I/O
//  2. defer GetDialLimiter().Release() immediately after acquire
//  3. Use RetryWithBackoff for context-aware retries (see helpers.go)
//  4. Use predeclared errors in types.go (avoid allocations)
//  5. Add sync.Pool in pool.go (see existing patterns)
//  6. Add factory case in factory.go
//  7. Return Result{Ent, Err, Payload} in all paths
//
// FILE NAMING:
//
//	pulse_<driver>.go        (e.g., pulse_dns.go)
//	intervention_<action>.go (e.g., intervention_k8s.go)
//	code_<channel>.go        (e.g., code_teams.go)
//
// CONCURRENCY HELPERS (see helpers.go):
//
//	RetryWithBackoff - Context-aware retry with exponential backoff
//	Or               - Combine multiple done channels
//	OrDone           - Wrap channel reads with cancellation
//	Tee              - Duplicate channel streams for fan-out

/*
EXAMPLE - Network Job:

type PulseDNSJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

func (j *PulseDNSJob) Execute(ctx context.Context) Result {
	payload := GetPulseDNSPayload()

	// Acquire dial slot (prevents CPU spikes during outages)
	if !GetDialLimiter().Acquire(ctx) {
		return Result{Ent: j.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
	}
	defer GetDialLimiter().Release()

	// Use RetryWithBackoff for context-aware retries with exponential backoff.
	// This replaces manual for-loops with time.Sleep (see "Concurrency in Go" p. 5-6).
	err := RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		return j.doDNSLookup(ctx)
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDNSCheckFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

// Required interface methods
func (j *PulseDNSJob) Copy() Job                  { job := *j; return &job }
func (j *PulseDNSJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *PulseDNSJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *PulseDNSJob) GetStartTime() time.Time    { return j.StartTime }
func (j *PulseDNSJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *PulseDNSJob) IsNil() bool                { return j == nil }
*/
