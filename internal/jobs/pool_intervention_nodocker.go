//go:build nodocker

package jobs

func releaseInterventionJob(job Job) {
	// No-op: intervention job types are excluded when built with `-tags nodocker`.
}

