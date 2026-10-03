package drivertest

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

// agentCase owns one explicitly requested verification action. The selected
// production job stays in memory; its intent, receipts and outcome use real Raft.
// A synthetic failed observation supplies incident context without contacting
// the manifest's health-check target or starting a controller scheduler.
type agentCase struct {
	store   *persistence.Store
	monitor persistence.Monitor
	action  persistence.Action
}

func prepareAgentCase(ctx context.Context, directory, runID, monitorID, kind string) (_ *agentCase, err error) {
	config := runtimeconfig.Default()
	config.Storage.Directory = filepath.Join(directory, kind)
	store, err := persistence.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	c := &agentCase{store: store}
	defer func() {
		if err != nil {
			_ = c.close()
		}
	}()
	m := persistence.Monitor{ID: monitorID, Name: "A2A provider verification", Revision: runID,
		Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Unhealthy: 1, Healthy: 1,
			Intervention: kind == "intervention", RecoveryMaxAttempts: 1}}
	if kind == "code" {
		m.Policy.Endpoints = map[string]int{"red": 1}
	}
	at := time.Now().UTC()
	if _, err = commitAgentCommand(ctx, store, persistence.Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}); err != nil {
		return nil, err
	}
	result, err := commitAgentCommand(ctx, store, persistence.Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})
	if err != nil {
		return nil, err
	}
	if result.Monitor == nil || len(result.Monitor.Actions) != 1 {
		return nil, errors.New("verification did not admit exactly one action")
	}
	c.monitor = result.Monitor.Clone()
	for _, action := range c.monitor.Actions {
		if action.Kind != kind || action.State != persistence.Queued {
			return nil, errors.New("verification admitted a different action")
		}
		c.action = action
	}
	return c, nil
}

func commitAgentCommand(ctx context.Context, store *persistence.Store, command persistence.Command) (persistence.Result, error) {
	results, err := store.Submit(ctx, []persistence.Command{command})
	if err != nil {
		return persistence.Result{}, err
	}
	if len(results) != 1 {
		return persistence.Result{}, persistence.ErrCommitUnconfirmed
	}
	return results[0], results[0].Err
}

func (c *agentCase) bind(dispatch *jobs.Dispatch) {
	dispatch.MonitorID, dispatch.CatalogUID = c.monitor.ID, c.monitor.CatalogUID
	dispatch.Revision, dispatch.ActionID = c.action.Revision, c.action.ID
	dispatch.IncidentID, dispatch.Attempt = c.action.IncidentID, c.action.Attempt
	dispatch.Endpoint = c.action.Endpoint
	dispatch.Authorize = func(ctx context.Context) (execution jobs.ActionExecution, err error) {
		handle, err := c.store.BeginLocalAction(ctx, persistence.Command{Kind: "start", MonitorID: c.monitor.ID,
			Revision: c.action.Revision, ActionID: c.action.ID, Driver: "a2a", At: time.Now().UTC()})
		if handle == nil {
			return execution, err
		}
		// Finalize even an uncertain start. The caller returns before this
		// callback; canceling the request is not evidence of remote completion.
		execution.Finish = func() error {
			return handle.Finish(context.Background())
		}
		var sequence uint64
		execution.RecordAgentReceipt = func(ctx context.Context, receipt jobs.A2AReceipt) error {
			command := persistence.AgentReceiptCommand{ExpectedSequence: sequence,
				Receipt: persistence.AgentReceipt{Sequence: sequence + 1, TaskID: receipt.TaskID,
					ContextID: receipt.ContextID, MessageID: receipt.MessageID, State: receipt.State, ObservedAt: time.Now().UTC()}}
			if err := handle.RecordAgentReceipt(ctx, command); err != nil {
				return err
			}
			sequence++
			return nil
		}
		return execution, err
	}
}

func (c *agentCase) finish(result jobs.Result) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !result.ExecutionStart.IsZero() {
		command := persistence.Command{Kind: "result", MonitorID: c.monitor.ID, Revision: c.action.Revision,
			ActionID: c.action.ID, At: time.Now().UTC(), Outcome: "success"}
		if result.Err != nil || result.FinalizationErr != nil {
			command.Outcome, command.Ambiguous = "failure", true
		}
		if _, err := commitAgentCommand(ctx, c.store, command); err != nil {
			return "", err
		}
	}
	action, found, err := c.store.ActionContext(ctx, c.action.ID)
	if err != nil || !found {
		return "", errors.New("verification action outcome is unavailable")
	}
	if result.Err == nil && (result.FinalizationErr != nil || action.State != persistence.Succeeded) {
		return string(action.State), errors.New("verification outcome was not committed")
	}
	return string(action.State), nil
}

func (c *agentCase) close() error {
	if c.store == nil {
		return nil
	}
	if err := c.store.Close(); err != nil {
		return err
	}
	c.store = nil
	return nil
}
