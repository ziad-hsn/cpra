package systems

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestReturnedNotificationReconcilesWithoutRepeatingEffect(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, failure := range []error{context.DeadlineExceeded, context.Canceled, persistence.ErrCommitUnconfirmed, raft.ErrNotLeader, raft.ErrLeadershipLost} {
			t.Run(fmt.Sprintf("committed_%t/%s", committed, failure), func(t *testing.T) {
				world := ecs.NewWorld()
				ent := releaseMonitor(t, &world, 1, 1)
				s := newIncidentSystem(t, &world, &admissionQueue{full: true}, &admissionQueue{full: true})
				m := applyIncidentPulse(t, s, ent, jobs.Result{Driver: "http", Err: errors.New("unhealthy")})
				actions := incidentActions(m, "code", "red")
				if len(actions) != 1 {
					t.Fatal("missing notification")
				}
				a := actions[0]
				started := time.Now().UTC()
				h, err := s.store.BeginLocalAction(t.Context(), persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: started, NoticeRevision: persistence.ActionNoticeRevision(a)})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := h.Finish(context.Background()); err != nil {
						t.Error(err)
					}
				})
				// Execute the production log transport, then withhold either the
				// completion submission or the reply after its actual durable commit.
				// Reconciliation must preserve the one independently counted effect.
				root := filepath.Join(t.TempDir(), "log")
				log, err := jobs.OpenNotificationLog(root)
				if err != nil {
					t.Fatal(err)
				}
				defer log.Close()
				cancelled, cancel := context.WithCancel(t.Context())
				cancel()
				d := jobs.NewDispatch(s.jobs.Get(ent).CodeJobs["red"][0], ent, "code", "red", 0, 0)
				d.MonitorID, d.Revision, d.ActionID = m.ID, m.Revision, a.ID
				d.NotificationLog = log
				d.Authorize = func(context.Context) (jobs.ActionExecution, error) {
					return jobs.ActionExecution{Deadline: h.Deadline(), Finish: func() error {
						if committed {
							if err := h.Finish(t.Context()); err != nil {
								t.Fatal(err)
							}
							row, ok, err := s.store.ActionContext(t.Context(), a.ID)
							if err != nil || !ok || row.ExecutorFinishedAt.IsZero() {
								t.Fatal("completion did not actually commit", err)
							}
						} else if err := h.Finish(cancelled); !errors.Is(err, context.Canceled) {
							t.Error(err)
						}
						return failure
					}}, nil
				}
				r := d.Execute()
				if r.Err != nil || !errors.Is(r.FinalizationErr, failure) {
					t.Fatal("fixture transport", r.Err, r.FinalizationErr)
				}
				before := time.Now().UTC()
				if !s.commitResults([]jobs.Result{r}) || s.lastError != nil {
					t.Fatal("completion stopped controller", s.lastError)
				}
				s.Update(&world)
				if !s.Progressing() {
					t.Fatal("owner did not resume safe admission", s.lastError)
				}
				row, ok, err := s.store.ActionContext(t.Context(), a.ID)
				if err != nil || !ok || row.State != persistence.Succeeded {
					t.Fatal("outcome not retained", row, err)
				}
				if row.ExecutorFinishedAt.IsZero() || row.ExecutorFinishedAt.After(before) {
					t.Fatal("completion time was renewed", row.ExecutorFinishedAt, before)
				}
				if status, err := s.store.RetryReturnedExecutions(t.Context()); err != nil || status.Returned != 0 || status.Running != 0 {
					t.Fatal("execution claim not reconciled", status, err)
				}
				data, err := os.ReadFile(filepath.Join(root, "alerts.jsonl"))
				if err != nil || bytes.Count(data, []byte("\n")) != 1 {
					t.Fatal("unexpected provider effect count", err)
				}
			})
		}
	}
}

func TestOwnerUnconfirmedReplyWithoutLeadershipErrorRemainsRecoverable(t *testing.T) {
	s, _, _ := recoveryOwner(t, 1)
	s.recoveryError(persistence.ErrCommitUnconfirmed, context.Background())
	if s.lastError != nil || !s.recovering.Load() || s.AdmissionReady() || !s.store.Status().Ready {
		t.Fatal("uncertain write became permanent failure")
	}
}

func TestOwnerFeedbackLeadershipLossRemainsRecoverable(t *testing.T) {
	s, _, _ := recoveryOwner(t, 1)
	s.recoveryError(raft.ErrNotLeader, context.Background())
	if s.lastError != nil || !s.recovering.Load() || s.AdmissionReady() || !s.store.Status().Ready {
		t.Fatal("feedback leadership loss became permanent failure")
	}
}
