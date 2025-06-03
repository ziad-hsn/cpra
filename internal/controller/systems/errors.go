package systems

import (
	"fmt"
	"time"
)

type ErrNoPulseJob struct {
	pulseType string
}

type ErrPulseJobTimeout struct {
	PulseType string
	Timeout   time.Duration
	Retries   int
	Err       error
}

func (e *ErrPulseJobTimeout) Error() string {
	return fmt.Sprintf("Job timeout for pulse type %s after %s (retried %d times): %s", e.PulseType, e.Timeout, e.Retries, e.Err)
}
