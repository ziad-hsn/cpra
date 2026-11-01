package management

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// This encrypted payload is a frozen connection schema, independent of future
// job options. It carries no message, task output or executable runtime object.
type agentConnectionV1 struct {
	Format            int           `json:"format"`
	URL               string        `json:"url"`
	Token             string        `json:"token"`
	Tenant            string        `json:"tenant"`
	RequestTimeout    time.Duration `json:"request_timeout"`
	AllowInsecureHTTP bool          `json:"allow_insecure_http"`
}

func (p agentConnectionV1) config() jobs.A2AConfig {
	return jobs.A2AConfig{URL: p.URL, Token: p.Token, Tenant: p.Tenant,
		RequestTimeout: p.RequestTimeout, AllowInsecureHTTP: p.AllowInsecureHTTP}
}

func (p agentConnectionV1) valid() bool {
	return p.Format == 1 && (&manifest.InterventionTargetA2A{URL: p.URL, Token: p.Token, Tenant: p.Tenant,
		RequestTimeout: p.RequestTimeout, AllowInsecureHTTP: p.AllowInsecureHTTP}).Validate() == nil
}

// PrepareAgentConnection resolves the same authenticated configuration as the
// queued job. The caller must commit the returned envelope with its original
// guard in BeginLocalAction before any agent request. Preparation grants no start.
func (c *Catalog) PrepareAgentConnection(ctx context.Context, monitorID, actionID, revision string, guard persistence.CatalogGuard) (*persistence.AgentConnection, error) {
	if err := c.readyContext(ctx); err != nil {
		return nil, err
	}
	if guard.Removed || !validID(monitorID) || actionID == "" {
		return nil, ErrValidation
	}
	a, found, err := c.store.ActionContext(ctx, actionID)
	if err != nil {
		return nil, err
	}
	if !found || a.MonitorID != monitorID || a.Revision != revision || a.State != persistence.Queued || a.ExecutorKind != "" {
		return nil, persistence.ErrAgentActionInvalid
	}
	view, err := c.SnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	p, err := c.PrepareRuntime(ctx, view, monitorID)
	if err != nil {
		return nil, err
	}
	current, exists, err := c.store.GetContext(ctx, monitorID)
	if err != nil {
		return nil, err
	}
	if !exists || current.Revision != p.ExecutionRevision || !current.ActionConfigurationCurrent(a.Action) || p.MonitorUID != a.CatalogUID || !reflect.DeepEqual(p.Conditions, guard.Conditions) {
		return nil, persistence.ErrCatalogDependency
	}
	endpoint := current.ActionEndpoint(a.Action)
	connection := agentConnectionV1{Format: 1}
	switch a.Kind {
	case "intervention":
		target := p.Monitor.Intervention.Target
		if a.Rule != (persistence.RecoverySelection{}) {
			target = nil
			for _, rule := range p.Monitor.Intervention.Rules {
				if rule.ID == a.Rule.RuleID && rule.Action == a.Rule.Target.Driver && !rule.None {
					target = rule.Target
					break
				}
			}
		}
		if target == nil {
			return nil, persistence.ErrAgentActionInvalid
		}
		config, ok := target.(*manifest.InterventionTargetA2A)
		if !ok || config == nil {
			return nil, persistence.ErrAgentActionInvalid
		}
		connection.URL, connection.Token, connection.Tenant = config.URL, config.Token, config.Tenant
		connection.RequestTimeout, connection.AllowInsecureHTTP = config.RequestTimeout, config.AllowInsecureHTTP
	case "code":
		code, ok := p.Monitor.Codes[a.Color]
		if !ok {
			return nil, persistence.ErrAgentActionInvalid
		}
		selected := code.Config
		if code.NotifyGroup != "" {
			group := p.NotificationGroups[code.NotifyGroup]
			if endpoint < 0 || endpoint >= len(group) {
				return nil, persistence.ErrAgentActionInvalid
			}
			selected = p.Endpoints[group[endpoint]].Config
		} else if endpoint != 0 {
			return nil, persistence.ErrAgentActionInvalid
		}
		config, ok := selected.(*manifest.CodeNotificationA2A)
		if !ok || config == nil {
			return nil, persistence.ErrAgentActionInvalid
		}
		connection.URL, connection.Token, connection.Tenant = config.URL, config.Token, config.Tenant
		connection.RequestTimeout, connection.AllowInsecureHTTP = config.RequestTimeout, config.AllowInsecureHTTP
	default:
		return nil, persistence.ErrAgentActionInvalid
	}
	if !connection.valid() {
		return nil, persistence.ErrAgentActionInvalid
	}
	return SealAgentConnection(ctx, c.sealer, c.storeID, a, connection.config())
}

