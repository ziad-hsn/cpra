package jobs

// TEMPLATE - Copy this file when creating a new job type.
//
// See docs/how-to/adding-new-jobs.md for the complete guide.
//
// SAFETY CHECKLIST (required for network jobs):
//
//  1. Call GetDialLimiter().Acquire(ctx) before network I/O
//  2. defer GetDialLimiter().Release() immediately after acquire
//  3. Check ctx.Done() before each retry attempt
//  4. Use predeclared errors in types.go (avoid allocations)
//  5. Add sync.Pool in pool.go (see existing patterns)
//  6. Add factory case in factory.go
//  7. Return Result{Ent, Err, Payload} in all paths
//
// FILE NAMING:
//   pulse_<driver>.go        (e.g., pulse_dns.go)
//   intervention_<action>.go (e.g., intervention_k8s.go)
//   code_<channel>.go        (e.g., code_teams.go)

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

	attempts := j.Retries + 1
	for i := 0; i < attempts; i++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		if err := j.doDNSLookup(ctx); err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if i < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDNSCheckFailed, Payload: payload}
}

// Required interface methods
func (j *PulseDNSJob) Copy() Job                  { job := *j; return &job }
func (j *PulseDNSJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *PulseDNSJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *PulseDNSJob) GetStartTime() time.Time    { return j.StartTime }
func (j *PulseDNSJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *PulseDNSJob) IsNil() bool                { return j == nil }
*/
