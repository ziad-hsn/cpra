//go:build externaljobs

package persistence

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerHeartbeatControlAndIgnoreRecoveryNotices(t *testing.T) {
	for _, color := range []string{"gray", "green"} {
		t.Run(color, func(t *testing.T) {
			s, authority, pollRequest, in, _ := workerPollFixture(t, "notification")
			m, _ := s.Get(in.MonitorID)
			at := time.Now().UTC()
			config := m.Clone()
			config.Policy.Endpoints = map[string]int{color: 1}
			config.Policy.NotificationTimeouts = map[string][]time.Duration{color: {time.Hour}}
			config.Policy.Healthy = 1
			config.Policy.WorkerNotificationSources = map[string][]WorkerNotificationSource{color: {{ID: in.Source.ID, UID: in.SourceUID, Revision: in.SourceRevision}}}
			m = *submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: &in.Guard, At: at})[0].Monitor
			if color == "gray" {
				m = *applyControlRequest(t, s, controlRequest(m, "dismiss", uuid.NewString(), time.Now().UTC())).Monitor
			}
			m = *applyControlRequest(t, s, controlRequest(m, "ignore", uuid.NewString(), time.Now().UTC())).Monitor
			if color == "gray" {
				config = m.Clone()
				config.Policy.Enabled = false
				m = *submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: &in.Guard, At: time.Now().UTC()})[0].Monitor
			} else {
				m = controlPulse(t, s, m, &in.Guard, time.Now().UTC(), "success")
			}
			action := notificationByColor(t, m, color, Queued)
			if action.IncidentID != "" || m.IncidentID == "" {
				t.Fatal("fixture must distinguish the notice from the retained incident")
			}
			in.ActionID, in.ControlRevision, in.Scheduled = action.ID, m.ControlRevision, action.NotBefore
			in.Deadline = action.Deadline
			in.ExecutionBudget = action.ExecutionBudget
			in.Notification = NotificationForWorker(action)
			if in.ExecutionBudget <= 0 || !in.Deadline.IsZero() {
				t.Fatal("new notice must retain its budget until the first committed start")
			}
			if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
				t.Fatal("notice admission", err)
			}
			poll, err := s.CommitWorkerPoll(t.Context(), authority, pollRequest)
			if err != nil || len(poll.Offers) != 1 {
				t.Fatal("notice offer", err)
			}
			startRequest := workerStartFromOffer(poll)
			start, err := s.CommitWorkerStart(t.Context(), authority, startRequest)
			if err != nil || start.Disposition != "granted" {
				t.Fatal("notice start", err)
			}
			heartbeat, err := s.CommitWorkerHeartbeat(t.Context(), authority, WorkerHeartbeatRequest{ServerID: start.ServerID, SessionID: start.SessionID, ExecutionID: in.ID, GrantID: start.GrantID})
			if err != nil || !heartbeat.Accepted {
				t.Fatal("eligible notice heartbeat cancelled its grant", err)
			}
			if color == "gray" {
				for _, control := range []string{"unignore", "ignore"} {
					m, _ = s.Get(in.MonitorID)
					m = *applyControlRequest(t, s, controlRequest(m, control, uuid.NewString(), time.Now().UTC())).Monitor
					heartbeat, err = s.CommitWorkerHeartbeat(t.Context(), authority, WorkerHeartbeatRequest{ServerID: start.ServerID, SessionID: start.SessionID, ExecutionID: in.ID, GrantID: start.GrantID})
					if err != nil || !heartbeat.Accepted {
						t.Fatal("gray delivery cancelled by "+control, err)
					}
				}
				m = *applyControlRequest(t, s, controlRequest(m, "snooze", uuid.NewString(), time.Now().UTC())).Monitor
				heartbeat, err = s.CommitWorkerHeartbeat(t.Context(), authority, WorkerHeartbeatRequest{ServerID: start.ServerID, SessionID: start.SessionID, ExecutionID: in.ID, GrantID: start.GrantID})
				if err != nil || heartbeat.Accepted {
					t.Fatal("gray delivery bypassed snooze", err)
				}
			}
		})
	}
}
