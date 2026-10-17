package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type monitorObservationView struct{ view fleetview.View }

func (v monitorObservationView) Page(ctx context.Context, selection, after string, limit int) ([]api.MonitorObservation, string, error) {
	rows, next, err := v.view.Page(ctx, selection, after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]api.MonitorObservation, 0, len(rows))
	for _, row := range rows {
		items = append(items, monitorObservation(row))
	}
	return items, next, nil
}

func observedTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	utc := at.UTC()
	return &utc
}

func monitorObservation(row fleetview.MonitorSummary) api.MonitorObservation {
	latency := api.Measurement{Available: row.LatencyAvailable, Reason: "notObserved"}
	if row.LatencyAvailable {
		latency.Value, latency.Reason = row.LatencyMs, ""
	}
	colors := append([]string{}, row.ActiveCodes...)
	out := api.MonitorObservation{
		MonitorID: row.MonitorID, IncarnationUID: row.IncarnationUID, NumericID: int64(row.ID),
		Name: row.Name, Source: api.MonitorObservationSource(row.Source), CheckType: row.PulseType,
		Target: row.Target, IntervalMS: row.IntervalMs, Health: api.MonitorObservationHealth(row.Health),
		Phase: api.MonitorObservationPhase(row.Phase), Enabled: row.Enabled, Ignored: row.Ignored,
		ConfiguredNotifications: colors, PendingNotifications: int64(row.PendingNotifications),
		CompletedNotifications: int64(row.CompletedNotifications), FailedNotifications: int64(row.FailedNotifications),
		UnknownNotifications: int64(row.UnknownNotifications), UnknownActions: int64(row.UnknownActions), ActionsNeedingReview: int64(row.ActionsNeedingReview),
		LastCheckLatencyMS: latency, MissedChecks: int64(row.MissedChecks), QueueRejections: int64(row.QueueRejections),
		CertificateVerificationDisabled: row.CertificateVerificationDisabled,
		SnoozedUntil:                    observedTime(row.SnoozedUntil), LastCheckedAt: observedTime(row.LastCheck),
		LastSuccessfulAt: observedTime(row.LastSuccess), NextCheckAt: observedTime(row.NextCheck),
		IncidentID: row.IncidentID, CheckAdmission: row.CheckAdmission,
	}
	if row.LastFailure.Code != "" {
		d := row.LastFailure
		out.LastFailure = &api.FailureDiagnostic{Code: d.Code, Message: d.Message}
		if d.HTTPStatus != 0 {
			out.LastFailure.HTTPStatus = &d.HTTPStatus
		}
		if d.GRPCStatus != "" {
			value := api.FailureDiagnosticGrpcStatus(d.GRPCStatus)
			out.LastFailure.GRPCStatus = &value
		}
		if d.GRPCCode != "" {
			out.LastFailure.GRPCCode = &d.GRPCCode
		}
		if !d.CertificateNotAfter.IsZero() {
			out.LastFailure.CertificateNotAfter = observedTime(d.CertificateNotAfter)
			out.LastFailure.CertificateDaysRemaining = &d.CertificateDaysRemaining
		}
	}
	return out
}

func (s *Server) registerMonitorObservations(mux *http.ServeMux, m *managementHTTP) {
	mux.HandleFunc("GET /api/v2/observations/monitors", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if _, err := m.authorizeRead(r, "ListMonitorObservations"); err != nil {
			writeManagementError(w, err)
			return
		}
		query, err := actionListQuery(r)
		if err != nil {
			writeManagementError(w, err)
			return
		}
		var result api.MonitorObservationList
		err = m.withSnapshotRead(r, "ListMonitorObservations", "MonitorObservation", query, func(ctx context.Context, read *snapshotRead) error {
			entry := &read.entry
			if read.fresh {
				index := s.holder.Index()
				if index == nil {
					return management.ErrUnavailable
				}
				entry.monitors = monitorObservationView{view: index.Snapshot()}
			}
			items, after, err := entry.monitors.Page(ctx, entry.monitorID, read.cursor.After, entry.limit)
			if err != nil {
				return err
			}
			items, after, err = boundSnapshotPage(items, after, func(item api.MonitorObservation) string { return item.MonitorID })
			read.after = after
			result = api.MonitorObservationList{Items: items, GeneratedAt: entry.created, Snapshot: read.cursor.View}
			if after != "" {
				result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: after})
			}
			return err
		})
		if err != nil {
			writeManagementError(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/v2/observations/monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		var result api.MonitorObservation
		err := m.withObservationRead(r, "GetMonitorObservation", func(ctx context.Context) error {
			if r.URL.RawQuery != "" {
				return managementFailure(400, "invalidQuery", "Monitor detail does not accept query parameters.")
			}
			index := s.holder.Index()
			if index == nil {
				return management.ErrUnavailable
			}
			row, ok := index.GetStable(r.PathValue("id"))
			if !ok {
				return managementFailure(404, "notFound", "The monitor observation was not found.")
			}
			result = monitorObservation(row)
			return ctx.Err()
		})
		if err != nil {
			writeManagementError(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, result)
	})
}
