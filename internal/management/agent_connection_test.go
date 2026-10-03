package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func retainedAgentConnection(t *testing.T, c *Catalog, store *persistence.Store, plaintext []byte, corrupt bool) persistence.ActionRecord {
	t.Helper()
	at := time.Now().UTC()
	monitor := persistence.Monitor{ID: "original-monitor", Name: "Agent monitor", Revision: "original-config",
		Policy: persistence.Policy{Enabled: true, Intervention: true, Interval: time.Minute, Unhealthy: 1, Healthy: 1}}
	commit := func(command persistence.Command) persistence.Result {
		rows, err := store.Submit(t.Context(), []persistence.Command{command})
		if err != nil || len(rows) != 1 || rows[0].Err != nil {
			t.Fatal("fixture commit failed", err, rows)
		}
		return rows[0]
	}
	commit(persistence.Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: at})
	result := commit(persistence.Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Generation: 1, Outcome: "failure", At: at})
	for _, action := range result.Monitor.Actions {
		if action.Kind != "intervention" {
			continue
		}
		payload, err := c.sealer.Seal(t.Context(), persistence.AgentConnectionBinding(c.storeID, monitor.ID, action), plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if corrupt {
			payload.Ciphertext[0] ^= 1
		}
		h, err := store.BeginLocalAction(t.Context(), persistence.Command{Kind: "start", MonitorID: monitor.ID, Revision: monitor.Revision,
			ActionID: action.ID, Driver: "a2a", At: at, AgentConnection: &persistence.AgentConnection{Payload: payload}})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
		commit(persistence.Command{Kind: "result", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: action.ID, At: at, Outcome: "failure", Ambiguous: true})
		record, found, err := store.ActionContext(t.Context(), action.ID)
		if err != nil || !found {
			t.Fatal("fixture action missing", err)
		}
		return record
	}
	t.Fatal("fixture intervention missing")
	return persistence.ActionRecord{}
}

func TestAgentConnectionStartupAuthenticatesOriginalPayload(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-key", "corrupt", "schema", "unknown-field", "unsafe-url", "future-format"} {
		t.Run(scenario, func(t *testing.T) {
			c, store := testCatalog(t)
			plain := []byte(`{"format":1,"url":"https://agent.example","token":"private-canary","tenant":"ops","request_timeout":1000000000,"allow_insecure_http":false}`)
			switch scenario {
			case "schema":
				plain = []byte(`{"format":1,"token":"private-canary"}`)
			case "unknown-field":
				plain = append(plain[:len(plain)-1], []byte(`,"private-canary":true}`)...)
			case "unsafe-url":
				plain = bytes.ReplaceAll(plain, []byte("https://"), []byte("http://"))
			case "future-format":
				plain = bytes.ReplaceAll(plain, []byte(`"format":1`), []byte(`"format":2`))
			}
			action := retainedAgentConnection(t, c, store, plain, scenario == "corrupt")
			sealer := c.sealer
			if scenario == "wrong-key" {
				wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{91}, 32))
				if err != nil {
					t.Fatal(err)
				}
				sealer, err = secureconfig.NewSealer(wrapper)
				if err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := NewCatalog(store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			err = reopened.Verify(t.Context())
			if scenario != "valid" {
				if !errors.Is(err, ErrUnavailable) || reopened.Ready() || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("invalid original connection did not stop startup safely", err)
				}
				return
			}
			if err != nil || !reopened.Ready() {
				t.Fatal("valid original connection rejected", err)
			}
			connection, err := reopened.AgentConnection(t.Context(), action)
			if err != nil || connection.URL != "https://agent.example" || connection.Token != "private-canary" {
				t.Fatal("original connection was not recovered", err)
			}
			raw, _ := json.Marshal(actionView(action))
			for _, forbidden := range []string{"private-canary", "agent.example", "agent_connection", action.AgentConnection.Payload.KeyID} {
				if bytes.Contains(raw, []byte(forbidden)) {
					t.Fatal("public action exposed connection material")
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := reopened.AgentConnection(ctx, action); !errors.Is(err, context.Canceled) || !reopened.Ready() {
				t.Fatal("canceled observation poisoned valid storage", err)
			}
		})
	}
}
