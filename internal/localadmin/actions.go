package localadmin

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

// LocalAction exposes evidence and review preconditions without encrypted
// connection material, provider parameters, credentials or executable state.
type LocalAction struct {
	ID             string                    `json:"id"`
	MonitorID      string                    `json:"monitorId"`
	MonitorUID     string                    `json:"monitorUid"`
	Revision       string                    `json:"revision"`
	Kind           string                    `json:"kind"`
	Color          string                    `json:"color,omitempty"`
	State          persistence.ActionState   `json:"state"`
	Outcome        string                    `json:"outcome,omitempty"`
	Held           bool                      `json:"held"`
	ExecutorFenced bool                      `json:"executorFenced"`
	Review         *persistence.ActionReview `json:"review,omitempty"`
}

type LocalActionPage struct {
	Items      []LocalAction `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type LocalActionReviewRequest struct {
	DataDirectory, ActionID, ExpectedRevision, Actor, Reason, Note, Resolution string
	EvidenceRefs                                                               []string
}

type LocalActionReviewReport struct {
	Action           *LocalAction `json:"action,omitempty"`
	IntendedRevision string       `json:"intendedRevision,omitempty"`
	Outcome          string       `json:"outcome"`
}

func localAction(record persistence.ActionRecord) LocalAction {
	return LocalAction{ID: record.ID, MonitorID: record.MonitorID, MonitorUID: record.ReviewMonitorIdentity(), Revision: record.ReviewRevision,
		Kind: record.Kind, Color: record.Color, State: record.State, Outcome: record.Outcome, Held: record.Held, ExecutorFenced: record.ExecutorFenced, Review: record.Review}
}

func openActionStore(ctx context.Context, directory string) (*persistence.Store, error) {
	if ctx == nil || directory == "" || !filepath.IsAbs(directory) || directory != filepath.Clean(directory) || strings.ContainsAny(directory, "\x00\r\n") || rejectSymlinkAncestors(directory) != nil {
		return nil, errors.New("local actions require a clean absolute existing data directory without symbolic links")
	}
	if err := requireExistingAuthenticationStore(directory); err != nil {
		return nil, &authenticationError{"local actions require an existing complete store", err}
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode, cfg.Storage.Directory = "raft", directory
	s, err := persistence.OpenAdministrative(ctx, cfg)
	if err != nil {
		return nil, &authenticationError{"cannot open local actions: stop the current owner and verify the selected state directory", err}
	}
	return s, nil
}

// ReadStoppedActions holds exclusive local ownership for the bounded read. It
// neither recovers lifecycle work nor changes executor or action records.
func ReadStoppedActions(ctx context.Context, directory, monitorID, after string, limit int) (page LocalActionPage, err error) {
	if limit < 1 || limit > 500 {
		return page, errors.New("action page limit must be between 1 and 500")
	}
	s, err := openActionStore(ctx, directory)
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	view, err := s.StoppedActions(ctx)
	if err != nil {
		return page, &authenticationError{"cannot read stopped actions", err}
	}
	defer view.Close()
	rows, next, err := view.PageContext(ctx, monitorID, after, limit)
	page.NextCursor = next
	if err != nil {
		return page, &authenticationError{"invalid action page request", err}
	}
	page.Items = make([]LocalAction, 0, len(rows))
	for _, row := range rows {
		page.Items = append(page.Items, localAction(row))
	}
	return page, nil
}

// ReviewStoppedAction never turns an administrator's assertion into a provider
// success or retries the external effect. Lost replies retain the intended
// revision so the caller can reconcile with ReadStoppedActions.
func ReviewStoppedAction(ctx context.Context, request LocalActionReviewRequest) (report LocalActionReviewReport, err error) {
	if request.ActionID == "" || request.ExpectedRevision == "" || strings.TrimSpace(request.Actor) == "" || strings.TrimSpace(request.Reason) == "" {
		return report, errors.New("action review requires its exact revision, actor and reason")
	}
	s, err := openActionStore(ctx, request.DataDirectory)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	view, err := s.StoppedActions(ctx)
	if err != nil {
		return report, &authenticationError{"cannot read stopped actions", err}
	}
	defer view.Close()
	a, found, err := view.GetContext(ctx, request.ActionID)
	if err != nil {
		return report, &authenticationError{"cannot read stopped action", err}
	}
	if !found {
		return report, errors.New("action was not found in the selected store")
	}
	report.IntendedRevision = uuid.NewString()
	r := persistence.ActionReviewCommand{ActionID: a.ID, MonitorUID: a.ReviewMonitorIdentity(), ExpectedRevision: request.ExpectedRevision,
		Revision: report.IntendedRevision, OperationID: report.IntendedRevision, Actor: request.Actor, Reason: request.Reason, Note: request.Note,
		Resolution: request.Resolution, EvidenceRefs: request.EvidenceRefs}
	_, err = s.ReviewStoppedAction(ctx, persistence.Command{Kind: "local_action_review", MonitorID: a.MonitorID, ActionReview: &r, At: time.Now().UTC()})
	if err != nil {
		report.Outcome = "not_committed"
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			report.Outcome = "unconfirmed"
		}
		return report, &authenticationError{"action review was not confirmed; inspect local actions and the intended revision before another review", err}
	}
	updatedView, readErr := s.StoppedActions(ctx)
	report.Outcome = "committed"
	if readErr != nil {
		return report, &authenticationError{"review committed; updated action could not be read", readErr}
	}
	defer updatedView.Close()
	updated, found, readErr := updatedView.GetContext(ctx, a.ID)
	if readErr != nil || !found {
		return report, &authenticationError{"review committed; updated action could not be read", readErr}
	}
	value := localAction(updated)
	report.Action = &value
	return report, nil
}
