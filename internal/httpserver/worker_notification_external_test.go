//go:build externaljobs

package httpserver

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerNotificationProductionReceiptAndUnknown(t *testing.T) {
	for _, status := range []string{"delivered", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := newWorkerSessionFixtureWithSource(t, "notification", "Monitor")
			record := queueWorkerHTTPActionSource(t, f, "notification", true)
			_, client, _ := startWorkerProductionServer(t, f, true, nil)
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 {
				t.Fatal("inline notification offer", err)
			}
			a := offers.Items[0]
			if a.ExecutionID != record.Intent.ID || a.IncarnationUID != record.Intent.MonitorUID || a.CredentialProfile != "worker-local-profile" || !bytes.Contains(a.Parameters, []byte("9007199254740993")) {
				t.Fatal("inline assignment lost original identity or parameters")
			}
			start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("inline notification start", err)
			}
			input := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: start.GrantID, Kind: "notification", Status: status, Evidence: []string{}, Data: json.RawMessage(`"private-schema-canary"`)}
			receipt, err := client.Result(t.Context(), input)
			if err != nil {
				t.Fatal("inline notification result", err)
			}
			again, err := client.Result(t.Context(), input)
			if err != nil || !reflect.DeepEqual(receipt, again) {
				t.Fatal("repeated result changed original receipt", err)
			}
			for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
				terminal, err := client.Start(t.Context(), startWorkerOffer(a, mode))
				if err != nil || terminal.Disposition != api.StartDispositionTerminal || terminal.GrantID != "" || !terminal.Deadline.IsZero() || terminal.ReceiptID != receipt.ReceiptID {
					t.Fatal("terminal notification received another executable grant", err)
				}
			}
			monitor, _ := f.store.Get(record.Intent.MonitorID)
			want := persistence.Succeeded
			if status == "unknown" {
				want = persistence.Unknown
			}
			if monitor.Actions[record.Intent.ActionID].State != want {
				t.Fatal("result did not update original action", monitor.Actions[record.Intent.ActionID].State)
			}
		})
	}
}