// SealAgentConnection freezes the exact job connection for its original action.
// The caller must still conditionally commit the start before invoking the job.
// File-only and managed execution use the same payload and binding format.
func SealAgentConnection(ctx context.Context, sealer *secureconfig.Sealer, storeID string, a persistence.ActionRecord, config jobs.A2AConfig) (*persistence.AgentConnection, error) {
	if ctx == nil {
		return nil, secureconfig.ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sealer == nil {
		return nil, secureconfig.ErrMissingKey
	}
	connection := agentConnectionV1{Format: 1, URL: config.URL, Token: config.Token, Tenant: config.Tenant,
		RequestTimeout: config.RequestTimeout, AllowInsecureHTTP: config.AllowInsecureHTTP}
	if !connection.valid() || a.State != persistence.Queued || a.ExecutorKind != "" || a.AgentConnection != nil {
		return nil, persistence.ErrAgentActionInvalid
	}
	plain, err := json.Marshal(connection)
	defer clear(plain)
	if err != nil || len(plain) > persistence.AgentConnectionMaxBytes {
		return nil, persistence.ErrAgentActionInvalid
	}
	payload, err := sealer.Seal(ctx, persistence.AgentConnectionBinding(storeID, a.MonitorID, a.Action), plain)
	if err != nil {
		return nil, err
	}
	return &persistence.AgentConnection{CatalogUID: a.CatalogUID, Payload: payload}, nil
}

// AgentConnection authenticates the retained original target, never the current
// monitor or endpoint. Rotated/revoked agent credentials may make reads fail;
// there is deliberately no fallback to new credentials or another destination.
func (c *Catalog) AgentConnection(ctx context.Context, action persistence.ActionRecord) (jobs.A2AConfig, error) {
	if err := c.readyContext(ctx); err != nil {
		return jobs.A2AConfig{}, err
	}
	return c.openAgentConnection(ctx, action)
}

func (c *Catalog) openAgentConnection(ctx context.Context, a persistence.ActionRecord) (jobs.A2AConfig, error) {
	if a.AgentConnection == nil || a.ExecutorKind != "agent" {
		return jobs.A2AConfig{}, persistence.ErrAgentActionInvalid
	}
	connection, err := OpenAgentConnection(ctx, c.sealer, c.storeID, a)
	if err != nil && ctx.Err() == nil {
		c.fail()
		return jobs.A2AConfig{}, ErrUnavailable
	}
	return connection, err
}

// OpenAgentConnection authenticates only retained connection data. It performs
// no agent I/O, catalog lookup or action transition. Errors contain no payload.
func OpenAgentConnection(ctx context.Context, sealer *secureconfig.Sealer, storeID string, a persistence.ActionRecord) (jobs.A2AConfig, error) {
	if ctx == nil {
		return jobs.A2AConfig{}, secureconfig.ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return jobs.A2AConfig{}, err
	}
	if a.AgentConnection == nil || a.ExecutorKind != "agent" {
		return jobs.A2AConfig{}, persistence.ErrAgentActionInvalid
	}
	if sealer == nil {
		return jobs.A2AConfig{}, secureconfig.ErrMissingKey
	}
	plain, err := sealer.Open(ctx, persistence.AgentConnectionBinding(storeID, a.MonitorID, a.Action), a.AgentConnection.Payload)
	defer clear(plain)
	if err != nil {
		if ctx.Err() != nil {
			return jobs.A2AConfig{}, ctx.Err()
		}
		return jobs.A2AConfig{}, err
	}
	var connection agentConnectionV1
	if len(plain) > persistence.AgentConnectionMaxBytes || api.StrictDecode(plain, &connection) != nil || !connection.valid() {
		return jobs.A2AConfig{}, secureconfig.ErrInvalidEnvelope
	}
	return connection.config(), nil
}

func (c *Catalog) verifyAgentConnections(ctx context.Context) error {
	return verifyAgentConnections(ctx, c.store, c.openAgentConnection)
}

// VerifyAgentConnections authenticates retained connections before file-only
// startup, including actions for monitors absent from the current manifest.
func VerifyAgentConnections(ctx context.Context, store *persistence.Store, sealer *secureconfig.Sealer) error {
	storeID := store.Status().NodeID
	return verifyAgentConnections(ctx, store, func(ctx context.Context, a persistence.ActionRecord) (jobs.A2AConfig, error) {
		return OpenAgentConnection(ctx, sealer, storeID, a)
	})
}

func verifyAgentConnections(ctx context.Context, store *persistence.Store, open func(context.Context, persistence.ActionRecord) (jobs.A2AConfig, error)) error {
	view, err := store.ActionSnapshotContext(ctx)
	if err != nil {
		return err
	}
	defer view.Close()
	for after := ""; ; {
		rows, next, err := view.PageContext(ctx, "", after, 100)
		if err != nil {
			return err
		}
		for _, a := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if a.AgentConnection != nil {
				if _, err := open(ctx, a); err != nil {
					return err
				}
			}
		}
		if next == "" {
			return ctx.Err()
		}
		after = next
	}
}
