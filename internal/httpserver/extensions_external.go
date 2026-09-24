//go:build externaljobs

package httpserver

import (
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type serverConfigExtensions struct {
	ExternalJobsEnabled bool `json:"-"`
}

type managementExtensions struct {
	jobTypesEnabled       bool
	jobTypeSnapshotBytes  atomic.Int64
	jobTypePrincipalBytes map[string]*atomic.Int64
}

type snapshotExtensions struct {
	jobTypes           snapshotPageView[api.JobType]
	jobTypeReservation *jobTypeSnapshotReservation
}

type jobTypeSnapshotReservation struct {
	budget          *atomic.Int64
	principalBudget *atomic.Int64
	bytes           atomic.Int64
	refs            atomic.Int32
}

const jobTypeSnapshotReservationBytes = persistence.MaxJobTypeTotalBytes + persistence.MaxJobTypes*1024
const maxJobTypeSnapshotBytes = 4 * jobTypeSnapshotReservationBytes
const maxPrincipalJobTypeSnapshotBytes = 2 * jobTypeSnapshotReservationBytes

func (s *Server) validateExtensions() error {
	if !s.cfg.ExternalJobsEnabled {
		return nil
	}
	if s.cfg.Management == nil || s.cfg.ManagementAuth == nil || s.cfg.Store == nil || s.cfg.Store.Status().Mode != "raft" {
		return errors.New("external jobs require authenticated management and Raft storage")
	}
	return nil
}

var jobTypeOperations = []struct{ method, operation, path string }{
	{http.MethodGet, "ListJobTypes", "/api/v2/job-types"},
	{http.MethodPost, "CreateJobType", "/api/v2/job-types"},
	{http.MethodGet, "GetJobType", "/api/v2/job-types/{id}"},
	{http.MethodPut, "ReplaceJobType", "/api/v2/job-types/{id}"},
	{http.MethodDelete, "DeleteJobType", "/api/v2/job-types/{id}"},
}

func (s *Server) registerManagementExtensions(mux *http.ServeMux, m *managementHTTP) {
	m.jobTypesEnabled = s.cfg.ExternalJobsEnabled
	if !m.jobTypesEnabled {
		return
	}
	for _, route := range jobTypeOperations {
		mux.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) { m.handleJobType(w, r, route.operation) })
	}
}

func (m *managementHTTP) discoverExtensions(result *api.Capabilities) {
	if !m.jobTypesEnabled {
		return
	}
	result.Resources = append(result.Resources, "JobType")
	for _, route := range jobTypeOperations {
		result.ResourceOperations["JobType"] = append(result.ResourceOperations["JobType"], route.operation)
	}
}

func (m *managementHTTP) extensionPermissions(supported map[string]bool) {
	if !m.jobTypesEnabled {
		return
	}
	for _, route := range jobTypeOperations {
		supported[route.operation] = true
	}
}

func (m *managementHTTP) reserveSnapshotExtension(kind, principal string) (snapshotExtensions, error) {
	if kind != "JobType" {
		return snapshotExtensions{}, nil
	}
	if m.jobTypePrincipalBytes == nil {
		m.jobTypePrincipalBytes = make(map[string]*atomic.Int64)
	}
	for id, charged := range m.jobTypePrincipalBytes {
		if charged.Load() == 0 {
			delete(m.jobTypePrincipalBytes, id)
		}
	}
	owned := m.jobTypePrincipalBytes[principal]
	if owned == nil {
		owned = &atomic.Int64{}
		m.jobTypePrincipalBytes[principal] = owned
	}
	if owned.Load() > maxPrincipalJobTypeSnapshotBytes-jobTypeSnapshotReservationBytes {
		return snapshotExtensions{}, managementFailure(429, "snapshotByteQuota", "Principal snapshot capacity is reserved. Reuse an existing cursor or wait for expiry.")
	}
	for {
		used := m.jobTypeSnapshotBytes.Load()
		if used > maxJobTypeSnapshotBytes-jobTypeSnapshotReservationBytes {
			return snapshotExtensions{}, managementFailure(429, "snapshotByteQuota", "Retained snapshots exceed the byte budget. Reuse an existing cursor or wait for expiry.")
		}
		if m.jobTypeSnapshotBytes.CompareAndSwap(used, used+jobTypeSnapshotReservationBytes) {
			break
		}
	}
	owned.Add(jobTypeSnapshotReservationBytes)
	reservation := &jobTypeSnapshotReservation{budget: &m.jobTypeSnapshotBytes, principalBudget: owned}
	reservation.bytes.Store(jobTypeSnapshotReservationBytes)
	reservation.refs.Store(2)
	return snapshotExtensions{jobTypeReservation: reservation}, nil
}

func (s snapshotExtensions) retain() {
	if s.jobTypeReservation != nil {
		s.jobTypeReservation.refs.Add(1)
	}
}
func (s snapshotExtensions) release() {
	if r := s.jobTypeReservation; r != nil && r.refs.Add(-1) == 0 {
		r.budget.Add(-r.bytes.Load())
		r.principalBudget.Add(-r.bytes.Load())
	}
}

func (r *jobTypeSnapshotReservation) shrink(size int64) error {
	if r == nil || size < 0 || size > jobTypeSnapshotReservationBytes {
		return managementFailure(503, "unavailable", "Snapshot accounting is unavailable.")
	}
	previous := r.bytes.Swap(size)
	r.budget.Add(size - previous)
	r.principalBudget.Add(size - previous)
	return nil
}
