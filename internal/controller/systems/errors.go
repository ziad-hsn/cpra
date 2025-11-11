package systems

import (
	"fmt"
	"time"
)

type ErrNoPulseJob struct {
	pulseType string
}

type ErrPulseJobTimeout struct {
	Err       error
	PulseType string
	Timeout   time.Duration
	Retries   int
}

func (e *ErrPulseJobTimeout) Error() string {
	return fmt.Sprintf("Job timeout for pulse type %s after %s (retried %d times): %s", e.PulseType, e.Timeout, e.Retries, e.Err)
}
